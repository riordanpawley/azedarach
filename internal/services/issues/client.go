package issues

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/dbpathguard"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/observability/tracesqlite"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

// ProjectionExport is a transactionally consistent, fully hydrated snapshot
// consumed by the user-level cross-project projection.
type ProjectionExport struct {
	Tasks             []domain.Task
	Checkpoint        uint64
	SchemaVersion     int
	SchemaFingerprint string
}

// OrchestrationProjectionExport is a transactionally consistent snapshot of
// the bounded issue/runtime projection. Checkpoint is read in the same SQLite
// transaction as Tasks, so callers never label rows with a newer revision.
type OrchestrationProjectionExport struct {
	Tasks                    []domain.Task
	Checkpoint               uint64
	OpenIssueCount           int
	UnresolvedInteractionIDs map[string]struct{}
	Interactions             []domain.InteractionRequest
	InvestigationAcceptances map[string]domain.InvestigationAcceptance
}

// ExportOrchestrationProjection reads a bounded project graph plus its durable
// lifecycle/runtime context at one checkpoint. Candidate count bounds roots;
// complete root closures and dependency targets preserve readiness semantics.
func (c *Client) ExportOrchestrationProjection(ctx context.Context, projectID string, limit int) (OrchestrationProjectionExport, error) {
	db, err := c.dbHandle()
	if err != nil {
		return OrchestrationProjectionExport{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OrchestrationProjectionExport{}, fmt.Errorf("begin orchestration projection export: %w", err)
	}
	defer tx.Rollback()
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	checkpoint, err := projectionCompositeCheckpoint(ctx, tx, projectID)
	if err != nil {
		return OrchestrationProjectionExport{}, err
	}
	if limit <= 0 {
		limit = 50
	}
	issueIDs, err := c.projectOrchestrationContextIDs(ctx, tx, projectID, limit)
	if err != nil {
		return OrchestrationProjectionExport{}, c.wrapError("export-orchestration-projection", projectID, err)
	}
	tasks := []domain.Task{}
	if len(issueIDs) > 0 {
		tasks, err = c.queryTasksWithRuntimeProjection(ctx, tx, projectID, true, taskDependencyLoadAll, ArchiveExclude, issueIDs...)
		if err != nil {
			return OrchestrationProjectionExport{}, c.wrapError("export-orchestration-projection", projectID, err)
		}
	}
	interactions, unresolvedInteractionIDs, err := orchestrationInteractions(ctx, tx, issueIDs)
	if err != nil {
		return OrchestrationProjectionExport{}, c.wrapError("export-orchestration-interactions", projectID, err)
	}
	investigationAcceptances, err := c.investigationAcceptances(ctx, tx, tasks)
	if err != nil {
		return OrchestrationProjectionExport{}, err
	}
	var openIssueCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM issues
		WHERE visibility = 'live'
		  AND disposition = 'ready'
		  AND engagement = 'idle'
	`).Scan(&openIssueCount); err != nil {
		return OrchestrationProjectionExport{}, c.wrapError("count-open-orchestration-issues", projectID, err)
	}
	divergent, err := runtimeDivergentIssueIDs(ctx, tx)
	if err != nil {
		return OrchestrationProjectionExport{}, err
	}
	for i := range tasks {
		if _, quarantined := divergent[tasks[i].ID.String()]; quarantined {
			tasks[i].Session, tasks[i].HasTmuxSession = nil, false
		}
	}
	if err := tx.Commit(); err != nil {
		return OrchestrationProjectionExport{}, fmt.Errorf("commit orchestration projection export read: %w", err)
	}
	return OrchestrationProjectionExport{
		Tasks: tasks, Checkpoint: checkpoint, OpenIssueCount: openIssueCount,
		UnresolvedInteractionIDs: unresolvedInteractionIDs,
		Interactions:             interactions,
		InvestigationAcceptances: investigationAcceptances,
	}, nil
}

func orchestrationInteractions(ctx context.Context, q sqlIssueDBTX, issueIDs []string) ([]domain.InteractionRequest, map[string]struct{}, error) {
	out := make(map[string]struct{})
	issueIDs = uniqueIssueIDStrings(issueIDs)
	if len(issueIDs) == 0 {
		return []domain.InteractionRequest{}, out, nil
	}
	args := make([]any, 0, len(issueIDs)+3)
	for _, id := range issueIDs {
		args = append(args, id)
	}
	args = append(args, string(domain.InteractionOpen), string(domain.InteractionDiscussing), string(domain.InteractionAnswerProposed))
	rows, err := q.QueryContext(ctx, `
		SELECT request_json FROM interaction_requests
		WHERE issue_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(issueIDs)), ",")+`)
		  AND state IN (?,?,?)
		ORDER BY created_at, id
	`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	interactions := make([]domain.InteractionRequest, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, nil, err
		}
		request, err := decodeInteractionRequest(raw)
		if err != nil {
			return nil, nil, err
		}
		interactions = append(interactions, request)
		if issueID := strings.TrimSpace(request.IssueID); issueID != "" {
			out[issueID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return interactions, out, nil
}

func (c *Client) projectOrchestrationContextIDs(ctx context.Context, q sqlIssueDBTX, projectID string, limit int) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
		WITH open_roots(id) AS (
			SELECT issue.id FROM issues issue INDEXED BY idx_issues_status_deleted_priority_updated
			WHERE issue.visibility = 'live'
			  AND issue.disposition = 'ready'
			  AND issue.engagement = 'idle'
			  AND NOT EXISTS (
				SELECT 1 FROM issue_dependencies parent
				JOIN issues parent_issue ON parent_issue.id = parent.depends_on_id
				  AND parent_issue.visibility = 'live'
				WHERE parent.issue_id = issue.id
				  AND parent.dependency_type IN (?, ?)
				  AND parent.tombstoned_at IS NULL
			  )
			ORDER BY issue.priority ASC, issue.updated_at ASC, issue.id ASC
			LIMIT ?
		), retained_roots(id) AS (
			SELECT issue.id FROM issues issue
			WHERE issue.visibility = 'live'
			  AND issue.disposition IN ('backlog', 'ready')
			  AND NOT (issue.disposition = 'ready' AND issue.engagement = 'idle')
			  AND NOT EXISTS (
				SELECT 1 FROM issue_dependencies parent
				JOIN issues parent_issue ON parent_issue.id = parent.depends_on_id
				  AND parent_issue.visibility = 'live'
				WHERE parent.issue_id = issue.id
				  AND parent.dependency_type IN (?, ?)
				  AND parent.tombstoned_at IS NULL
			  )
		), active_roots(id) AS (
			SELECT DISTINCT issue.id FROM issues issue
			JOIN daemon_session_projections session
			  ON session.project_id = ? AND session.issue_id = issue.id AND session.state != 'stopped'
			WHERE issue.visibility = 'live'
			  AND NOT EXISTS (
				SELECT 1 FROM issue_dependencies parent
				JOIN issues parent_issue ON parent_issue.id = parent.depends_on_id
				  AND parent_issue.visibility = 'live'
				WHERE parent.issue_id = issue.id
				  AND parent.dependency_type IN (?, ?)
				  AND parent.tombstoned_at IS NULL
			)
		), roots(id) AS (
			SELECT id FROM open_roots
			UNION SELECT id FROM retained_roots
			UNION SELECT id FROM active_roots
		), graph(id) AS (
			SELECT id FROM roots
			UNION SELECT closure.descendant_id FROM roots
			JOIN issue_graph_closure closure INDEXED BY idx_issue_graph_closure_ancestor
			  ON closure.ancestor_id = roots.id
			WHERE closure.project_id = ? AND closure.dependency_type = ?
		), context(id) AS (
			SELECT id FROM graph
			UNION SELECT dep.depends_on_id FROM graph
			JOIN issue_dependencies dep INDEXED BY idx_dependencies_issue_active_type
			  ON dep.issue_id = graph.id AND dep.tombstoned_at IS NULL
		)
		SELECT id FROM context ORDER BY id
	`, string(domain.DependencyParentChild), "parent_child", limit, string(domain.DependencyParentChild), "parent_child", projectID, string(domain.DependencyParentChild), "parent_child", issueGraphClosureProjectID, string(domain.DependencyParentChild))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit*2)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// ExportProjection reads the source contract, composite issue/runtime
// checkpoint, issues, dependencies, and runtime projections in one SQLite
// read transaction. A successful export therefore never acknowledges state
// newer than the rows it contains.
func (c *Client) ExportProjection(ctx context.Context, projectID string) (ProjectionExport, error) {
	db, err := c.dbHandle()
	if err != nil {
		return ProjectionExport{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectionExport{}, fmt.Errorf("begin projection export: %w", err)
	}
	defer tx.Rollback()
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	version, fingerprint, err := projectionSchemaContract(ctx, tx)
	if err != nil {
		return ProjectionExport{}, err
	}
	checkpoint, err := projectionCompositeCheckpoint(ctx, tx, projectID)
	if err != nil {
		return ProjectionExport{}, err
	}
	tasks, err := c.queryTasksWithRuntimeArchiveMode(ctx, tx, projectID, ArchiveInclude)
	if err != nil {
		return ProjectionExport{}, c.wrapError("export-projection", projectID, err)
	}
	divergent, err := runtimeDivergentIssueIDs(ctx, tx)
	if err != nil {
		return ProjectionExport{}, err
	}
	for i := range tasks {
		if _, quarantined := divergent[tasks[i].ID.String()]; quarantined {
			tasks[i].Session, tasks[i].HasTmuxSession = nil, false
		}
	}
	if err := tx.Commit(); err != nil {
		return ProjectionExport{}, fmt.Errorf("commit projection export read: %w", err)
	}
	return ProjectionExport{Tasks: tasks, Checkpoint: checkpoint, SchemaVersion: version, SchemaFingerprint: fingerprint}, nil
}

func runtimeDivergentIssueIDs(ctx context.Context, q sqlIssueQueryer) (map[string]struct{}, error) {
	rows, err := q.QueryContext(ctx, `SELECT issue_id FROM issue_runtime_divergences WHERE resolved_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

func (c *Client) RecordRuntimeDivergence(ctx context.Context, issueID, reason string) error {
	return c.withMutationLock(ctx, func(lockCtx context.Context) error {
		return c.recordRuntimeDivergenceLocked(lockCtx, issueID, reason)
	})
}

func (c *Client) recordRuntimeDivergenceLocked(ctx context.Context, issueID, reason string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO issue_runtime_divergences(issue_id,kind,reason,detected_at,resolved_at) VALUES(?,'lifecycle_runtime',?,?,NULL)
		ON CONFLICT(issue_id,kind) DO UPDATE SET reason=excluded.reason,detected_at=excluded.detected_at,resolved_at=NULL`, strings.TrimSpace(issueID), strings.TrimSpace(reason), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (c *Client) ClearRuntimeDivergence(ctx context.Context, issueID string) error {
	return c.withMutationLock(ctx, func(lockCtx context.Context) error {
		return c.clearRuntimeDivergenceLocked(lockCtx, issueID)
	})
}

func (c *Client) clearRuntimeDivergenceLocked(ctx context.Context, issueID string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE issue_runtime_divergences SET resolved_at=? WHERE issue_id=? AND kind='lifecycle_runtime' AND resolved_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(issueID))
	return err
}

func (c *Client) ListActiveRuntimeDivergenceIssueIDs(ctx context.Context) ([]string, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT issue_id FROM issue_runtime_divergences WHERE kind='lifecycle_runtime' AND resolved_at IS NULL ORDER BY issue_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func projectionSchemaContract(ctx context.Context, q sqlIssueDBTX) (int, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM schema_migrations ORDER BY id`)
	if err != nil {
		return 0, "", fmt.Errorf("read projection schema migrations: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, "", err
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return 0, "", err
	}
	rows, err = q.QueryContext(ctx, `SELECT type,name,COALESCE(sql,'') FROM sqlite_master WHERE type IN ('table','index','trigger','view') AND name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		return 0, "", fmt.Errorf("read projection schema contract: %w", err)
	}
	h := sha256.New()
	for _, id := range ids {
		fmt.Fprintf(h, "migration\x00%s\n", id)
	}
	for rows.Next() {
		var typ, name, ddl string
		if err = rows.Scan(&typ, &name, &ddl); err != nil {
			rows.Close()
			return 0, "", err
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", typ, name, strings.Join(strings.Fields(ddl), " "))
	}
	if err = rows.Close(); err != nil {
		return 0, "", err
	}
	return len(ids), fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

func projectionCompositeCheckpoint(ctx context.Context, q sqlIssueDBTX, projectID string) (uint64, error) {
	_ = projectID // The project database has one revision domain; project ID scopes runtime rows within it.
	var revision int64
	if err := q.QueryRowContext(ctx, `SELECT revision FROM projection_source_revision WHERE singleton=1`).Scan(&revision); err != nil {
		return 0, fmt.Errorf("read projection source revision: %w", err)
	}
	if revision < 0 {
		return 0, fmt.Errorf("invalid negative projection source revision %d", revision)
	}
	return uint64(revision), nil
}

type dependencyRemovalConfirmationKey struct{}
type parentChildOrphanConfirmationKey struct{}
type issueMutationLockKey struct{}
type issueMutationOperationKey struct{}
type issueMutationLockWaitHookKey struct{}

var issueOperationLocks sync.Map

type issueOperationLock struct {
	token       chan struct{}
	mu          sync.RWMutex
	holder      string
	holderSince time.Time
}

func newIssueOperationLock() *issueOperationLock {
	lock := &issueOperationLock{token: make(chan struct{}, 1)}
	lock.token <- struct{}{}
	return lock
}

func (l *issueOperationLock) currentHolder() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.holder
}

func (l *issueOperationLock) holderSnapshot() (string, time.Time) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.holder, l.holderSince
}

func (l *issueOperationLock) acquire(ctx context.Context, operation string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-l.token:
		if err := ctx.Err(); err != nil {
			l.token <- struct{}{}
			return "", err
		}
		l.mu.Lock()
		l.holder = operation
		l.holderSince = time.Now()
		l.mu.Unlock()
		return "", nil
	default:
	}
	holder := l.currentHolder()
	if hook, _ := ctx.Value(issueMutationLockWaitHookKey{}).(func(string, string)); hook != nil {
		hook(operation, holder)
	}
	select {
	case <-ctx.Done():
		return holder, ctx.Err()
	case <-l.token:
		if err := ctx.Err(); err != nil {
			l.token <- struct{}{}
			return holder, err
		}
		l.mu.Lock()
		l.holder = operation
		l.holderSince = time.Now()
		l.mu.Unlock()
		return holder, nil
	}
}

func (l *issueOperationLock) release() {
	l.mu.Lock()
	l.holder = ""
	l.holderSince = time.Time{}
	l.mu.Unlock()
	l.token <- struct{}{}
}

// ContextWithMutationOperation attaches a stable, low-cardinality operation
// name used to attribute issue-store mutation waiters and holders.
func ContextWithMutationOperation(ctx context.Context, operation string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return ctx
	}
	return context.WithValue(ctx, issueMutationOperationKey{}, operation)
}

// WithMutationLockWaitHookForTest installs a deterministic contention barrier.
// Production callers must not use it for mutation policy.
func WithMutationLockWaitHookForTest(ctx context.Context, hook func(string, string)) context.Context {
	return context.WithValue(ctx, issueMutationLockWaitHookKey{}, hook)
}

func mutationOperationFromContext(ctx context.Context) string {
	if ctx != nil {
		if operation, _ := ctx.Value(issueMutationOperationKey{}).(string); strings.TrimSpace(operation) != "" {
			return strings.TrimSpace(operation)
		}
	}
	pcs := make([]uintptr, 12)
	count := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:count])
	for {
		frame, more := frames.Next()
		name := frame.Function
		if !strings.HasSuffix(name, ".mutationOperationFromContext") &&
			!strings.HasSuffix(name, ".(*Client).WithMutationLock") &&
			!strings.HasSuffix(name, ".(*Client).withMutationLock") {
			if slash := strings.LastIndex(name, "/"); slash >= 0 {
				name = name[slash+1:]
			}
			if name != "" {
				return name
			}
		}
		if !more {
			break
		}
	}
	return "issue_store.mutation"
}

// ProjectionSourceVersion reports the applied project-schema migration count.
func (c *Client) ProjectionSourceVersion(ctx context.Context) (int, error) {
	db, err := c.dbHandle()
	if err != nil {
		return 0, err
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		return 0, fmt.Errorf("read projection source schema version: %w", err)
	}
	return count, nil
}

// ProjectionSourceCheckpoint returns the same durable revision used by
// ExportProjection. It is retained for callers that only need freshness.
func (c *Client) ProjectionSourceCheckpoint(ctx context.Context) (uint64, error) {
	db, err := c.dbHandle()
	if err != nil {
		return 0, err
	}
	var value int64
	if err := db.QueryRowContext(ctx, `SELECT revision FROM projection_source_revision WHERE singleton=1`).Scan(&value); err != nil {
		return 0, fmt.Errorf("read projection source checkpoint: %w", err)
	}
	if value < 0 {
		return 0, fmt.Errorf("invalid negative projection source revision %d", value)
	}
	return uint64(value), nil
}

type ArchiveMode string

const (
	ArchiveExclude  ArchiveMode = "exclude"
	ArchiveInclude  ArchiveMode = "include"
	ArchiveOnly     ArchiveMode = "only"
	refuseDBPathEnv             = dbpathguard.LegacyRefusePathEnv
)

func NormalizeArchiveMode(value string) ArchiveMode {
	switch ArchiveMode(strings.TrimSpace(strings.ToLower(value))) {
	case ArchiveInclude:
		return ArchiveInclude
	case ArchiveOnly:
		return ArchiveOnly
	default:
		return ArchiveExclude
	}
}

func archiveWhere(alias string, mode ArchiveMode) string {
	prefix := strings.TrimSpace(alias)
	if prefix != "" && !strings.HasSuffix(prefix, ".") {
		prefix += "."
	}
	switch NormalizeArchiveMode(string(mode)) {
	case ArchiveInclude:
		return "1=1"
	case ArchiveOnly:
		return prefix + "visibility = 'archived'"
	default:
		return prefix + "visibility = 'live'"
	}
}

type issueStateColumns struct {
	Disposition  string
	Engagement   string
	Visibility   string
	LegacyStatus string
	ArchivedAt   sql.NullString
}

type issueStateWriteValues struct {
	Disposition   string
	Engagement    string
	Visibility    string
	LegacyStatus  string
	Lifecycle     string
	ClosedOutcome string
	Review        string
	ArchivedAt    any
}

func issueStateFromColumns(issueID string, priority domain.Priority, cols issueStateColumns) (domain.IssueState, error) {
	disposition := strings.TrimSpace(cols.Disposition)
	if disposition == "" {
		return domain.IssueState{}, fmt.Errorf("issue %s missing canonical disposition", issueID)
	}
	state, err := domain.NewCanonicalIssueState(domain.CanonicalIssueStateParts{
		Disposition: domain.IssueDisposition(disposition),
		Engagement:  domain.IssueEngagement(strings.TrimSpace(cols.Engagement)),
		Visibility:  domain.IssueVisibility(strings.TrimSpace(cols.Visibility)),
	})
	if err != nil {
		return domain.IssueState{}, fmt.Errorf("issue %s canonical state: %w", issueID, err)
	}
	return state, nil
}

func issueStateFromStatus(status domain.Status) (domain.IssueState, error) {
	return domain.IssueStateFromStatus(status)
}

func issueStateWithLifecycle(state domain.IssueState, lifecycle domain.IssueWorkflow) (domain.IssueState, error) {
	switch lifecycle {
	case "":
		return state, nil
	case domain.IssueWorkflowBacklog, domain.IssueWorkflowOpen:
	default:
		return domain.IssueState{}, fmt.Errorf("unsupported lifecycle mutation: %s", lifecycle)
	}
	return domain.NewIssueState(domain.IssueStateParts{
		Workflow:     lifecycle,
		Review:       domain.IssueReviewNone,
		CloseOutcome: domain.IssueCloseNone,
		Archive:      state.Archive(),
	})
}

func issueStateWriteValuesFromState(state domain.IssueState, archivedAt any) issueStateWriteValues {
	return issueStateWriteValues{
		Disposition:   string(state.Disposition),
		Engagement:    string(state.Engagement),
		Visibility:    string(state.Visibility),
		LegacyStatus:  string(legacyStatusFromIssueState(state)),
		Lifecycle:     string(state.Workflow()),
		ClosedOutcome: string(state.CloseOutcome()),
		Review:        string(state.Review()),
		ArchivedAt:    archivedAt,
	}
}

func legacyStatusFromIssueState(state domain.IssueState) domain.Status {
	if state.Workflow() == domain.IssueWorkflowClosed {
		if state.CloseOutcome() == domain.IssueCloseCancelled {
			return domain.StatusCancelled
		}
		return domain.StatusDone
	}
	if state.Review() == domain.IssueReviewRequested {
		return domain.StatusInReview
	}
	if state.Workflow() == domain.IssueWorkflowActive {
		return domain.StatusInProgress
	}
	return domain.StatusOpen
}

func issueArchiveStateFromTimestamp(value sql.NullString) domain.IssueArchiveState {
	if nonEmptyNullString(value) {
		return domain.IssueArchiveArchived
	}
	return domain.IssueArchiveLive
}

func nonEmptyNullString(value sql.NullString) bool {
	return value.Valid && strings.TrimSpace(value.String) != ""
}

const runtimeSessionProjectionUnionSQL = `
	SELECT
		project_id,
		session_id,
		issue_id,
		state,
		observed_state,
		activity,
		activity_source,
		tmux_attached_count,
		started_at,
		updated_at
	FROM daemon_session_projections
	INDEXED BY idx_daemon_session_projections_project_issue_updated
	UNION ALL
	SELECT
		project_id,
		session_id,
		issue_id,
		state,
		observed_state,
		activity,
		activity_source,
		tmux_attached_count,
		started_at,
		updated_at
	FROM daemon_session_observations
	INDEXED BY idx_daemon_session_observations_project_issue_updated
`

// ErrDependencyRemovalConfirmationRequired is returned when a removal that can
// unblock or retarget workflow is attempted without explicit confirmation.
var ErrDependencyRemovalConfirmationRequired = errors.New("explicit confirmation required")
var ErrParentChildOrphanConfirmationRequired = errors.New("parent-child removal would orphan active child; keep the parent-child hierarchy and use blocks/open status to pause or supersede child work, or pass explicit parent-orphan confirmation")
var ErrDeleteBlockedByRuntimeAttachments = errors.New("delete blocked: task has worktree or active session")
var ErrIssueHasLiveChildren = errors.New("issue has undeleted descendants")
var ErrIssueHasArchivedParents = errors.New("issue has archived parents")

type ParentChangeRequiredError struct {
	IssueID         string
	CurrentParent   string
	RequestedParent string
}

func (e ParentChangeRequiredError) Error() string {
	return fmt.Sprintf("refusing to change parent for %s: current parent %s, requested parent %s", e.IssueID, e.CurrentParent, e.RequestedParent)
}

type LiveChildrenMutationError struct {
	Operation       string
	IssueID         string
	DescendantCount int
}

func (e LiveChildrenMutationError) Error() string {
	descendantLabel := "descendants"
	if e.DescendantCount == 1 {
		descendantLabel = "descendant"
	}
	return fmt.Sprintf(
		"cannot %s issue %s: %d undeleted %s remain through active parent-child edges; use an explicit recursive cleanup or supersede workflow that handles every child edge and descendant issue",
		e.Operation,
		e.IssueID,
		e.DescendantCount,
		descendantLabel,
	)
}

func (e LiveChildrenMutationError) Is(target error) bool {
	return target == ErrIssueHasLiveChildren
}

type ArchivedParentsMutationError struct {
	Operation   string
	IssueID     string
	ParentCount int
}

func (e ArchivedParentsMutationError) Error() string {
	parentLabel := "parents"
	if e.ParentCount == 1 {
		parentLabel = "parent"
	}
	return fmt.Sprintf(
		"cannot %s issue %s: %d archived %s remain through parent-child edges; unarchive the parent first or pass --with-parents",
		e.Operation,
		e.IssueID,
		e.ParentCount,
		parentLabel,
	)
}

func (e ArchivedParentsMutationError) Is(target error) bool {
	return target == ErrIssueHasArchivedParents
}

type UnarchiveOptions struct {
	WithParents     bool
	CascadeChildren bool
}

type UnarchiveResult struct {
	UnarchivedIDs []string
}

// WithDependencyRemovalConfirmation marks a context as explicitly confirming a
// dependency removal that can unblock or retarget workflow.
func WithDependencyRemovalConfirmation(ctx context.Context) context.Context {
	return context.WithValue(ctx, dependencyRemovalConfirmationKey{}, true)
}

// WithParentChildOrphanConfirmation marks a context as explicitly confirming a
// parent-child removal that can move active child work to the root board.
func WithParentChildOrphanConfirmation(ctx context.Context) context.Context {
	return context.WithValue(ctx, parentChildOrphanConfirmationKey{}, true)
}

func hasDependencyRemovalConfirmation(ctx context.Context) bool {
	confirmed, _ := ctx.Value(dependencyRemovalConfirmationKey{}).(bool)
	return confirmed
}

func hasParentChildOrphanConfirmation(ctx context.Context) bool {
	confirmed, _ := ctx.Value(parentChildOrphanConfirmationKey{}).(bool)
	return confirmed
}

const (
	nextAlphaIssueIndexMetaKey  = "issue:id_next_alpha_index"
	sqliteBusyPrimaryCode       = 5
	sqliteCorruptPrimaryCode    = 11
	defaultSQLiteBusyTimeout    = 5 * time.Second
	defaultSQLiteBusyRetryDelay = 100 * time.Millisecond
	// Keep at least one foreground reader available while Linear sync owns a write connection.
	sqliteMaxOpenConns         = 4
	issueGraphClosureProjectID = "default"
)

// ErrSQLiteCorrupt marks structural SQLite damage. Callers must preserve the
// database and WAL and recover through a consistent clone instead of retrying
// writes against the damaged authority.
var ErrSQLiteCorrupt = errors.New("issue database structural corruption")

type sqliteCorruptionState struct {
	err error
}

// Client wraps local SQLite task store operations.
type Client struct {
	dbPath                string
	logger                *slog.Logger
	sqliteBusyTimeout     time.Duration
	sqliteBusyRetryBudget time.Duration
	sqliteBusyRetryDelay  time.Duration
	sqliteBusyWait        func(context.Context, time.Duration) error

	mu             sync.Mutex
	db             *sql.DB
	dbGeneration   uint64
	walMu          sync.Mutex
	lastWALCheckAt time.Time

	stateModelV2MigrationFailureHook     func(stage string) error
	boardViewsMigrationFailureHook       func(stage string) error
	humanAuthorityMigrationFailureHook   func(stage string) error
	mailboxProjectionFailureHook         func(stage string) error
	mailboxReplayRepairFailureHook     func(stage string) error
	projectionDeltaChecksumRepairHook    func(stage string) error
	projectionDeltaReadHook              func()
	projectionWatchBeforeSubscribeHook func()
	projectionNotifierBeforeCloseHook  func()
	projectionNotifierAfterClearHook   func()
	projectionSnapshotSourceRowsHook     func(projectionDeltaRows) projectionDeltaRows
	projectionWatchActive                atomic.Int64
	projectionWatchStarted               atomic.Uint64
	projectionWatchCompleted             atomic.Uint64
	corruption                           atomic.Pointer[sqliteCorruptionState]
	projectionNotifierMu               sync.Mutex
	projectionNotifier                 projectionDeltaNotifier
	projectionNotifierClose            *projectionDeltaNotifierCloseState
	projectionNotifierSubscriptions    map[*projectionDeltaSubscription]struct{}
	projectionNotifierWG               sync.WaitGroup
	decisionOutboxMigrationFailureHook   func(stage string) error
	decisionIdempotencyFailureHook       func(stage string) error
	eventSearchMigrationFailureHook      func(stage string) error
	legacyAttachmentMigrationFailureHook func(stage string) error
	legacyAttachmentDirectorySyncHook    func(path string) error
	requireExistingDB                    bool
	interactionMu                        sync.RWMutex
	interactionCache                     map[string]domain.InteractionRequest
}

// ClientOption configures optional issue-store behavior while preserving
// production defaults for ordinary constructors.
type ClientOption func(*Client)

// WithSQLiteBusyPolicy overrides the SQLite busy timeout and retry backoff.
// It is primarily useful for bounded callers and deterministic tests.
func WithSQLiteBusyPolicy(timeout, retryDelay time.Duration) ClientOption {
	return func(c *Client) {
		if timeout > 0 {
			c.sqliteBusyTimeout = timeout
			c.sqliteBusyRetryBudget = timeout
		}
		if retryDelay > 0 {
			c.sqliteBusyRetryDelay = retryDelay
		}
	}
}

// WithExistingDatabaseOnly prevents lazy client initialization from creating
// a missing database or its parent directory. It is intended for background
// discovery of registered project stores, where stale registry entries must
// remain unavailable rather than being revived as empty projects.
func WithExistingDatabaseOnly() ClientOption {
	return func(c *Client) {
		c.requireExistingDB = true
	}
}

func withSQLiteBusyRetryBudget(budget time.Duration) ClientOption {
	return func(c *Client) {
		if budget > 0 {
			c.sqliteBusyRetryBudget = budget
		}
	}
}

func withSQLiteBusyWait(wait func(context.Context, time.Duration) error) ClientOption {
	return func(c *Client) {
		if wait != nil {
			c.sqliteBusyWait = wait
		}
	}
}

func waitSQLiteBusyRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type sqlIssueExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type sqlIssueQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type sqlIssueDBTX interface {
	sqlIssueExecer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// WithMutationLock serializes issue-store writes that must not interleave with
// multi-step daemon side effects for the same SQLite database.
func (c *Client) WithMutationLock(ctx context.Context, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if locked, _ := ctx.Value(issueMutationLockKey{}).(bool); locked {
		return fn(ctx)
	}
	operation := mutationOperationFromContext(ctx)
	lock := issueOperationLockForPath(c.dbPath)
	holderOperation := lock.currentHolder()
	ctx, endSpan := latencytrace.StartSpanWithEndAttributes(ctx, "dependency", "issue_store.mutation_lock",
		"dependency.name", "sqlite",
		"dependency.operation", "issue_mutation_lock",
		"mutation.waiter_operation", operation,
		"mutation.holder_operation", holderOperation,
	)
	var spanErr error
	defer func() { endSpan(spanErr, "mutation.holder_operation", holderOperation) }()
	holderOperation, spanErr = lock.acquire(ctx, operation)
	if spanErr != nil {
		return spanErr
	}
	defer lock.release()
	spanErr = fn(context.WithValue(ctx, issueMutationLockKey{}, true))
	return spanErr
}

func (c *Client) withMutationLock(ctx context.Context, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Initialization performs coordinated startup repairs of its own. Complete
	// it before taking the steady-state write lock so a first mutation cannot
	// recursively acquire the same canonical database lock.
	if _, err := c.dbHandle(); err != nil {
		return err
	}
	runWrite := func(lockCtx context.Context) error {
		lockCtx = sqliteutil.ContextWithWriteOperation(lockCtx, mutationOperationFromContext(lockCtx))
		return sqliteutil.WithWriteLockContext(lockCtx, c.dbPath, fn)
	}
	var spanErr error
	if locked, _ := ctx.Value(issueMutationLockKey{}).(bool); locked {
		spanErr = runWrite(ctx)
	} else {
		spanErr = c.WithMutationLock(ctx, runWrite)
	}
	if spanErr == nil {
		if err := signalProjectionDeltaNotification(c.dbPath); err != nil && c.logger != nil {
			c.logger.Warn("failed to signal projection delta watchers after committed issue mutation", "db_path", sqliteutil.CanonicalPath(c.dbPath), "error", err)
		}
		c.maybeMaintainSQLiteWAL(ctx)
	}
	return spanErr
}

func issueOperationLockForPath(dbPath string) *issueOperationLock {
	key := sqliteutil.CanonicalPath(dbPath)
	if key == "" {
		key = "."
	}
	value, _ := issueOperationLocks.LoadOrStore(key, newIssueOperationLock())
	return value.(*issueOperationLock)
}

// NewClient creates a SQLite-backed issue store client rooted at the repository.
func NewClient(repoDir string, logger *slog.Logger, opts ...ClientOption) *Client {
	dbPath, err := resolveDBPath(repoDir)
	if err != nil {
		// Keep daemon bootstrap non-fatal and surface DB errors on first operation.
		if logger != nil {
			logger.Warn("failed to resolve azedarach issue database path", "repoDir", repoDir, "error", err)
		}
		fallbackRoot := strings.TrimSpace(repoDir)
		if normalizedRoot, normalizeErr := config.ResolveProjectRoot(repoDir); normalizeErr == nil {
			fallbackRoot = normalizedRoot
		}
		if strings.TrimSpace(fallbackRoot) == "" {
			fallbackRoot = "."
		}
		dbPath = filepath.Join(fallbackRoot, ".azedarach", "azedarach.db")
	}
	return NewClientAtPath(dbPath, logger, opts...)
}

// NewClientAtPath creates a SQLite-backed issue store client for tests and explicit wiring.
func NewClientAtPath(dbPath string, logger *slog.Logger, opts ...ClientOption) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	client := &Client{
		dbPath:                dbPath,
		logger:                logger,
		sqliteBusyTimeout:     defaultSQLiteBusyTimeout,
		sqliteBusyRetryBudget: defaultSQLiteBusyTimeout,
		sqliteBusyRetryDelay:  defaultSQLiteBusyRetryDelay,
		sqliteBusyWait:        waitSQLiteBusyRetry,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(client)
		}
	}
	return client
}

func (c *Client) dbHandle() (*sql.DB, error) {
	initStartedAt := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.corruptionError(); err != nil {
		return nil, &domain.TaskStoreError{Op: "open-db", Err: err}
	}
	if c.db != nil {
		return c.db, nil
	}
	if err := dbpathguard.Check(c.dbPath); err != nil {
		return nil, c.wrapError("open-db", "", fmt.Errorf("refuse issue database: %w", err))
	}

	dbDir := filepath.Dir(c.dbPath)
	var existingDB bool
	if c.requireExistingDB {
		info, err := os.Stat(c.dbPath)
		if err != nil {
			return nil, c.wrapError("open-db", "", fmt.Errorf("require existing database: %w", err))
		}
		if !info.Mode().IsRegular() {
			return nil, c.wrapError("open-db", "", fmt.Errorf("require existing database: %s is not a regular file", c.dbPath))
		}
		existingDB = true
	} else {
		if info, err := os.Stat(c.dbPath); err == nil {
			if !info.Mode().IsRegular() {
				return nil, c.wrapError("open-db", "", fmt.Errorf("issue database path %s is not a regular file", c.dbPath))
			}
			existingDB = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, c.wrapError("open-db", "", fmt.Errorf("inspect issue database: %w", err))
		}
		if dbDir != "" && dbDir != "." {
			if err := os.MkdirAll(dbDir, 0o755); err != nil {
				return nil, c.wrapError("open-db", "", fmt.Errorf("create db directory: %w", err))
			}
		}
	}
	if existingDB {
		if err := checkSQLitePathStructuralIntegrity(c.dbPath, c.sqliteBusyTimeout); err != nil {
			return nil, c.wrapError("open-db", "", err)
		}
	}

	busyTimeoutMillis := max(min(c.sqliteBusyTimeout, c.sqliteBusyRetryDelay).Milliseconds(), int64(1))
	mode := ""
	if c.requireExistingDB {
		// mode=rw makes the existence contract atomic at SQLite open time if the
		// file is removed after the stat above.
		mode = "mode=rw&"
	}
	dsn := fmt.Sprintf(
		"file:%s?%s_pragma=busy_timeout(%d)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_txlock=immediate",
		filepath.ToSlash(c.dbPath),
		mode,
		busyTimeoutMillis,
	)
	db, err := tracesqlite.Open(dsn)
	if err != nil {
		return nil, c.wrapError("open-db", "", err)
	}

	db.SetMaxOpenConns(sqliteMaxOpenConns)
	db.SetMaxIdleConns(sqliteMaxOpenConns)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	pingDoneAt := time.Now()
	if err := c.configureSQLite(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	configDoneAt := time.Now()
	if err := c.runMigrations(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	migrationsDoneAt := time.Now()
	if err := c.normalizeDependencyEnumRows(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	normalizeDoneAt := time.Now()
	if err := c.ensureSpecSchema(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	specSchemaDoneAt := time.Now()
	if err := c.ensureSpecAuditSchema(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	if err := c.ensureDecisionSchema(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	if err := c.ensureDecisionAuditSchema(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	if err := c.ensureRuntimeProjectionSchema(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	if err := c.normalizeProviderDisplayKeyIssueIDs(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	if err := repairIssueIDAllocationSchema(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	specAuditDoneAt := time.Now()

	c.db = db
	c.dbGeneration++
	if c.logger != nil {
		c.logger.Info(
			"issue store init timings",
			"db_path", c.dbPath,
			"total_ms", specAuditDoneAt.Sub(initStartedAt).Milliseconds(),
			"ping_ms", pingDoneAt.Sub(initStartedAt).Milliseconds(),
			"configure_sqlite_ms", configDoneAt.Sub(pingDoneAt).Milliseconds(),
			"migrations_ms", migrationsDoneAt.Sub(configDoneAt).Milliseconds(),
			"normalize_dependency_rows_ms", normalizeDoneAt.Sub(migrationsDoneAt).Milliseconds(),
			"ensure_spec_schema_ms", specSchemaDoneAt.Sub(normalizeDoneAt).Milliseconds(),
			"ensure_spec_audit_schema_ms", specAuditDoneAt.Sub(specSchemaDoneAt).Milliseconds(),
		)
	}
	return c.db, nil
}

func checkSQLiteStructuralIntegrity(db *sql.DB) error {
	var result string
	if err := db.QueryRow(`PRAGMA quick_check(1)`).Scan(&result); err != nil {
		return fmt.Errorf("pre-write SQLite integrity check: %w", err)
	}
	result = strings.TrimSpace(result)
	if !strings.EqualFold(result, "ok") {
		return fmt.Errorf("%w: pre-write SQLite integrity check returned %q", ErrSQLiteCorrupt, result)
	}
	return nil
}

// checkSQLitePathStructuralIntegrity validates an existing authority through a
// read-only connection before any read-write pool exists. In WAL mode SQLite
// reads the committed database and WAL snapshot without checkpointing or
// recovering either authority file.
func checkSQLitePathStructuralIntegrity(dbPath string, busyTimeout time.Duration) error {
	busyTimeoutMillis := max(busyTimeout.Milliseconds(), int64(1))
	dsn := fmt.Sprintf(
		"file:%s?mode=ro&_pragma=query_only(ON)&_pragma=busy_timeout(%d)",
		filepath.ToSlash(dbPath),
		busyTimeoutMillis,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open read-only SQLite integrity preflight: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	return checkSQLiteStructuralIntegrity(db)
}

func (c *Client) ensureRuntimeProjectionSchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS daemon_session_projections (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			state TEXT NOT NULL,
			activity TEXT,
			activity_source TEXT,
			started_at TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue
			ON daemon_session_projections (project_id, issue_id)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue_updated
			ON daemon_session_projections (project_id, issue_id, updated_at DESC, session_id DESC)`,
		`CREATE TABLE IF NOT EXISTS daemon_session_observations (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			state TEXT NOT NULL,
			observed_state TEXT,
			activity TEXT,
			activity_source TEXT,
			tmux_attached_count INTEGER NOT NULL DEFAULT 0,
			started_at TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_observations_project_issue
			ON daemon_session_observations (project_id, issue_id)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_observations_project_issue_updated
			ON daemon_session_observations (project_id, issue_id, updated_at DESC, session_id DESC)`,
		`CREATE TABLE IF NOT EXISTS daemon_worktree_projections (
			project_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			path TEXT NOT NULL,
			branch TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			git_status_json TEXT,
			git_status_updated_at TEXT,
			PRIMARY KEY (project_id, issue_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_worktree_projections_project_path
			ON daemon_worktree_projections (project_id, path)`,
		`CREATE TABLE IF NOT EXISTS daemon_rooted_bootstrap_acknowledgements (
			project_id TEXT NOT NULL CHECK (trim(project_id) <> ''),
			root_issue_id TEXT NOT NULL CHECK (trim(root_issue_id) <> ''),
			session_id TEXT NOT NULL CHECK (trim(session_id) <> ''),
			prompt_hash TEXT NOT NULL CHECK (trim(prompt_hash) <> ''),
			runtime_nonce TEXT NOT NULL CHECK (trim(runtime_nonce) <> ''),
			acknowledged_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, root_issue_id),
			UNIQUE (project_id, session_id)
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure runtime projection schema: %w", err)
		}
	}
	if err := ensureSQLiteColumn(db, "daemon_session_projections", "tmux_attached_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("ensure runtime projection schema: %w", err)
	}
	if err := ensureSQLiteColumn(db, "daemon_session_projections", "observed_state", "TEXT"); err != nil {
		return fmt.Errorf("ensure runtime projection schema: %w", err)
	}
	if err := ensureSQLiteColumn(db, "daemon_session_projections", "activity", "TEXT"); err != nil {
		return fmt.Errorf("ensure runtime projection schema: %w", err)
	}
	if err := ensureSQLiteColumn(db, "daemon_session_projections", "activity_source", "TEXT"); err != nil {
		return fmt.Errorf("ensure runtime projection schema: %w", err)
	}
	for _, column := range []struct{ name, ddl string }{{"role", "TEXT NOT NULL DEFAULT 'worker'"}, {"scope_kind", "TEXT NOT NULL DEFAULT 'issue'"}, {"scope_id", "TEXT NOT NULL DEFAULT ''"}} {
		if err := ensureSQLiteColumn(db, "daemon_session_projections", column.name, column.ddl); err != nil {
			return fmt.Errorf("ensure runtime projection schema: %w", err)
		}
	}
	for _, column := range []struct {
		name string
		ddl  string
	}{
		{"started_at", "TEXT"},
		{"observed_state", "TEXT"},
		{"activity", "TEXT"},
		{"activity_source", "TEXT"},
		{"tmux_attached_count", "INTEGER NOT NULL DEFAULT 0"},
		{"role", "TEXT NOT NULL DEFAULT 'worker'"},
		{"scope_kind", "TEXT NOT NULL DEFAULT 'issue'"},
		{"scope_id", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := ensureSQLiteColumn(db, "daemon_session_observations", column.name, column.ddl); err != nil {
			return fmt.Errorf("ensure runtime projection schema: %w", err)
		}
	}
	if err := migrateRuntimeSessionObservations(db); err != nil {
		return fmt.Errorf("ensure runtime projection schema: %w", err)
	}
	return nil
}

func migrateRuntimeSessionObservations(db *sql.DB) error {
	logicalIdentity := false
	var logicalColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_xinfo('daemon_session_observations') WHERE name='logical_id'`).Scan(&logicalColumns); err == nil {
		logicalIdentity = logicalColumns > 0
	}
	if logicalIdentity {
		if _, err := db.Exec(`DELETE FROM daemon_session_observations WHERE EXISTS(SELECT 1 FROM daemon_session_projections p WHERE p.project_id=daemon_session_observations.project_id AND p.session_id=daemon_session_observations.session_id AND instr(p.session_id,'.pane-')>0);
			INSERT INTO daemon_session_observations(project_id,session_id,issue_id,role,scope_kind,scope_id,state,observed_state,activity,activity_source,tmux_attached_count,started_at,updated_at)
			SELECT project_id,session_id,issue_id,role,scope_kind,scope_id,state,observed_state,activity,activity_source,COALESCE(tmux_attached_count,0),started_at,updated_at
			FROM daemon_session_projections WHERE instr(session_id,'.pane-')>0
			ON CONFLICT DO UPDATE SET session_id=excluded.session_id,issue_id=excluded.issue_id,role=excluded.role,scope_kind=excluded.scope_kind,scope_id=excluded.scope_id,state=excluded.state,observed_state=excluded.observed_state,activity=excluded.activity,activity_source=excluded.activity_source,tmux_attached_count=excluded.tmux_attached_count,started_at=excluded.started_at,updated_at=excluded.updated_at;
			DELETE FROM daemon_session_projections WHERE instr(session_id,'.pane-')>0`); err != nil {
			return err
		}
		return nil
	}
	if _, err := db.Exec(`
		INSERT INTO daemon_session_observations (
			project_id,
			session_id,
			issue_id,
			state,
			observed_state,
			activity,
			activity_source,
			tmux_attached_count,
			started_at,
			updated_at
		)
		SELECT
			project_id,
			session_id,
			issue_id,
			state,
			observed_state,
			activity,
			activity_source,
			COALESCE(tmux_attached_count, 0),
			started_at,
			updated_at
		FROM daemon_session_projections
		WHERE instr(session_id, '.pane-') > 0
		ON CONFLICT(project_id, session_id) DO UPDATE SET
			issue_id = excluded.issue_id,
			state = excluded.state,
			observed_state = excluded.observed_state,
			activity = excluded.activity,
			activity_source = excluded.activity_source,
			tmux_attached_count = excluded.tmux_attached_count,
			started_at = excluded.started_at,
			updated_at = excluded.updated_at
	`); err != nil {
		return fmt.Errorf("migrate session observations: copy pane rows: %w", err)
	}
	if _, err := db.Exec(`
		DELETE FROM daemon_session_projections
		WHERE instr(session_id, '.pane-') > 0
	`); err != nil {
		return fmt.Errorf("migrate session observations: delete pane rows from intent table: %w", err)
	}
	return nil
}

func ensureSQLiteColumn(db *sql.DB, tableName, columnName, columnDDL string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, columnName) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, columnName, columnDDL))
	return err
}

