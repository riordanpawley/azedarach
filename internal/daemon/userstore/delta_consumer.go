package userstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

var ErrProjectDeltaConflict = errors.New("project delta component changed concurrently")

// ProjectDeltaState is one vector component in the root-user read model. Its
// cursor orders only one project's transitional delivery stream; Hash chains
// verified batches for that project and never creates a global order.
type ProjectDeltaState struct {
	ProjectID    string
	Cursor       uint64
	Hash         string
	SourceVector []protocol.ProjectionSourceRange
	Projector    protocol.ProjectionProjector
	Initialized  bool
}

type ProjectDeltaChange struct {
	IssueID string
	Delete  bool
	Issue   *domain.Task
}

type ProjectDeltaApply struct {
	Project  CatalogProject
	Expected ProjectDeltaState
	Next     ProjectDeltaState
	Changes  []ProjectDeltaChange
}

func (s *Store) ProjectDeltaState(ctx context.Context, projectID string) (ProjectDeltaState, error) {
	return readProjectDeltaState(ctx, s.db, projectID)
}

func readProjectDeltaState(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID string) (ProjectDeltaState, error) {
	state := ProjectDeltaState{ProjectID: strings.TrimSpace(projectID)}
	var sourceVector []byte
	err := q.QueryRowContext(ctx, `SELECT delta_cursor,delta_hash,delta_source_vector_json,delta_projector_id,delta_projector_schema,delta_projector_build,delta_projector_checksum
		FROM projects WHERE project_id=?`, state.ProjectID).Scan(&state.Cursor, &state.Hash, &sourceVector, &state.Projector.ID, &state.Projector.SchemaVersion, &state.Projector.Build, &state.Projector.Checksum)
	if err != nil {
		return state, err
	}
	if err := decodeJSON(sourceVector, &state.SourceVector); err != nil {
		return state, fmt.Errorf("decode project delta source vector: %w", err)
	}
	state.Initialized = strings.TrimSpace(state.Projector.ID) != ""
	return state, nil
}

