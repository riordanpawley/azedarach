package issues

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

var ErrBoardViewNotFound = errors.New("board view not found")
var ErrBoardViewBuiltIn = errors.New("cannot modify built-in board view")

func (c *Client) ListBoardViews(ctx context.Context, projectID string) ([]domain.BoardViewRecord, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = normalizeBoardViewProjectID(projectID)
	if err := c.seedBuiltInBoardViews(ctx, db, projectID); err != nil {
		return nil, c.wrapError("board-view-list", projectID, err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT project_id, id, name, definition_json, built_in, created_at, updated_at
		FROM board_views
		WHERE project_id = ? AND deleted_at IS NULL
		ORDER BY built_in DESC, name COLLATE NOCASE ASC, id ASC
	`, projectID)
	if err != nil {
		return nil, c.wrapError("board-view-list", projectID, err)
	}
	defer rows.Close()
	var out []domain.BoardViewRecord
	for rows.Next() {
		record, err := scanBoardViewRecord(rows)
		if err != nil {
			return nil, c.wrapError("board-view-list", projectID, err)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("board-view-list", projectID, err)
	}
	return out, nil
}

func (c *Client) GetBoardView(ctx context.Context, projectID, viewID string) (domain.BoardViewRecord, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.BoardViewRecord{}, err
	}
	projectID = normalizeBoardViewProjectID(projectID)
	if err := c.seedBuiltInBoardViews(ctx, db, projectID); err != nil {
		return domain.BoardViewRecord{}, c.wrapError("board-view-get", projectID, err)
	}
	viewID = domain.NormalizeBoardViewID(viewID)
	if viewID == "" {
		viewID = domain.DefaultBoardViewID
	}
	row := db.QueryRowContext(ctx, `
		SELECT project_id, id, name, definition_json, built_in, created_at, updated_at
		FROM board_views
		WHERE project_id = ? AND id = ? AND deleted_at IS NULL
	`, projectID, viewID)
	record, err := scanBoardViewRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BoardViewRecord{}, ErrBoardViewNotFound
	}
	if err != nil {
		return domain.BoardViewRecord{}, c.wrapError("board-view-get", projectID, err)
	}
	return record, nil
}

func (c *Client) SaveBoardView(ctx context.Context, projectID string, view domain.BoardViewDefinition) (domain.BoardViewRecord, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.BoardViewRecord{}, err
	}
	projectID = normalizeBoardViewProjectID(projectID)
	if err := c.seedBuiltInBoardViews(ctx, db, projectID); err != nil {
		return domain.BoardViewRecord{}, c.wrapError("board-view-save", projectID, err)
	}
	view = view.Normalized()
	definitionJSON, err := domain.EncodeBoardViewDefinitionJSON(view)
	if err != nil {
		return domain.BoardViewRecord{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := c.withMutationLock(ctx, func(lockCtx context.Context) error {
		var builtIn int
		err := db.QueryRowContext(lockCtx, `
			SELECT built_in
			FROM board_views
			WHERE project_id = ? AND id = ? AND deleted_at IS NULL
		`, projectID, view.ID).Scan(&builtIn)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if builtIn != 0 {
			return ErrBoardViewBuiltIn
		}
		_, execErr := db.ExecContext(lockCtx, `
			INSERT INTO board_views (project_id, id, name, definition_json, built_in, created_at, updated_at, deleted_at)
			VALUES (?, ?, ?, ?, 0, ?, ?, NULL)
			ON CONFLICT(project_id, id) DO UPDATE SET
				name = excluded.name,
				definition_json = excluded.definition_json,
				built_in = 0,
				updated_at = excluded.updated_at,
				deleted_at = NULL
		`, projectID, view.ID, view.Name, string(definitionJSON), now, now)
		return execErr
	}); err != nil {
		return domain.BoardViewRecord{}, c.wrapError("board-view-save", projectID, err)
	}
	return c.GetBoardView(ctx, projectID, view.ID)
}

func (c *Client) DeleteBoardView(ctx context.Context, projectID, viewID string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	projectID = normalizeBoardViewProjectID(projectID)
	if err := c.seedBuiltInBoardViews(ctx, db, projectID); err != nil {
		return c.wrapError("board-view-delete", projectID, err)
	}
	viewID = domain.NormalizeBoardViewID(viewID)
	if viewID == "" {
		return ErrBoardViewNotFound
	}
	return c.withMutationLock(ctx, func(lockCtx context.Context) error {
		var builtIn int
		err := db.QueryRowContext(lockCtx, `
			SELECT built_in
			FROM board_views
			WHERE project_id = ? AND id = ? AND deleted_at IS NULL
		`, projectID, viewID).Scan(&builtIn)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrBoardViewNotFound
		}
		if err != nil {
			return c.wrapError("board-view-delete", projectID, err)
		}
		if builtIn != 0 {
			return ErrBoardViewBuiltIn
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result, err := db.ExecContext(lockCtx, `
			UPDATE board_views
			SET deleted_at = ?, updated_at = ?
			WHERE project_id = ? AND id = ? AND deleted_at IS NULL
		`, now, now, projectID, viewID)
		if err != nil {
			return c.wrapError("board-view-delete", projectID, err)
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return ErrBoardViewNotFound
		}
		return nil
	})
}

func (c *Client) seedBuiltInBoardViews(ctx context.Context, db *sql.DB, projectID string) error {
	if err := ensureBoardViewsSchema(db); err != nil {
		return err
	}
	projectID = normalizeBoardViewProjectID(projectID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, view := range domain.BuiltInBoardViews() {
		view = view.Normalized()
		definitionJSON, err := domain.EncodeBoardViewDefinitionJSON(view)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO board_views (project_id, id, name, definition_json, built_in, created_at, updated_at, deleted_at)
			VALUES (?, ?, ?, ?, 1, ?, ?, NULL)
			ON CONFLICT(project_id, id) DO UPDATE SET
				name = excluded.name,
				definition_json = excluded.definition_json,
				built_in = 1,
				updated_at = excluded.updated_at,
				deleted_at = NULL
			WHERE board_views.built_in = 1
		`, projectID, view.ID, view.Name, string(definitionJSON), now, now); err != nil {
			return fmt.Errorf("seed board view %q: %w", view.ID, err)
		}
	}
	return nil
}