func (c *Client) CloseDB() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var dbErr error
	if c.db != nil {
		dbErr = c.db.Close()
		c.db = nil
	}

	// The descriptor-safe notifier watches only its dedicated signal sidecar.
	// Keep the client lifecycle boundary held through notifier shutdown so a new
	// pool generation cannot open while the previous notifier is closing.
	if c.projectionNotifierBeforeCloseHook != nil {
		c.projectionNotifierBeforeCloseHook()
	}
	notifierErr := c.closeProjectionDeltaNotifier()
	if dbErr != nil {
		return c.wrapError("close-db", "", dbErr)
	}
	if notifierErr != nil {
		return c.wrapError("close-projection-notifier", "", notifierErr)
	}
	return nil
}

func (c *Client) DBStats() (sql.DBStats, error) {
	db, err := c.dbHandle()
	if err != nil {
		return sql.DBStats{}, err
	}
	return db.Stats(), nil
}

// StoreResourceDiagnostics reports the process-owned resources for one issue
// store without opening a closed database as a side effect of diagnosis.
type StoreResourceDiagnostics struct {
	DBPath                   string
	Open                     bool
	DBStats                  sql.DBStats
	MutationHolder           string
	MutationHeldFor          time.Duration
	SQLiteWriteHolder        string
	SQLiteWriteHeldFor       time.Duration
	ProjectionWatchesActive  int64
	ProjectionWatchesStarted uint64
	ProjectionWatchesDone    uint64
}

func (c *Client) ResourceDiagnostics() StoreResourceDiagnostics {
	if c == nil {
		return StoreResourceDiagnostics{}
	}
	c.mu.Lock()
	db := c.db
	dbPath := c.dbPath
	c.mu.Unlock()

	diagnostic := StoreResourceDiagnostics{
		DBPath:                   dbPath,
		Open:                     db != nil,
		ProjectionWatchesActive:  c.projectionWatchActive.Load(),
		ProjectionWatchesStarted: c.projectionWatchStarted.Load(),
		ProjectionWatchesDone:    c.projectionWatchCompleted.Load(),
	}
	if db != nil {
		diagnostic.DBStats = db.Stats()
	}
	diagnostic.MutationHolder, diagnostic.MutationHeldFor = issueOperationLockForPath(dbPath).holderDuration(time.Now())
	writeLock := sqliteutil.WriteLockResourceDiagnostics(dbPath)
	diagnostic.SQLiteWriteHolder = writeLock.Holder
	diagnostic.SQLiteWriteHeldFor = writeLock.HeldFor
	return diagnostic
}

func (l *issueOperationLock) holderDuration(now time.Time) (string, time.Duration) {
	holder, since := l.holderSnapshot()
	if holder == "" || since.IsZero() || now.Before(since) {
		return holder, 0
	}
	return holder, now.Sub(since)
}

func (c *Client) configureSQLite(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("apply %s: %w", pragma, err)
		}
	}
	return nil
}

