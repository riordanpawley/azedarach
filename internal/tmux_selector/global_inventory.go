package tmuxselector

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

const (
	defaultInventorySessionLimit = 200
	defaultInventoryProjectLimit = 64
	currentSessionEnvKey         = "AZEDARACH_TMUX_CURRENT_SESSION"
)

// SessionInventory lists live tmux sessions for the global loader.
type SessionInventory interface {
	ListSessionInfos(context.Context) ([]tmux.SessionInfo, error)
}

type CurrentSessionSource interface {
	CurrentSession(context.Context) (string, error)
}

// ProjectSnapshotSource is the thin daemon-client read surface used to enrich live tmux inventory.
type ProjectSnapshotSource interface {
	ListProjectSnapshots(context.Context) ([]ProjectInventorySnapshot, error)
}

type ProjectInventorySnapshot struct {
	ProjectID   string
	ProjectPath string
	Tasks       []domain.Task
}

// GlobalInventoryLoader builds inventory from live tmux first, then projection metadata.
type GlobalInventoryLoader struct {
	tmux        SessionInventory
	source      ProjectSnapshotSource
	projectDirs []string
	logger      *slog.Logger
	limit       int
}

type GlobalInventoryOption func(*GlobalInventoryLoader)

func WithProjectSnapshotSource(source ProjectSnapshotSource) GlobalInventoryOption {
	return func(l *GlobalInventoryLoader) {
		l.source = source
	}
}

func WithProjectDirs(projectDirs ...string) GlobalInventoryOption {
	return func(l *GlobalInventoryLoader) {
		l.projectDirs = append([]string(nil), projectDirs...)
	}
}

func WithInventoryLimit(limit int) GlobalInventoryOption {
	return func(l *GlobalInventoryLoader) {
		if limit > 0 {
			l.limit = limit
		}
	}
}

func NewGlobalInventoryLoader(tmuxInventory SessionInventory, logger *slog.Logger, opts ...GlobalInventoryOption) *GlobalInventoryLoader {
	if logger == nil {
		logger = slog.Default()
	}
	l := &GlobalInventoryLoader{
		tmux:   tmuxInventory,
		logger: logger,
		limit:  defaultInventorySessionLimit,
	}
	for _, opt := range opts {
		opt(l)
	}
	if l.limit <= 0 {
		l.limit = defaultInventorySessionLimit
	}
	return l
}

func NewDefaultGlobalInventoryLoader(tmuxInventory SessionInventory, logger *slog.Logger) *GlobalInventoryLoader {
	projectDirs := KnownProjectDirs()
	return NewGlobalInventoryLoader(
		tmuxInventory,
		logger,
		WithProjectDirs(projectDirs...),
		WithProjectSnapshotSource(NewDaemonSnapshotSource(projectDirs, logger)),
	)
}

func KnownProjectDirs() []string {
	seen := map[string]struct{}{}
	var dirs []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return
		}
		if _, exists := seen[abs]; exists {
			return
		}
		seen[abs] = struct{}{}
		dirs = append(dirs, abs)
	}
	if registry, err := config.LoadProjectsRegistry(); err == nil && registry != nil {
		for _, project := range registry.Projects {
			add(project.Path)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if root, err := config.ResolveProjectRoot(cwd); err == nil {
			add(root)
		}
	}
	sort.Strings(dirs)
	return dirs
}

