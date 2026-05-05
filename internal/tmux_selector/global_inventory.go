package tmuxselector

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

const (
	defaultInventorySessionLimit = 200
	defaultInventoryProjectLimit = 64
)

// SessionInventory lists live tmux sessions for the global loader.
type SessionInventory interface {
	ListSessionInfos(context.Context) ([]tmux.SessionInfo, error)
}

// RuntimeProjectionStore is the projection read surface used to enrich live tmux inventory.
type RuntimeProjectionStore interface {
	ListProjectIDs(context.Context) ([]string, error)
	ListSessionStates(context.Context, string) ([]daemonstate.Session, error)
	ListWorktreeStates(context.Context, string) ([]daemonstate.WorktreeState, error)
}

// GlobalInventoryLoader builds inventory from live tmux first, then projection metadata.
type GlobalInventoryLoader struct {
	tmux        SessionInventory
	stores      []RuntimeProjectionStore
	projectDirs []string
	logger      *slog.Logger
	limit       int
}

type GlobalInventoryOption func(*GlobalInventoryLoader)

func WithRuntimeProjectionStores(stores ...RuntimeProjectionStore) GlobalInventoryOption {
	return func(l *GlobalInventoryLoader) {
		l.stores = append([]RuntimeProjectionStore(nil), stores...)
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
	stores := make([]RuntimeProjectionStore, 0, intMin(len(projectDirs), defaultInventoryProjectLimit))
	for _, projectDir := range projectDirs {
		stores = append(stores, daemonstate.NewRuntimeStateStore(projectDir, logger))
		if len(stores) >= defaultInventoryProjectLimit {
			break
		}
	}
	return NewGlobalInventoryLoader(
		tmuxInventory,
		logger,
		WithProjectDirs(projectDirs...),
		WithRuntimeProjectionStores(stores...),
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
	projections := l.loadProjections(ctx)
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
		if !ok || parsed.IssueID.IsZero() {
			continue
		}
		entry := InventoryEntry{
			SessionID:      sessionName,
			IssueID:        parsed.IssueID.String(),
			ProjectID:      parsed.Project,
			Worktree:       strings.TrimSpace(info.Path),
			StartedAt:      info.CreatedAt,
			HasTmuxSession: true,
			HasWorktree:    strings.TrimSpace(info.Path) != "",
			State:          domain.SessionWaiting,
			IssueStatus:    domain.StatusInProgress,
			TaskTitle:      parsed.IssueID.String(),
		}
		if projection, ok := projections[sessionName]; ok {
			entry = mergeProjectedInventory(entry, projection)
		}
		if entry.ProjectPath == "" && entry.Worktree != "" {
			entry.ProjectPath = inferProjectPath(entry.Worktree, l.projectDirs)
		}
		if entry.ProjectPath != "" {
			entry.TaskTitle = entry.IssueID + " (" + filepath.Base(entry.ProjectPath) + ")"
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		leftKnown := entries[i].ProjectPath != ""
		rightKnown := entries[j].ProjectPath != ""
		if leftKnown != rightKnown {
			return leftKnown
		}
		if entries[i].ProjectPath != entries[j].ProjectPath {
			return entries[i].ProjectPath < entries[j].ProjectPath
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
	}, nil
}

type projectedInventory struct {
	projectID   string
	projectPath string
	issueID     string
	state       domain.SessionState
	startedAt   *time.Time
	worktree    string
}

func (l *GlobalInventoryLoader) loadProjections(ctx context.Context) map[string]projectedInventory {
	out := map[string]projectedInventory{}
	for _, store := range l.stores {
		if store == nil {
			continue
		}
		projectIDs, err := store.ListProjectIDs(ctx)
		if err != nil {
			continue
		}
		if len(projectIDs) == 0 {
			projectIDs = []string{protocol.DefaultProjectID}
		}
		for _, projectID := range projectIDs {
			projectID = protocol.NormalizeProjectID(projectID)
			worktreeByIssue := map[string]string{}
			if worktrees, err := store.ListWorktreeStates(ctx, projectID); err == nil {
				for _, worktree := range worktrees {
					if strings.TrimSpace(worktree.IssueID) != "" {
						worktreeByIssue[worktree.IssueID] = strings.TrimSpace(worktree.Path)
					}
				}
			}
			sessions, err := store.ListSessionStates(ctx, projectID)
			if err != nil {
				continue
			}
			projectPath := projectPathForID(projectID, l.projectDirs)
			for _, session := range sessions {
				sessionID := strings.TrimSpace(session.ID)
				issueID := strings.TrimSpace(session.IssueID)
				if sessionID == "" || issueID == "" {
					continue
				}
				out[sessionID] = projectedInventory{
					projectID:   projectID,
					projectPath: projectPath,
					issueID:     issueID,
					state:       domainStateFromProjection(session),
					startedAt:   session.StartedAt,
					worktree:    worktreeByIssue[issueID],
				}
			}
		}
	}
	return out
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
	return entry
}

func taskFromInventoryEntry(entry InventoryEntry) domain.Task {
	issueID := naming.IssueID(entry.IssueID)
	return domain.Task{
		ID:             issueID,
		Title:          entry.TaskTitle,
		Status:         entry.IssueStatus,
		Priority:       domain.P2,
		Type:           domain.TypeTask,
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

func domainStateFromProjection(session daemonstate.Session) domain.SessionState {
	switch session.ObservedState {
	case daemonstate.SessionStateStarting:
		return domain.SessionBusy
	case daemonstate.SessionStateAttached:
		return domain.SessionWaiting
	case daemonstate.SessionStatePaused:
		return domain.SessionPaused
	case daemonstate.SessionStateStopped:
		return domain.SessionDone
	}
	switch session.State {
	case daemonstate.SessionStateStarting:
		return domain.SessionBusy
	case daemonstate.SessionStateAttached:
		return domain.SessionWaiting
	case daemonstate.SessionStatePaused:
		return domain.SessionPaused
	case daemonstate.SessionStateStopped:
		return domain.SessionDone
	default:
		return domain.SessionWaiting
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

func projectPathForID(projectID string, projectDirs []string) string {
	projectID = protocol.NormalizeProjectID(projectID)
	for _, projectDir := range projectDirs {
		if projectIDForPath(projectDir) == projectID {
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