func (c *Client) normalizeDependencyEnumRows(db *sql.DB) error {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT issue_id FROM issue_dependencies
		WHERE dependency_type IN ('parent_child','blocked_by','related_to','discovered_from')
		ORDER BY issue_id
	`)
	if err != nil {
		return fmt.Errorf("normalize dependency enum rows: list affected issues: %w", err)
	}
	var issueIDs []string
	for rows.Next() {
		var issueID string
		if err := rows.Scan(&issueID); err != nil {
			rows.Close()
			return err
		}
		issueIDs = append(issueIDs, issueID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(issueIDs) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("normalize dependency enum rows: begin: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE issue_dependencies
		SET dependency_type = CASE
			WHEN dependency_type = 'parent_child' THEN 'parent-child'
			WHEN dependency_type = 'blocked_by' THEN 'blocked-by'
			WHEN dependency_type = 'related_to' THEN 'related'
			WHEN dependency_type = 'discovered_from' THEN 'discovered-from'
			ELSE dependency_type
		END
		WHERE dependency_type = 'parent_child'
			OR dependency_type = 'blocked_by'
			OR dependency_type = 'related_to'
			OR dependency_type = 'discovered_from'
	`)
	if err != nil {
		return fmt.Errorf("normalize dependency enum rows: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected > 0 {
		if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
			return fmt.Errorf("rebuild graph closure after dependency enum normalization: %w", err)
		}
		for _, issueID := range issueIDs {
			if err := c.appendIssueObservationEvent(ctx, tx, issueID, domain.IssueEventIssueDetailsChanged, map[string]any{"reason": "dependency_enum_normalization"}); err != nil {
				return fmt.Errorf("emit dependency enum normalization for %s: %w", issueID, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("normalize dependency enum rows: commit: %w", err)
	}
	return nil
}

type issueIDMigration struct {
	OldID         string
	NewID         string
	Provider      string
	ProviderScope string
	RemoteKey     string
	DisplayKey    string
}

func (c *Client) normalizeProviderDisplayKeyIssueIDs(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
		SELECT id
		FROM issues
		WHERE visibility = 'live'
		ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("list issues for provider display-key normalization: %w", err)
	}
	defer rows.Close()

	existing := map[string]struct{}{}
	legacyIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan issue id for provider display-key normalization: %w", err)
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		existing[id] = struct{}{}
		if isLinearDisplayKeyIssueID(id) {
			legacyIDs = append(legacyIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate issue ids for provider display-key normalization: %w", err)
	}
	if len(legacyIDs) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin provider display-key normalization: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	nextIndex := 0
	if raw, err := c.getMetaValue(ctx, tx, nextAlphaIssueIndexMetaKey); err == nil {
		nextIndex = parseNextAlphaIssueIndex(raw)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read next alpha id for provider display-key normalization: %w", err)
	}
	if allocated, err := c.loadAllocatedIssueIDs(ctx, tx); err != nil {
		return fmt.Errorf("read allocated issue ids for provider display-key normalization: %w", err)
	} else {
		for id := range allocated {
			existing[id] = struct{}{}
		}
	}

	migrations := make([]issueIDMigration, 0, len(legacyIDs))
	for _, oldID := range legacyIDs {
		newID, nextReserved := allocateNextAlphaIssueID(nextIndex, existing)
		nextIndex = nextReserved
		existing[newID] = struct{}{}
		prefix := strings.TrimSpace(strings.SplitN(oldID, "-", 2)[0])
		migrations = append(migrations, issueIDMigration{
			OldID:         oldID,
			NewID:         newID,
			Provider:      "linear",
			ProviderScope: "team:" + prefix,
			RemoteKey:     oldID,
			DisplayKey:    oldID,
		})
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, migration := range migrations {
		if err := c.migrateProviderDisplayKeyIssueID(ctx, tx, migration, now); err != nil {
			return err
		}
		if err := c.reserveIssueID(ctx, tx, migration.NewID, now, "provider-display-key-normalization"); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value
	`, nextAlphaIssueIndexMetaKey, strconv.Itoa(nextIndex)); err != nil {
		return fmt.Errorf("persist next alpha id after provider display-key normalization: %w", err)
	}
	if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
		return fmt.Errorf("rebuild graph closure after provider display-key normalization: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider display-key normalization: %w", err)
	}
	tx = nil
	if c.logger != nil {
		c.logger.Info("normalized provider display-key issue ids", "count", len(migrations))
	}
	return nil
}

func (c *Client) migrateProviderDisplayKeyIssueID(ctx context.Context, tx *sql.Tx, migration issueIDMigration, now string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issues (
			id,
			title,
			description,
			status,
			disposition,
			engagement,
			visibility,
			priority,
			issue_type,
			created_at,
			updated_at,
			closed_at,
			assignee,
			labels_json,
			implementations_json,
			design,
			notes,
			acceptance,
			estimate,
			deleted_at,
			lifecycle_state,
			closed_outcome,
			review_state,
			archived_at
		)
		SELECT
			?,
			title,
			description,
			status,
			disposition,
			engagement,
			visibility,
			priority,
			issue_type,
			created_at,
			?,
			closed_at,
			assignee,
			labels_json,
			implementations_json,
			design,
			notes,
			acceptance,
			estimate,
			deleted_at,
			lifecycle_state,
			closed_outcome,
			review_state,
			archived_at
		FROM issues
		WHERE id = ?
	`, migration.NewID, now, migration.OldID); err != nil {
		return fmt.Errorf("copy provider display-key issue %s to %s: %w", migration.OldID, migration.NewID, err)
	}

	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE issue_dependencies SET issue_id = ? WHERE issue_id = ?`, []any{migration.NewID, migration.OldID}},
		{`UPDATE issue_dependencies SET depends_on_id = ? WHERE depends_on_id = ?`, []any{migration.NewID, migration.OldID}},
		{`UPDATE spec_requirements SET issue_id = ? WHERE issue_id = ?`, []any{migration.NewID, migration.OldID}},
		{`UPDATE spec_links SET issue_id = ? WHERE issue_id = ?`, []any{migration.NewID, migration.OldID}},
		{`UPDATE issue_external_refs SET issue_id = ?, updated_at = ? WHERE issue_id = ?`, []any{migration.NewID, now, migration.OldID}},
		{`UPDATE daemon_session_projections SET issue_id = ?, scope_id = CASE WHEN scope_id = ? THEN ? ELSE scope_id END, updated_at = ? WHERE issue_id = ?`, []any{migration.NewID, migration.OldID, migration.NewID, now, migration.OldID}},
		{`UPDATE daemon_session_observations SET issue_id = ?, scope_id = CASE WHEN scope_id = ? THEN ? ELSE scope_id END, updated_at = ? WHERE issue_id = ?`, []any{migration.NewID, migration.OldID, migration.NewID, now, migration.OldID}},
		{`UPDATE daemon_worktree_projections SET issue_id = ?, updated_at = ? WHERE issue_id = ?`, []any{migration.NewID, now, migration.OldID}},
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			return fmt.Errorf("rewrite references from provider display-key issue %s to %s: %w", migration.OldID, migration.NewID, err)
		}
	}

	metadataJSON, err := marshalStringMap(map[string]string{
		"migrated_from_issue_id": migration.OldID,
		"migration":              "provider-display-key-issue-id",
	})
	if err != nil {
		return fmt.Errorf("marshal provider display-key migration metadata %s: %w", migration.OldID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issue_external_refs (
			issue_id,
			provider,
			provider_scope,
			remote_key,
			display_key,
			url,
			metadata_json,
			created_at,
			updated_at
		)
		SELECT ?, ?, ?, ?, ?, NULL, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1
			FROM issue_external_refs
			WHERE provider = ?
				AND provider_scope = ?
				AND remote_key = ?
				AND deleted_at IS NULL
		)
	`, migration.NewID, migration.Provider, migration.ProviderScope, migration.RemoteKey, migration.DisplayKey, metadataJSON, now, now, migration.Provider, migration.ProviderScope, migration.RemoteKey); err != nil {
		return fmt.Errorf("record external ref for provider display-key issue %s: %w", migration.OldID, err)
	}
	if err := c.appendIssueObservationEvent(ctx, tx, migration.NewID, domain.IssueEventIssueDetailsChanged, map[string]any{
		"reason":            "provider_display_key_id_normalization",
		"previous_issue_id": migration.OldID,
	}); err != nil {
		return fmt.Errorf("emit provider display-key issue projection %s: %w", migration.NewID, err)
	}
	if err := c.appendIssueObservationEvent(ctx, tx, migration.OldID, domain.IssueEventIssueDeleted, map[string]any{
		"reason":       "provider_display_key_id_normalization",
		"new_issue_id": migration.NewID,
	}); err != nil {
		return fmt.Errorf("emit provider display-key issue tombstone %s: %w", migration.OldID, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, migration.OldID); err != nil {
		return fmt.Errorf("delete provider display-key issue %s after migration to %s: %w", migration.OldID, migration.NewID, err)
	}
	return nil
}

func isLinearDisplayKeyIssueID(id string) bool {
	id = strings.TrimSpace(id)
	prefix, numeric, ok := strings.Cut(id, "-")
	if !ok || prefix == "" || numeric == "" {
		return false
	}
	for _, r := range prefix {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	for _, r := range numeric {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// List fetches all active issues from local SQLite store.
func (c *Client) List(ctx context.Context) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	tasks, err := c.queryTasks(ctx, db, `
		SELECT
			id,
			title,
			COALESCE(description, ''),
			COALESCE(notes, ''),
			COALESCE(design, ''),
			COALESCE(acceptance, ''),
			COALESCE(assignee, ''),
			COALESCE(labels_json, '[]'),
			estimate,
			status,
			COALESCE(disposition, ''),
			COALESCE(engagement, ''),
			COALESCE(visibility, ''),
			archived_at,
			priority,
			issue_type,
			COALESCE(implementations_json, '[]'),
			created_at,
			updated_at
		FROM issues
		WHERE visibility = 'live'
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, c.wrapError("list", "", err)
	}
	return tasks, nil
}

// ListWithRuntime fetches active issues and runtime projection fields using a single joined SQLite query.
func (c *Client) ListWithRuntime(ctx context.Context, projectID string) ([]domain.Task, error) {
	return c.ListWithRuntimeArchiveMode(ctx, projectID, ArchiveExclude)
}

// ListWithRuntimeArchiveMode fetches full issue bodies, all dependency edges,
// and joined runtime projections for user-level cross-project projection.
func (c *Client) ListWithRuntimeArchiveMode(ctx context.Context, projectID string, archiveMode ArchiveMode) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	tasks, err := c.queryTasksWithRuntimeArchiveMode(ctx, db, projectID, archiveMode)
	if err != nil {
		return nil, c.wrapError("list-with-runtime", projectID, err)
	}
	return tasks, nil
}

// SearchWithRuntime fetches active issues matching query through the issue
// content FTS index, then hydrates only the matching runtime rows.
func (c *Client) SearchWithRuntime(ctx context.Context, projectID, query string) ([]domain.Task, error) {
	return c.SearchWithRuntimeArchiveMode(ctx, projectID, query, ArchiveExclude)
}

// SearchWithRuntimeArchiveMode fetches issues matching query through the issue
// content FTS index, then hydrates only the matching runtime rows.
func (c *Client) SearchWithRuntimeArchiveMode(ctx context.Context, projectID, query string, archiveMode ArchiveMode) ([]domain.Task, error) {
	startedAt := time.Now()
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	expr := domain.ContentQueryFTSExpression(query)
	if expr == "" {
		return []domain.Task{}, nil
	}
	archiveMode = NormalizeArchiveMode(string(archiveMode))
	if archiveMode != ArchiveExclude {
		tasks := []domain.Task{}
		if archiveMode == ArchiveInclude {
			active, err := c.searchActiveWithRuntime(ctx, db, projectID, query, expr, startedAt)
			if err != nil {
				return nil, err
			}
			tasks = append(tasks, active...)
		}
		archived, err := c.queryTasksWithRuntimeArchiveMode(ctx, db, projectID, ArchiveOnly)
		if err != nil {
			return nil, c.wrapError("search-with-runtime", projectID, err)
		}
		tasks = append(tasks, domain.FilterTasksByContentQuery(archived, query)...)
		sort.SliceStable(tasks, func(i, j int) bool {
			if tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
				return tasks[i].ID < tasks[j].ID
			}
			return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
		})
		return tasks, nil
	}
	return c.searchActiveWithRuntime(ctx, db, projectID, query, expr, startedAt)
}

func (c *Client) searchActiveWithRuntime(ctx context.Context, db *sql.DB, projectID, query, expr string, startedAt time.Time) ([]domain.Task, error) {
	rows, err := db.QueryContext(ctx, issueSearchIDsQuery(ArchiveExclude), expr)
	if err != nil {
		c.logSQLiteRead(ctx, "issue.search_ids_fts", startedAt, 0, err)
		return nil, c.wrapError("search-with-runtime", projectID, err)
	}

	ids := make([]string, 0, 32)
	seen := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			c.logSQLiteRead(ctx, "issue.search_ids_fts", startedAt, len(ids), err)
			return nil, c.wrapError("search-with-runtime", projectID, err)
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		c.logSQLiteRead(ctx, "issue.search_ids_fts", startedAt, len(ids), err)
		return nil, c.wrapError("search-with-runtime", projectID, err)
	}
	if err := rows.Close(); err != nil {
		c.logSQLiteRead(ctx, "issue.search_ids_fts", startedAt, len(ids), err)
		return nil, c.wrapError("search-with-runtime", projectID, err)
	}
	c.logSQLiteRead(ctx, "issue.search_ids_fts", startedAt, len(ids), nil)
	if len(ids) == 0 {
		return []domain.Task{}, nil
	}

	tasks, err := c.queryTasksWithRuntimeArchiveMode(ctx, db, projectID, ArchiveExclude, ids...)
	if err != nil {
		return nil, c.wrapError("search-with-runtime", projectID, err)
	}
	return domain.FilterTasksByContentQuery(tasks, query), nil
}

func issueSearchIDsQuery(archiveMode ArchiveMode) string {
	return fmt.Sprintf(`
		SELECT i.id
		FROM issue_search_fts
		JOIN issues i ON i.rowid = issue_search_fts.rowid
		WHERE issue_search_fts MATCH ?
			AND %s
		ORDER BY i.updated_at DESC, i.id
	`, archiveWhere("i", archiveMode))
}

// ListSummariesWithRuntime fetches active issues with runtime projection fields
// but without long-form detail text. It is intended for board/list snapshots
// where fetching full issue bodies for every task dominates load time.
func (c *Client) ListSummariesWithRuntime(ctx context.Context, projectID string) ([]domain.Task, error) {
	return c.ListSummariesWithRuntimeArchiveMode(ctx, projectID, ArchiveExclude)
}

// ListSummariesWithRuntimeDependencies fetches active issue summaries with runtime projection fields
// and full outgoing dependency edges.
func (c *Client) ListSummariesWithRuntimeDependencies(ctx context.Context, projectID string) ([]domain.Task, error) {
	return c.ListSummariesWithRuntimeDependenciesArchiveMode(ctx, projectID, ArchiveExclude)
}

func (c *Client) ListSummariesWithRuntimeArchiveMode(ctx context.Context, projectID string, archiveMode ArchiveMode) ([]domain.Task, error) {
	return c.listSummariesWithRuntime(ctx, projectID, false, archiveMode)
}

func (c *Client) ListSummariesWithRuntimeDependenciesArchiveMode(ctx context.Context, projectID string, archiveMode ArchiveMode) ([]domain.Task, error) {
	return c.listSummariesWithRuntime(ctx, projectID, true, archiveMode)
}

func (c *Client) listSummariesWithRuntime(ctx context.Context, projectID string, includeDependencies bool, archiveMode ArchiveMode) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	tasks, err := c.queryTaskSummariesWithRuntimeArchiveMode(ctx, db, projectID, includeDependencies, archiveMode)
	if err != nil {
		return nil, c.wrapError("list-summaries-with-runtime", projectID, err)
	}
	return tasks, nil
}

// HydrateRuntime overlays the current runtime projection onto the provided
// tasks while preserving their durable issue fields and dependency shape.
func (c *Client) HydrateRuntime(ctx context.Context, projectID string, tasks []domain.Task) ([]domain.Task, error) {
	if len(tasks) == 0 {
		return nil, nil
	}
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if id := strings.TrimSpace(task.ID.String()); id != "" {
			ids = append(ids, id)
		}
	}
	runtimeTasks, err := c.queryTaskMetadataWithRuntime(ctx, db, projectID, ids...)
	if err != nil {
		return nil, c.wrapError("hydrate-runtime", projectID, err)
	}
	runtimeByID := make(map[string]domain.Task, len(runtimeTasks))
	for _, task := range runtimeTasks {
		runtimeByID[strings.TrimSpace(task.ID.String())] = task
	}
	out := make([]domain.Task, len(tasks))
	for i := range out {
		out[i] = cloneTaskForRuntimeOverlay(tasks[i])
		applyRuntimeOverlay(&out[i], runtimeByID[strings.TrimSpace(out[i].ID.String())])
	}
	return out, nil
}

// ListGraphReadinessWithRuntime is the single indexed candidate read for rooted
// and project orchestration. A root selects its graph closure; an empty root
// selects a bounded project candidate window. Final semantics are always
// applied by domain.AssessOrchestrationCandidate after hydration.
func (c *Client) ListGraphReadinessWithRuntime(ctx context.Context, projectID, rootID string, projectLimit ...int) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		limit := 50
		if len(projectLimit) > 0 && projectLimit[0] > 0 {
			limit = projectLimit[0]
		}
		issueIDs, err := c.projectGraphReadinessContextIDs(ctx, db, limit)
		if err != nil {
			return nil, c.wrapError("list-graph-readiness-with-runtime", projectID, err)
		}
		if len(issueIDs) == 0 {
			return []domain.Task{}, nil
		}
		// Project stewardship is bounded and needs contract fields for
		// executability assessment. Rooted graph reads remain summary-only.
		tasks, err := c.queryTasksWithRuntimeProjection(ctx, db, projectID, true, taskDependencyLoadAll, ArchiveExclude, issueIDs...)
		if err != nil {
			return nil, c.wrapError("list-graph-readiness-with-runtime", projectID, err)
		}
		return tasks, nil
	}
	issueIDs, err := c.graphReadinessContextIDs(ctx, db, rootID)
	if err != nil {
		return nil, c.wrapError("list-graph-readiness-with-runtime", rootID, err)
	}
	if len(issueIDs) == 0 {
		return []domain.Task{}, nil
	}
	tasks, err := c.queryTasksWithRuntimeProjection(ctx, db, projectID, false, taskDependencyLoadAll, ArchiveExclude, issueIDs...)
	if err != nil {
		return nil, c.wrapError("list-graph-readiness-with-runtime", rootID, err)
	}
	return tasks, nil
}

// CountOpenOrchestrationIssues returns the project-wide canonical lifecycle-Open
// count used by the rootless safety threshold. Open is derived from live,
// ready-disposition, idle-engagement facts; backlog, active, review-requested,
// and terminal lifecycle states do not count even when compatibility status is
// open.
func (c *Client) CountOpenOrchestrationIssues(ctx context.Context) (int, error) {
	db, err := c.dbHandle()
	if err != nil {
		return 0, err
	}
	var count int
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM issues
		WHERE visibility = 'live'
		  AND disposition = 'ready'
		  AND engagement = 'idle'
	`).Scan(&count)
	if err != nil {
		return 0, c.wrapError("count-open-orchestration-issues", "", err)
	}
	return count, nil
}

func (c *Client) projectGraphReadinessContextIDs(ctx context.Context, db *sql.DB, limit int) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		WITH candidates(id) AS (
			SELECT id FROM issues
			WHERE visibility = 'live'
			  AND disposition = 'ready'
			  AND engagement = 'idle'
			ORDER BY priority ASC, updated_at ASC, id ASC
			LIMIT ?
		), context(id) AS (
			SELECT id FROM candidates
			UNION SELECT closure.ancestor_id FROM candidates c
			JOIN issue_graph_closure closure INDEXED BY idx_issue_graph_closure_descendant
			  ON closure.descendant_id = c.id
			WHERE closure.project_id = ? AND closure.dependency_type = ?
			UNION SELECT dep.depends_on_id FROM candidates c
			JOIN issue_dependencies dep INDEXED BY idx_dependencies_issue_active_type
			  ON dep.issue_id = c.id AND dep.tombstoned_at IS NULL
		)
		SELECT id FROM context ORDER BY id
	`, limit, issueGraphClosureProjectID, string(domain.DependencyParentChild))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit*2)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// ListParentChildSubtreeWithRuntime fetches the requested issue and active
// parent-child descendants with runtime projection fields. It intentionally
// avoids full-project scans for close/review guards.
func (c *Client) ListParentChildSubtreeWithRuntime(ctx context.Context, projectID, rootID string) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return []domain.Task{}, nil
	}
	issueIDs, err := c.parentChildSubtreeIDs(ctx, db, rootID)
	if err != nil {
		return nil, c.wrapError("list-parent-child-subtree-with-runtime", rootID, err)
	}
	if len(issueIDs) == 0 {
		return []domain.Task{}, nil
	}
	tasks, err := c.queryTasksWithRuntimeProjection(ctx, db, projectID, false, taskDependencyLoadAll, ArchiveExclude, issueIDs...)
	if err != nil {
		return nil, c.wrapError("list-parent-child-subtree-with-runtime", rootID, err)
	}
	return tasks, nil
}

// GetWithRuntime fetches one active issue with runtime projection fields.
func (c *Client) GetWithRuntime(ctx context.Context, projectID, id string) (domain.Task, error) {
	return c.GetWithRuntimeArchiveMode(ctx, projectID, id, ArchiveExclude)
}

// GetWithRuntimeArchiveMode fetches one issue with runtime projection fields.
func (c *Client) GetWithRuntimeArchiveMode(ctx context.Context, projectID, id string, archiveMode ArchiveMode) (domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.Task{}, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.Task{}, c.wrapError("get-with-runtime", id, domain.ErrNotFound)
	}
	tasks, err := c.queryTasksWithRuntimeArchiveMode(ctx, db, projectID, archiveMode, id)
	if err != nil {
		return domain.Task{}, c.wrapError("get-with-runtime", id, err)
	}
	if len(tasks) == 0 {
		return domain.Task{}, c.wrapError("get-with-runtime", id, domain.ErrNotFound)
	}
	return tasks[0], nil
}

// GetManyWithRuntime fetches active issues by ID with runtime projection fields.
func (c *Client) GetManyWithRuntime(ctx context.Context, projectID string, ids []string) ([]domain.Task, error) {
	return c.GetManyWithRuntimeArchiveMode(ctx, projectID, ids, ArchiveExclude)
}

// GetManyWithRuntimeArchiveMode fetches issues by ID with runtime projection
// fields using the requested archive visibility.
func (c *Client) GetManyWithRuntimeArchiveMode(ctx context.Context, projectID string, ids []string, archiveMode ArchiveMode) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	issueIDs := uniqueIssueIDStrings(ids)
	if len(issueIDs) == 0 {
		return []domain.Task{}, nil
	}
	tasks, err := c.queryTasksWithRuntimeArchiveMode(ctx, db, projectID, archiveMode, issueIDs...)
	if err != nil {
		return nil, c.wrapError("get-many-with-runtime", strings.Join(issueIDs, ","), err)
	}
	return tasks, nil
}

// GetWithDependencyContextRuntime fetches one issue plus direct dependencies and dependents.
func (c *Client) GetWithDependencyContextRuntime(ctx context.Context, projectID, id string) ([]domain.Task, error) {
	return c.GetWithDependencyContextRuntimeArchiveMode(ctx, projectID, id, ArchiveExclude)
}

// GetWithDependencyContextRuntimeArchiveMode fetches one issue plus direct dependencies and dependents.
func (c *Client) GetWithDependencyContextRuntimeArchiveMode(ctx context.Context, projectID, id string, archiveMode ArchiveMode) ([]domain.Task, error) {
	archiveMode = NormalizeArchiveMode(string(archiveMode))
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, c.wrapError("get-with-dependency-context-runtime", id, domain.ErrNotFound)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT id
		FROM (
			SELECT ? AS id
			UNION ALL
			SELECT depends_on_id AS id
			FROM issue_dependencies
			WHERE issue_id = ? AND tombstoned_at IS NULL
			UNION ALL
			SELECT issue_id AS id
			FROM issue_dependencies
			WHERE depends_on_id = ? AND tombstoned_at IS NULL
		)
	`, id, id, id)
	if err != nil {
		return nil, c.wrapError("get-with-dependency-context-runtime", id, err)
	}

	issueIDs := make([]string, 0, 8)
	for rows.Next() {
		var issueID string
		if err := rows.Scan(&issueID); err != nil {
			_ = rows.Close()
			return nil, c.wrapError("get-with-dependency-context-runtime", id, err)
		}
		if strings.TrimSpace(issueID) != "" {
			issueIDs = append(issueIDs, issueID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, c.wrapError("get-with-dependency-context-runtime", id, err)
	}
	if err := rows.Close(); err != nil {
		return nil, c.wrapError("get-with-dependency-context-runtime", id, err)
	}

	tasks, err := c.queryTasksWithRuntimeArchiveMode(ctx, db, projectID, archiveMode, issueIDs...)
	if err != nil {
		return nil, c.wrapError("get-with-dependency-context-runtime", id, err)
	}
	for _, task := range tasks {
		if task.ID.String() == id {
			return tasks, nil
		}
	}
	return nil, c.wrapError("get-with-dependency-context-runtime", id, domain.ErrNotFound)
}

type dependencyContextOptions struct {
	includeAncestors          bool
	includeDependents         bool
	parentChildDependentsOnly bool
}

// DependencyContextOption configures task dependency context reads.
type DependencyContextOption func(*dependencyContextOptions)

// WithAncestorContext includes the full parent-child ancestor chain for each requested issue.
func WithAncestorContext() DependencyContextOption {
	return func(opts *dependencyContextOptions) {
		opts.includeAncestors = true
	}
}

// WithoutDependentContext omits direct dependents from dependency context reads.
func WithoutDependentContext() DependencyContextOption {
	return func(opts *dependencyContextOptions) {
		opts.includeDependents = false
	}
}

// WithParentChildDependentContext limits dependent context to direct parent-child children.
func WithParentChildDependentContext() DependencyContextOption {
	return func(opts *dependencyContextOptions) {
		opts.includeDependents = true
		opts.parentChildDependentsOnly = true
	}
}

// GetManyWithDependencyContextRuntime fetches issues plus direct dependencies and dependents.
func (c *Client) GetManyWithDependencyContextRuntime(ctx context.Context, projectID string, ids []string, options ...DependencyContextOption) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	opts := dependencyContextOptions{includeDependents: true}
	for _, option := range options {
		if option != nil {
			option(&opts)
		}
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}

	seen := map[string]struct{}{}
	issueIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		issueIDs = append(issueIDs, id)
	}
	if len(issueIDs) == 0 {
		return nil, c.wrapError("get-many-with-dependency-context-runtime", "", domain.ErrNotFound)
	}

	query, args := dependencyContextIDsQuery(issueIDs, opts)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
	}

	contextIDs := make([]string, 0, len(issueIDs)*2)
	for rows.Next() {
		var issueID string
		if err := rows.Scan(&issueID); err != nil {
			_ = rows.Close()
			return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
		}
		if strings.TrimSpace(issueID) != "" {
			contextIDs = append(contextIDs, issueID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
	}
	if err := rows.Close(); err != nil {
		return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
	}
	if opts.includeAncestors {
		ancestorIDs, err := c.parentAncestorIDs(ctx, db, issueIDs)
		if err != nil {
			return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
		}
		contextIDs = append(contextIDs, ancestorIDs...)
	}
	if len(contextIDs) == 0 {
		return []domain.Task{}, nil
	}

	tasks, err := c.queryTasksWithRuntime(ctx, db, projectID, contextIDs...)
	if err != nil {
		return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
	}
	return tasks, nil
}

func dependencyContextIDsQuery(ids []string, opts dependencyContextOptions) (string, []any) {
	issueIDs := uniqueIssueIDStrings(ids)
	if len(issueIDs) == 0 {
		return "", nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(issueIDs)), ",")
	dependentQuery := ""
	if opts.includeDependents {
		typeFilter := ""
		if opts.parentChildDependentsOnly {
			typeFilter = " AND dependency_type IN (?, ?)"
		}
		dependentQuery = fmt.Sprintf(`
			UNION ALL
			SELECT issue_id AS id
			FROM issue_dependencies
			WHERE depends_on_id IN (%s) AND tombstoned_at IS NULL%s
		`, placeholders, typeFilter)
	}
	query := fmt.Sprintf(`
		SELECT DISTINCT id
		FROM (
			SELECT id
			FROM issues
			WHERE visibility = 'live' AND id IN (%s)
			UNION ALL
			SELECT depends_on_id AS id
			FROM issue_dependencies
			WHERE issue_id IN (%s) AND tombstoned_at IS NULL
			%s
		)
	`, placeholders, placeholders, dependentQuery)
	args := make([]any, 0, len(issueIDs)*3)
	for _, issueID := range issueIDs {
		args = append(args, issueID)
	}
	for _, issueID := range issueIDs {
		args = append(args, issueID)
	}
	if opts.includeDependents {
		for _, issueID := range issueIDs {
			args = append(args, issueID)
		}
		if opts.parentChildDependentsOnly {
			args = append(args, string(domain.DependencyParentChild), "parent_child")
		}
	}
	return query, args
}

func (c *Client) graphReadinessContextIDs(ctx context.Context, db *sql.DB, rootID string) ([]string, error) {
	startedAt := time.Now()
	query, args := graphReadinessContextIDsQuery(rootID)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		c.logSQLiteRead(ctx, "issue.graph_readiness_context_ids", startedAt, 0, err)
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0, 16)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			c.logSQLiteRead(ctx, "issue.graph_readiness_context_ids", startedAt, len(ids), err)
			return nil, err
		}
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		c.logSQLiteRead(ctx, "issue.graph_readiness_context_ids", startedAt, len(ids), err)
		return nil, err
	}
	c.logSQLiteRead(ctx, "issue.graph_readiness_context_ids", startedAt, len(ids), nil)
	return ids, nil
}

func (c *Client) parentChildSubtreeIDs(ctx context.Context, db *sql.DB, rootID string) ([]string, error) {
	query, args := parentChildSubtreeIDsQuery(rootID)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0, 16)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func graphReadinessContextIDsQuery(rootID string) (string, []any) {
	query := `
		WITH graph(id) AS (
			SELECT id
			FROM issues
			WHERE id = ? AND visibility = 'live'

			UNION

			SELECT closure.descendant_id
			FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_ancestor
			INNER JOIN issues child
				ON child.id = closure.descendant_id
				AND child.visibility = 'live'
			WHERE closure.project_id = ?
				AND closure.dependency_type = ?
				AND closure.ancestor_id = ?
		),
		context(id) AS (
			SELECT id FROM graph

			UNION

			SELECT dep.depends_on_id
			FROM graph graph_issue
			CROSS JOIN issue_dependencies dep INDEXED BY idx_dependencies_issue_active_type
			CROSS JOIN issues dep_issue
			WHERE dep.issue_id = graph_issue.id
				AND dep_issue.id = dep.depends_on_id
				AND dep_issue.visibility = 'live'
				AND dep.tombstoned_at IS NULL
		)
		SELECT id
		FROM context
	`
	return query, []any{
		strings.TrimSpace(rootID),
		issueGraphClosureProjectID,
		string(domain.DependencyParentChild),
		strings.TrimSpace(rootID),
	}
}

