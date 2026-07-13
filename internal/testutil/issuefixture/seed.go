package issuefixture

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	_ "modernc.org/sqlite"
)

const (
	// MaxRows bounds a single fixture request so an accidentally unbounded test
	// cannot turn this helper into another hidden scale problem.
	MaxRows = 10_000
	// transactionRows keeps write locks bounded while still amortizing fixture
	// setup overhead for large-project tests.
	transactionRows = 500
)

type Issue struct {
	ID       string
	Title    string
	Status   domain.Status
	Priority domain.Priority
	Type     domain.TaskType
}

type Dependency struct {
	IssueID     string
	DependsOnID string
	Type        domain.DependencyType
}

type Learning struct {
	LocalID         string
	ProjectID       string
	Summary         string
	Evidence        string
	EvidencePrivate bool
	Status          string
}

type SessionProjection struct {
	ProjectID string
	SessionID string
	IssueID   string
	State     string
	Activity  string
	Source    string
}

type Fixture struct {
	Issues       []Issue
	Dependencies []Dependency
	Learnings    []Learning
	Sessions     []SessionProjection
}

type Result struct {
	Rows     int
	Duration time.Duration
}

// SeedPath opens an already migrated fixture database and seeds it using the
// same bounded transactions as SeedDB. It never creates or migrates a schema.
func SeedPath(ctx context.Context, path string, fixture Fixture) (Result, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return Result{}, fmt.Errorf("open issue fixture database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return SeedDB(ctx, db, fixture)
}

// SeedDB inserts typed scale-test fixtures into an already migrated issue
// database. Production read/query paths remain responsible for every behavior
// asserted after setup.
func SeedDB(ctx context.Context, db *sql.DB, fixture Fixture) (Result, error) {
	started := time.Now()
	rows := len(fixture.Issues) + len(fixture.Dependencies) + len(fixture.Learnings) + len(fixture.Sessions)
	if rows > MaxRows {
		return Result{}, fmt.Errorf("issue fixture has %d rows, maximum is %d", rows, MaxRows)
	}
	if err := validateFixture(fixture); err != nil {
		return Result{}, err
	}
	if err := seedBatches(ctx, db, fixture.Issues, seedIssues); err != nil {
		return Result{}, err
	}
	if err := seedBatches(ctx, db, fixture.Dependencies, seedDependencies); err != nil {
		return Result{}, err
	}
	if len(fixture.Dependencies) > 0 {
		if err := rebuildGraphClosure(ctx, db); err != nil {
			return Result{}, err
		}
	}
	if err := seedBatches(ctx, db, fixture.Learnings, seedLearnings); err != nil {
		return Result{}, err
	}
	if err := seedBatches(ctx, db, fixture.Sessions, seedSessions); err != nil {
		return Result{}, err
	}
	return Result{Rows: rows, Duration: time.Since(started)}, nil
}

func validateFixture(fixture Fixture) error {
	for i, issue := range fixture.Issues {
		if strings.TrimSpace(issue.ID) == "" || strings.TrimSpace(issue.Title) == "" {
			return fmt.Errorf("issue fixture row %d requires id and title", i)
		}
	}
	for i, dep := range fixture.Dependencies {
		if strings.TrimSpace(dep.IssueID) == "" || strings.TrimSpace(dep.DependsOnID) == "" || dep.Type == "" {
			return fmt.Errorf("dependency fixture row %d requires issue, dependency, and type", i)
		}
	}
	for i, learning := range fixture.Learnings {
		if strings.TrimSpace(learning.LocalID) == "" || strings.TrimSpace(learning.ProjectID) == "" || strings.TrimSpace(learning.Summary) == "" {
			return fmt.Errorf("learning fixture row %d requires local id, project id, and summary", i)
		}
	}
	for i, session := range fixture.Sessions {
		if strings.TrimSpace(session.ProjectID) == "" || strings.TrimSpace(session.SessionID) == "" || strings.TrimSpace(session.IssueID) == "" || strings.TrimSpace(session.State) == "" {
			return fmt.Errorf("session fixture row %d requires project, session, issue, and state", i)
		}
	}
	return nil
}

func seedBatches[T any](ctx context.Context, db *sql.DB, rows []T, seed func(context.Context, *sql.Tx, []T) error) error {
	for start := 0; start < len(rows); start += transactionRows {
		end := min(start+transactionRows, len(rows))
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin issue fixture transaction: %w", err)
		}
		if err := seed(ctx, tx, rows[start:end]); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit issue fixture transaction: %w", err)
		}
	}
	return nil
}

