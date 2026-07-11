package tmuxselector

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	defaultInventorySessionLimit          = 200
	defaultInventoryProjectLimit          = 64
	defaultInventoryProjectSnapshotBudget = 750 * time.Millisecond
	currentSessionEnvKey                  = "AZEDARACH_TMUX_CURRENT_SESSION"
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

type taskSnapshotReader interface {
	GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnly(context.Context, []string) (daemonclient.TaskSnapshot, error)
	GetManyTaskSnapshotWithAncestors(context.Context, []string) (daemonclient.TaskSnapshot, error)
	ListTasksSnapshot(context.Context) (daemonclient.TaskSnapshot, error)
}

type ProjectInventorySnapshot struct {
	ProjectID   string
	ProjectPath string
	Tasks       []domain.Task
}

// GlobalInventoryLoader builds inventory from live tmux first, then projection metadata.
type GlobalInventoryLoader struct {
	tmux                SessionInventory
	source              ProjectSnapshotSource
	projectDirs         []string
	projectDirsProvider func() []string
	logger              *slog.Logger
	limit               int
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

func WithProjectDirsProvider(provider func() []string) GlobalInventoryOption {
	return func(l *GlobalInventoryLoader) {
		l.projectDirsProvider = provider
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
	return NewGlobalInventoryLoader(
		tmuxInventory,
		logger,
		WithProjectDirsProvider(KnownProjectDirs),
		WithProjectSnapshotSource(NewDaemonSnapshotSource(nil, logger)),
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
	start := time.Now()
	live, err := l.tmux.ListSessionInfos(ctx)
	if err != nil {
		if l.logger != nil {
			l.logger.Warn("global selector full snapshot tmux load failed", "elapsed_ms", time.Since(start).Milliseconds(), "error", err)
		}
		return Snapshot{}, err
	}
	if len(live) > l.limit {
		live = live[:l.limit]
	}
	snapshot := l.snapshotFromLive(ctx, live, nil, true)
	projectDirs := l.projectDirsForLiveSessions(live)
	projections, tasks := l.loadProjectionsForEntries(ctx, projectDirs, snapshot.Entries)
	enriched := l.enrichEntries(snapshot, projections, tasks, projectDirs)
	if l.logger != nil {
		l.logger.Info("global selector full snapshot loaded",
			"elapsed_ms", time.Since(start).Milliseconds(),
			"session_count", len(enriched.Entries),
			"project_count", len(projectDirs),
		)
	}
	return enriched, nil
}

func (l *GlobalInventoryLoader) ListLiveSnapshot(ctx context.Context) (Snapshot, error) {
	if l == nil || l.tmux == nil {
		return Snapshot{}, errInventoryUnavailable()
	}
	start := time.Now()
	live, err := l.tmux.ListSessionInfos(ctx)
	if err != nil {
		if l.logger != nil {
			l.logger.Warn("global selector live snapshot tmux load failed", "elapsed_ms", time.Since(start).Milliseconds(), "error", err)
		}
		return Snapshot{}, err
	}
	if len(live) > l.limit {
		live = live[:l.limit]
	}
	snapshot := l.snapshotFromLive(ctx, live, nil, true)
	if l.logger != nil {
		l.logger.Info("global selector live snapshot loaded",
			"elapsed_ms", time.Since(start).Milliseconds(),
			"session_count", len(snapshot.Entries),
		)
	}
	return snapshot, nil
}

func (l *GlobalInventoryLoader) EnrichSnapshot(ctx context.Context, snapshot Snapshot) (Snapshot, error) {
	if l == nil {
		return snapshot, errInventoryUnavailable()
	}
	start := time.Now()
	projectDirs := l.projectDirsForEntries(snapshot.Entries)
	projections, tasks := l.loadProjectionsForEntries(ctx, projectDirs, snapshot.Entries)
	enriched := l.enrichEntries(snapshot, projections, tasks, projectDirs)
	if l.logger != nil {
		l.logger.Info("global selector snapshot enriched",
			"elapsed_ms", time.Since(start).Milliseconds(),
			"session_count", len(enriched.Entries),
			"project_count", len(projectDirs),
			"ancestor_count", len(enriched.TreeTasks),
		)
	}
	return enriched, nil
}

func (l *GlobalInventoryLoader) configuredProjectDirs() []string {
	if l == nil {
		return nil
	}
	if len(l.projectDirs) > 0 {
		return append([]string(nil), l.projectDirs...)
	}
	if l.projectDirsProvider == nil {
		return nil
	}
	return append([]string(nil), l.projectDirsProvider()...)
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
			SessionID:         sessionName,
			ProjectID:         parsed.Project,
			Worktree:          strings.TrimSpace(info.Path),
			StartedAt:         info.CreatedAt,
			LastAttachedAt:    info.LastAttachedAt,
			LastUpdatedAt:     info.LastAttachedAt,
			TmuxAttached:      info.AttachedCount > 0,
			TmuxAttachedCount: info.AttachedCount,
			HasTmuxSession:    true,
			HasWorktree:       strings.TrimSpace(info.Path) != "",
			State:             domain.SessionWaiting,
			IssueStatus:       domain.StatusInProgress,
			Priority:          domain.P2,
			Type:              domain.TypeTask,
			TaskTitle:         sessionName,
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

func (l *GlobalInventoryLoader) enrichEntries(snapshot Snapshot, projections map[string]projectedInventory, tasks projectTaskIndex, projectDirs []string) Snapshot {
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
			entry.ProjectPath = projectPathForSessionPrefix(entry, projectDirs)
		}
		if entry.ProjectPath == "" && entry.Worktree != "" {
			entry.ProjectPath = inferProjectPath(entry.Worktree, projectDirs)
		}
		entries = append(entries, entry)
	}
	enriched := l.snapshotFromEntries(entries, false)
	enriched.CurrentSessionID = snapshot.CurrentSessionID
	enriched.TreeTasks = ancestorTasksForEntries(entries, tasks)
	return enriched
}

func (l *GlobalInventoryLoader) snapshotFromEntries(entries []InventoryEntry, enriching bool) Snapshot {
	sort.SliceStable(entries, func(i, j int) bool {
		leftAttention := inventoryHumanAttentionRank(entries[i])
		rightAttention := inventoryHumanAttentionRank(entries[j])
		if leftAttention != rightAttention {
			return leftAttention > rightAttention
		}
		leftCurrent := entries[i].TmuxAttached || entries[i].TmuxAttachedCount > 0
		rightCurrent := entries[j].TmuxAttached || entries[j].TmuxAttachedCount > 0
		if leftCurrent != rightCurrent {
			return leftCurrent
		}
		if entries[i].TmuxAttachedCount != entries[j].TmuxAttachedCount {
			return entries[i].TmuxAttachedCount > entries[j].TmuxAttachedCount
		}
		leftAttached := entries[i].LastAttachedAt != nil
		rightAttached := entries[j].LastAttachedAt != nil
		if leftAttached != rightAttached {
			return leftAttached
		}
		if leftAttached && rightAttached && !entries[i].LastAttachedAt.Equal(*entries[j].LastAttachedAt) {
			return entries[i].LastAttachedAt.After(*entries[j].LastAttachedAt)
		}
		leftStarted := entries[i].StartedAt != nil
		rightStarted := entries[j].StartedAt != nil
		if leftStarted != rightStarted {
			return leftStarted
		}
		if leftStarted && rightStarted && !entries[i].StartedAt.Equal(*entries[j].StartedAt) {
			return entries[i].StartedAt.After(*entries[j].StartedAt)
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

func inventoryHumanAttentionRank(entry InventoryEntry) int {
	// Live tmux discovery uses a provisional waiting state until daemon
	// enrichment arrives. Only durable task metadata is authoritative enough to
	// promote a selector entry as requiring human attention.
	if entry.Task.ID.IsZero() {
		return 0
	}
	return domain.HumanAttentionRank(entry.Task)
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
	activity    string
	activitySrc string
	startedAt   *time.Time
	worktree    string
	task        domain.Task
}

type projectTaskIndex struct {
	byScope map[string]map[string]domain.Task
	global  map[string]domain.Task
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
	projectDirs := l.configuredProjectDirs()
	if len(projectDirs) == 0 || len(entries) == 0 {
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
	for _, projectDir := range projectDirs {
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
		entry := InventoryEntry{
			SessionID:         sessionName,
			Worktree:          strings.TrimSpace(info.Path),
			TmuxAttached:      info.AttachedCount > 0,
			TmuxAttachedCount: info.AttachedCount,
			LastAttachedAt:    info.LastAttachedAt,
			StartedAt:         info.CreatedAt,
			LastUpdatedAt:     info.LastAttachedAt,
		}
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

func (l *GlobalInventoryLoader) loadProjectionsForEntries(ctx context.Context, projectDirs []string, entries []InventoryEntry) (map[string]projectedInventory, projectTaskIndex) {
	out := map[string]projectedInventory{}
	tasks := projectTaskIndex{
		byScope: map[string]map[string]domain.Task{},
		global:  map[string]domain.Task{},
	}
	source := l.source
	if source == nil {
		return out, tasks
	}
	if _, ok := source.(*DaemonSnapshotSource); ok {
		source = NewDaemonSnapshotSourceForTasks(projectDirs, taskIDsByProjectDir(entries, projectDirs), l.logger)
	}
	start := time.Now()
	snapshots, err := source.ListProjectSnapshots(ctx)
	if err != nil {
		if l.logger != nil {
			l.logger.Debug("global selector daemon snapshot enrichment failed",
				"elapsed_ms", time.Since(start).Milliseconds(),
				"project_count", len(projectDirs),
				"error", err,
			)
		}
		return out, tasks
	}
	for _, snapshot := range snapshots {
		projectID := protocol.NormalizeProjectID(snapshot.ProjectID)
		projectPath := strings.TrimSpace(snapshot.ProjectPath)
		for _, task := range snapshot.Tasks {
			issueID := task.ID.String()
			if issueID == "" {
				continue
			}
			tasks.add(projectID, projectPath, issueID, task)
			projection := projectedInventory{
				projectID:   projectID,
				projectPath: projectPath,
				issueID:     issueID,
				task:        task,
			}
			if task.Session != nil {
				projection.state = task.Session.State
				projection.activity = task.Session.Activity
				projection.activitySrc = task.Session.ActivitySource
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
	if l.logger != nil {
		l.logger.Info("global selector daemon snapshots loaded",
			"elapsed_ms", time.Since(start).Milliseconds(),
			"project_count", len(projectDirs),
			"snapshot_count", len(snapshots),
			"projection_count", len(out),
		)
	}
	return out, tasks
}

func taskIDsByProjectDir(entries []InventoryEntry, projectDirs []string) map[string][]string {
	if len(entries) == 0 || len(projectDirs) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, entry := range entries {
		issueID := entryIssueIDForInventory(entry)
		if issueID == "" {
			continue
		}
		projectDir := inventoryEntryProjectDir(entry, projectDirs)
		if projectDir == "" {
			continue
		}
		key := cleanProjectDirKey(projectDir)
		if key == "" {
			continue
		}
		out[key] = append(out[key], issueID)
	}
	return normalizeTaskIDsByProjectDir(out)
}

func inventoryEntryProjectDir(entry InventoryEntry, projectDirs []string) string {
	if projectPath := strings.TrimSpace(entry.ProjectPath); projectPath != "" {
		return projectPath
	}
	if projectPath := projectPathForSessionPrefix(entry, projectDirs); projectPath != "" {
		return projectPath
	}
	if worktree := strings.TrimSpace(entry.Worktree); worktree != "" {
		return inferProjectPath(worktree, projectDirs)
	}
	return ""
}

func entryIssueIDForInventory(entry InventoryEntry) string {
	if issueID := strings.TrimSpace(entry.IssueID); issueID != "" {
		return issueID
	}
	if issueID := strings.TrimSpace(entry.Task.ID.String()); issueID != "" {
		return issueID
	}
	if parsed, ok := ParseAzedarachSessionName(strings.TrimSpace(entry.SessionID)); ok && !parsed.IssueID.IsZero() {
		return parsed.IssueID.String()
	}
	return ""
}

func (i projectTaskIndex) add(projectID, projectPath, issueID string, task domain.Task) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return
	}
	for _, scope := range taskScopeKeys(projectID, projectPath) {
		if i.byScope[scope] == nil {
			i.byScope[scope] = map[string]domain.Task{}
		}
		i.byScope[scope][issueID] = task
	}
	if len(taskScopeKeys(projectID, projectPath)) == 0 {
		i.global[issueID] = task
	}
}

func (i projectTaskIndex) lookup(entry InventoryEntry, issueID string) (domain.Task, bool) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return domain.Task{}, false
	}
	for _, scope := range taskScopeKeys(entry.ProjectID, entry.ProjectPath) {
		if tasksByIssue := i.byScope[scope]; tasksByIssue != nil {
			if task, ok := tasksByIssue[issueID]; ok {
				return task, true
			}
		}
	}
	if len(taskScopeKeys(entry.ProjectID, entry.ProjectPath)) > 0 {
		return domain.Task{}, false
	}
	task, ok := i.global[issueID]
	return task, ok
}

func taskScopeKeys(projectID, projectPath string) []string {
	keys := make([]string, 0, 2)
	if projectPath = strings.TrimSpace(projectPath); projectPath != "" {
		if abs, err := filepath.Abs(projectPath); err == nil {
			projectPath = abs
		}
		keys = append(keys, "path:"+filepath.Clean(projectPath))
	}
	if projectID = protocol.TrimProjectID(projectID); projectID != "" {
		keys = append(keys, "id:"+projectID)
	}
	return keys
}

func ancestorTasksForEntries(entries []InventoryEntry, tasks projectTaskIndex) []domain.Task {
	if len(entries) == 0 || (len(tasks.byScope) == 0 && len(tasks.global) == 0) {
		return nil
	}
	outByIssue := map[string]domain.Task{}
	for _, entry := range entries {
		for parentID := entryParentID(entry); parentID != ""; {
			task, ok := tasks.lookup(entry, parentID)
			if !ok {
				break
			}
			outByIssue[parentID] = task
			parentID = taskParentID(task)
		}
	}
	if len(outByIssue) == 0 {
		return nil
	}
	out := make([]domain.Task, 0, len(outByIssue))
	for _, task := range outByIssue {
		out = append(out, task)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ID.String() < out[j].ID.String()
	})
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
	if projection.activity != "" {
		entry.Activity = projection.activity
	}
	if projection.activitySrc != "" {
		entry.ActivitySource = projection.activitySrc
	}
	if projection.task.ID.String() != "" {
		entry.Task = projection.task
		entry.TaskTitle = projection.task.Title
		entry.IssueStatus = projection.task.Status
		entry.Priority = projection.task.Priority
		entry.Type = projection.task.Type
		if !projection.task.RuntimeUpdatedAt.IsZero() {
			lastUpdatedAt := projection.task.RuntimeUpdatedAt
			entry.LastUpdatedAt = &lastUpdatedAt
		} else if !projection.task.UpdatedAt.IsZero() {
			lastUpdatedAt := projection.task.UpdatedAt
			entry.LastUpdatedAt = &lastUpdatedAt
		}
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
		ID:               issueID,
		Title:            entry.TaskTitle,
		Status:           entry.IssueStatus,
		Priority:         entry.Priority,
		Type:             entry.Type,
		RuntimeUpdatedAt: valueOrZeroTime(entry.LastUpdatedAt),
		HasTmuxSession:   entry.HasTmuxSession,
		HasWorktree:      entry.HasWorktree,
		Session: &domain.Session{
			IssueID:        issueID,
			State:          entry.State,
			Activity:       entry.Activity,
			ActivitySource: entry.ActivitySource,
			StartedAt:      entry.StartedAt,
			UpdatedAt:      valueOrZeroTime(entry.LastUpdatedAt),
			Worktree:       entry.Worktree,
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
	projectDirs           []string
	taskIDsByDir          map[string][]string
	logger                *slog.Logger
	projectSnapshotBudget time.Duration
	projectSnapshotLoader func(context.Context, string) (ProjectInventorySnapshot, bool)
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

func NewDaemonSnapshotSourceForTasks(projectDirs []string, taskIDsByDir map[string][]string, logger *slog.Logger) *DaemonSnapshotSource {
	source := NewDaemonSnapshotSource(projectDirs, logger)
	source.taskIDsByDir = normalizeTaskIDsByProjectDir(taskIDsByDir)
	return source
}

func (s *DaemonSnapshotSource) ListProjectSnapshots(ctx context.Context) ([]ProjectInventorySnapshot, error) {
	if s == nil {
		return nil, nil
	}
	start := time.Now()
	type projectResult struct {
		index    int
		snapshot ProjectInventorySnapshot
		ok       bool
	}
	budget := s.snapshotBudget()
	results := make(chan projectResult, len(s.projectDirs))
	pending := make(map[int]string, len(s.projectDirs))
	for i, projectDir := range s.projectDirs {
		i, projectDir := i, projectDir
		pending[i] = projectDir
		go func() {
			projectCtx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			snapshot, ok := s.loadProjectSnapshot(projectCtx, projectDir)
			results <- projectResult{index: i, snapshot: snapshot, ok: ok}
		}()
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	completed := make([]projectResult, 0, len(s.projectDirs))
	timeoutCount := 0
	for len(pending) > 0 {
		select {
		case result := <-results:
			delete(pending, result.index)
			if result.ok {
				completed = append(completed, result)
			}
		case <-timer.C:
			timeoutCount = len(pending)
			for _, projectDir := range pending {
				if s.logger != nil {
					s.logger.Warn("global selector project snapshot timed out",
						"elapsed_ms", time.Since(start).Milliseconds(),
						"project_dir", projectDir,
						"budget_ms", budget.Milliseconds(),
					)
				}
			}
			pending = nil
		case <-ctx.Done():
			timeoutCount = len(pending)
			pending = nil
		}
	}
	sort.SliceStable(completed, func(i, j int) bool {
		return completed[i].index < completed[j].index
	})
	out := make([]ProjectInventorySnapshot, 0, len(completed))
	for _, result := range completed {
		if result.ok {
			out = append(out, result.snapshot)
		}
	}
	if s.logger != nil {
		s.logger.Info("global selector project snapshots complete",
			"elapsed_ms", time.Since(start).Milliseconds(),
			"project_count", len(s.projectDirs),
			"snapshot_count", len(out),
			"timeout_count", timeoutCount,
			"fallback_count", len(s.projectDirs)-len(out),
			"budget_ms", budget.Milliseconds(),
		)
	}
	return out, nil
}

func (s *DaemonSnapshotSource) snapshotBudget() time.Duration {
	if s != nil && s.projectSnapshotBudget > 0 {
		return s.projectSnapshotBudget
	}
	return defaultInventoryProjectSnapshotBudget
}

func (s *DaemonSnapshotSource) loadProjectSnapshot(ctx context.Context, projectDir string) (ProjectInventorySnapshot, bool) {
	if s == nil {
		return ProjectInventorySnapshot{}, false
	}
	if s.projectSnapshotLoader != nil {
		return s.projectSnapshotLoader(ctx, projectDir)
	}
	projectStart := time.Now()
	projectID := projectIDForPath(projectDir)
	if projectID == "" {
		return ProjectInventorySnapshot{}, false
	}
	socketPath := config.DaemonSocketPathFor(projectDir)
	if err := validateSharedDaemonExecutable(socketPath); err != nil {
		if s.logger != nil {
			s.logger.Warn("global selector project snapshot blocked by daemon fence",
				"project_dir", projectDir,
				"project_id", projectID,
				"error", err,
			)
		}
		return ProjectInventorySnapshot{}, false
	}
	client := daemonclient.New(transport.NewClient(socketPath)).WithProjectID(projectID)
	taskIDs := s.taskIDsForProjectDir(projectDir)
	snapshot, err := s.loadTaskSnapshot(ctx, client, taskIDs)
	if err != nil {
		if s.logger != nil {
			s.logger.Debug("global selector project snapshot failed",
				"elapsed_ms", time.Since(projectStart).Milliseconds(),
				"project_dir", projectDir,
				"project_id", projectID,
				"requested_task_count", len(taskIDs),
				"error", err,
			)
		}
		return ProjectInventorySnapshot{}, false
	}
	if s.logger != nil {
		s.logger.Info("global selector project snapshot loaded",
			"elapsed_ms", time.Since(projectStart).Milliseconds(),
			"project_dir", projectDir,
			"project_id", projectID,
			"task_count", len(snapshot.Tasks),
			"requested_task_count", len(taskIDs),
		)
	}
	return ProjectInventorySnapshot{
		ProjectID:   projectID,
		ProjectPath: projectDir,
		Tasks:       snapshot.Tasks,
	}, true
}

func (s *DaemonSnapshotSource) loadTaskSnapshot(ctx context.Context, client taskSnapshotReader, taskIDs []string) (daemonclient.TaskSnapshot, error) {
	if len(taskIDs) == 0 {
		return client.ListTasksSnapshot(ctx)
	}
	return client.GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnly(ctx, taskIDs)
}

func (s *DaemonSnapshotSource) taskIDsForProjectDir(projectDir string) []string {
	if s == nil || len(s.taskIDsByDir) == 0 {
		return nil
	}
	keys := []string{
		cleanProjectDirKey(projectDir),
		strings.TrimSpace(projectDir),
	}
	for _, key := range keys {
		if taskIDs := s.taskIDsByDir[key]; len(taskIDs) > 0 {
			return append([]string(nil), taskIDs...)
		}
	}
	return nil
}

func normalizeTaskIDsByProjectDir(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for projectDir, taskIDs := range in {
		key := cleanProjectDirKey(projectDir)
		if key == "" {
			continue
		}
		seen := map[string]struct{}{}
		normalized := make([]string, 0, len(taskIDs))
		for _, taskID := range taskIDs {
			taskID = strings.TrimSpace(taskID)
			if taskID == "" {
				continue
			}
			if _, exists := seen[taskID]; exists {
				continue
			}
			seen[taskID] = struct{}{}
			normalized = append(normalized, taskID)
		}
		if len(normalized) > 0 {
			out[key] = normalized
		}
	}
	return out
}

func cleanProjectDirKey(projectDir string) string {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return ""
	}
	if abs, err := filepath.Abs(projectDir); err == nil {
		projectDir = abs
	}
	return filepath.Clean(projectDir)
}