func parentChildSubtreeIDsQuery(rootID string) (string, []any) {
	query := `
		SELECT id
		FROM issues
		WHERE id = ? AND visibility = 'live'

		UNION

		SELECT closure.descendant_id AS id
		FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_ancestor
		INNER JOIN issues child
			ON child.id = closure.descendant_id
			AND child.visibility = 'live'
		WHERE closure.project_id = ?
			AND closure.dependency_type = ?
			AND closure.ancestor_id = ?
		ORDER BY id
	`
	return query, []any{
		strings.TrimSpace(rootID),
		issueGraphClosureProjectID,
		string(domain.DependencyParentChild),
		strings.TrimSpace(rootID),
	}
}

// GetManyMetadataWithRuntime fetches lightweight issue metadata plus stored runtime projection fields.
func (c *Client) GetManyMetadataWithRuntime(ctx context.Context, projectID string, ids []string) ([]domain.Task, error) {
	return c.getManyMetadataWithRuntime(ctx, projectID, ids, false, ArchiveExclude)
}

// GetManyMetadataWithAncestorContextRuntime fetches lightweight issue metadata plus parent ancestor context.
func (c *Client) GetManyMetadataWithAncestorContextRuntime(ctx context.Context, projectID string, ids []string) ([]domain.Task, error) {
	return c.getManyMetadataWithRuntime(ctx, projectID, ids, true, ArchiveExclude)
}

// GetRuntimeWorktreeIssueContext fetches only the requested issues and their
// parent-child ancestors, with lightweight metadata and runtime projection
// fields. Runtime projection maintenance uses this to answer eligibility and
// ancestor-base questions without hydrating the full project task list.
func (c *Client) GetRuntimeWorktreeIssueContext(ctx context.Context, projectID string, ids []string) ([]domain.Task, error) {
	if len(uniqueIssueIDStrings(ids)) == 0 {
		return []domain.Task{}, nil
	}
	return c.getManyMetadataWithRuntime(ctx, projectID, ids, true, ArchiveInclude)
}

func (c *Client) getManyMetadataWithRuntime(ctx context.Context, projectID string, ids []string, includeAncestors bool, archiveMode ArchiveMode) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	issueIDs := uniqueIssueIDStrings(ids)
	if len(issueIDs) == 0 {
		return nil, c.wrapError("get-many-metadata-runtime", "", domain.ErrNotFound)
	}
	contextIDs := append([]string(nil), issueIDs...)
	if includeAncestors {
		ancestorIDs, err := c.parentAncestorIDs(ctx, db, issueIDs)
		if err != nil {
			return nil, c.wrapError("get-many-metadata-runtime", strings.Join(issueIDs, ","), err)
		}
		contextIDs = uniqueIssueIDStrings(append(contextIDs, ancestorIDs...))
	}
	tasks, err := c.queryTaskMetadataWithRuntimeArchiveMode(ctx, db, projectID, archiveMode, contextIDs...)
	if err != nil {
		return nil, c.wrapError("get-many-metadata-runtime", strings.Join(issueIDs, ","), err)
	}
	return tasks, nil
}

func (c *Client) parentAncestorIDs(ctx context.Context, db *sql.DB, issueIDs []string) ([]string, error) {
	seen := map[string]struct{}{}
	seeds := make([]string, 0, len(issueIDs))
	for _, issueID := range issueIDs {
		issueID = strings.TrimSpace(issueID)
		if issueID == "" {
			continue
		}
		if _, ok := seen[issueID]; ok {
			continue
		}
		seen[issueID] = struct{}{}
		seeds = append(seeds, issueID)
	}
	if len(seeds) == 0 {
		return nil, nil
	}
	query, args := parentAncestorIDsQuery(seeds)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ancestors := make([]string, 0, len(seeds))
	for rows.Next() {
		var issueID string
		if err := rows.Scan(&issueID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(issueID) != "" {
			ancestors = append(ancestors, issueID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ancestors, nil
}

func parentAncestorIDsQuery(issueIDs []string) (string, []any) {
	seeds := uniqueIssueIDStrings(issueIDs)
	if len(seeds) == 0 {
		return "", nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(seeds)), ",")
	query := fmt.Sprintf(`
		SELECT DISTINCT closure.ancestor_id
		FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_descendant
		JOIN issues i ON i.id = closure.ancestor_id
		WHERE i.visibility = 'live'
			AND closure.project_id = ?
			AND closure.dependency_type = ?
			AND closure.descendant_id IN (%s)
	`, placeholders)
	args := make([]any, 0, len(seeds)+2)
	args = append(args, issueGraphClosureProjectID, string(domain.DependencyParentChild))
	for _, seed := range seeds {
		args = append(args, seed)
	}
	return query, args
}

// ListGraphDescendantIDs returns active descendant issue IDs for an ancestor in
// the materialized issue graph closure projection.
func (c *Client) ListGraphDescendantIDs(ctx context.Context, ancestorID, dependencyType string) ([]string, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	canonicalType, err := canonicalGraphClosureDependencyType(dependencyType)
	if err != nil {
		return nil, c.wrapError("list-graph-descendants", ancestorID, err)
	}
	ids, err := c.listGraphDescendantIDs(ctx, db, strings.TrimSpace(ancestorID), canonicalType)
	if err != nil {
		return nil, c.wrapError("list-graph-descendants", ancestorID, err)
	}
	return ids, nil
}

// ListGraphAncestorIDs returns active ancestor issue IDs for a descendant in
// the materialized issue graph closure projection.
func (c *Client) ListGraphAncestorIDs(ctx context.Context, descendantID, dependencyType string) ([]string, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	canonicalType, err := canonicalGraphClosureDependencyType(dependencyType)
	if err != nil {
		return nil, c.wrapError("list-graph-ancestors", descendantID, err)
	}
	ids, err := c.listGraphAncestorIDs(ctx, db, strings.TrimSpace(descendantID), canonicalType)
	if err != nil {
		return nil, c.wrapError("list-graph-ancestors", descendantID, err)
	}
	return ids, nil
}

func canonicalGraphClosureDependencyType(value string) (string, error) {
	canonicalType, err := canonicalDependencyType(value)
	if err != nil {
		return "", err
	}
	if canonicalType != string(domain.DependencyParentChild) {
		return "", fmt.Errorf("unsupported graph closure dependency type %q", strings.TrimSpace(value))
	}
	return canonicalType, nil
}

func (c *Client) listGraphDescendantIDs(ctx context.Context, queryer sqlIssueDBTX, ancestorID, dependencyType string) ([]string, error) {
	if strings.TrimSpace(ancestorID) == "" {
		return []string{}, nil
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT closure.descendant_id
		FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_ancestor
		INNER JOIN issues descendant
			ON descendant.id = closure.descendant_id
			AND descendant.visibility = 'live'
		WHERE closure.project_id = ?
			AND closure.dependency_type = ?
			AND closure.ancestor_id = ?
		ORDER BY closure.depth, closure.descendant_id
	`, issueGraphClosureProjectID, dependencyType, ancestorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssueIDRows(rows)
}

func (c *Client) listGraphAncestorIDs(ctx context.Context, queryer sqlIssueDBTX, descendantID, dependencyType string) ([]string, error) {
	if strings.TrimSpace(descendantID) == "" {
		return []string{}, nil
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT closure.ancestor_id
		FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_descendant
		INNER JOIN issues ancestor
			ON ancestor.id = closure.ancestor_id
			AND ancestor.visibility = 'live'
		WHERE closure.project_id = ?
			AND closure.dependency_type = ?
			AND closure.descendant_id = ?
		ORDER BY closure.depth, closure.ancestor_id
	`, issueGraphClosureProjectID, dependencyType, descendantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssueIDRows(rows)
}

func scanIssueIDRows(rows *sql.Rows) ([]string, error) {
	ids := []string{}
	for rows.Next() {
		var issueID string
		if err := rows.Scan(&issueID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(issueID) != "" {
			ids = append(ids, issueID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func uniqueIssueIDStrings(ids []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// Search queries issues by id/title/description.
func (c *Client) Search(ctx context.Context, query string) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return c.List(ctx)
	}

	like := "%" + q + "%"
	tasks, err := c.queryTasks(ctx, db, `
		SELECT
			id,
			title,
			COALESCE(description, ''),
			COALESCE(notes, ''),
			COALESCE(design, ''),
			COALESCE(acceptance, ''),
			COALESCE(assignee, ''),
			COALESCE(labels_json, '[]'),
			estimate,
			status,
			COALESCE(disposition, ''),
			COALESCE(engagement, ''),
			COALESCE(visibility, ''),
			archived_at,
			priority,
			issue_type,
			COALESCE(implementations_json, '[]'),
			created_at,
			updated_at
		FROM issues
		WHERE
			visibility = 'live'
			AND (id LIKE ? OR title LIKE ? OR description LIKE ?)
		ORDER BY updated_at DESC
		LIMIT 200
	`, like, like, like)
	if err != nil {
		return nil, c.wrapError("search", query, err)
	}
	return tasks, nil
}

// Ready fetches open tasks that do not have unresolved blockers.
func (c *Client) Ready(ctx context.Context) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	tasks, err := c.queryTasks(ctx, db, `
		SELECT
			i.id,
			i.title,
			COALESCE(i.description, ''),
			COALESCE(i.notes, ''),
			COALESCE(i.design, ''),
			COALESCE(i.acceptance, ''),
			COALESCE(i.assignee, ''),
			COALESCE(i.labels_json, '[]'),
			i.estimate,
			i.status,
			COALESCE(i.disposition, ''),
			COALESCE(i.engagement, ''),
			COALESCE(i.visibility, ''),
			i.archived_at,
			i.priority,
			i.issue_type,
			COALESCE(i.implementations_json, '[]'),
			i.created_at,
			i.updated_at
		FROM issues i
		WHERE
			i.visibility = 'live'
			AND i.disposition = 'ready'
			AND i.engagement = 'idle'
			AND NOT EXISTS (
				SELECT 1
				FROM issue_dependencies d
				JOIN issues dep ON dep.id = d.depends_on_id
				WHERE
					d.issue_id = i.id
					AND d.tombstoned_at IS NULL
					AND d.dependency_type = 'blocks'
					AND dep.visibility = 'live'
					AND dep.disposition NOT IN ('completed','cancelled')
			)
		ORDER BY i.priority ASC, i.updated_at DESC
	`)
	if err != nil {
		return nil, c.wrapError("ready", "", err)
	}
	return tasks, nil
}

// Update changes an issue status.
func (c *Client) Update(ctx context.Context, id string, status domain.Status) error {
	return c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			return c.updateLocked(ctx, id, status, false)
		})
	})
}

// RepairReadyIdleEngagement atomically promotes the compatibility store shape
// for canonical ready+idle to ready+working. It deliberately uses a guarded
// update so a concurrent terminal/backlog/archive transition always wins.
func (c *Client) RepairReadyIdleEngagement(ctx context.Context, id string) (bool, error) {
	var repaired bool
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return c.wrapError("repair-ready-idle", id, err)
			}
			defer func() {
				if tx != nil {
					_ = tx.Rollback()
				}
			}()
			now := time.Now().UTC().Format(time.RFC3339Nano)
			res, err := tx.ExecContext(ctx, `UPDATE issues
					SET engagement='working', status=?, lifecycle_state='active', review_state='none', closed_outcome='none', updated_at=?
					WHERE id=? AND disposition='ready' AND engagement='idle' AND visibility='live'`, domain.StatusInProgress, now, strings.TrimSpace(id))
			if err != nil {
				return c.wrapError("repair-ready-idle", id, err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return c.wrapError("repair-ready-idle", id, err)
			}
			if affected == 1 {
				if err := c.appendIssueObservationEvent(ctx, tx, strings.TrimSpace(id), domain.IssueEventIssueStatusChanged, map[string]any{"from_status": string(domain.StatusOpen), "to_status": string(domain.StatusInProgress), "reason": "live_managed_runtime"}); err != nil {
					return c.wrapError("repair-ready-idle", id, err)
				}
				repaired = true
			}
			if err := tx.Commit(); err != nil {
				return c.wrapError("repair-ready-idle", id, err)
			}
			tx = nil
			return nil
		})
	})
	return repaired, err
}

func (c *Client) updateLocked(ctx context.Context, id string, status domain.Status, releaseExecutionLease bool) error {
	return c.updateLockedWithPrecondition(ctx, id, status, releaseExecutionLease, nil)
}

func (c *Client) updateLockedWithPrecondition(ctx context.Context, id string, status domain.Status, releaseExecutionLease bool, precondition func(context.Context, *sql.Tx) error) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("update", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	var oldPriorityRaw int
	var oldStateCols issueStateColumns
	if err := tx.QueryRowContext(ctx, `
		SELECT
			status,
			COALESCE(disposition, ''),
			COALESCE(engagement, ''),
			COALESCE(visibility, ''),
			archived_at,
			priority
		FROM issues
		WHERE id = ? AND visibility = 'live'
	`, id).Scan(
		&oldStateCols.LegacyStatus,
		&oldStateCols.Disposition,
		&oldStateCols.Engagement,
		&oldStateCols.Visibility,
		&oldStateCols.ArchivedAt,
		&oldPriorityRaw,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.wrapError("update", id, domain.ErrNotFound)
		}
		return c.wrapError("update", id, err)
	}
	oldState, err := issueStateFromColumns(id, domain.Priority(oldPriorityRaw), oldStateCols)
	if err != nil {
		return c.wrapError("update", id, err)
	}
	nextState, err := issueStateFromStatus(status)
	if err != nil {
		return c.wrapError("update", id, err)
	}
	if err := domain.ValidateIssueStateTransition(oldState, nextState); err != nil {
		return c.wrapError("update", id, err)
	}
	if precondition != nil {
		if err := precondition(ctx, tx); err != nil {
			return c.wrapError("update", id, err)
		}
	}
	if oldState.Engagement == domain.IssueEngagementReviewRequested && nextState.Engagement != domain.IssueEngagementReviewRequested {
		var activeReviewLeases int
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM issue_coordination_leases
			WHERE issue_id=? AND purpose=? AND (expires_at IS NULL OR expires_at>?)
		`, id, domain.CoordinationLeaseReview, now).Scan(&activeReviewLeases); err != nil {
			return c.wrapError("update", id, err)
		}
		if activeReviewLeases > 0 {
			return c.wrapError("update", id, fmt.Errorf("%w: active review admission lease fences the current review epoch", domain.ErrConflict))
		}
	}
	if nextState.Workflow() == domain.IssueWorkflowBacklog {
		hasAdmittedAncestor, err := c.hasLiveAdmittedAncestor(ctx, tx, id)
		if err != nil {
			return c.wrapError("update", id, err)
		}
		if hasAdmittedAncestor {
			return c.wrapError("update", id, fmt.Errorf("%w: descendant cannot regress to backlog below an admitted ancestor", domain.ErrConflict))
		}
	}
	if status == domain.StatusInReview || nextState.Workflow() == domain.IssueWorkflowClosed {
		openChildCount, err := c.countLiveNonterminalDescendants(ctx, tx, id)
		if err != nil {
			return c.wrapError("update", id, err)
		}
		if openChildCount > 0 {
			return c.wrapError("update", id, fmt.Errorf("%w: parent review or close requires all live descendants terminal; %d remain nonterminal", domain.ErrConflict, openChildCount))
		}
	}
	if releaseExecutionLease {
		res, err := tx.ExecContext(ctx, `DELETE FROM issue_coordination_leases WHERE issue_id = ? AND purpose = ?`, id, domain.CoordinationLeaseExecution)
		if err != nil {
			return c.wrapError("update", id, fmt.Errorf("release execution lease before terminal status write: %w", err))
		}
		released, err := res.RowsAffected()
		if err != nil {
			return c.wrapError("update", id, fmt.Errorf("inspect execution lease release before terminal status write: %w", err))
		}
		if released > 0 {
			if err := c.appendIssueObservationEvent(ctx, tx, id, domain.IssueEventIssueOwnershipChanged, map[string]any{
				"action":      "released",
				"released_by": "task.close",
				"forced":      true,
				"purpose":     domain.CoordinationLeaseExecution,
				"reason":      "terminal_close",
			}); err != nil {
				return c.wrapError("update", id, err)
			}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var closedAt *string
	if nextState.Workflow() == domain.IssueWorkflowClosed {
		closedAt = &now
	}
	writeState := issueStateWriteValuesFromState(nextState, nil)
	res, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET
			disposition = ?,
			engagement = ?,
			visibility = ?,
			status = ?,
			lifecycle_state = ?,
			closed_outcome = ?,
			review_state = ?,
			archived_at = ?,
			deleted_at = ?,
			updated_at = ?,
			closed_at = ?
		WHERE id = ? AND visibility = 'live'
	`, writeState.Disposition, writeState.Engagement, writeState.Visibility, writeState.LegacyStatus, writeState.Lifecycle, writeState.ClosedOutcome, writeState.Review, writeState.ArchivedAt, writeState.ArchivedAt, now, closedAt, id)
	if err != nil {
		return c.wrapError("update", id, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("update", id, domain.ErrNotFound)
	}
	oldLegacyStatus := legacyStatusFromIssueState(oldState)
	if oldLegacyStatus != domain.Status(writeState.LegacyStatus) {
		if err := c.appendIssueObservationEvent(ctx, tx, id, domain.IssueEventIssueStatusChanged, map[string]any{
			"from_status": string(oldLegacyStatus),
			"to_status":   writeState.LegacyStatus,
		}); err != nil {
			return c.wrapError("update", id, err)
		}
	}
	if nextState.Disposition == domain.IssueDispositionReady && oldState.Disposition == domain.IssueDispositionBacklog {
		if err := c.enforceAncestorLifecycleFloor(ctx, tx, id, "ancestor_lifecycle_floor_admitted"); err != nil {
			return c.wrapError("update", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return c.wrapError("update", id, err)
	}
	tx = nil
	return nil
}

// UpdateWithRuntime changes an issue status and returns the changed issue.
func (c *Client) UpdateWithRuntime(ctx context.Context, projectID, id string, status domain.Status) (domain.Task, error) {
	if err := c.Update(ctx, id, status); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, id)
}

// CloseWithRuntime atomically releases the execution lease before writing a
// terminal issue state, then returns the changed issue with runtime projection.
func (c *Client) CloseWithRuntime(ctx context.Context, projectID, id string, status domain.Status) (domain.Task, error) {
	nextState, err := issueStateFromStatus(status)
	if err != nil {
		return domain.Task{}, c.wrapError("close", strings.TrimSpace(id), err)
	}
	if nextState.Workflow() != domain.IssueWorkflowClosed {
		return domain.Task{}, c.wrapError("close", strings.TrimSpace(id), fmt.Errorf("status %s is not terminal", status))
	}
	if err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			return c.updateLocked(ctx, id, status, true)
		})
	}); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, id)
}

// CloseWithRuntimeReviewEvidence revalidates the exact accepted evidence in
// the same SQLite transaction that releases the execution lease and writes the
// terminal issue state. A newer or altered evidence event fails closed.
func (c *Client) CloseWithRuntimeReviewEvidence(ctx context.Context, projectID, id string, status domain.Status, pin ReviewEvidencePin) (domain.Task, error) {
	nextState, err := issueStateFromStatus(status)
	if err != nil {
		return domain.Task{}, c.wrapError("close", strings.TrimSpace(id), err)
	}
	if nextState.Workflow() != domain.IssueWorkflowClosed {
		return domain.Task{}, c.wrapError("close", strings.TrimSpace(id), fmt.Errorf("status %s is not terminal", status))
	}
	if strings.TrimSpace(pin.Source) != "issue_event" || pin.EventID <= 0 || pin.Seq != 0 || strings.TrimSpace(pin.Digest) == "" {
		return domain.Task{}, c.wrapError("close", strings.TrimSpace(id), errors.New("review evidence pin must identify one durable issue event"))
	}
	if err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			return c.updateLockedWithPrecondition(ctx, id, status, true, func(ctx context.Context, tx *sql.Tx) error {
				return validateTerminalReviewEvidencePin(ctx, tx, id, pin)
			})
		})
	}); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, id)
}

// CloseWithRuntimeReviewEvidenceFence consumes the matching durable evidence
// fence in the same transaction as final pin validation and terminal state.
func (c *Client) CloseWithRuntimeReviewEvidenceFence(ctx context.Context, projectID, id string, status domain.Status, pin ReviewEvidencePin, fenceToken string) (domain.Task, error) {
	nextState, err := issueStateFromStatus(status)
	if err != nil {
		return domain.Task{}, c.wrapError("close", strings.TrimSpace(id), err)
	}
	if nextState.Workflow() != domain.IssueWorkflowClosed {
		return domain.Task{}, c.wrapError("close", strings.TrimSpace(id), fmt.Errorf("status %s is not terminal", status))
	}
	fenceToken = strings.TrimSpace(fenceToken)
	if strings.TrimSpace(pin.Source) != "issue_event" || pin.EventID <= 0 || pin.Seq != 0 || strings.TrimSpace(pin.Digest) == "" || fenceToken == "" {
		return domain.Task{}, c.wrapError("close", strings.TrimSpace(id), errors.New("review evidence close requires one durable issue-event pin and fence"))
	}
	if err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			return c.updateLockedWithPrecondition(ctx, id, status, true, func(ctx context.Context, tx *sql.Tx) error {
				if err := validateTerminalReviewEvidencePin(ctx, tx, id, pin); err != nil {
					return err
				}
				result, err := tx.ExecContext(ctx, `DELETE FROM issue_coordination_leases
					WHERE issue_id=? AND purpose=? AND owner_id=? AND owner_kind=?`, strings.TrimSpace(id), domain.CoordinationLeaseReview, fenceToken, reviewEvidenceCloseFenceOwnerKind)
				if err != nil {
					return err
				}
				removed, err := result.RowsAffected()
				if err != nil {
					return err
				}
				if removed != 1 {
					return fmt.Errorf("%w: accepted review evidence close fence is missing or changed", domain.ErrConflict)
				}
				return nil
			})
		})
	}); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, id)
}

func validateTerminalReviewEvidencePin(ctx context.Context, tx *sql.Tx, issueID string, pin ReviewEvidencePin) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE issue_id = ?
		  AND (
			event_type = ?
			OR LOWER(REPLACE(REPLACE(TRIM(event_type), '_', '.'), '-', '.')) IN (?, ?, ?, ?)
		  )
		ORDER BY observed_at ASC, id ASC
	`, strings.TrimSpace(issueID),
		string(domain.IssueEventIssueStatusChanged),
		string(domain.IssueEventEvidenceSubmitted),
		"worker.integration.ready",
		"worker.ready",
		"worker.complete",
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	events := make([]domain.IssueObservationEvent, 0, 16)
	for rows.Next() {
		event, err := scanIssueObservationEvent(rows)
		if err != nil {
			return err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	evidence := domain.ReduceReviewReadyEvidence(events).LatestEvidence
	if evidence == nil || !evidence.Validation.Complete {
		return errors.New("reviewed evidence is no longer complete; fresh review required")
	}
	if evidence.SourceEvent.ID != pin.EventID {
		return fmt.Errorf("reviewed evidence changed from event %d to event %d; fresh review required", pin.EventID, evidence.SourceEvent.ID)
	}
	body, err := json.Marshal(evidence.Evidence)
	if err != nil {
		return fmt.Errorf("encode terminal review evidence: %w", err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	if digest != strings.TrimSpace(pin.Digest) {
		return errors.New("reviewed evidence digest changed; fresh review required")
	}
	return nil
}

type OwnershipClaimParams struct {
	OwnerID                 string
	OwnerKind               string
	TTL                     time.Duration
	Force                   bool
	ReleasedBy              string
	Purpose                 domain.CoordinationLeasePurpose
	ExpectedReviewAdmission *ReviewAdmissionPin
	ExpectedParentIssueID   string
	ReviewSourceOID         string
}

func (c *Client) ClaimOwnershipWithRuntime(ctx context.Context, projectID, issueID string, params OwnershipClaimParams) (domain.Task, error) {
	if err := c.claimOwnership(ctx, issueID, params); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, issueID)
}

func (c *Client) ReleaseOwnershipWithRuntime(ctx context.Context, projectID, issueID string, params OwnershipClaimParams) (domain.Task, error) {
	if err := c.releaseOwnership(ctx, issueID, params); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, issueID)
}

func (c *Client) claimOwnership(ctx context.Context, issueID string, params OwnershipClaimParams) error {
	issueID = strings.TrimSpace(issueID)
	ownerID := strings.TrimSpace(params.OwnerID)
	ownerKind := strings.TrimSpace(params.OwnerKind)
	if ownerID == "" {
		return c.wrapError("claim-ownership", issueID, errors.New("owner id is required"))
	}
	if ownerKind == "" {
		ownerKind = "agent"
	}
	purpose := params.Purpose
	if purpose == "" {
		purpose = domain.CoordinationLeaseExecution
	}
	if !purpose.Valid() {
		return c.wrapError("claim-ownership", issueID, fmt.Errorf("invalid ownership purpose %q", purpose))
	}
	if params.ExpectedReviewAdmission != nil && purpose != domain.CoordinationLeaseReview {
		return c.wrapError("claim-ownership", issueID, errors.New("review admission pin requires review lease purpose"))
	}
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		db, err := c.dbHandle()
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return c.wrapError("claim-ownership", issueID, err)
		}
		defer func() {
			if tx != nil {
				_ = tx.Rollback()
			}
		}()

		_, err = c.issueOwnershipForUpdate(ctx, tx, issueID)
		if err != nil {
			return c.wrapError("claim-ownership", issueID, err)
		}
		if err := issueLeaseEligibilityForUpdate(ctx, tx, issueID, purpose); err != nil {
			return c.wrapError("claim-ownership", issueID, err)
		}
		if params.ExpectedReviewAdmission != nil {
			if err := validateReviewAdmissionPin(ctx, tx, issueID, *params.ExpectedReviewAdmission); err != nil {
				return c.wrapError("claim-ownership", issueID, err)
			}
			if err := validateReviewAdmissionParent(ctx, tx, issueID, params.ExpectedParentIssueID); err != nil {
				return c.wrapError("claim-ownership", issueID, err)
			}
		}
		now := time.Now().UTC()
		var lease *domain.CoordinationLease
		lease, err = coordinationLeaseForUpdate(ctx, tx, issueID, purpose)
		if err != nil {
			return c.wrapError("claim-ownership", issueID, err)
		}
		if lease != nil && !lease.IsExpired(now) && !strings.EqualFold(lease.OwnerID, ownerID) && !params.Force {
			return c.wrapError("claim-ownership", issueID, fmt.Errorf("%w: %s lease owned by %s", domain.ErrConflict, purpose, lease.OwnerID))
		}
		var expiresAt any
		var expiresPayload any
		if params.TTL > 0 {
			expires := now.Add(params.TTL).UTC()
			expiresAt = expires.Format(time.RFC3339Nano)
			expiresPayload = expiresAt
		}
		nowRaw := now.Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_coordination_leases
				(issue_id, purpose, owner_id, owner_kind, claimed_at, expires_at)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(issue_id, purpose) DO UPDATE SET owner_id=excluded.owner_id,
				owner_kind=excluded.owner_kind, claimed_at=excluded.claimed_at, expires_at=excluded.expires_at`,
			issueID, purpose, ownerID, ownerKind, nowRaw, expiresAt); err != nil {
			return c.wrapError("claim-ownership", issueID, err)
		}
		claimPayload := map[string]any{
			"action":           "claimed",
			"owner_id":         ownerID,
			"owner_kind":       ownerKind,
			"owner_expires_at": expiresPayload,
			"forced":           params.Force,
			"purpose":          purpose,
		}
		if params.ExpectedReviewAdmission != nil {
			claimPayload["review_epoch_event_id"] = params.ExpectedReviewAdmission.ReviewEpochEventID
			claimPayload["review_parent_issue_id"] = strings.TrimSpace(params.ExpectedParentIssueID)
			claimPayload["review_source_oid"] = strings.TrimSpace(params.ReviewSourceOID)
			if params.ExpectedReviewAdmission.Evidence != nil {
				claimPayload["review_evidence_event_id"] = params.ExpectedReviewAdmission.Evidence.EventID
				claimPayload["review_evidence_digest"] = params.ExpectedReviewAdmission.Evidence.Digest
			}
		}
		if err := c.appendIssueObservationEvent(ctx, tx, issueID, domain.IssueEventIssueOwnershipChanged, claimPayload); err != nil {
			return c.wrapError("claim-ownership", issueID, err)
		}
		if err := tx.Commit(); err != nil {
			return c.wrapError("claim-ownership", issueID, err)
		}
		tx = nil
		return nil
	})
}