func seedIssues(ctx context.Context, tx *sql.Tx, rows []Issue) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issues (
			id, title, description, status, disposition, engagement, visibility,
			priority, issue_type, created_at, updated_at, closed_at, labels_json,
			implementations_json, lifecycle_state, closed_outcome, review_state
		) VALUES (?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', '[]', ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare issue fixtures: %w", err)
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range rows {
		status := row.Status
		if status == "" {
			status = domain.StatusOpen
		}
		issueType := row.Type
		if issueType == "" {
			issueType = domain.TypeTask
		}
		state, err := domain.IssueStateFromStatus(status)
		if err != nil {
			return fmt.Errorf("derive fixture state for issue %s: %w", row.ID, err)
		}
		var closedAt any
		if state.Workflow() == domain.IssueWorkflowClosed {
			closedAt = now
		}
		if _, err := stmt.ExecContext(ctx, row.ID, row.Title, status, state.Disposition, state.Engagement, state.Visibility, int(row.Priority), issueType, now, now, closedAt, state.Workflow(), state.CloseOutcome(), state.Review()); err != nil {
			return fmt.Errorf("seed issue %s: %w", row.ID, err)
		}
	}
	return nil
}

func seedDependencies(ctx context.Context, tx *sql.Tx, rows []Dependency) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES (?, ?, ?, NULL)
	`)
	if err != nil {
		return fmt.Errorf("prepare dependency fixtures: %w", err)
	}
	defer stmt.Close()
	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx, row.IssueID, row.DependsOnID, row.Type); err != nil {
			return fmt.Errorf("seed dependency %s -> %s: %w", row.IssueID, row.DependsOnID, err)
		}
	}
	return nil
}

func rebuildGraphClosure(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin issue fixture graph closure transaction: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		DELETE FROM issue_graph_closure;
		INSERT INTO issue_graph_closure (
			project_id, ancestor_id, descendant_id, dependency_type, depth, updated_at
		)
		WITH RECURSIVE parent_edges(ancestor_id, descendant_id) AS (
			SELECT d.depends_on_id, d.issue_id
			FROM issue_dependencies d
			JOIN issues ancestor ON ancestor.id=d.depends_on_id AND ancestor.deleted_at IS NULL
			JOIN issues descendant ON descendant.id=d.issue_id AND descendant.deleted_at IS NULL
			WHERE d.tombstoned_at IS NULL AND d.dependency_type IN ('parent-child','parent_child')
		), closure(ancestor_id, descendant_id, depth, path) AS (
			SELECT ancestor_id, descendant_id, 1, ','||ancestor_id||','||descendant_id||',' FROM parent_edges
			UNION ALL
			SELECT c.ancestor_id, e.descendant_id, c.depth+1, c.path||e.descendant_id||','
			FROM closure c JOIN parent_edges e ON e.ancestor_id=c.descendant_id
			WHERE instr(c.path, ','||e.descendant_id||',')=0
		)
		SELECT 'default', ancestor_id, descendant_id, 'parent-child', MIN(depth), ?
		FROM closure WHERE ancestor_id<>descendant_id GROUP BY ancestor_id, descendant_id
	`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("rebuild issue fixture graph closure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit issue fixture graph closure: %w", err)
	}
	return nil
}

func seedLearnings(ctx context.Context, tx *sql.Tx, rows []Learning) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO agent_learnings (
			local_id, project_id, summary, evidence, evidence_private, status,
			review_note, reviewed_at, tags_json, files_json, created_at, updated_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '[]', '[]', ?, ?, NULL)
	`)
	if err != nil {
		return fmt.Errorf("prepare learning fixtures: %w", err)
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range rows {
		status := strings.TrimSpace(row.Status)
		if status == "" {
			status = "candidate"
		}
		var reviewNote, reviewedAt any
		if status == "rejected" || status == "stale" {
			reviewNote, reviewedAt = "fixture lifecycle", now
		}
		if _, err := stmt.ExecContext(ctx, row.LocalID, row.ProjectID, row.Summary, row.Evidence, row.EvidencePrivate, status, reviewNote, reviewedAt, now, now); err != nil {
			return fmt.Errorf("seed learning %s: %w", row.LocalID, err)
		}
	}
	return nil
}

func seedSessions(ctx context.Context, tx *sql.Tx, rows []SessionProjection) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO daemon_session_projections (
			project_id, session_id, issue_id, role, scope_kind, scope_id,
			state, observed_state, activity, activity_source, updated_at
		) VALUES (?, ?, ?, 'worker', 'issue', ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare session fixtures: %w", err)
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx, row.ProjectID, row.SessionID, row.IssueID, row.IssueID, row.State, row.State, row.Activity, row.Source, now); err != nil {
			return fmt.Errorf("seed session %s: %w", row.SessionID, err)
		}
	}
	return nil
}