// ApplyProjectDelta atomically updates keyed root rows and the corresponding
// per-project vector component. It never writes the project authority store.
func (s *Store) ApplyProjectDelta(ctx context.Context, apply ProjectDeltaApply) error {
	projectID := strings.TrimSpace(apply.Project.ProjectID)
	if projectID == "" || apply.Expected.ProjectID != projectID || apply.Next.ProjectID != projectID {
		return fmt.Errorf("project delta component identity mismatch: project=%q expected=%q next=%q", projectID, apply.Expected.ProjectID, apply.Next.ProjectID)
	}
	if !apply.Next.Initialized || strings.TrimSpace(apply.Next.Projector.ID) == "" {
		return errors.New("next project delta component is uninitialized")
	}
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin project delta apply: %w", err)
		}
		defer tx.Rollback()
		current, err := readProjectDeltaState(ctx, tx, projectID)
		if err != nil {
			return fmt.Errorf("read project delta component: %w", err)
		}
		if projectDeltaStateEqual(current, apply.Next) {
			return tx.Commit()
		}
		if !projectDeltaStateEqual(current, apply.Expected) {
			return fmt.Errorf("%w: project=%s cursor=%d hash=%q expected_cursor=%d expected_hash=%q", ErrProjectDeltaConflict, projectID, current.Cursor, current.Hash, apply.Expected.Cursor, apply.Expected.Hash)
		}
		if apply.Next.Cursor <= current.Cursor {
			return fmt.Errorf("project delta cursor did not advance: current=%d next=%d", current.Cursor, apply.Next.Cursor)
		}
		for _, change := range apply.Changes {
			if err := s.applyProjectIssueChange(ctx, tx, projectID, change); err != nil {
				return err
			}
		}
		sourceVector, err := encodeJSON(apply.Next.SourceVector)
		if err != nil {
			return fmt.Errorf("encode project delta source vector: %w", err)
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE projects SET name=?,path=?,db_path=?,projection_version=?,delta_cursor=?,delta_hash=?,delta_source_vector_json=?,delta_projector_id=?,delta_projector_schema=?,delta_projector_build=?,delta_projector_checksum=?,freshness='fresh',refreshed_at=?,last_attempt_at=?,last_error='',registered=1 WHERE project_id=?`,
			apply.Project.Name, cleanCatalogPath(apply.Project.Path), cleanCatalogPath(apply.Project.DBPath), projectionVersion,
			apply.Next.Cursor, apply.Next.Hash, sourceVector, apply.Next.Projector.ID, apply.Next.Projector.SchemaVersion, apply.Next.Projector.Build, apply.Next.Projector.Checksum,
			now, now, projectID)
		if err != nil {
			return fmt.Errorf("advance project delta component: %w", err)
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			return fmt.Errorf("advance project delta component changed %d rows (count error %v), want 1", rows, rowsErr)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit project delta apply: %w", err)
		}
		return nil
	})
}

func projectDeltaStateEqual(left, right ProjectDeltaState) bool {
	return left.ProjectID == right.ProjectID && left.Cursor == right.Cursor && left.Hash == right.Hash &&
		left.Projector == right.Projector && reflect.DeepEqual(left.SourceVector, right.SourceVector)
}

func encodeJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

func (s *Store) applyProjectIssueChange(ctx context.Context, tx *sql.Tx, projectID string, change ProjectDeltaChange) error {
	issueID := strings.TrimSpace(change.IssueID)
	if issueID == "" {
		return errors.New("project delta issue ID is empty")
	}
	if change.Delete {
		return deleteProjectedIssue(ctx, tx, projectID, issueID)
	}
	if change.Issue == nil || change.Issue.ID.String() != issueID {
		return fmt.Errorf("project delta issue payload key mismatch for %q", issueID)
	}
	issue := *change.Issue
	current, err := s.tasks(ctx, tx, projectID, "", nil, nil, []string{issueID})
	if err != nil {
		return fmt.Errorf("read existing projected issue %s: %w", issueID, err)
	}
	if len(current) > 0 {
		preserveRuntimeProjection(&issue, current[0])
	}
	if err := deleteProjectedIssue(ctx, tx, projectID, issueID); err != nil {
		return err
	}
	if err := insertTask(ctx, tx, projectID, issue); err != nil {
		return fmt.Errorf("apply projected issue %s: %w", issueID, err)
	}
	return nil
}

func deleteProjectedIssue(ctx context.Context, tx *sql.Tx, projectID, issueID string) error {
	for _, statement := range []string{
		`DELETE FROM project_issue_search_projection WHERE project_id=? AND issue_id=?`,
		`DELETE FROM project_issue_dependency_projection WHERE project_id=? AND issue_id=?`,
		`DELETE FROM project_session_projection WHERE project_id=? AND issue_id=?`,
		`DELETE FROM project_worktree_projection WHERE project_id=? AND issue_id=?`,
		`DELETE FROM project_issue_projection WHERE project_id=? AND issue_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, projectID, issueID); err != nil {
			return fmt.Errorf("delete projected issue %s: %w", issueID, err)
		}
	}
	return nil
}

func preserveRuntimeProjection(next *domain.Task, current domain.Task) {
	next.Session = current.Session
	next.HasTmuxSession = current.HasTmuxSession
	next.HasWorktree = current.HasWorktree
	next.GitAheadCount = current.GitAheadCount
	next.GitBehindCount = current.GitBehindCount
	next.HasUncommittedChanges = current.HasUncommittedChanges
	next.HasConflicts = current.HasConflicts
	next.ConflictFiles = append([]string(nil), current.ConflictFiles...)
	next.GitAdditions = current.GitAdditions
	next.GitDeletions = current.GitDeletions
	next.Origin = current.Origin
	next.PullRequest = current.PullRequest
	next.RuntimeUpdatedAt = current.RuntimeUpdatedAt
	next.Ownership = current.Ownership
	next.CoordinationLeases = append([]domain.CoordinationLease(nil), current.CoordinationLeases...)
}

func (s *Store) MarkProjectDeltaStale(ctx context.Context, projectID string, cause error) error {
	message := "project delta consumer is stale"
	if cause != nil {
		message = cause.Error()
	}
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		_, err := s.db.ExecContext(ctx, `UPDATE projects SET freshness='stale',last_attempt_at=?,last_error=? WHERE project_id=?`, s.now().UTC().Format(time.RFC3339Nano), message, strings.TrimSpace(projectID))
		return err
	})
}