func validateReviewAdmissionParent(ctx context.Context, tx *sql.Tx, issueID, expectedParentID string) error {
	expectedParentID = strings.TrimSpace(expectedParentID)
	rows, err := tx.QueryContext(ctx, `
		SELECT depends_on_id
		FROM issue_dependencies
		WHERE issue_id=? AND tombstoned_at IS NULL
		  AND dependency_type IN ('parent-child','parent_child')
		ORDER BY depends_on_id
	`, strings.TrimSpace(issueID))
	if err != nil {
		return err
	}
	defer rows.Close()
	parents := make([]string, 0, 1)
	for rows.Next() {
		var parentID string
		if err := rows.Scan(&parentID); err != nil {
			return err
		}
		parents = append(parents, strings.TrimSpace(parentID))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if expectedParentID == "" && len(parents) == 0 {
		return nil
	}
	if len(parents) == 1 && naming.IssueIDsEqual(parents[0], expectedParentID) {
		return nil
	}
	return fmt.Errorf("%w: review parent changed from %q to %v", domain.ErrConflict, expectedParentID, parents)
}

func issueLeaseEligibilityForUpdate(ctx context.Context, tx *sql.Tx, issueID string, purpose domain.CoordinationLeasePurpose) error {
	var disposition, engagement, visibility string
	if err := tx.QueryRowContext(ctx, `SELECT disposition,engagement,visibility FROM issues WHERE id=?`, issueID).Scan(&disposition, &engagement, &visibility); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		return err
	}
	eligible := visibility == string(domain.IssueVisibilityLive)
	switch purpose {
	case domain.CoordinationLeaseExecution:
		eligible = eligible && disposition == string(domain.IssueDispositionReady)
	case domain.CoordinationLeaseReview:
		eligible = eligible && disposition == string(domain.IssueDispositionReady) && engagement == string(domain.IssueEngagementReviewRequested)
		if eligible {
			outcome, err := latestTrustedReviewOutcomeForCurrentEpoch(ctx, tx, issueID)
			if err != nil {
				return err
			}
			if outcome == domain.ReviewOutcomeAccepted {
				return fmt.Errorf("%w: durable accepted review is awaiting authoritative close", domain.ErrConflict)
			}
		}
	case domain.CoordinationLeaseOrchestration:
		eligible = eligible && (disposition == string(domain.IssueDispositionBacklog) || disposition == string(domain.IssueDispositionReady))
	default:
		eligible = false
	}
	if !eligible {
		return fmt.Errorf("%w: issue state is ineligible for %s lease", domain.ErrConflict, purpose)
	}
	return nil
}

func latestTrustedReviewOutcomeForCurrentEpoch(ctx context.Context, tx *sql.Tx, issueID string) (domain.ReviewOutcome, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE issue_id = ? AND event_type IN (?, ?)
		ORDER BY id DESC
	`, issueID, domain.IssueEventReviewCompleted, domain.IssueEventIssueStatusChanged)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		event, err := scanIssueObservationEvent(rows)
		if err != nil {
			return "", err
		}
		if domain.IsReviewRequestTransition(event) {
			return "", nil
		}
		if outcome, trusted := domain.TrustedReviewOutcome(event); trusted {
			return outcome, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", nil
}

func (c *Client) releaseOwnership(ctx context.Context, issueID string, params OwnershipClaimParams) error {
	issueID = strings.TrimSpace(issueID)
	actorID := strings.TrimSpace(params.OwnerID)
	if actorID == "" {
		actorID = strings.TrimSpace(params.ReleasedBy)
	}
	purpose := params.Purpose
	if purpose == "" {
		purpose = domain.CoordinationLeaseExecution
	}
	if !purpose.Valid() {
		return c.wrapError("release-ownership", issueID, fmt.Errorf("invalid ownership purpose %q", purpose))
	}
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		db, err := c.dbHandle()
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return c.wrapError("release-ownership", issueID, err)
		}
		defer func() {
			if tx != nil {
				_ = tx.Rollback()
			}
		}()

		_, err = c.issueOwnershipForUpdate(ctx, tx, issueID)
		if err != nil {
			return c.wrapError("release-ownership", issueID, err)
		}
		now := time.Now().UTC()
		var lease *domain.CoordinationLease
		lease, err = coordinationLeaseForUpdate(ctx, tx, issueID, purpose)
		if err != nil {
			return c.wrapError("release-ownership", issueID, err)
		}
		if lease != nil && !lease.IsExpired(now) && !strings.EqualFold(lease.OwnerID, actorID) && !params.Force {
			return c.wrapError("release-ownership", issueID, fmt.Errorf("%w: %s lease owned by %s", domain.ErrConflict, purpose, lease.OwnerID))
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM issue_coordination_leases WHERE issue_id = ? AND purpose = ?`, issueID, purpose); err != nil {
			return c.wrapError("release-ownership", issueID, err)
		}
		if err := c.appendIssueObservationEvent(ctx, tx, issueID, domain.IssueEventIssueOwnershipChanged, map[string]any{
			"action":      "released",
			"released_by": actorID,
			"forced":      params.Force,
			"purpose":     purpose,
		}); err != nil {
			return c.wrapError("release-ownership", issueID, err)
		}
		if err := tx.Commit(); err != nil {
			return c.wrapError("release-ownership", issueID, err)
		}
		tx = nil
		return nil
	})
}