func (l *GlobalInventoryLoader) ListTasksSnapshot(ctx context.Context) (Snapshot, error) {
	if l == nil || l.tmux == nil {
		return Snapshot{}, errInventoryUnavailable()
	}
	live, err := l.tmux.ListSessionInfos(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if len(live) > l.limit {
		live = live[:l.limit]
	}
	snapshot := l.snapshotFromLive(ctx, live, nil, true)
	projectDirs := l.projectDirsForLiveSessions(live)
	projections := l.loadProjections(ctx, projectDirs)
	return l.enrichEntries(snapshot, projections), nil
}

func (l *GlobalInventoryLoader) ListLiveSnapshot(ctx context.Context) (Snapshot, error) {
	if l == nil || l.tmux == nil {
		return Snapshot{}, errInventoryUnavailable()
	}
	live, err := l.tmux.ListSessionInfos(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if len(live) > l.limit {
		live = live[:l.limit]
	}
	return l.snapshotFromLive(ctx, live, nil, true), nil
}

func (l *GlobalInventoryLoader) EnrichSnapshot(ctx context.Context, snapshot Snapshot) (Snapshot, error) {
	if l == nil {
		return snapshot, errInventoryUnavailable()
	}
	projectDirs := l.projectDirsForEntries(snapshot.Entries)
	projections := l.loadProjections(ctx, projectDirs)
	return l.enrichEntries(snapshot, projections), nil
}

func (l *GlobalInventoryLoader) snapshotFromLive(ctx context.Context, live []tmux.SessionInfo, projections map[string]projectedInventory, enriching bool) Snapshot {
	entries := make([]InventoryEntry, 0, len(live))
	seen := make(map[string]struct{}, len(live))
	for _, info := range live {
		sessionName := strings.TrimSpace(info.Name)
		if sessionName == "" {
			continue
		}
		if _, ok := seen[sessionName]; ok {
			continue
		}
		seen[sessionName] = struct{}{}

		parsed, ok := ParseAzedarachSessionName(sessionName)
		if !ok {
			if projection, ok := projections[sessionName]; ok {
				parsed = ParsedSessionName{IssueID: naming.IssueID(projection.issueID)}
				ok = true
			}
		}
		entry := InventoryEntry{
			SessionID:      sessionName,
			ProjectID:      parsed.Project,
			Worktree:       strings.TrimSpace(info.Path),
			StartedAt:      info.CreatedAt,
			HasTmuxSession: true,
			HasWorktree:    strings.TrimSpace(info.Path) != "",
			State:          domain.SessionWaiting,
			IssueStatus:    domain.StatusInProgress,
			Priority:       domain.P2,
			Type:           domain.TypeTask,
			TaskTitle:      sessionName,
		}
		if ok && !parsed.IssueID.IsZero() {
			entry.IssueID = parsed.IssueID.String()
			entry.TaskTitle = parsed.IssueID.String()
		}
		if projection, ok := projections[sessionName]; ok {
			entry = mergeProjectedInventory(entry, projection)
		} else if entry.IssueID != "" {
			if projection, ok := projections[entry.IssueID]; ok {
				entry = mergeProjectedInventory(entry, projection)
			}
		}
		if entry.ProjectPath == "" {
			entry.ProjectPath = projectPathForSessionPrefix(entry, l.projectDirs)
		}
		if entry.ProjectPath == "" && entry.Worktree != "" {
			entry.ProjectPath = inferProjectPath(entry.Worktree, l.projectDirs)
		}
		entries = append(entries, entry)
	}
	snapshot := l.snapshotFromEntries(entries, enriching)
	snapshot.CurrentSessionID = l.currentSession(ctx)
	return snapshot
}

func (l *GlobalInventoryLoader) enrichEntries(snapshot Snapshot, projections map[string]projectedInventory) Snapshot {
	entries := make([]InventoryEntry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if projection, ok := projections[entry.SessionID]; ok {
			entry = mergeProjectedInventory(entry, projection)
		} else if entry.IssueID != "" {
			if projection, ok := projections[entry.IssueID]; ok {
				entry = mergeProjectedInventory(entry, projection)
			}
		}
		if entry.ProjectPath == "" {
			entry.ProjectPath = projectPathForSessionPrefix(entry, l.projectDirs)
		}
		if entry.ProjectPath == "" && entry.Worktree != "" {
			entry.ProjectPath = inferProjectPath(entry.Worktree, l.projectDirs)
		}
		entries = append(entries, entry)
	}
	enriched := l.snapshotFromEntries(entries, false)
	enriched.CurrentSessionID = snapshot.CurrentSessionID
	return enriched
}

func (l *GlobalInventoryLoader) snapshotFromEntries(entries []InventoryEntry, enriching bool) Snapshot {
	sort.SliceStable(entries, func(i, j int) bool {
		leftStarted := entries[i].StartedAt != nil
		rightStarted := entries[j].StartedAt != nil
		if leftStarted != rightStarted {
			return leftStarted
		}
		if leftStarted && rightStarted && !entries[i].StartedAt.Equal(*entries[j].StartedAt) {
			return entries[i].StartedAt.Before(*entries[j].StartedAt)
		}
		leftKnown := entries[i].ProjectPath != ""
		rightKnown := entries[j].ProjectPath != ""
		if leftKnown != rightKnown {
			return leftKnown
		}
		if entries[i].ProjectPath != entries[j].ProjectPath {
			return entries[i].ProjectPath < entries[j].ProjectPath
		}
		leftIssue := entries[i].IssueID != ""
		rightIssue := entries[j].IssueID != ""
		if leftIssue != rightIssue {
			return leftIssue
		}
		if entries[i].IssueID != entries[j].IssueID {
			return entries[i].IssueID < entries[j].IssueID
		}
		return entries[i].SessionID < entries[j].SessionID
	})
	tasks := make([]domain.Task, 0, len(entries))
	for _, entry := range entries {
		tasks = append(tasks, taskFromInventoryEntry(entry))
	}
	return Snapshot{
		Entries:       entries,
		Tasks:         tasks,
		LastCheckedAt: time.Now().UTC(),
		Freshness:     "fresh",
		Enriching:     enriching,
	}
}

func (l *GlobalInventoryLoader) currentSession(ctx context.Context) string {
	if current := strings.TrimSpace(os.Getenv(currentSessionEnvKey)); current != "" {
		return current
	}
	if strings.TrimSpace(os.Getenv("TMUX")) == "" {
		return ""
	}
	source, ok := l.tmux.(CurrentSessionSource)
	if !ok {
		return ""
	}
	current, err := source.CurrentSession(ctx)
	if err != nil {
		if l.logger != nil {
			l.logger.Debug("global selector current tmux session lookup failed", "error", err)
		}
		return ""
	}
	return strings.TrimSpace(current)
}

type projectedInventory struct {
	projectID   string
	projectPath string
	issueID     string
	state       domain.SessionState
	startedAt   *time.Time
	worktree    string
	task        domain.Task
}

func (l *GlobalInventoryLoader) projectDirsForLiveSessions(live []tmux.SessionInfo) []string {
	seen := map[string]struct{}{}
	var dirs []string
	entries := entriesFromLive(live)
	l.addMatchingConfiguredProjectDirs(&dirs, seen, entries)
	return limitSortedProjectDirs(dirs)
}

func (l *GlobalInventoryLoader) projectDirsForEntries(entries []InventoryEntry) []string {
	seen := map[string]struct{}{}
	var dirs []string
	l.addMatchingConfiguredProjectDirs(&dirs, seen, entries)
	return limitSortedProjectDirs(dirs)
}

func limitSortedProjectDirs(dirs []string) []string {
	sort.Strings(dirs)
	if len(dirs) > defaultInventoryProjectLimit {
		dirs = dirs[:defaultInventoryProjectLimit]
	}
	return dirs
}

func (l *GlobalInventoryLoader) addMatchingConfiguredProjectDirs(dirs *[]string, seen map[string]struct{}, entries []InventoryEntry) map[string]struct{} {
	matched := map[string]struct{}{}
	if len(l.projectDirs) == 0 || len(entries) == 0 {
		return matched
	}
	entriesByPrefix := map[string][]InventoryEntry{}
	for _, entry := range entries {
		prefix := strings.TrimSpace(entry.ProjectID)
		if prefix == "" {
			continue
		}
		entriesByPrefix[prefix] = append(entriesByPrefix[prefix], entry)
	}
	for _, projectDir := range l.projectDirs {
		root := strings.TrimSpace(projectDir)
		if root == "" {
			continue
		}
		prefix := naming.ProjectSessionPrefix(root)
		matchingEntries := entriesByPrefix[prefix]
		if len(matchingEntries) == 0 {
			continue
		}
		addProjectDir(dirs, seen, root)
		for _, entry := range matchingEntries {
			if entry.SessionID != "" {
				matched[entry.SessionID] = struct{}{}
			}
		}
	}
	return matched
}

func entriesFromLive(live []tmux.SessionInfo) []InventoryEntry {
	entries := make([]InventoryEntry, 0, len(live))
	for _, info := range live {
		sessionName := strings.TrimSpace(info.Name)
		if sessionName == "" {
			continue
		}
		entry := InventoryEntry{SessionID: sessionName, Worktree: strings.TrimSpace(info.Path)}
		if parsed, ok := ParseAzedarachSessionName(sessionName); ok {
			entry.IssueID = parsed.IssueID.String()
			entry.ProjectID = parsed.Project
		}
		entries = append(entries, entry)
	}
	return entries
}

func addProjectDir(dirs *[]string, seen map[string]struct{}, path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	if _, exists := seen[abs]; exists {
		return
	}
	seen[abs] = struct{}{}
	*dirs = append(*dirs, abs)
}

func (l *GlobalInventoryLoader) loadProjections(ctx context.Context, projectDirs []string) map[string]projectedInventory {
	out := map[string]projectedInventory{}
	source := l.source
	if source == nil {
		return out
	}
	if _, ok := source.(*DaemonSnapshotSource); ok {
		source = NewDaemonSnapshotSource(projectDirs, l.logger)
	}
	snapshots, err := source.ListProjectSnapshots(ctx)
	if err != nil {
		if l.logger != nil {
			l.logger.Debug("global selector daemon snapshot enrichment failed", "error", err)
		}
		return out
	}
	for _, snapshot := range snapshots {
		projectID := protocol.NormalizeProjectID(snapshot.ProjectID)
		projectPath := strings.TrimSpace(snapshot.ProjectPath)
		for _, task := range snapshot.Tasks {
			issueID := task.ID.String()
			if issueID == "" {
				continue
			}
			projection := projectedInventory{
				projectID:   projectID,
				projectPath: projectPath,
				issueID:     issueID,
				task:        task,
			}
			if task.Session != nil {
				projection.state = task.Session.State
				projection.startedAt = task.Session.StartedAt
				projection.worktree = task.Session.Worktree
			}
			if projection.worktree == "" {
				projection.worktree = taskWorktree(task)
			}
			addProjection(out, issueID, projection)
			addProjection(out, naming.CanonicalSessionID(projectID, issueID), projection)
			addProjection(out, naming.CanonicalSessionID(projectPath, issueID), projection)
		}
	}
	return out
}

func addProjection(out map[string]projectedInventory, key string, projection projectedInventory) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	out[key] = projection
}

func mergeProjectedInventory(entry InventoryEntry, projection projectedInventory) InventoryEntry {
	if projection.issueID != "" {
		entry.IssueID = projection.issueID
	}
	if projection.projectID != "" {
		entry.ProjectID = projection.projectID
	}
	if projection.projectPath != "" {
		entry.ProjectPath = projection.projectPath
	}
	if projection.worktree != "" {
		entry.Worktree = projection.worktree
		entry.HasWorktree = true
	}
	if projection.startedAt != nil {
		entry.StartedAt = projection.startedAt
	}
	if projection.state != "" {
		entry.State = projection.state
	}
	if projection.task.ID.String() != "" {
		entry.Task = projection.task
		entry.TaskTitle = projection.task.Title
		entry.IssueStatus = projection.task.Status
		entry.Priority = projection.task.Priority
		entry.Type = projection.task.Type
		entry.HasWorktree = entry.HasWorktree || projection.task.HasWorktree
		entry.GitAheadCount = projection.task.GitAheadCount
		entry.GitBehindCount = projection.task.GitBehindCount
		entry.HasUncommittedChanges = projection.task.HasUncommittedChanges
		entry.HasConflicts = projection.task.HasConflicts
		entry.GitAdditions = projection.task.GitAdditions
		entry.GitDeletions = projection.task.GitDeletions
	}
	return entry
}

func taskFromInventoryEntry(entry InventoryEntry) domain.Task {
	issueID := naming.IssueID(entry.IssueID)
	return domain.Task{
		ID:             issueID,
		Title:          entry.TaskTitle,
		Status:         entry.IssueStatus,
		Priority:       entry.Priority,
		Type:           entry.Type,
		HasTmuxSession: entry.HasTmuxSession,
		HasWorktree:    entry.HasWorktree,
		Session: &domain.Session{
			IssueID:   issueID,
			State:     entry.State,
			StartedAt: entry.StartedAt,
			Worktree:  entry.Worktree,
		},
	}
}

func projectIDForPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	projectID, err := config.ProjectIDForRoot(path)
	if err != nil {
		return ""
	}
	return protocol.NormalizeProjectID(projectID)
}