func ensureBoardViewsSchema(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS board_views (
			project_id TEXT NOT NULL,
			id TEXT NOT NULL,
			name TEXT NOT NULL,
			definition_json TEXT NOT NULL,
			built_in INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			PRIMARY KEY (project_id, id)
		)
	`); err != nil {
		return fmt.Errorf("ensure board_views table: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_board_views_project_active ON board_views (project_id, deleted_at, built_in, name)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure board_views index: %w", err)
		}
	}
	return nil
}

type boardViewScanner interface {
	Scan(dest ...any) error
}

func scanBoardViewRecord(scanner boardViewScanner) (domain.BoardViewRecord, error) {
	var (
		record         domain.BoardViewRecord
		viewID         string
		name           string
		definitionJSON string
		builtIn        int
		createdAt      string
		updatedAt      string
	)
	if err := scanner.Scan(&record.ProjectID, &viewID, &name, &definitionJSON, &builtIn, &createdAt, &updatedAt); err != nil {
		return domain.BoardViewRecord{}, err
	}
	view, err := domain.DecodeBoardViewDefinitionJSON([]byte(definitionJSON))
	if err != nil {
		return domain.BoardViewRecord{}, fmt.Errorf("decode board view %q: %w", viewID, err)
	}
	record.View = view
	record.BuiltIn = builtIn != 0
	record.CreatedAt = parseBoardViewTime(createdAt)
	record.UpdatedAt = parseBoardViewTime(updatedAt)
	return record, nil
}

func parseBoardViewTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

func normalizeBoardViewProjectID(projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "default"
	}
	return projectID
}