func (c *Client) issueOwnershipForUpdate(ctx context.Context, tx *sql.Tx, issueID string) (domain.Task, error) {
	var task domain.Task
	err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM issues
		WHERE id = ? AND visibility = 'live'
	`, issueID).Scan(&task.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Task{}, domain.ErrNotFound
		}
		return domain.Task{}, err
	}
	lease, err := coordinationLeaseForUpdate(ctx, tx, issueID, domain.CoordinationLeaseExecution)
	if err != nil {
		return domain.Task{}, err
	}
	if lease != nil {
		task.Ownership = &domain.IssueOwnership{OwnerID: lease.OwnerID, OwnerKind: lease.OwnerKind, ClaimedAt: lease.ClaimedAt, ExpiresAt: lease.ExpiresAt}
	}
	return task, nil
}

func coordinationLeaseForUpdate(ctx context.Context, tx *sql.Tx, issueID string, purpose domain.CoordinationLeasePurpose) (*domain.CoordinationLease, error) {
	var lease domain.CoordinationLease
	var claimedRaw, expiresRaw string
	err := tx.QueryRowContext(ctx, `SELECT purpose, owner_id, owner_kind, claimed_at, COALESCE(expires_at, '')
		FROM issue_coordination_leases WHERE issue_id = ? AND purpose = ?`, issueID, purpose).
		Scan(&lease.Purpose, &lease.OwnerID, &lease.OwnerKind, &claimedRaw, &expiresRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lease.ClaimedAt = parseTimestamp(claimedRaw)
	lease.ExpiresAt = parseOptionalTimestamp(expiresRaw)
	return &lease, nil
}

func (c *Client) countOpenChildren(ctx context.Context, db sqlIssueQueryer, parentID string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM issue_dependencies d
		JOIN issues child ON child.id = d.issue_id
		WHERE
			d.depends_on_id = ?
			AND d.tombstoned_at IS NULL
			AND d.dependency_type IN ('parent-child', 'parent_child')
			AND child.visibility = 'live'
			AND child.disposition NOT IN ('completed','cancelled')
	`, parentID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// CreateTaskParams contains parameters for creating a new issue.
type CreateTaskParams struct {
	Title           string
	Description     string
	Type            domain.TaskType
	Priority        domain.Priority
	Status          domain.Status
	Lifecycle       domain.IssueWorkflow
	Assignee        string
	Labels          []string
	Implementations []string
	Design          string
	Notes           string
	Acceptance      string
	Estimate        *int
	ParentID        *string
}

type UpsertExternalIssueRefParams struct {
	IssueID       string
	Provider      string
	ProviderScope string
	RemoteKey     string
	DisplayKey    string
	URL           string
	Metadata      map[string]string
}

// Create inserts a new issue and returns its generated id.
func (c *Client) Create(ctx context.Context, params CreateTaskParams) (string, error) {
	var issueID string
	err := c.retrySQLiteBusy(ctx, func() error {
		var err error
		issueID, err = c.createOnce(ctx, params)
		return err
	})
	if err != nil {
		return "", err
	}
	return issueID, nil
}

func (c *Client) createOnce(ctx context.Context, params CreateTaskParams) (string, error) {
	var issueID string
	err := c.withMutationLock(ctx, func(ctx context.Context) error {
		var err error
		issueID, err = c.createOnceLocked(ctx, params)
		return err
	})
	return issueID, err
}

func (c *Client) createOnceLocked(ctx context.Context, params CreateTaskParams) (string, error) {
	db, err := c.dbHandle()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(params.Title) == "" {
		return "", c.wrapError("create", "", errors.New("title is required"))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", c.wrapError("create", "", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	nextIndex := 0
	if raw, err := c.getMetaValue(ctx, tx, nextAlphaIssueIndexMetaKey); err == nil {
		nextIndex = parseNextAlphaIssueIndex(raw)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", c.wrapError("create", "", err)
	}

	existing, err := c.loadAllocatedIssueIDs(ctx, tx)
	if err != nil {
		return "", c.wrapError("create", "", err)
	}

	issueID, nextReserved := allocateNextAlphaIssueID(nextIndex, existing)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := c.reserveIssueID(ctx, tx, issueID, now, "issue.create"); err != nil {
		return "", c.wrapError("create", issueID, err)
	}
	issueType := params.Type
	if issueType == "" {
		issueType = domain.TypeTask
	}
	status := params.Status
	if status == "" {
		status = domain.StatusOpen
	}
	state, err := issueStateFromStatus(status)
	if err != nil {
		return "", c.wrapError("create", issueID, err)
	}
	if params.Lifecycle != "" {
		if state.Workflow() != domain.IssueWorkflowBacklog && state.Workflow() != domain.IssueWorkflowOpen {
			return "", c.wrapError("create", issueID, fmt.Errorf("lifecycle %s cannot be combined with status %s", params.Lifecycle, status))
		}
		state, err = issueStateWithLifecycle(state, params.Lifecycle)
		if err != nil {
			return "", c.wrapError("create", issueID, err)
		}
	}
	writeState := issueStateWriteValuesFromState(state, nil)
	labelsJSON, err := marshalOptionalStringSlice(params.Labels)
	if err != nil {
		return "", c.wrapError("create", issueID, err)
	}
	implementationsJSON, err := marshalOptionalStringSlice(params.Implementations)
	if err != nil {
		return "", c.wrapError("create", issueID, err)
	}
	var closedAt any
	if state.Workflow() == domain.IssueWorkflowClosed {
		closedAt = now
	}
	var estimate any
	if params.Estimate != nil {
		estimate = *params.Estimate
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issues (
			id,
			title,
			description,
			status,
			disposition,
			engagement,
			visibility,
			priority,
			issue_type,
			created_at,
			updated_at,
			closed_at,
			assignee,
			labels_json,
			implementations_json,
			design,
			notes,
			acceptance,
			estimate,
			deleted_at,
			lifecycle_state,
			closed_outcome,
			review_state,
			archived_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, issueID, params.Title, nullableString(params.Description), writeState.LegacyStatus, writeState.Disposition, writeState.Engagement, writeState.Visibility, int(params.Priority), string(issueType), now, now, closedAt, nullableString(params.Assignee), labelsJSON, implementationsJSON, nullableString(params.Design), nullableString(params.Notes), nullableString(params.Acceptance), estimate, writeState.ArchivedAt, writeState.Lifecycle, writeState.ClosedOutcome, writeState.Review, writeState.ArchivedAt); err != nil {
		return "", c.wrapError("create", issueID, err)
	}
	createPayload := map[string]any{
		"title":      params.Title,
		"status":     writeState.LegacyStatus,
		"issue_type": string(issueType),
		"priority":   int(params.Priority),
	}
	if params.ParentID != nil && strings.TrimSpace(*params.ParentID) != "" {
		createPayload["parent_id"] = strings.TrimSpace(*params.ParentID)
	}
	if err := c.appendIssueObservationEvent(ctx, tx, issueID, domain.IssueEventIssueCreated, createPayload); err != nil {
		return "", c.wrapError("create", issueID, err)
	}

	if params.ParentID != nil && strings.TrimSpace(*params.ParentID) != "" {
		parentID := strings.TrimSpace(*params.ParentID)
		parentExists, err := c.issueExists(ctx, tx, parentID)
		if err != nil {
			return "", c.wrapError("create", issueID, err)
		}
		if !parentExists {
			return "", c.wrapError("create", issueID, domain.ErrNotFound)
		}
		if err := c.reopenClosedParentForActiveChild(ctx, tx, issueID, parentID); err != nil {
			return "", c.wrapError("create", issueID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
			VALUES (?, ?, ?, NULL)
			ON CONFLICT(issue_id, depends_on_id, dependency_type)
			DO UPDATE SET tombstoned_at = NULL
		`, issueID, parentID, string(domain.DependencyParentChild)); err != nil {
			return "", c.wrapError("create", issueID, err)
		}
		if err := c.appendIssueObservationEvent(ctx, tx, issueID, domain.IssueEventIssueDependencyAdded, map[string]any{
			"depends_on_id":   parentID,
			"dependency_type": string(domain.DependencyParentChild),
		}); err != nil {
			return "", c.wrapError("create", issueID, err)
		}
		if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
			return "", c.wrapError("create", issueID, err)
		}
		if err := c.enforceAncestorLifecycleFloor(ctx, tx, issueID, "ancestor_lifecycle_floor_create"); err != nil {
			return "", c.wrapError("create", issueID, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value
	`, nextAlphaIssueIndexMetaKey, strconv.Itoa(nextReserved)); err != nil {
		return "", c.wrapError("create", issueID, err)
	}

	if err := tx.Commit(); err != nil {
		return "", c.wrapError("create", issueID, err)
	}
	tx = nil
	return issueID, nil
}

// CreateWithRuntime inserts a new issue and returns the created task with runtime projection fields.
func (c *Client) CreateWithRuntime(ctx context.Context, projectID string, params CreateTaskParams) (domain.Task, error) {
	id, err := c.Create(ctx, params)
	if err != nil {
		return domain.Task{}, err
	}
	var task domain.Task
	err = c.retrySQLiteBusy(ctx, func() error {
		var err error
		task, err = c.GetWithRuntime(ctx, projectID, id)
		return err
	})
	if err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

func (c *Client) retrySQLiteBusy(ctx context.Context, fn func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	retryCtx, cancel := context.WithTimeout(ctx, c.sqliteBusyRetryBudget)
	defer cancel()
	var lastErr error
	for {
		if retryCtx.Err() != nil {
			if lastErr != nil {
				return lastErr
			}
			return retryCtx.Err()
		}
		err := fn()
		if err == nil {
			return nil
		}
		if !IsSQLiteBusy(err) {
			return err
		}
		lastErr = err
		if retryCtx.Err() != nil {
			return lastErr
		}
		if err := c.sqliteBusyWait(retryCtx, c.sqliteBusyRetryDelay); err != nil {
			return lastErr
		}
	}
}

// IsSQLiteBusy reports whether err wraps a temporary SQLite busy/locked result.
func IsSQLiteBusy(err error) bool {
	for err != nil {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqliteBusyPrimaryCode {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

// IsSQLiteCorrupt reports whether err is a typed store quarantine or wraps a
// SQLite result with primary code SQLITE_CORRUPT (11).
func IsSQLiteCorrupt(err error) bool {
	if errors.Is(err, ErrSQLiteCorrupt) {
		return true
	}
	for err != nil {
		var coded interface{ Code() int }
		if errors.As(err, &coded) && coded.Code()&0xff == sqliteCorruptPrimaryCode {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

func (c *Client) UpsertExternalIssueRef(ctx context.Context, params UpsertExternalIssueRefParams) (domain.ExternalIssueRef, error) {
	var out domain.ExternalIssueRef
	err := c.withMutationLock(ctx, func(lockCtx context.Context) error {
		var err error
		out, err = c.upsertExternalIssueRefLocked(lockCtx, params)
		return err
	})
	return out, err
}

func (c *Client) upsertExternalIssueRefLocked(ctx context.Context, params UpsertExternalIssueRefParams) (domain.ExternalIssueRef, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.ExternalIssueRef{}, err
	}
	normalized, err := normalizeExternalIssueRefParams(params)
	if err != nil {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", params.IssueID, err)
	}
	exists, err := c.issueExists(ctx, db, normalized.IssueID)
	if err != nil {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", normalized.IssueID, err)
	}
	if !exists {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", normalized.IssueID, fmt.Errorf("issue not found: %s", normalized.IssueID))
	}

	metadataJSON, err := marshalStringMap(normalized.Metadata)
	if err != nil {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", normalized.IssueID, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO issue_external_refs (
			issue_id,
			provider,
			provider_scope,
			remote_key,
			display_key,
			url,
			metadata_json,
			created_at,
			updated_at,
			deleted_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(issue_id, provider, provider_scope, remote_key)
		DO UPDATE SET
			display_key = excluded.display_key,
			url = excluded.url,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at,
			deleted_at = NULL
	`, normalized.IssueID, normalized.Provider, normalized.ProviderScope, normalized.RemoteKey, nullableString(normalized.DisplayKey), nullableString(normalized.URL), nullableString(metadataJSON), now, now); err != nil {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", normalized.IssueID, err)
	}
	ref, found, err := c.GetExternalIssueRef(ctx, normalized.Provider, normalized.ProviderScope, normalized.RemoteKey)
	if err != nil {
		return domain.ExternalIssueRef{}, err
	}
	if !found {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", normalized.IssueID, errors.New("external ref not found after upsert"))
	}
	return ref, nil
}

func (c *Client) GetExternalIssueRef(ctx context.Context, provider, providerScope, remoteKey string) (domain.ExternalIssueRef, bool, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.ExternalIssueRef{}, false, err
	}
	provider = strings.TrimSpace(provider)
	providerScope = strings.TrimSpace(providerScope)
	remoteKey = strings.TrimSpace(remoteKey)
	if provider == "" || remoteKey == "" {
		return domain.ExternalIssueRef{}, false, c.wrapError("get-external-ref", "", errors.New("provider and remote key are required"))
	}
	row := db.QueryRowContext(ctx, `
		SELECT issue_id, provider, provider_scope, remote_key, COALESCE(display_key, ''), COALESCE(url, ''), COALESCE(metadata_json, ''), created_at, updated_at
		FROM issue_external_refs
		WHERE provider = ? AND provider_scope = ? AND remote_key = ? AND deleted_at IS NULL
	`, provider, providerScope, remoteKey)
	ref, err := scanExternalIssueRef(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExternalIssueRef{}, false, nil
	}
	if err != nil {
		return domain.ExternalIssueRef{}, false, c.wrapError("get-external-ref", "", err)
	}
	return ref, true, nil
}

func (c *Client) GetExternalIssueRefByDisplayKey(ctx context.Context, provider, providerScope, displayKey string) (domain.ExternalIssueRef, bool, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.ExternalIssueRef{}, false, err
	}
	provider = strings.TrimSpace(provider)
	providerScope = strings.TrimSpace(providerScope)
	displayKey = strings.TrimSpace(displayKey)
	if provider == "" || displayKey == "" {
		return domain.ExternalIssueRef{}, false, nil
	}
	ref, err := scanExternalIssueRef(db.QueryRowContext(ctx, `
		SELECT
			issue_id,
			provider,
			provider_scope,
			remote_key,
			COALESCE(display_key, ''),
			COALESCE(url, ''),
			COALESCE(metadata_json, ''),
			created_at,
			updated_at
		FROM issue_external_refs
		WHERE provider = ?
			AND provider_scope = ?
			AND display_key = ?
			AND deleted_at IS NULL
		ORDER BY updated_at DESC, issue_id ASC
		LIMIT 1
	`, provider, providerScope, displayKey))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ExternalIssueRef{}, false, nil
		}
		return domain.ExternalIssueRef{}, false, c.wrapError("get-external-ref-by-display-key", displayKey, err)
	}
	return ref, true, nil
}

func (c *Client) ListExternalIssueRefs(ctx context.Context, issueID string) ([]domain.ExternalIssueRef, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, c.wrapError("list-external-refs", "", errors.New("issue id is required"))
	}
	rows, err := db.QueryContext(ctx, `
		SELECT issue_id, provider, provider_scope, remote_key, COALESCE(display_key, ''), COALESCE(url, ''), COALESCE(metadata_json, ''), created_at, updated_at
		FROM issue_external_refs
		WHERE issue_id = ? AND deleted_at IS NULL
		ORDER BY provider, provider_scope, remote_key
	`, issueID)
	if err != nil {
		return nil, c.wrapError("list-external-refs", issueID, err)
	}
	defer rows.Close()

	var refs []domain.ExternalIssueRef
	for rows.Next() {
		ref, err := scanExternalIssueRef(rows)
		if err != nil {
			return nil, c.wrapError("list-external-refs", issueID, err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-external-refs", issueID, err)
	}
	return refs, nil
}

// AddDependency creates or restores a dependency edge between two issues.
func (c *Client) AddDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.addDependency(ctx, issueID, dependsOnID, dependencyType, false)
	})
}

// AddDependencyWithParentChange creates or restores a dependency edge, allowing
// an existing parent-child edge to be replaced when forceParentChange is true.
func (c *Client) AddDependencyWithParentChange(ctx context.Context, issueID, dependsOnID, dependencyType string, forceParentChange bool) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.addDependency(ctx, issueID, dependsOnID, dependencyType, forceParentChange)
	})
}

func (c *Client) addDependency(ctx context.Context, issueID, dependsOnID, dependencyType string, forceParentChange bool) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}

	issueID = strings.TrimSpace(issueID)
	dependsOnID = strings.TrimSpace(dependsOnID)
	dependencyType = strings.TrimSpace(dependencyType)
	if issueID == "" || dependsOnID == "" || dependencyType == "" {
		return c.wrapError("add-dependency", issueID, errors.New("issue id, dependency id, and dependency type are required"))
	}

	canonicalType, err := canonicalDependencyType(dependencyType)
	if err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}

	if dependencyIsAcyclic(canonicalType) {
		if issueID == dependsOnID {
			return c.wrapError("add-dependency", issueID, domain.ErrConflict)
		}

		cycle, err := c.wouldCreateDependencyCycle(ctx, db, issueID, dependsOnID)
		if err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
		if cycle {
			return c.wrapError("add-dependency", issueID, domain.ErrConflict)
		}
	}

	sourceExists, err := c.issueExists(ctx, db, issueID)
	if err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	if !sourceExists {
		return c.wrapError("add-dependency", issueID, domain.ErrNotFound)
	}

	targetExists, err := c.issueExists(ctx, db, dependsOnID)
	if err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	if !targetExists {
		return c.wrapError("add-dependency", issueID, domain.ErrNotFound)
	}

	tombstoneOldParent := ""
	if canonicalType == string(domain.DependencyParentChild) {
		currentParents, err := c.activeParents(ctx, db, issueID)
		if err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
		for _, currentParent := range currentParents {
			if currentParent == dependsOnID {
				continue
			}
			if !forceParentChange {
				return c.wrapError("add-dependency", issueID, ParentChangeRequiredError{
					IssueID:         issueID,
					CurrentParent:   currentParent,
					RequestedParent: dependsOnID,
				})
			}
			tombstoneOldParent = currentParent
			break
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if canonicalType == string(domain.DependencyParentChild) {
		if err := rejectActiveReviewAdmissionLease(ctx, tx, issueID); err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
	}

	if tombstoneOldParent != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_dependencies
			SET tombstoned_at = ?
			WHERE issue_id = ? AND depends_on_id != ? AND dependency_type IN (?, 'parent_child') AND tombstoned_at IS NULL
		`, time.Now().UTC().Format(time.RFC3339Nano), issueID, dependsOnID, canonicalType); err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
		if err := c.appendIssueObservationEvent(ctx, tx, issueID, domain.IssueEventIssueDependencyRemoved, map[string]any{
			"depends_on_id":   tombstoneOldParent,
			"dependency_type": canonicalType,
			"reason":          "parent_replaced",
		}); err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
	}

	if canonicalType == string(domain.DependencyParentChild) {
		sourceExists, err := c.issueExists(ctx, tx, issueID)
		if err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
		if !sourceExists {
			return c.wrapError("add-dependency", issueID, domain.ErrNotFound)
		}
		targetExists, err := c.issueExists(ctx, tx, dependsOnID)
		if err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
		if !targetExists {
			return c.wrapError("add-dependency", issueID, domain.ErrNotFound)
		}
		if err := c.reopenClosedParentForActiveChild(ctx, tx, issueID, dependsOnID); err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES (?, ?, ?, NULL)
		ON CONFLICT(issue_id, depends_on_id, dependency_type)
		DO UPDATE SET tombstoned_at = NULL
	`, issueID, dependsOnID, canonicalType); err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	if err := c.appendIssueObservationEvent(ctx, tx, issueID, domain.IssueEventIssueDependencyAdded, map[string]any{
		"depends_on_id":   dependsOnID,
		"dependency_type": canonicalType,
	}); err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	if canonicalType == string(domain.DependencyParentChild) {
		if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
		if err := c.enforceAncestorLifecycleFloor(ctx, tx, issueID, "ancestor_lifecycle_floor_parent_attach"); err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	committed = true
	return nil
}

// AddDependencyWithRuntime creates or restores a dependency edge and returns the changed issue.
func (c *Client) AddDependencyWithRuntime(ctx context.Context, projectID, issueID, dependsOnID, dependencyType string) (domain.Task, error) {
	return c.AddDependencyWithRuntimeAndParentChange(ctx, projectID, issueID, dependsOnID, dependencyType, false)
}

// AddDependencyWithRuntimeAndParentChange creates or restores a dependency edge
// and returns the changed issue.
func (c *Client) AddDependencyWithRuntimeAndParentChange(ctx context.Context, projectID, issueID, dependsOnID, dependencyType string, forceParentChange bool) (domain.Task, error) {
	if err := c.AddDependencyWithParentChange(ctx, issueID, dependsOnID, dependencyType, forceParentChange); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, issueID)
}

func (c *Client) activeParents(ctx context.Context, db *sql.DB, issueID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT depends_on_id
		FROM issue_dependencies
		WHERE issue_id = ? AND dependency_type IN (?, 'parent_child') AND tombstoned_at IS NULL
		ORDER BY depends_on_id
	`, issueID, string(domain.DependencyParentChild))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var parentIDs []string
	for rows.Next() {
		var parentID string
		if err := rows.Scan(&parentID); err != nil {
			return nil, err
		}
		parentIDs = append(parentIDs, parentID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return parentIDs, nil
}

func (c *Client) reopenClosedParentForActiveChild(ctx context.Context, execer sqlIssueDBTX, childID, parentID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	activeState, err := issueStateFromStatus(domain.StatusInProgress)
	if err != nil {
		return err
	}
	writeState := issueStateWriteValuesFromState(activeState, nil)
	res, err := execer.ExecContext(ctx, `
		UPDATE issues
		SET
			disposition = ?,
			engagement = ?,
			visibility = ?,
			status = ?,
			lifecycle_state = ?,
			closed_outcome = ?,
			review_state = ?,
			archived_at = ?,
			deleted_at = ?,
			updated_at = ?,
			closed_at = NULL
		WHERE
			id = ?
			AND visibility = 'live'
			AND disposition IN ('completed','cancelled')
			AND EXISTS (
				SELECT 1
				FROM issues child
				WHERE
					child.id = ?
					AND child.visibility = 'live'
					AND child.disposition NOT IN ('completed','cancelled')
			)
	`, writeState.Disposition, writeState.Engagement, writeState.Visibility, writeState.LegacyStatus, writeState.Lifecycle, writeState.ClosedOutcome, writeState.Review, writeState.ArchivedAt, writeState.ArchivedAt, now, parentID, childID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		if err := c.appendIssueObservationEvent(ctx, execer, parentID, domain.IssueEventIssueStatusChanged, map[string]any{
			"to_status": domain.StatusInProgress,
			"reason":    "active_child_added",
			"child_id":  childID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) issueExists(ctx context.Context, queryer sqlIssueQueryer, id string) (bool, error) {
	var exists bool
	if err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM issues
			WHERE id = ? AND visibility = 'live'
		)
	`, id).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (c *Client) rebuildIssueGraphClosure(ctx context.Context, execer sqlIssueExecer) error {
	if _, err := execer.ExecContext(ctx, `DELETE FROM issue_graph_closure`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := execer.ExecContext(ctx, `
		INSERT INTO issue_graph_closure (
			project_id,
			ancestor_id,
			descendant_id,
			dependency_type,
			depth,
			updated_at
		)
		WITH RECURSIVE parent_edges(ancestor_id, descendant_id) AS (
			SELECT d.depends_on_id, d.issue_id
			FROM issue_dependencies d
			INNER JOIN issues ancestor
				ON ancestor.id = d.depends_on_id
				AND ancestor.visibility = 'live'
			INNER JOIN issues descendant
				ON descendant.id = d.issue_id
				AND descendant.visibility = 'live'
			WHERE d.tombstoned_at IS NULL
				AND d.dependency_type IN (?, 'parent_child')
		),
		closure(ancestor_id, descendant_id, depth, path) AS (
			SELECT ancestor_id, descendant_id, 1, ',' || ancestor_id || ',' || descendant_id || ','
			FROM parent_edges
			UNION ALL
			SELECT c.ancestor_id, e.descendant_id, c.depth + 1, c.path || e.descendant_id || ','
			FROM closure c
			INNER JOIN parent_edges e
				ON e.ancestor_id = c.descendant_id
			WHERE instr(c.path, ',' || e.descendant_id || ',') = 0
		)
		SELECT
			?,
			ancestor_id,
			descendant_id,
			?,
			MIN(depth),
			?
		FROM closure
		WHERE ancestor_id <> descendant_id
		GROUP BY ancestor_id, descendant_id
	`, string(domain.DependencyParentChild), issueGraphClosureProjectID, string(domain.DependencyParentChild), now); err != nil {
		return err
	}
	return nil
}

// enforceAncestorLifecycleFloor promotes live backlog issues in root's subtree
// when they have a live admitted ancestor. The caller must hold the mutation
// lock and an open transaction so graph mutation, promotion, and audit evidence
// commit atomically across daemon processes.
func (c *Client) enforceAncestorLifecycleFloor(ctx context.Context, tx *sql.Tx, rootID, reason string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT candidate.id
		FROM issues candidate
		WHERE candidate.visibility = 'live'
			AND candidate.disposition = 'backlog'
			AND (candidate.id = ? OR EXISTS (
				SELECT 1 FROM issue_graph_closure subtree
				WHERE subtree.dependency_type = ?
					AND subtree.ancestor_id = ?
					AND subtree.descendant_id = candidate.id
			))
			AND EXISTS (
				SELECT 1
				FROM issue_graph_closure ancestry
				JOIN issues ancestor ON ancestor.id = ancestry.ancestor_id
				WHERE ancestry.dependency_type = ?
					AND ancestry.descendant_id = candidate.id
					AND ancestor.visibility = 'live'
					AND ancestor.disposition = 'ready'
			)
		ORDER BY candidate.id
	`, rootID, string(domain.DependencyParentChild), rootID, string(domain.DependencyParentChild))
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `
			UPDATE issues
			SET disposition='ready', engagement='idle', status='open',
				lifecycle_state='open', closed_outcome='none', review_state='none', updated_at=?
			WHERE id=? AND visibility='live' AND disposition='backlog'
		`, now, id)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			continue
		}
		if err := c.appendIssueObservationEvent(ctx, tx, id, domain.IssueEventIssueStatusChanged, map[string]any{
			"from_status":    domain.StatusOpen,
			"to_status":      domain.StatusOpen,
			"from_lifecycle": domain.IssueWorkflowBacklog,
			"to_lifecycle":   domain.IssueWorkflowOpen,
			"reason":         reason,
			"ancestor_id":    rootID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) hasLiveAdmittedAncestor(ctx context.Context, tx *sql.Tx, issueID string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM issue_graph_closure ancestry
			JOIN issues ancestor ON ancestor.id = ancestry.ancestor_id
			WHERE ancestry.dependency_type = ?
				AND ancestry.descendant_id = ?
				AND ancestor.visibility = 'live'
				AND ancestor.disposition = 'ready'
		)
	`, string(domain.DependencyParentChild), issueID).Scan(&exists)
	return exists, err
}

func (c *Client) countLiveNonterminalDescendants(ctx context.Context, tx *sql.Tx, issueID string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM issue_graph_closure closure
		JOIN issues descendant ON descendant.id = closure.descendant_id
		WHERE closure.dependency_type = ?
			AND closure.ancestor_id = ?
			AND descendant.visibility = 'live'
			AND descendant.disposition NOT IN ('completed','cancelled')
	`, string(domain.DependencyParentChild), issueID).Scan(&count)
	return count, err
}

// RemoveDependency tombstones a dependency edge between two issues.
func (c *Client) RemoveDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.removeDependency(ctx, issueID, dependsOnID, dependencyType)
	})
}

func (c *Client) removeDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}

	issueID = strings.TrimSpace(issueID)
	dependsOnID = strings.TrimSpace(dependsOnID)
	dependencyType = strings.TrimSpace(dependencyType)
	if issueID == "" || dependsOnID == "" || dependencyType == "" {
		return c.wrapError("remove-dependency", issueID, errors.New("issue id, dependency id, and dependency type are required"))
	}

	canonicalType, err := canonicalDependencyType(dependencyType)
	if err != nil {
		return c.wrapError("remove-dependency", issueID, err)
	}

	if dependencyTypeRequiresConfirmation(canonicalType) && !hasDependencyRemovalConfirmation(ctx) {
		return c.wrapError("remove-dependency", issueID, ErrDependencyRemovalConfirmationRequired)
	}
	if canonicalType == string(domain.DependencyParentChild) && !hasParentChildOrphanConfirmation(ctx) {
		active, err := c.parentChildRemovalWouldOrphanActiveChild(ctx, db, issueID, dependsOnID)
		if err != nil {
			return c.wrapError("remove-dependency", issueID, err)
		}
		if active {
			return c.wrapError("remove-dependency", issueID, ErrParentChildOrphanConfirmationRequired)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("remove-dependency", issueID, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if canonicalType == string(domain.DependencyParentChild) {
		if err := rejectActiveReviewAdmissionLease(ctx, tx, issueID); err != nil {
			return c.wrapError("remove-dependency", issueID, err)
		}
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE issue_dependencies
		SET tombstoned_at = ?
		WHERE issue_id = ? AND depends_on_id = ? AND dependency_type = ? AND tombstoned_at IS NULL
	`, time.Now().UTC().Format(time.RFC3339Nano), issueID, dependsOnID, canonicalType)
	if err != nil {
		return c.wrapError("remove-dependency", issueID, err)
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("remove-dependency", issueID, domain.ErrNotFound)
	}
	if err := c.appendIssueObservationEvent(ctx, tx, issueID, domain.IssueEventIssueDependencyRemoved, map[string]any{
		"depends_on_id":   dependsOnID,
		"dependency_type": canonicalType,
	}); err != nil {
		return c.wrapError("remove-dependency", issueID, err)
	}
	if canonicalType == string(domain.DependencyParentChild) {
		if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
			return c.wrapError("remove-dependency", issueID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return c.wrapError("remove-dependency", issueID, err)
	}
	committed = true
	return nil
}

func rejectActiveReviewAdmissionLease(ctx context.Context, queryer sqlIssueQueryer, issueID string) error {
	var active bool
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM issue_coordination_leases
			WHERE issue_id=? AND purpose=? AND (expires_at IS NULL OR expires_at>?)
		)
	`, strings.TrimSpace(issueID), domain.CoordinationLeaseReview, now).Scan(&active); err != nil {
		return err
	}
	if active {
		return fmt.Errorf("%w: active review admission lease fences parent identity", domain.ErrConflict)
	}
	return nil
}

// RemoveDependencyWithRuntime tombstones a dependency edge and returns the changed issue.
func (c *Client) RemoveDependencyWithRuntime(ctx context.Context, projectID, issueID, dependsOnID, dependencyType string) (domain.Task, error) {
	canonicalType, canonicalErr := canonicalDependencyType(dependencyType)
	if canonicalErr == nil && canonicalType == string(domain.DependencyParentChild) && !hasParentChildOrphanConfirmation(ctx) {
		exists, err := c.dependencyEdgeExists(ctx, issueID, dependsOnID, canonicalType)
		if err != nil {
			return domain.Task{}, c.wrapError("remove-dependency", issueID, err)
		}
		if !exists {
			if err := c.RemoveDependency(ctx, issueID, dependsOnID, dependencyType); err != nil {
				return domain.Task{}, err
			}
			return c.GetWithRuntime(ctx, projectID, issueID)
		}
		task, err := c.GetWithRuntime(ctx, projectID, issueID)
		if err != nil {
			return domain.Task{}, err
		}
		if parentChildRemovalWouldOrphanRuntimeChild(task) {
			return domain.Task{}, c.wrapError("remove-dependency", issueID, ErrParentChildOrphanConfirmationRequired)
		}
	}
	if err := c.RemoveDependency(ctx, issueID, dependsOnID, dependencyType); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, issueID)
}

func (c *Client) dependencyEdgeExists(ctx context.Context, issueID, dependsOnID, dependencyType string) (bool, error) {
	db, err := c.dbHandle()
	if err != nil {
		return false, err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM issue_dependencies
			WHERE issue_id = ? AND depends_on_id = ? AND dependency_type = ? AND tombstoned_at IS NULL
		)
	`, issueID, dependsOnID, dependencyType).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (c *Client) parentChildRemovalWouldOrphanActiveChild(ctx context.Context, db *sql.DB, issueID, dependsOnID string) (bool, error) {
	var disposition string
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(i.disposition, '')
		FROM issues i
		INNER JOIN issue_dependencies d
			ON d.issue_id = i.id
			AND d.depends_on_id = ?
			AND d.dependency_type = ?
			AND d.tombstoned_at IS NULL
		WHERE i.id = ? AND i.visibility = 'live'
	`, dependsOnID, string(domain.DependencyParentChild), issueID).Scan(&disposition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(disposition) != string(domain.IssueDispositionCompleted) && strings.TrimSpace(disposition) != string(domain.IssueDispositionCancelled), nil
}

func parentChildRemovalWouldOrphanRuntimeChild(task domain.Task) bool {
	return isActiveParentChildRemovalStatus(task.Status) || task.HasWorktree || task.HasTmuxSession || task.Session != nil
}

func isActiveParentChildRemovalStatus(status domain.Status) bool {
	switch status {
	case domain.StatusOpen, domain.StatusInProgress, domain.StatusInReview:
		return true
	default:
		return false
	}
}

func dependencyTypeRequiresConfirmation(dependencyType string) bool {
	switch dependencyType {
	case string(domain.DependencyBlocks), string(domain.DependencyParentChild):
		return true
	default:
		return false
	}
}

// Close marks an issue as closed.
func (c *Client) Close(ctx context.Context, id string, _ string) error {
	return c.Update(ctx, id, domain.StatusDone)
}

// Delete permanently removes an issue and its dependency rows.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.deleteLocked(ctx, id)
	})
}

func (c *Client) deleteLocked(ctx context.Context, id string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("delete", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	var runtimeAttachmentCount int
	runtimeAttachmentQuery := `
		SELECT (
			CASE
				WHEN EXISTS (
					SELECT 1
					FROM daemon_worktree_projections
					WHERE issue_id = ? AND TRIM(COALESCE(path, '')) <> ''
				)
				THEN 1 ELSE 0
			END
		) + (
			CASE
				WHEN EXISTS (
					SELECT 1
					FROM (` + runtimeSessionProjectionUnionSQL + `)
					WHERE issue_id = ? AND LOWER(TRIM(COALESCE(state, ''))) <> 'stopped'
				)
				THEN 1 ELSE 0
			END
		)
	`
	if err := tx.QueryRowContext(ctx, runtimeAttachmentQuery, id, id).Scan(&runtimeAttachmentCount); err != nil {
		return c.wrapError("delete", id, err)
	}
	if runtimeAttachmentCount > 0 {
		return c.wrapError("delete", id, ErrDeleteBlockedByRuntimeAttachments)
	}
	if err := c.guardNoUndeletedParentChildDescendants(ctx, tx, "delete", id); err != nil {
		return c.wrapError("delete", id, err)
	}
	if err := c.appendIssueObservationEvent(ctx, tx, id, domain.IssueEventIssueDeleted, map[string]any{}); err != nil {
		return c.wrapError("delete", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_dependencies WHERE issue_id = ? OR depends_on_id = ?`, id, id); err != nil {
		return c.wrapError("delete", id, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, id)
	if err != nil {
		return c.wrapError("delete", id, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("delete", id, domain.ErrNotFound)
	}
	if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
		return c.wrapError("delete", id, err)
	}
	if err := tx.Commit(); err != nil {
		return c.wrapError("delete", id, err)
	}
	tx = nil
	return nil
}

// Archive soft-deletes an issue.
func (c *Client) Archive(ctx context.Context, id string) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.archiveLocked(ctx, id)
	})
}

func (c *Client) archiveLocked(ctx context.Context, id string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("archive", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	if err := c.guardNoUndeletedParentChildDescendants(ctx, tx, "archive", id); err != nil {
		return c.wrapError("archive", id, err)
	}
	var blockers int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM issue_coordination_leases WHERE issue_id=?) +
		(SELECT COUNT(*) FROM daemon_worktree_projections WHERE issue_id=? AND trim(path)!='') +
		(SELECT COUNT(*) FROM (`+runtimeSessionProjectionUnionSQL+`) WHERE issue_id=?)`, id, id, id).Scan(&blockers); err != nil {
		return c.wrapError("archive", id, err)
	}
	if blockers != 0 {
		return c.wrapError("archive", id, fmt.Errorf("%w: archived issue must be unclaimed and resource-free", domain.ErrConflict))
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET
			visibility = 'archived',
			engagement = 'idle',
			status = CASE WHEN disposition='ready' THEN 'open' ELSE status END,
			lifecycle_state = CASE WHEN disposition='ready' THEN 'open' ELSE lifecycle_state END,
			review_state = 'none',
			archived_at = ?,
			updated_at = ?
		WHERE id = ? AND visibility = 'live'
	`, now, now, id)
	if err != nil {
		return c.wrapError("archive", id, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("archive", id, domain.ErrNotFound)
	}
	if err := c.appendIssueObservationEvent(ctx, tx, id, domain.IssueEventIssueArchived, map[string]any{}); err != nil {
		return c.wrapError("archive", id, err)
	}
	if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
		return c.wrapError("archive", id, err)
	}
	if err := tx.Commit(); err != nil {
		return c.wrapError("archive", id, err)
	}
	tx = nil
	return nil
}

// Unarchive restores a soft-deleted issue to active issue reads.
func (c *Client) Unarchive(ctx context.Context, id string) error {
	_, err := c.UnarchiveWithOptions(ctx, id, UnarchiveOptions{})
	return err
}

// UnarchiveWithOptions restores an archived issue and optionally its archived
// parent chain and parent-child descendants.
func (c *Client) UnarchiveWithOptions(ctx context.Context, id string, opts UnarchiveOptions) (UnarchiveResult, error) {
	var result UnarchiveResult
	err := c.withMutationLock(ctx, func(ctx context.Context) error {
		var err error
		result, err = c.unarchiveLocked(ctx, id, opts)
		return err
	})
	return result, err
}

func (c *Client) unarchiveLocked(ctx context.Context, id string, opts UnarchiveOptions) (UnarchiveResult, error) {
	result := UnarchiveResult{}
	db, err := c.dbHandle()
	if err != nil {
		return result, err
	}
	id = strings.TrimSpace(id)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, c.wrapError("unarchive", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	var targetVisibility string
	if err := tx.QueryRowContext(ctx, `
		SELECT visibility
		FROM issues
		WHERE id = ?
	`, id).Scan(&targetVisibility); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, c.wrapError("unarchive", id, domain.ErrNotFound)
		}
		return result, c.wrapError("unarchive", id, err)
	}

	archivedParents, err := c.archivedParentChildAncestorIDs(ctx, tx, id)
	if err != nil {
		return result, c.wrapError("unarchive", id, err)
	}
	if len(archivedParents) > 0 && !opts.WithParents {
		return result, c.wrapError("unarchive", id, ArchivedParentsMutationError{
			Operation:   "unarchive",
			IssueID:     id,
			ParentCount: len(archivedParents),
		})
	}

	restoreIDs := make([]string, 0, len(archivedParents)+1)
	if opts.WithParents {
		restoreIDs = append(restoreIDs, archivedParents...)
	}
	if targetVisibility == string(domain.IssueVisibilityArchived) {
		restoreIDs = append(restoreIDs, id)
	}
	if opts.CascadeChildren {
		descendants, err := c.archivedParentChildDescendantIDs(ctx, tx, id)
		if err != nil {
			return result, c.wrapError("unarchive", id, err)
		}
		restoreIDs = append(restoreIDs, descendants...)
	}
	restoreIDs = dedupeNonEmptyStrings(restoreIDs)
	if len(restoreIDs) == 0 {
		return result, c.wrapError("unarchive", id, domain.ErrNotFound)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(restoreIDs)), ",")
	args := make([]any, 0, len(restoreIDs)+1)
	args = append(args, now)
	for _, restoreID := range restoreIDs {
		args = append(args, restoreID)
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET
			visibility = 'live',
			archived_at = NULL,
			updated_at = ?
		WHERE visibility = 'archived'
			AND id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return result, c.wrapError("unarchive", id, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return result, c.wrapError("unarchive", id, domain.ErrNotFound)
	}
	for _, restoreID := range restoreIDs {
		if err := c.appendIssueObservationEvent(ctx, tx, restoreID, domain.IssueEventIssueUnarchived, map[string]any{}); err != nil {
			return result, c.wrapError("unarchive", id, err)
		}
	}
	if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
		return result, c.wrapError("unarchive", id, err)
	}
	for _, restoreID := range restoreIDs {
		if err := c.enforceAncestorLifecycleFloor(ctx, tx, restoreID, "ancestor_lifecycle_floor_restore"); err != nil {
			return result, c.wrapError("unarchive", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return result, c.wrapError("unarchive", id, err)
	}
	tx = nil
	result.UnarchivedIDs = restoreIDs
	return result, nil
}

func (c *Client) archivedParentChildAncestorIDs(ctx context.Context, queryer sqlIssueQueryer, issueID string) ([]string, error) {
	rows, err := queryer.QueryContext(ctx, `
		WITH RECURSIVE ancestors(id, depth, path) AS (
			SELECT parent.id, 1, ',' || child.id || ',' || parent.id || ','
			FROM issue_dependencies dep
			INNER JOIN issues child ON child.id = dep.issue_id
			INNER JOIN issues parent ON parent.id = dep.depends_on_id
			WHERE dep.issue_id = ?
				AND dep.tombstoned_at IS NULL
				AND dep.dependency_type IN (?, 'parent_child')

			UNION ALL

			SELECT parent.id, ancestors.depth + 1, ancestors.path || parent.id || ','
			FROM ancestors
			INNER JOIN issue_dependencies dep ON dep.issue_id = ancestors.id
			INNER JOIN issues parent ON parent.id = dep.depends_on_id
			WHERE dep.tombstoned_at IS NULL
				AND dep.dependency_type IN (?, 'parent_child')
				AND instr(ancestors.path, ',' || parent.id || ',') = 0
		)
		SELECT ancestors.id
		FROM ancestors
		INNER JOIN issues parent ON parent.id = ancestors.id
		WHERE parent.visibility = 'archived'
		ORDER BY ancestors.depth DESC, ancestors.id
	`, issueID, string(domain.DependencyParentChild), string(domain.DependencyParentChild))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRows(rows)
}

func (c *Client) archivedParentChildDescendantIDs(ctx context.Context, queryer sqlIssueQueryer, issueID string) ([]string, error) {
	rows, err := queryer.QueryContext(ctx, `
		WITH RECURSIVE descendants(id, depth, path) AS (
			SELECT child.id, 1, ',' || parent.id || ',' || child.id || ','
			FROM issue_dependencies dep
			INNER JOIN issues parent ON parent.id = dep.depends_on_id
			INNER JOIN issues child ON child.id = dep.issue_id
			WHERE dep.depends_on_id = ?
				AND dep.tombstoned_at IS NULL
				AND dep.dependency_type IN (?, 'parent_child')

			UNION ALL

			SELECT child.id, descendants.depth + 1, descendants.path || child.id || ','
			FROM descendants
			INNER JOIN issue_dependencies dep ON dep.depends_on_id = descendants.id
			INNER JOIN issues child ON child.id = dep.issue_id
			WHERE dep.tombstoned_at IS NULL
				AND dep.dependency_type IN (?, 'parent_child')
				AND instr(descendants.path, ',' || child.id || ',') = 0
		)
		SELECT descendants.id
		FROM descendants
		INNER JOIN issues child ON child.id = descendants.id
		WHERE child.visibility = 'archived'
		ORDER BY descendants.depth ASC, descendants.id
	`, issueID, string(domain.DependencyParentChild), string(domain.DependencyParentChild))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRows(rows)
}

func scanStringRows(rows *sql.Rows) ([]string, error) {
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		value = strings.TrimSpace(value)
		if value != "" {
			values = append(values, value)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func dedupeNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (c *Client) EnsureNoUndeletedParentChildDescendants(ctx context.Context, operation, issueID string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	issueID = strings.TrimSpace(issueID)
	if err := c.guardNoUndeletedParentChildDescendants(ctx, db, operation, issueID); err != nil {
		return c.wrapError(operation, issueID, err)
	}
	return nil
}

func (c *Client) guardNoUndeletedParentChildDescendants(ctx context.Context, queryer sqlIssueQueryer, operation, issueID string) error {
	count, err := c.countUndeletedParentChildDescendants(ctx, queryer, issueID)
	if err != nil {
		return err
	}
	if count > 0 {
		return LiveChildrenMutationError{
			Operation:       operation,
			IssueID:         issueID,
			DescendantCount: count,
		}
	}
	return nil
}

func (c *Client) countUndeletedParentChildDescendants(ctx context.Context, queryer sqlIssueQueryer, issueID string) (int, error) {
	var count int
	if err := queryer.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT closure.descendant_id)
		FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_ancestor
		INNER JOIN issues descendant
			ON descendant.id = closure.descendant_id
			AND descendant.visibility = 'live'
		WHERE closure.project_id = ?
			AND closure.dependency_type = ?
			AND closure.ancestor_id = ?
	`, issueGraphClosureProjectID, string(domain.DependencyParentChild), issueID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

type UpdateTaskParams struct {
	Title           string
	Description     string
	Design          *string
	Notes           *string
	Acceptance      *string
	Estimate        *int
	EstimateSet     bool
	Type            domain.TaskType
	Priority        domain.Priority
	Lifecycle       *domain.IssueWorkflow
	Implementations []string
}

// AppendNotes appends a single line to task notes.
func (c *Client) AppendNotes(ctx context.Context, id, line string) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.appendNotesLocked(ctx, id, line)
	})
}

func (c *Client) appendNotesLocked(ctx context.Context, id, line string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	noteLine := strings.TrimSpace(line)
	if noteLine == "" {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("append-notes", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET
			notes = CASE
				WHEN notes IS NULL OR TRIM(notes) = '' THEN ?
				ELSE notes || CHAR(10) || ?
			END,
			updated_at = ?
		WHERE id = ? AND visibility = 'live'
	`, noteLine, noteLine, now, id)
	if err != nil {
		return c.wrapError("append-notes", id, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("append-notes", id, domain.ErrNotFound)
	}
	if err := c.appendIssueObservationEvent(ctx, tx, id, domain.IssueEventIssueNotesAppended, map[string]any{
		"line": noteLine,
	}); err != nil {
		return c.wrapError("append-notes", id, err)
	}
	if err := tx.Commit(); err != nil {
		return c.wrapError("append-notes", id, err)
	}
	tx = nil
	return nil
}

// AppendNotesWithRuntime appends notes and returns the changed issue.
func (c *Client) AppendNotesWithRuntime(ctx context.Context, projectID, id, line string) (domain.Task, error) {
	if err := c.AppendNotes(ctx, id, line); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, id)
}

// UpdateDetails updates non-status issue metadata.
func (c *Client) UpdateDetails(ctx context.Context, id string, params UpdateTaskParams) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.updateDetailsLocked(ctx, id, params)
	})
}

func (c *Client) updateDetailsLocked(ctx context.Context, id string, params UpdateTaskParams) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("update-details", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	var oldTitle, oldDescription, oldDesign, oldNotes, oldAcceptance, oldType, oldImplementations string
	var oldPriority int
	var oldEstimate sql.NullInt64
	var oldStateCols issueStateColumns
	if err := tx.QueryRowContext(ctx, `
		SELECT
			title,
			COALESCE(description, ''),
			COALESCE(design, ''),
			COALESCE(notes, ''),
			COALESCE(acceptance, ''),
			estimate,
			issue_type,
			priority,
			COALESCE(implementations_json, '[]'),
			status,
			COALESCE(disposition, ''),
			COALESCE(engagement, ''),
			COALESCE(visibility, ''),
			archived_at
		FROM issues
		WHERE id = ? AND visibility = 'live'
	`, id).Scan(
		&oldTitle,
		&oldDescription,
		&oldDesign,
		&oldNotes,
		&oldAcceptance,
		&oldEstimate,
		&oldType,
		&oldPriority,
		&oldImplementations,
		&oldStateCols.LegacyStatus,
		&oldStateCols.Disposition,
		&oldStateCols.Engagement,
		&oldStateCols.Visibility,
		&oldStateCols.ArchivedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.wrapError("update-details", id, domain.ErrNotFound)
		}
		return c.wrapError("update-details", id, err)
	}
	oldState, err := issueStateFromColumns(id, domain.Priority(oldPriority), oldStateCols)
	if err != nil {
		return c.wrapError("update-details", id, err)
	}
	nextState := oldState
	if params.Lifecycle != nil {
		nextState, err = issueStateWithLifecycle(oldState, *params.Lifecycle)
		if err != nil {
			return c.wrapError("update-details", id, err)
		}
		if err := domain.ValidateIssueStateTransition(oldState, nextState); err != nil {
			return c.wrapError("update-details", id, err)
		}
		if nextState.Workflow() == domain.IssueWorkflowBacklog {
			hasAdmittedAncestor, err := c.hasLiveAdmittedAncestor(ctx, tx, id)
			if err != nil {
				return c.wrapError("update-details", id, err)
			}
			if hasAdmittedAncestor {
				return c.wrapError("update-details", id, fmt.Errorf("%w: descendant cannot regress to backlog below an admitted ancestor", domain.ErrConflict))
			}
		}
	}
	writeState := issueStateWriteValuesFromState(nextState, oldStateCols.ArchivedAt)
	implSet := 0
	implementationsJSON := ""
	if params.Implementations != nil {
		implsJSON, err := json.Marshal(params.Implementations)
		if err != nil {
			return c.wrapError("update-details", id, err)
		}
		implSet = 1
		implementationsJSON = string(implsJSON)
	}
	noteSet := 0
	noteValue := ""
	if params.Notes != nil {
		noteSet = 1
		noteValue = *params.Notes
	}
	designSet := 0
	designValue := ""
	if params.Design != nil {
		designSet = 1
		designValue = *params.Design
	}
	acceptanceSet := 0
	acceptanceValue := ""
	if params.Acceptance != nil {
		acceptanceSet = 1
		acceptanceValue = *params.Acceptance
	}
	estimateSet := 0
	var estimateValue any
	if params.EstimateSet {
		estimateSet = 1
		if params.Estimate != nil {
			estimateValue = *params.Estimate
		}
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET
			title = ?,
			description = ?,
			design = CASE WHEN ? = 1 THEN ? ELSE design END,
			notes = CASE WHEN ? = 1 THEN ? ELSE notes END,
			acceptance = CASE WHEN ? = 1 THEN ? ELSE acceptance END,
			estimate = CASE WHEN ? = 1 THEN ? ELSE estimate END,
			issue_type = ?,
			priority = ?,
			disposition = ?,
			engagement = ?,
			visibility = ?,
			status = ?,
			lifecycle_state = ?,
			closed_outcome = ?,
			closed_at = CASE WHEN ? = 'closed' THEN closed_at ELSE NULL END,
			review_state = ?,
			implementations_json = CASE WHEN ? = 1 THEN ? ELSE implementations_json END,
			updated_at = ?
		WHERE id = ? AND visibility = 'live'
	`, params.Title, nullableString(params.Description), designSet, nullableString(designValue), noteSet, nullableString(noteValue), acceptanceSet, nullableString(acceptanceValue), estimateSet, estimateValue, string(params.Type), int(params.Priority), writeState.Disposition, writeState.Engagement, writeState.Visibility, writeState.LegacyStatus, writeState.Lifecycle, writeState.ClosedOutcome, writeState.Lifecycle, writeState.Review, implSet, implementationsJSON, now, id)
	if err != nil {
		return c.wrapError("update-details", id, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("update-details", id, domain.ErrNotFound)
	}
	oldLegacyStatus := legacyStatusFromIssueState(oldState)
	if oldLegacyStatus != domain.Status(writeState.LegacyStatus) {
		if err := c.appendIssueObservationEvent(ctx, tx, id, domain.IssueEventIssueStatusChanged, map[string]any{
			"from_status": string(oldLegacyStatus),
			"to_status":   writeState.LegacyStatus,
		}); err != nil {
			return c.wrapError("update-details", id, err)
		}
	}
	changedFields := make([]string, 0, 6)
	if oldTitle != params.Title {
		changedFields = append(changedFields, "title")
	}
	if oldDescription != params.Description {
		changedFields = append(changedFields, "description")
	}
	if params.Design != nil && oldDesign != designValue {
		changedFields = append(changedFields, "design")
	}
	if params.Notes != nil && oldNotes != noteValue {
		changedFields = append(changedFields, "notes")
	}
	if params.Acceptance != nil && oldAcceptance != acceptanceValue {
		changedFields = append(changedFields, "acceptance")
	}
	if params.EstimateSet && estimateChanged(oldEstimate, params.Estimate) {
		changedFields = append(changedFields, "estimate")
	}
	if oldType != string(params.Type) {
		changedFields = append(changedFields, "issue_type")
	}
	if oldPriority != int(params.Priority) {
		changedFields = append(changedFields, "priority")
	}
	if implSet == 1 && oldImplementations != implementationsJSON {
		changedFields = append(changedFields, "implementations")
	}
	if len(changedFields) > 0 {
		if err := c.appendIssueObservationEvent(ctx, tx, id, domain.IssueEventIssueDetailsChanged, map[string]any{
			"changed_fields": changedFields,
		}); err != nil {
			return c.wrapError("update-details", id, err)
		}
	}
	if nextState.Disposition == domain.IssueDispositionReady && oldState.Disposition == domain.IssueDispositionBacklog {
		if err := c.enforceAncestorLifecycleFloor(ctx, tx, id, "ancestor_lifecycle_floor_admitted"); err != nil {
			return c.wrapError("update-details", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return c.wrapError("update-details", id, err)
	}
	tx = nil
	return nil
}

func estimateChanged(old sql.NullInt64, next *int) bool {
	if next == nil {
		return old.Valid
	}
	return !old.Valid || old.Int64 != int64(*next)
}

// UpdateDetailsWithRuntime updates issue metadata and returns the changed issue.
func (c *Client) UpdateDetailsWithRuntime(ctx context.Context, projectID, id string, params UpdateTaskParams) (domain.Task, error) {
	if err := c.UpdateDetails(ctx, id, params); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, id)
}

func (c *Client) queryTasks(ctx context.Context, db sqlIssueDBTX, query string, args ...any) ([]domain.Task, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0, 32)
	taskIDs := make([]naming.IssueID, 0, 32)
	taskIndexByID := map[naming.IssueID]int{}

	for rows.Next() {
		task := domain.Task{}
		var createdRaw string
		var updatedRaw string
		var stateCols issueStateColumns
		var typeRaw string
		var priorityRaw int
		var assigneeRaw string
		var labelsRaw string
		var estimateRaw sql.NullInt64
		var implementationsRaw string
		if err := rows.Scan(
			&task.ID,
			&task.Title,
			&task.Description,
			&task.Notes,
			&task.Design,
			&task.Acceptance,
			&assigneeRaw,
			&labelsRaw,
			&estimateRaw,
			&stateCols.LegacyStatus,
			&stateCols.Disposition,
			&stateCols.Engagement,
			&stateCols.Visibility,
			&stateCols.ArchivedAt,
			&priorityRaw,
			&typeRaw,
			&implementationsRaw,
			&createdRaw,
			&updatedRaw,
		); err != nil {
			return nil, err
		}
		task.Priority = domain.Priority(priorityRaw)
		state, err := issueStateFromColumns(task.ID.String(), task.Priority, stateCols)
		if err != nil {
			return nil, err
		}
		task.State = state
		task.Status = legacyStatusFromIssueState(state)
		task.Type = domain.TaskType(typeRaw)
		task.CreatedAt = parseTimestamp(createdRaw)
		task.UpdatedAt = parseTimestamp(updatedRaw)
		task.Assignee = strings.TrimSpace(assigneeRaw)
		task.Labels = decodeStringSliceJSON(labelsRaw)
		if estimateRaw.Valid {
			estimateValue := int(estimateRaw.Int64)
			task.Estimate = &estimateValue
		}
		task.Implementations = decodeImplementationsJSON(implementationsRaw)

		tasks = append(tasks, task)
		taskIDs = append(taskIDs, task.ID)
		taskIndexByID[task.ID] = len(tasks) - 1
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return tasks, nil
	}

	if err := c.loadDependenciesForTasks(ctx, db, taskIDs, taskIndexByID, tasks, taskDependencyLoadAll); err != nil {
		return nil, err
	}

	return tasks, nil
}

func (c *Client) queryTaskSummariesWithRuntime(ctx context.Context, db *sql.DB, projectID string, includeDependencies bool, issueIDs ...string) ([]domain.Task, error) {
	return c.queryTaskSummariesWithRuntimeArchiveMode(ctx, db, projectID, includeDependencies, ArchiveExclude, issueIDs...)
}

func (c *Client) queryTaskSummariesWithRuntimeArchiveMode(ctx context.Context, db *sql.DB, projectID string, includeDependencies bool, archiveMode ArchiveMode, issueIDs ...string) ([]domain.Task, error) {
	dependencyMode := taskDependencyLoadParentOnly
	if includeDependencies {
		dependencyMode = taskDependencyLoadAll
	}
	return c.queryTasksWithRuntimeProjection(ctx, db, projectID, false, dependencyMode, archiveMode, issueIDs...)
}

func (c *Client) queryTasksWithRuntime(ctx context.Context, db *sql.DB, projectID string, issueIDs ...string) ([]domain.Task, error) {
	return c.queryTasksWithRuntimeArchiveMode(ctx, db, projectID, ArchiveExclude, issueIDs...)
}

func (c *Client) queryTasksWithRuntimeArchiveMode(ctx context.Context, db sqlIssueDBTX, projectID string, archiveMode ArchiveMode, issueIDs ...string) ([]domain.Task, error) {
	return c.queryTasksWithRuntimeProjection(ctx, db, projectID, true, taskDependencyLoadAll, archiveMode, issueIDs...)
}

func (c *Client) queryTaskMetadataWithRuntime(ctx context.Context, db *sql.DB, projectID string, issueIDs ...string) ([]domain.Task, error) {
	return c.queryTaskMetadataWithRuntimeArchiveMode(ctx, db, projectID, ArchiveExclude, issueIDs...)
}

func (c *Client) queryTaskMetadataWithRuntimeArchiveMode(ctx context.Context, db *sql.DB, projectID string, archiveMode ArchiveMode, issueIDs ...string) ([]domain.Task, error) {
	ids := uniqueIssueIDStrings(issueIDs)
	if len(ids) == 0 {
		return []domain.Task{}, nil
	}
	startedAt := time.Now()
	query, args := taskMetadataRuntimeProjectionQuery(projectID, archiveMode, ids...)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		c.logSQLiteRead(ctx, "issue.metadata_runtime_projection", startedAt, 0, err, "issue_count", len(ids))
		return nil, err
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0, len(ids))
	seen := map[naming.IssueID]int{}
	for rows.Next() {
		task := domain.Task{}
		var (
			stateCols          issueStateColumns
			typeRaw            string
			priorityRaw        int
			createdRaw         string
			updatedRaw         string
			sessionStateRaw    string
			sessionStartedRaw  string
			sessionUpdatedRaw  string
			sessionActivityRaw string
			sessionSourceRaw   string
			tmuxAttachedCount  int
			worktreePath       string
			gitStatusRaw       string
			worktreeUpdatedRaw string
			gitUpdatedRaw      string
			ownerIDRaw         string
			ownerKindRaw       string
			ownerClaimedRaw    string
			ownerExpiresRaw    string
			parentIDRaw        string
		)
		if err := rows.Scan(
			&task.ID,
			&task.Title,
			&stateCols.LegacyStatus,
			&stateCols.Disposition,
			&stateCols.Engagement,
			&stateCols.Visibility,
			&stateCols.ArchivedAt,
			&priorityRaw,
			&typeRaw,
			&createdRaw,
			&updatedRaw,
			&sessionStateRaw,
			&sessionStartedRaw,
			&sessionUpdatedRaw,
			&sessionActivityRaw,
			&sessionSourceRaw,
			&tmuxAttachedCount,
			&worktreePath,
			&gitStatusRaw,
			&worktreeUpdatedRaw,
			&gitUpdatedRaw,
			&ownerIDRaw,
			&ownerKindRaw,
			&ownerClaimedRaw,
			&ownerExpiresRaw,
			&parentIDRaw,
		); err != nil {
			c.logSQLiteRead(ctx, "issue.metadata_runtime_projection", startedAt, len(tasks), err, "issue_count", len(ids))
			return nil, err
		}
		if idx, ok := seen[task.ID]; ok {
			if tasks[idx].ParentID == nil {
				if parentID, err := naming.ParseIssueID(parentIDRaw); err == nil {
					tasks[idx].ParentID = &parentID
				}
			}
			continue
		}
		task.Priority = domain.Priority(priorityRaw)
		state, err := issueStateFromColumns(task.ID.String(), task.Priority, stateCols)
		if err != nil {
			c.logSQLiteRead(ctx, "issue.metadata_runtime_projection", startedAt, len(tasks), err, "issue_count", len(ids))
			return nil, err
		}
		task.State = state
		task.Status = legacyStatusFromIssueState(state)
		task.Type = domain.TaskType(typeRaw)
		task.CreatedAt = parseTimestamp(createdRaw)
		task.UpdatedAt = parseTimestamp(updatedRaw)
		task.RuntimeUpdatedAt = newestParsedTimestamp(task.UpdatedAt, gitUpdatedRaw)
		task.Origin = "local"
		task.Ownership = parseIssueOwnership(ownerIDRaw, ownerKindRaw, ownerClaimedRaw, ownerExpiresRaw)
		if parentID, err := naming.ParseIssueID(parentIDRaw); err == nil {
			task.ParentID = &parentID
		}
		worktreePath = strings.TrimSpace(worktreePath)
		if worktreePath != "" {
			task.HasWorktree = true
		}
		applyGitStatusProjection(&task, gitStatusRaw)
		sessionStateRaw = strings.TrimSpace(sessionStateRaw)
		if sessionStateRaw != "" && sessionStateRaw != "stopped" {
			startedAt := parseOptionalTimestamp(sessionStartedRaw)
			if startedAt == nil {
				startedAt = parseOptionalTimestamp(sessionUpdatedRaw)
			}
			task.Session = &domain.Session{
				IssueID:           task.ID,
				State:             mapRuntimeSessionState(sessionStateRaw),
				Activity:          strings.ToLower(strings.TrimSpace(sessionActivityRaw)),
				ActivitySource:    strings.ToLower(strings.TrimSpace(sessionSourceRaw)),
				TmuxAttached:      tmuxAttachedCount > 0,
				TmuxAttachedCount: tmuxAttachedCount,
				StartedAt:         startedAt,
				UpdatedAt:         parseTimestamp(sessionUpdatedRaw),
				Worktree:          worktreePath,
			}
			task.HasTmuxSession = true
		}
		seen[task.ID] = len(tasks)
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		c.logSQLiteRead(ctx, "issue.metadata_runtime_projection", startedAt, len(tasks), err, "issue_count", len(ids))
		return nil, err
	}
	if err := c.loadPullRequestsForTasks(ctx, db, tasks); err != nil {
		c.logSQLiteRead(ctx, "issue.metadata_runtime_projection", startedAt, len(tasks), err, "issue_count", len(ids))
		return nil, err
	}
	if err := c.loadCoordinationLeasesForTasks(ctx, db, tasks); err != nil {
		c.logSQLiteRead(ctx, "issue.metadata_runtime_projection", startedAt, len(tasks), err, "issue_count", len(ids))
		return nil, err
	}
	c.logSQLiteRead(ctx, "issue.metadata_runtime_projection", startedAt, len(tasks), nil, "issue_count", len(ids))
	return tasks, nil
}

func (c *Client) loadCoordinationLeasesForTasks(ctx context.Context, db sqlIssueDBTX, tasks []domain.Task) error {
	if len(tasks) == 0 {
		return nil
	}
	indexes := make(map[string]int, len(tasks))
	args := make([]any, 0, len(tasks))
	for i := range tasks {
		id := tasks[i].ID.String()
		indexes[id] = i
		args = append(args, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
	rows, err := db.QueryContext(ctx, `SELECT issue_id, purpose, owner_id, owner_kind, claimed_at, COALESCE(expires_at, '')
		FROM issue_coordination_leases WHERE issue_id IN (`+placeholders+`) ORDER BY issue_id, purpose`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var issueID, claimedRaw, expiresRaw string
		var lease domain.CoordinationLease
		if err := rows.Scan(&issueID, &lease.Purpose, &lease.OwnerID, &lease.OwnerKind, &claimedRaw, &expiresRaw); err != nil {
			return err
		}
		lease.ClaimedAt = parseTimestamp(claimedRaw)
		lease.ExpiresAt = parseOptionalTimestamp(expiresRaw)
		if i, ok := indexes[issueID]; ok {
			tasks[i].CoordinationLeases = append(tasks[i].CoordinationLeases, lease)
			if lease.Purpose == domain.CoordinationLeaseExecution {
				tasks[i].Ownership = &domain.IssueOwnership{OwnerID: lease.OwnerID, OwnerKind: lease.OwnerKind, ClaimedAt: lease.ClaimedAt, ExpiresAt: lease.ExpiresAt}
			}
		}
	}
	return rows.Err()
}

func taskMetadataRuntimeProjectionQuery(projectID string, archiveMode ArchiveMode, issueIDs ...string) (string, []any) {
	ids := uniqueIssueIDStrings(issueIDs)
	if len(ids) == 0 {
		return "", nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	query := fmt.Sprintf(`
		WITH ranked_session AS (
			SELECT
				issue_id,
				COALESCE(NULLIF(TRIM(observed_state), ''), state) AS state,
				COALESCE(started_at, '') AS started_at,
				updated_at,
				session_id,
				COALESCE(activity, '') AS activity,
				COALESCE(activity_source, '') AS activity_source,
				COALESCE(tmux_attached_count, 0) AS tmux_attached_count,
				ROW_NUMBER() OVER (
					PARTITION BY issue_id
					ORDER BY
						CASE COALESCE(NULLIF(TRIM(observed_state), ''), state)
							WHEN 'running' THEN 0
							WHEN 'attached' THEN 0
							WHEN 'paused' THEN 1
							WHEN 'starting' THEN 2
							WHEN 'stopped' THEN 3
							ELSE 4
						END,
						updated_at DESC,
						session_id DESC
				) AS rn
			FROM (%s)
			WHERE project_id = ? AND issue_id IN (%s)
		),
		session_pick AS (
			SELECT issue_id, state, started_at, updated_at, activity, activity_source, tmux_attached_count
			FROM ranked_session
			WHERE rn = 1
		)
		SELECT
			i.id,
			i.title,
			i.status,
			COALESCE(i.disposition, ''),
			COALESCE(i.engagement, ''),
			COALESCE(i.visibility, ''),
			i.archived_at,
			i.priority,
			i.issue_type,
			i.created_at,
			i.updated_at,
			COALESCE(sp.state, ''),
			COALESCE(sp.started_at, ''),
			COALESCE(sp.updated_at, ''),
			COALESCE(sp.activity, ''),
			COALESCE(sp.activity_source, ''),
			COALESCE(sp.tmux_attached_count, 0),
			COALESCE(w.path, ''),
			COALESCE(w.git_status_json, ''),
			COALESCE(w.updated_at, ''),
			COALESCE(w.git_status_updated_at, ''),
			COALESCE((SELECT owner_id FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'), ''),
			COALESCE((SELECT owner_kind FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'), ''),
			COALESCE((SELECT claimed_at FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'), ''),
			COALESCE((SELECT expires_at FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'), ''),
			COALESCE(parent.depends_on_id, '')
		FROM issues i
		LEFT JOIN session_pick sp ON sp.issue_id = i.id
		LEFT JOIN daemon_worktree_projections w
			ON w.project_id = ? AND w.issue_id = i.id
		LEFT JOIN issue_dependencies parent
			ON parent.issue_id = i.id
			AND parent.tombstoned_at IS NULL
			AND parent.dependency_type IN (?, ?)
		WHERE %s AND i.id IN (%s)
		ORDER BY i.updated_at DESC
	`, runtimeSessionProjectionUnionSQL, placeholders, archiveWhere("i", archiveMode), placeholders)
	args := make([]any, 0, len(ids)*2+4)
	args = append(args, projectID)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, projectID, string(domain.DependencyParentChild), "parent_child")
	for _, id := range ids {
		args = append(args, id)
	}
	return query, args
}

type taskDependencyLoadMode int

const (
	taskDependencyLoadAll taskDependencyLoadMode = iota
	taskDependencyLoadParentOnly
)

func (c *Client) queryTasksWithRuntimeProjection(ctx context.Context, db sqlIssueDBTX, projectID string, includeDetails bool, dependencyMode taskDependencyLoadMode, archiveMode ArchiveMode, issueIDs ...string) ([]domain.Task, error) {
	startedAt := time.Now()
	issueCount := len(uniqueIssueIDStrings(issueIDs))
	query, args := taskRuntimeProjectionQuery(projectID, includeDetails, archiveMode, issueIDs...)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, 0, err, "include_details", includeDetails, "issue_count", issueCount)
		return nil, err
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0, 32)
	taskIDs := make([]naming.IssueID, 0, 32)
	taskIndexByID := map[naming.IssueID]int{}

	for rows.Next() {
		task := domain.Task{}
		var (
			createdRaw         string
			updatedRaw         string
			stateCols          issueStateColumns
			typeRaw            string
			priorityRaw        int
			assigneeRaw        string
			labelsRaw          string
			estimateRaw        sql.NullInt64
			implementationsRaw string
			sessionStateRaw    string
			sessionStartedRaw  string
			sessionUpdatedRaw  string
			sessionActivityRaw string
			sessionSourceRaw   string
			tmuxAttachedCount  int
			worktreePath       string
			gitStatusRaw       string
			worktreeUpdatedRaw string
			gitUpdatedRaw      string
			originProvider     string
			ownerIDRaw         string
			ownerKindRaw       string
			ownerClaimedRaw    string
			ownerExpiresRaw    string
		)
		if err := rows.Scan(
			&task.ID,
			&task.Title,
			&task.Description,
			&task.Notes,
			&task.Design,
			&task.Acceptance,
			&assigneeRaw,
			&labelsRaw,
			&estimateRaw,
			&stateCols.LegacyStatus,
			&stateCols.Disposition,
			&stateCols.Engagement,
			&stateCols.Visibility,
			&stateCols.ArchivedAt,
			&priorityRaw,
			&typeRaw,
			&implementationsRaw,
			&createdRaw,
			&updatedRaw,
			&sessionStateRaw,
			&sessionStartedRaw,
			&sessionUpdatedRaw,
			&sessionActivityRaw,
			&sessionSourceRaw,
			&tmuxAttachedCount,
			&worktreePath,
			&gitStatusRaw,
			&worktreeUpdatedRaw,
			&gitUpdatedRaw,
			&originProvider,
			&ownerIDRaw,
			&ownerKindRaw,
			&ownerClaimedRaw,
			&ownerExpiresRaw,
		); err != nil {
			c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), err, "include_details", includeDetails, "issue_count", issueCount)
			return nil, err
		}
		if origin := strings.TrimSpace(strings.ToLower(originProvider)); origin != "" {
			task.Origin = origin
		} else {
			task.Origin = "local"
		}

		task.Priority = domain.Priority(priorityRaw)
		state, err := issueStateFromColumns(task.ID.String(), task.Priority, stateCols)
		if err != nil {
			c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), err, "include_details", includeDetails, "issue_count", issueCount)
			return nil, err
		}
		task.State = state
		task.Status = legacyStatusFromIssueState(state)
		task.Type = domain.TaskType(typeRaw)
		task.CreatedAt = parseTimestamp(createdRaw)
		task.UpdatedAt = parseTimestamp(updatedRaw)
		task.RuntimeUpdatedAt = newestParsedTimestamp(task.UpdatedAt, gitUpdatedRaw)
		task.Assignee = strings.TrimSpace(assigneeRaw)
		task.Labels = decodeStringSliceJSON(labelsRaw)
		task.Ownership = parseIssueOwnership(ownerIDRaw, ownerKindRaw, ownerClaimedRaw, ownerExpiresRaw)
		if estimateRaw.Valid {
			estimateValue := int(estimateRaw.Int64)
			task.Estimate = &estimateValue
		}
		task.Implementations = decodeImplementationsJSON(implementationsRaw)

		worktreePath = strings.TrimSpace(worktreePath)
		if worktreePath != "" {
			task.HasWorktree = true
		}
		sessionStateRaw = strings.TrimSpace(sessionStateRaw)
		if sessionStateRaw != "" && sessionStateRaw != "stopped" {
			startedAt := parseOptionalTimestamp(sessionStartedRaw)
			if startedAt == nil {
				startedAt = parseOptionalTimestamp(sessionUpdatedRaw)
			}
			task.Session = &domain.Session{
				IssueID:           task.ID,
				State:             mapRuntimeSessionState(sessionStateRaw),
				Activity:          strings.ToLower(strings.TrimSpace(sessionActivityRaw)),
				ActivitySource:    strings.ToLower(strings.TrimSpace(sessionSourceRaw)),
				TmuxAttached:      tmuxAttachedCount > 0,
				TmuxAttachedCount: tmuxAttachedCount,
				StartedAt:         startedAt,
				UpdatedAt:         parseTimestamp(sessionUpdatedRaw),
				Worktree:          worktreePath,
			}
			task.HasTmuxSession = true
		}

		applyGitStatusProjection(&task, gitStatusRaw)

		tasks = append(tasks, task)
		taskIDs = append(taskIDs, task.ID)
		taskIndexByID[task.ID] = len(tasks) - 1
	}
	if err := rows.Err(); err != nil {
		c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), err, "include_details", includeDetails, "issue_count", issueCount)
		return nil, err
	}
	if len(tasks) == 0 {
		c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), nil, "include_details", includeDetails, "issue_count", issueCount)
		return tasks, nil
	}
	if err := c.loadDependenciesForTasks(ctx, db, taskIDs, taskIndexByID, tasks, dependencyMode); err != nil {
		c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), err, "include_details", includeDetails, "issue_count", issueCount)
		return nil, err
	}
	if err := c.loadPullRequestsForTasks(ctx, db, tasks); err != nil {
		c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), err, "include_details", includeDetails, "issue_count", issueCount)
		return nil, err
	}
	if err := c.loadCoordinationLeasesForTasks(ctx, db, tasks); err != nil {
		c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), err, "include_details", includeDetails, "issue_count", issueCount)
		return nil, err
	}
	c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), nil, "include_details", includeDetails, "issue_count", issueCount)
	return tasks, nil
}