func taskWorktree(task domain.Task) string {
	if task.Session != nil && strings.TrimSpace(task.Session.Worktree) != "" {
		return strings.TrimSpace(task.Session.Worktree)
	}
	return ""
}

func projectPathForSessionPrefix(entry InventoryEntry, projectDirs []string) string {
	prefix := strings.TrimSpace(entry.ProjectID)
	if prefix == "" {
		return ""
	}
	for _, projectDir := range projectDirs {
		projectDir = strings.TrimSpace(projectDir)
		if projectDir == "" {
			continue
		}
		if naming.ProjectSessionPrefix(projectDir) == prefix {
			return projectDir
		}
	}
	return ""
}

func inferProjectPath(worktree string, projectDirs []string) string {
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if worktree == "." || worktree == "" {
		return ""
	}
	best := ""
	for _, projectDir := range projectDirs {
		cleanProjectDir := filepath.Clean(strings.TrimSpace(projectDir))
		if cleanProjectDir == "" {
			continue
		}
		if strings.HasPrefix(worktree, cleanProjectDir+string(filepath.Separator)) || worktree == cleanProjectDir {
			if len(cleanProjectDir) > len(best) {
				best = cleanProjectDir
			}
		}
	}
	return best
}

func errInventoryUnavailable() error {
	return &inventoryUnavailableError{}
}