func (c *Client) loadPullRequestsForTasks(ctx context.Context, db sqlIssueDBTX, tasks []domain.Task) error {
	if len(tasks) == 0 {
		return nil
	}
	taskIndexByID := make(map[string]int, len(tasks))
	ids := make([]string, 0, len(tasks))
	for i := range tasks {
		id := strings.TrimSpace(tasks[i].ID.String())
		if id == "" {
			continue
		}
		taskIndexByID[id] = i
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	query := fmt.Sprintf(`
		SELECT issue_id, remote_key, COALESCE(display_key, ''), COALESCE(url, ''), COALESCE(metadata_json, '')
		FROM issue_external_refs
		WHERE deleted_at IS NULL
			AND LOWER(provider) IN ('github', 'gh')
			AND issue_id IN (%s)
		ORDER BY updated_at DESC
	`, placeholders)
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var issueID, remoteKey, displayKey, url, metadataRaw string
		if err := rows.Scan(&issueID, &remoteKey, &displayKey, &url, &metadataRaw); err != nil {
			return err
		}
		idx, ok := taskIndexByID[strings.TrimSpace(issueID)]
		if !ok || tasks[idx].PullRequest != nil {
			continue
		}
		tasks[idx].PullRequest = pullRequestFromExternalRef(remoteKey, displayKey, url, metadataRaw)
		if strings.TrimSpace(tasks[idx].Origin) == "" || strings.EqualFold(tasks[idx].Origin, "local") {
			tasks[idx].Origin = "github"
		}
	}
	return rows.Err()
}

func pullRequestFromExternalRef(remoteKey, displayKey, refURL, metadataRaw string) *domain.PullRequest {
	metadata := map[string]string{}
	if strings.TrimSpace(metadataRaw) != "" {
		_ = json.Unmarshal([]byte(metadataRaw), &metadata)
	}
	pr := &domain.PullRequest{
		RemoteKey:  strings.TrimSpace(remoteKey),
		DisplayKey: strings.TrimSpace(displayKey),
		URL:        strings.TrimSpace(refURL),
		State:      firstMetadataValue(metadata, "state", "pr_state"),
		Draft:      parseMetadataBool(firstMetadataValue(metadata, "draft", "is_draft")),
	}
	pr.ChecksStatus = firstMetadataValue(metadata, "checks_status", "checks", "status_checks")
	if number := firstMetadataValue(metadata, "number", "pr_number"); number != "" {
		if parsed, err := strconv.Atoi(strings.TrimPrefix(number, "#")); err == nil {
			pr.Number = parsed
		}
	}
	if pr.Number == 0 {
		for _, candidate := range []string{pr.DisplayKey, pr.RemoteKey} {
			candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "#")
			if parsed, err := strconv.Atoi(candidate); err == nil {
				pr.Number = parsed
				break
			}
		}
	}
	if pr.DisplayKey == "" && pr.Number > 0 {
		pr.DisplayKey = fmt.Sprintf("#%d", pr.Number)
	}
	return pr
}

func firstMetadataValue(metadata map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			return value
		}
	}
	return ""
}

func parseMetadataBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (c *Client) logSQLiteRead(ctx context.Context, operation string, startedAt time.Time, rowCount int, err error, attrs ...any) {
	if c == nil || startedAt.IsZero() {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	base := []any{
		"event", "sqlite.query.completed",
		"service", "azedarach.issue_store",
		"dependency.name", "sqlite",
		"dependency.operation", operation,
		"db.path", sqliteutil.CanonicalPath(c.dbPath),
		"dependency.duration_ms", time.Since(startedAt).Milliseconds(),
		"outcome", outcome,
		"row_count", rowCount,
	}
	base = append(base, attrs...)
	if err != nil {
		base = append(base, "error_class", "sqlite_query")
		if details, ok := sqliteutil.Details(err); ok {
			base = append(base,
				"sqlite.code", details.PrimaryCode,
				"sqlite.extended_code", details.ExtendedCode,
				"sqlite.symbol", details.Symbol,
			)
		}
	}
	latencytrace.LogPhaseContext(ctx, nil, "dependency", "sqlite."+operation, startedAt, base...)
	if c.logger == nil {
		return
	}
	if err != nil {
		c.logger.WarnContext(ctx, "sqlite query completed", base...)
		return
	}
	c.logger.DebugContext(ctx, "sqlite query completed", base...)
}

func applyGitStatusProjection(task *domain.Task, raw string) {
	if task == nil || strings.TrimSpace(raw) == "" {
		return
	}
	var status git.GitStatus
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return
	}
	task.HasUncommittedChanges = status.HasChanges
	task.HasConflicts = status.HasConflicts
	task.ConflictFiles = append([]string(nil), status.Conflicted...)
	task.GitAdditions = status.GitAdditions
	task.GitDeletions = status.GitDeletions
	task.GitAheadCount = status.GitAheadCount
	task.GitBehindCount = status.GitBehindCount
	if task.GitAdditions == 0 {
		task.GitAdditions = len(status.Added) + len(status.Modified) + len(status.Staged)
	}
	if task.GitDeletions == 0 {
		task.GitDeletions = len(status.Deleted)
	}
}

func applyRuntimeOverlay(task *domain.Task, runtime domain.Task) {
	if task == nil {
		return
	}
	task.Session = cloneDomainSession(runtime.Session)
	task.HasTmuxSession = runtime.HasTmuxSession
	task.HasWorktree = runtime.HasWorktree
	task.GitAheadCount = runtime.GitAheadCount
	task.GitBehindCount = runtime.GitBehindCount
	task.HasUncommittedChanges = runtime.HasUncommittedChanges
	task.HasConflicts = runtime.HasConflicts
	task.ConflictFiles = append([]string(nil), runtime.ConflictFiles...)
	task.GitAdditions = runtime.GitAdditions
	task.GitDeletions = runtime.GitDeletions
	task.RuntimeUpdatedAt = runtime.RuntimeUpdatedAt
	task.Ownership = cloneIssueOwnership(runtime.Ownership)
	task.CoordinationLeases = append([]domain.CoordinationLease(nil), runtime.CoordinationLeases...)
	task.PullRequest = clonePullRequest(runtime.PullRequest)
	if strings.TrimSpace(runtime.Origin) != "" {
		task.Origin = runtime.Origin
	}
}

func cloneTaskForRuntimeOverlay(task domain.Task) domain.Task {
	task.Session = cloneDomainSession(task.Session)
	task.Ownership = cloneIssueOwnership(task.Ownership)
	task.CoordinationLeases = append([]domain.CoordinationLease(nil), task.CoordinationLeases...)
	task.Dependencies = append([]domain.Dependency(nil), task.Dependencies...)
	task.Implementations = append([]string(nil), task.Implementations...)
	task.Labels = append([]string(nil), task.Labels...)
	task.ConflictFiles = append([]string(nil), task.ConflictFiles...)
	task.PullRequest = clonePullRequest(task.PullRequest)
	return task
}

func clonePullRequest(pr *domain.PullRequest) *domain.PullRequest {
	if pr == nil {
		return nil
	}
	cloned := *pr
	return &cloned
}

func cloneDomainSession(session *domain.Session) *domain.Session {
	if session == nil {
		return nil
	}
	cloned := *session
	return &cloned
}

func cloneIssueOwnership(ownership *domain.IssueOwnership) *domain.IssueOwnership {
	if ownership == nil {
		return nil
	}
	cloned := *ownership
	if ownership.ExpiresAt != nil {
		expiresAt := *ownership.ExpiresAt
		cloned.ExpiresAt = &expiresAt
	}
	return &cloned
}

func parseIssueOwnership(ownerIDRaw, ownerKindRaw, claimedRaw, expiresRaw string) *domain.IssueOwnership {
	ownerID := strings.TrimSpace(ownerIDRaw)
	if ownerID == "" {
		return nil
	}
	claimedAt := parseTimestamp(claimedRaw)
	ownership := &domain.IssueOwnership{
		OwnerID:   ownerID,
		OwnerKind: strings.TrimSpace(ownerKindRaw),
		ClaimedAt: claimedAt,
		ExpiresAt: parseOptionalTimestamp(expiresRaw),
	}
	if ownership.OwnerKind == "" {
		ownership.OwnerKind = "agent"
	}
	return ownership
}

func taskRuntimeProjectionQuery(projectID string, includeDetails bool, archiveMode ArchiveMode, issueIDs ...string) (string, []any) {
	detailSelect := `
			COALESCE(i.description, ''),
			COALESCE(i.notes, ''),
			COALESCE(i.design, ''),
			COALESCE(i.acceptance, ''),`
	if !includeDetails {
		detailSelect = `
			'',
			'',
			'',
			'',`
	}

	seen := map[string]struct{}{}
	trimmedIDs := make([]string, 0, len(issueIDs))
	for _, issueID := range issueIDs {
		issueID = strings.TrimSpace(issueID)
		if issueID == "" {
			continue
		}
		if _, ok := seen[issueID]; ok {
			continue
		}
		seen[issueID] = struct{}{}
		trimmedIDs = append(trimmedIDs, issueID)
	}
	filtered := len(trimmedIDs) > 0
	idPlaceholders := ""
	if filtered {
		idPlaceholders = strings.TrimSuffix(strings.Repeat("?,", len(trimmedIDs)), ",")
	}
	sessionFilter := ""
	originFilter := ""
	whereFilter := ""
	if filtered {
		sessionFilter = fmt.Sprintf(" AND issue_id IN (%s)", idPlaceholders)
		originFilter = fmt.Sprintf(" AND issue_id IN (%s)", idPlaceholders)
		whereFilter = fmt.Sprintf(" AND i.id IN (%s)\n", idPlaceholders)
	}

	query := `
		WITH ranked_session AS (
			SELECT
				issue_id,
				COALESCE(NULLIF(TRIM(observed_state), ''), state) AS state,
				COALESCE(started_at, '') AS started_at,
				updated_at,
				session_id,
				COALESCE(activity, '') AS activity,
				COALESCE(activity_source, '') AS activity_source,
				COALESCE(tmux_attached_count, 0) AS tmux_attached_count,
				ROW_NUMBER() OVER (
					PARTITION BY issue_id
					ORDER BY
						CASE COALESCE(NULLIF(TRIM(observed_state), ''), state)
							WHEN 'running' THEN 0
							WHEN 'attached' THEN 0
							WHEN 'paused' THEN 1
							WHEN 'starting' THEN 2
							WHEN 'stopped' THEN 3
							ELSE 4
						END,
						updated_at DESC,
						session_id DESC
				) AS rn
			FROM (` + runtimeSessionProjectionUnionSQL + `)
			WHERE project_id = ?` + sessionFilter + `
		),
		session_pick AS (
			SELECT issue_id, state, started_at, updated_at, activity, activity_source, tmux_attached_count
			FROM ranked_session
			WHERE rn = 1
		),
		origin_pick AS (
			SELECT issue_id, MIN(provider) AS provider
			FROM issue_external_refs
			WHERE deleted_at IS NULL` + originFilter + `
			GROUP BY issue_id
		)
		SELECT
			i.id,
			i.title,
` + detailSelect + `
			COALESCE(i.assignee, ''),
			COALESCE(i.labels_json, '[]'),
			i.estimate,
			i.status,
			COALESCE(i.disposition, ''),
			COALESCE(i.engagement, ''),
			COALESCE(i.visibility, ''),
			i.archived_at,
			i.priority,
			i.issue_type,
			COALESCE(i.implementations_json, '[]'),
			i.created_at,
			i.updated_at,
			COALESCE(sp.state, ''),
			COALESCE(sp.started_at, ''),
			COALESCE(sp.updated_at, ''),
			COALESCE(sp.activity, ''),
			COALESCE(sp.activity_source, ''),
			COALESCE(sp.tmux_attached_count, 0),
			COALESCE(w.path, ''),
			COALESCE(w.git_status_json, ''),
			COALESCE(w.updated_at, ''),
			COALESCE(w.git_status_updated_at, ''),
			COALESCE(o.provider, ''),
			COALESCE((SELECT owner_id FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'), ''),
			COALESCE((SELECT owner_kind FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'), ''),
			COALESCE((SELECT claimed_at FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'), ''),
			COALESCE((SELECT expires_at FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'), '')
		FROM issues i
		LEFT JOIN session_pick sp ON sp.issue_id = i.id
		LEFT JOIN daemon_worktree_projections w
			ON w.project_id = ? AND w.issue_id = i.id
		LEFT JOIN origin_pick o ON o.issue_id = i.id
		WHERE ` + archiveWhere("i", archiveMode) + `
	` + whereFilter
	query += " ORDER BY i.updated_at DESC"

	args := []any{projectID}
	if filtered {
		for _, issueID := range trimmedIDs {
			args = append(args, issueID)
		}
		for _, issueID := range trimmedIDs {
			args = append(args, issueID)
		}
	}
	args = append(args, projectID)
	if filtered {
		for _, issueID := range trimmedIDs {
			args = append(args, issueID)
		}
	}
	return query, args
}

func decodeImplementationsJSON(raw string) []string {
	return decodeStringSliceJSON(raw)
}

func decodeStringSliceJSON(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func parseOptionalTimestamp(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed := parseTimestamp(raw)
	if parsed.IsZero() {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}

func newestParsedTimestamp(base time.Time, rawValues ...string) time.Time {
	latest := base
	for _, raw := range rawValues {
		candidate := parseTimestamp(raw)
		if candidate.IsZero() {
			continue
		}
		if latest.IsZero() || candidate.After(latest) {
			latest = candidate
		}
	}
	return latest
}

func mapRuntimeSessionState(value string) domain.SessionState {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "paused":
		return domain.SessionPaused
	case "running", "attached", "starting":
		return domain.SessionBusy
	case "done":
		return domain.SessionDone
	case "waiting":
		return domain.SessionWaiting
	case "error":
		return domain.SessionError
	default:
		return domain.SessionBusy
	}
}

func (c *Client) loadDependenciesForTasks(
	ctx context.Context,
	db sqlIssueDBTX,
	taskIDs []naming.IssueID,
	taskIndexByID map[naming.IssueID]int,
	tasks []domain.Task,
	mode taskDependencyLoadMode,
) error {
	const maxPlaceholders = 500
	for start := 0; start < len(taskIDs); start += maxPlaceholders {
		end := start + maxPlaceholders
		if end > len(taskIDs) {
			end = len(taskIDs)
		}
		chunk := taskIDs[start:end]
		query, dependencyTypeArgs := taskDependencyRowsQuery(len(chunk), mode)
		depArgs := make([]any, 0, len(chunk))
		for _, id := range chunk {
			depArgs = append(depArgs, id.String())
		}
		depArgs = append(depArgs, dependencyTypeArgs...)

		rows, err := db.QueryContext(ctx, query, depArgs...)
		if err != nil {
			return err
		}

		for rows.Next() {
			var issueID string
			var dependsOnID string
			var dependencyType string
			if err := rows.Scan(&issueID, &dependsOnID, &dependencyType); err != nil {
				_ = rows.Close()
				return err
			}
			issueIDTyped, err := naming.ParseIssueID(issueID)
			if err != nil {
				continue
			}
			taskIndex, ok := taskIndexByID[issueIDTyped]
			if !ok {
				continue
			}
			task := &tasks[taskIndex]
			if normalizeDependencyType(dependencyType) == string(domain.DependencyParentChild) {
				parentID, err := naming.ParseIssueID(dependsOnID)
				if err != nil {
					continue
				}
				task.ParentID = &parentID
				continue
			}
			dependencyID, err := naming.ParseIssueID(dependsOnID)
			if err != nil {
				continue
			}
			task.Dependencies = append(task.Dependencies, domain.Dependency{
				ID:   dependencyID,
				Type: domain.DependencyType(normalizeDependencyType(dependencyType)),
			})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func taskDependencyRowsQuery(issueIDCount int, mode taskDependencyLoadMode) (string, []any) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", issueIDCount), ",")
	typeFilter := ""
	args := []any(nil)
	if mode == taskDependencyLoadParentOnly {
		typeFilter = " AND dependency_type IN (?, ?)"
		args = append(args, string(domain.DependencyParentChild), "parent_child")
	}
	return fmt.Sprintf(`
		SELECT issue_id, depends_on_id, dependency_type
		FROM issue_dependencies
		WHERE tombstoned_at IS NULL
			AND issue_id IN (%s)
			%s
	`, placeholders, typeFilter), args
}

func normalizeDependencyType(value string) string {
	switch strings.TrimSpace(value) {
	case "blocked_by", "blocked-by":
		return string(domain.DependencyBlockedBy)
	case "blocks":
		return string(domain.DependencyBlocks)
	case "related_to", "related-to", "related":
		return string(domain.DependencyRelatedTo)
	case "parent_child", "parent-child":
		return string(domain.DependencyParentChild)
	case "discovered_from", "discovered-from":
		return string(domain.DependencyDiscovered)
	case "created_by", "created-by", "created_from", "created-from", "created_in", "created-in":
		return string(domain.DependencyCreatedIn)
	default:
		return value
	}
}

func canonicalDependencyType(value string) (string, error) {
	switch normalizeDependencyType(strings.TrimSpace(value)) {
	case string(domain.DependencyBlocks), string(domain.DependencyBlockedBy):
		return string(domain.DependencyBlocks), nil
	case string(domain.DependencyParentChild):
		return string(domain.DependencyParentChild), nil
	case string(domain.DependencyRelatedTo):
		return string(domain.DependencyRelatedTo), nil
	case string(domain.DependencyDiscovered):
		return string(domain.DependencyDiscovered), nil
	case string(domain.DependencyCreatedIn):
		return string(domain.DependencyCreatedIn), nil
	default:
		return "", fmt.Errorf("unsupported dependency type %q", strings.TrimSpace(value))
	}
}

func dependencyIsAcyclic(dependencyType string) bool {
	switch dependencyType {
	case string(domain.DependencyBlocks), string(domain.DependencyParentChild):
		return true
	default:
		return false
	}
}

func (c *Client) wouldCreateDependencyCycle(ctx context.Context, db *sql.DB, issueID, dependsOnID string) (bool, error) {
	rows, err := db.QueryContext(ctx, `
		WITH RECURSIVE reachable(id) AS (
			SELECT depends_on_id
			FROM issue_dependencies
			WHERE issue_id = ?
				AND tombstoned_at IS NULL
				AND dependency_type IN ('blocks', 'parent-child', 'parent_child')
			UNION
			SELECT d.depends_on_id
			FROM issue_dependencies d
			JOIN reachable r ON d.issue_id = r.id
			WHERE d.tombstoned_at IS NULL
				AND d.dependency_type IN ('blocks', 'parent-child', 'parent_child')
		)
		SELECT 1
		FROM reachable
		WHERE id = ?
		LIMIT 1
	`, dependsOnID, issueID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	return rows.Next(), rows.Err()
}

func parseTimestamp(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.000Z07:00",
	}
	for _, format := range formats {
		if parsed, err := time.Parse(format, raw); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func marshalOptionalStringSlice(values []string) (any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return string(payload), nil
}

func marshalStringMap(values map[string]string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	normalized := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		normalized[key] = strings.TrimSpace(value)
	}
	if len(normalized) == 0 {
		return "", nil
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func normalizeExternalIssueRefParams(params UpsertExternalIssueRefParams) (UpsertExternalIssueRefParams, error) {
	params.IssueID = strings.TrimSpace(params.IssueID)
	params.Provider = strings.TrimSpace(params.Provider)
	params.ProviderScope = strings.TrimSpace(params.ProviderScope)
	params.RemoteKey = strings.TrimSpace(params.RemoteKey)
	params.DisplayKey = strings.TrimSpace(params.DisplayKey)
	params.URL = strings.TrimSpace(params.URL)
	if params.IssueID == "" {
		return UpsertExternalIssueRefParams{}, errors.New("issue id is required")
	}
	if params.Provider == "" {
		return UpsertExternalIssueRefParams{}, errors.New("provider is required")
	}
	if params.RemoteKey == "" {
		return UpsertExternalIssueRefParams{}, errors.New("remote key is required")
	}
	return params, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanExternalIssueRef(row scanner) (domain.ExternalIssueRef, error) {
	var ref domain.ExternalIssueRef
	var metadataRaw string
	var createdRaw string
	var updatedRaw string
	if err := row.Scan(&ref.IssueID, &ref.Provider, &ref.ProviderScope, &ref.RemoteKey, &ref.DisplayKey, &ref.URL, &metadataRaw, &createdRaw, &updatedRaw); err != nil {
		return domain.ExternalIssueRef{}, err
	}
	ref.CreatedAt = parseTimestamp(createdRaw)
	ref.UpdatedAt = parseTimestamp(updatedRaw)
	ref.Metadata = map[string]string{}
	if strings.TrimSpace(metadataRaw) != "" {
		if err := json.Unmarshal([]byte(metadataRaw), &ref.Metadata); err != nil {
			return domain.ExternalIssueRef{}, err
		}
	}
	if len(ref.Metadata) == 0 {
		ref.Metadata = nil
	}
	return ref, nil
}

func (c *Client) getMetaValue(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return "", err
	}
	return value, nil
}

func (c *Client) loadAllocatedIssueIDs(ctx context.Context, queryer sqlIssueDBTX) (map[string]struct{}, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT id FROM issue_id_allocations
		UNION
		SELECT id FROM issues
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		ids[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (c *Client) reserveIssueID(ctx context.Context, execer sqlIssueExecer, issueID, allocatedAt, source string) error {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return fmt.Errorf("issue id is required")
	}
	if strings.TrimSpace(allocatedAt) == "" {
		allocatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := execer.ExecContext(ctx, `
		INSERT INTO issue_id_allocations (id, allocated_at, source)
		VALUES (?, ?, ?)
	`, issueID, allocatedAt, strings.TrimSpace(source))
	return err
}

func parseNextAlphaIssueIndex(raw string) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func allocateNextAlphaIssueID(startIndex int, existing map[string]struct{}) (string, int) {
	candidateIndex := startIndex
	for {
		candidateID := encodeAlphaIssueIndex(candidateIndex)
		if _, ok := existing[candidateID]; !ok {
			return candidateID, candidateIndex + 1
		}
		candidateIndex++
	}
}

func encodeAlphaIssueIndex(index int) string {
	if index < 0 {
		return "a"
	}
	remaining := index
	encoded := ""
	for remaining >= 0 {
		digit := remaining % 26
		encoded = string(rune('a'+digit)) + encoded
		remaining = (remaining / 26) - 1
	}
	return encoded
}

func resolveDBPath(repoDir string) (string, error) {
	startDir := repoDir
	if strings.TrimSpace(startDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		startDir = cwd
	}
	absStart, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	baseRoot, err := config.ResolveProjectRoot(absStart)
	if err == nil {
		candidate := filepath.Join(baseRoot, ".azedarach", "azedarach.db")
		if fromEnv := strings.TrimSpace(os.Getenv("AZEDARACH_DB_PATH")); fromEnv != "" {
			useOverride, useErr := dbpathguard.UseProjectOverride(candidate, fromEnv)
			if useErr != nil {
				return "", fmt.Errorf("resolve test database override: %w", useErr)
			}
			if useOverride {
				return fromEnv, nil
			}
		}
		return candidate, nil
	}
	return "", fmt.Errorf("resolve project root: %w", err)
}

func (c *Client) wrapError(op string, issueID string, err error) error {
	if IsSQLiteCorrupt(err) {
		err = c.markSQLiteCorrupt(err)
	}
	storeErr := &domain.TaskStoreError{
		Op:  op,
		Err: err,
	}
	if issueID != "" {
		storeErr.TaskID = issueID
	}
	return storeErr
}

func (c *Client) markSQLiteCorrupt(cause error) error {
	if existing := c.corruption.Load(); existing != nil {
		return existing.err
	}
	quarantineErr := fmt.Errorf(
		"%w detected; this issue store is quarantined in the current process. Preserve the database and WAL, create a consistent online-backup clone, and recover or replace the authority from validated clone evidence before retrying: %w",
		ErrSQLiteCorrupt,
		cause,
	)
	state := &sqliteCorruptionState{err: quarantineErr}
	if c.corruption.CompareAndSwap(nil, state) {
		return quarantineErr
	}
	return c.corruption.Load().err
}

func (c *Client) corruptionError() error {
	state := c.corruption.Load()
	if state == nil {
		return nil
	}
	return state.err
}

// CorruptionError reports the process-local corruption quarantine, if any.
// It does not open or inspect the database.
func (c *Client) CorruptionError() error {
	if c == nil {
		return nil
	}
	return c.corruptionError()
}