type inventoryUnavailableError struct{}

func (*inventoryUnavailableError) Error() string {
	return "tmux inventory is unavailable"
}

func intMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type DaemonSnapshotSource struct {
	projectDirs []string
	logger      *slog.Logger
}

func NewDaemonSnapshotSource(projectDirs []string, logger *slog.Logger) *DaemonSnapshotSource {
	if logger == nil {
		logger = slog.Default()
	}
	limited := append([]string(nil), projectDirs...)
	if len(limited) > defaultInventoryProjectLimit {
		limited = limited[:defaultInventoryProjectLimit]
	}
	return &DaemonSnapshotSource{projectDirs: limited, logger: logger}
}

func (s *DaemonSnapshotSource) ListProjectSnapshots(ctx context.Context) ([]ProjectInventorySnapshot, error) {
	if s == nil {
		return nil, nil
	}
	type projectResult struct {
		snapshot ProjectInventorySnapshot
		ok       bool
	}
	results := make([]projectResult, len(s.projectDirs))
	var wg sync.WaitGroup
	for i, projectDir := range s.projectDirs {
		i, projectDir := i, projectDir
		wg.Add(1)
		go func() {
			defer wg.Done()
			projectID := projectIDForPath(projectDir)
			if projectID == "" {
				return
			}
			socketPath := config.DaemonSocketPathFor(projectDir)
			client := daemonclient.New(transport.NewClient(socketPath)).WithProjectID(projectID)
			snapshot, err := client.ListTasksSnapshot(ctx)
			if err != nil {
				if s.logger != nil {
					s.logger.Debug("global selector project snapshot failed", "project_dir", projectDir, "project_id", projectID, "error", err)
				}
				return
			}
			results[i] = projectResult{
				snapshot: ProjectInventorySnapshot{
					ProjectID:   projectID,
					ProjectPath: projectDir,
					Tasks:       snapshot.Tasks,
				},
				ok: true,
			}
		}()
	}
	wg.Wait()
	out := make([]ProjectInventorySnapshot, 0, len(results))
	for _, result := range results {
		if result.ok {
			out = append(out, result.snapshot)
		}
	}
	return out, nil
}
