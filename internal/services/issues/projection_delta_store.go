package issues

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

const (
	defaultProjectionDeltaLimit = 500
)

type ProjectionDeltaParams struct {
	ProjectID      string
	Kind           domain.ProjectionKind
	Key            string
	Operation      domain.ProjectionDeltaOperation
	IdempotencyKey string
	Payload        json.RawMessage
	CommittedAt    time.Time
}

type ProjectionSourceAdvance struct {
	ProjectID       string
	SourceAuthority string
	SourcePosition  string
	SourceHash      string
	IdempotencyKey  string
	CommittedAt     time.Time
}

// CommitProjectionEmptyAdvance records source progress with zero keyed
// materialization changes. The resulting row is a transitional delivery marker
// and is filtered from public delta values and snapshots.
func (c *Client) CommitProjectionEmptyAdvance(ctx context.Context, advance ProjectionSourceAdvance) (domain.ProjectionDelta, error) {
	var committed domain.ProjectionDelta
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			committed, err = appendProjectionEmptyAdvance(ctx, tx, advance)
			if err != nil {
				return err
			}
			return tx.Commit()
		})
	})
	return committed, err
}

func appendProjectionEmptyAdvance(ctx context.Context, tx sqlIssueDBTX, advance ProjectionSourceAdvance) (domain.ProjectionDelta, error) {
	authority, position := strings.TrimSpace(advance.SourceAuthority), strings.TrimSpace(advance.SourcePosition)
	if authority == "" || position == "" {
		return domain.ProjectionDelta{}, errors.New("projection source authority and position are required")
	}
	payload, err := json.Marshal(map[string]string{"authority": authority, "position": position, "source_hash": strings.TrimSpace(advance.SourceHash)})
	if err != nil {
		return domain.ProjectionDelta{}, err
	}
	key := strings.TrimSpace(advance.IdempotencyKey)
	if key == "" {
		key = "source-advance:" + authority + ":" + position
	}
	return appendProjectionDelta(ctx, tx, ProjectionDeltaParams{ProjectID: advance.ProjectID, Kind: domain.ProjectionKindSourceAdvance, Key: authority, Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: key, Payload: payload, CommittedAt: advance.CommittedAt})
}

// ProjectionMutation is the transaction boundary offered to authoritative
// stores. Writes made through it commit atomically with their projection delta.
type ProjectionMutation interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// CommitProjectionDelta runs mutate and appends its typed keyed delta in the
// same SQLite transaction. Replaying an idempotency key returns the original
// delta without re-running mutate.
func (c *Client) CommitProjectionDelta(ctx context.Context, params ProjectionDeltaParams, mutate func(context.Context, ProjectionMutation) error) (domain.ProjectionDelta, error) {
	params, err := normalizeProjectionDeltaParams(params)
	if err != nil {
		return domain.ProjectionDelta{}, err
	}
	var committed domain.ProjectionDelta
	err = c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin projection delta commit: %w", err)
			}
			defer tx.Rollback()

			existing, found, err := findProjectionDeltaByIdempotencyKey(ctx, tx, params.ProjectID, params.IdempotencyKey)
			if err != nil {
				return err
			}
			if found {
				if !projectionDeltaMatches(existing, params) {
					return fmt.Errorf("projection idempotency key %q reused with different semantics: %w", params.IdempotencyKey, domain.ErrConflict)
				}
				committed = existing
				return nil
			}

			if mutate != nil {
				if err := mutate(ctx, tx); err != nil {
					return fmt.Errorf("apply authoritative mutation: %w", err)
				}
			}
			appended, err := appendProjectionDelta(ctx, tx, params)
			if err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit projection delta: %w", err)
			}
			committed = appended
			return nil
		})
	})
	return committed, err
}

func appendProjectionDelta(ctx context.Context, tx sqlIssueDBTX, params ProjectionDeltaParams) (domain.ProjectionDelta, error) {
	params, err := normalizeProjectionDeltaParams(params)
	if err != nil {
		return domain.ProjectionDelta{}, err
	}
	existing, found, err := findProjectionDeltaByIdempotencyKey(ctx, tx, params.ProjectID, params.IdempotencyKey)
	if err != nil {
		return domain.ProjectionDelta{}, err
	}
	if found {
		if !projectionDeltaMatches(existing, params) {
			return domain.ProjectionDelta{}, fmt.Errorf("projection idempotency key %q reused with different semantics: %w", params.IdempotencyKey, domain.ErrConflict)
		}
		return existing, nil
	}
	now := params.CommittedAt.UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO projection_streams(project_id,head_cursor,updated_at) VALUES(?,0,?) ON CONFLICT(project_id) DO NOTHING`, params.ProjectID, now.Format(time.RFC3339Nano)); err != nil {
		return domain.ProjectionDelta{}, fmt.Errorf("ensure projection stream: %w", err)
	}
	var head uint64
	if err := tx.QueryRowContext(ctx, `SELECT head_cursor FROM projection_streams WHERE project_id=?`, params.ProjectID).Scan(&head); err != nil {
		return domain.ProjectionDelta{}, fmt.Errorf("read projection head: %w", err)
	}
	cursor := head + 1
	result, err := tx.ExecContext(ctx, `UPDATE projection_streams SET head_cursor=?,updated_at=? WHERE project_id=? AND head_cursor=?`, cursor, now.Format(time.RFC3339Nano), params.ProjectID, head)
	if err != nil {
		return domain.ProjectionDelta{}, fmt.Errorf("advance projection head: %w", err)
	}
	advanced, err := result.RowsAffected()
	if err != nil || advanced != 1 {
		return domain.ProjectionDelta{}, fmt.Errorf("advance projection head changed %d rows (count error %v), want 1", advanced, err)
	}
	payload := normalizedProjectionPayload(params.Payload)
	if _, err := tx.ExecContext(ctx, `INSERT INTO projection_deltas(project_id,cursor,kind,key,operation,idempotency_key,payload_json,committed_at) VALUES(?,?,?,?,?,?,?,?)`,
		params.ProjectID, cursor, params.Kind, params.Key, params.Operation, params.IdempotencyKey, string(payload), now.Format(time.RFC3339Nano)); err != nil {
		return domain.ProjectionDelta{}, fmt.Errorf("append projection delta: %w", err)
	}
	delta := domain.ProjectionDelta{ProjectID: params.ProjectID, Cursor: cursor, Kind: params.Kind, Key: params.Key, Operation: params.Operation, IdempotencyKey: params.IdempotencyKey, Payload: payload, CommittedAt: now}
	delta.Source = domain.ProjectionSourceForDelta(delta)
	return delta, nil
}

func (c *Client) ListProjectionDeltas(ctx context.Context, projectID string, after uint64, limit int) ([]domain.ProjectionDelta, uint64, error) {
	if c.projectionDeltaReadHook != nil {
		c.projectionDeltaReadHook()
	}
	db, err := c.projectionReadDBHandle()
	if err != nil {
		return nil, 0, err
	}
	projectID = normalizeProjectionProjectID(projectID)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, c.projectionReadError("begin projection delta read", err)
	}
	defer tx.Rollback()
	head, err := projectionHead(ctx, tx, projectID)
	if err != nil {
		return nil, 0, c.projectionReadError("read projection delta head", err)
	}
	if after > head {
		return nil, head, &domain.ProjectionGapError{ProjectID: projectID, Expected: head, Actual: after}
	}
	if limit <= 0 {
		limit = defaultProjectionDeltaLimit
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := tx.QueryContext(ctx, `SELECT project_id,cursor,kind,key,operation,idempotency_key,payload_json,committed_at FROM projection_deltas WHERE project_id=? AND cursor>? ORDER BY cursor LIMIT ?`, projectID, after, limit)
	if err != nil {
		return nil, head, c.projectionReadError("list projection deltas", err)
	}
	defer rows.Close()
	deltas := make([]domain.ProjectionDelta, 0, min(limit, 64))
	for rows.Next() {
		delta, err := scanProjectionDelta(rows)
		if err != nil {
			return nil, head, err
		}
		deltas = append(deltas, delta)
	}
	if err := rows.Err(); err != nil {
		return nil, head, c.projectionReadError("iterate projection deltas", err)
	}
	if err := rows.Close(); err != nil {
		return nil, head, c.projectionReadError("close projection delta rows", err)
	}
	if after < head {
		if len(deltas) == 0 {
			return nil, head, &domain.ProjectionGapError{ProjectID: projectID, Expected: after + 1, Actual: head + 1}
		}
		expected := after + 1
		for _, delta := range deltas {
			if delta.Cursor != expected {
				return nil, head, &domain.ProjectionGapError{ProjectID: projectID, Expected: expected, Actual: delta.Cursor}
			}
			expected++
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, head, c.projectionReadError("commit projection delta read", err)
	}
	return deltas, head, nil
}

// WatchProjectionDeltas blocks until at least one delta follows after. It only
// performs read queries; context termination is returned as a typed error.
func (c *Client) WatchProjectionDeltas(ctx context.Context, projectID string, after uint64, limit int) ([]domain.ProjectionDelta, uint64, error) {
	deltas, head, err := c.ListProjectionDeltas(ctx, projectID, after, limit)
	if err != nil || len(deltas) > 0 {
		return deltas, head, projectionWatchError(err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, head, fmt.Errorf("create projection cursor watcher: %w: %w", err, domain.ErrProjectionRetryable)
	}
	c.projectionWatchStarted.Add(1)
	c.projectionWatchActive.Add(1)
	defer func() {
		closeProjectionDeltaWatcher(watcher)
		c.projectionWatchActive.Add(-1)
		c.projectionWatchCompleted.Add(1)
	}()
	if err := watcher.Add(filepath.Dir(c.dbPath)); err != nil {
		return nil, head, fmt.Errorf("watch projection cursor directory: %w: %w", err, domain.ErrProjectionRetryable)
	}
	// Re-read after registration to close the commit-before-watch race. From
	// here onward SQLite WAL/main-file notifications wake the reader without a
	// periodic query loop, including commits from another daemon process.
	deltas, head, err = c.ListProjectionDeltas(ctx, projectID, after, limit)
	if err != nil || len(deltas) > 0 {
		return deltas, head, projectionWatchError(err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil, head, &domain.ProjectionCanceledError{Cause: ctx.Err()}
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return nil, head, fmt.Errorf("projection cursor watcher closed: %w", domain.ErrProjectionRetryable)
			}
			return nil, head, fmt.Errorf("projection cursor watcher failed: %w: %w", watchErr, domain.ErrProjectionRetryable)
		case event, ok := <-watcher.Events:
			if !ok {
				return nil, head, fmt.Errorf("projection cursor watcher closed: %w", domain.ErrProjectionRetryable)
			}
			if !projectionDBEvent(c.dbPath, event.Name) {
				continue
			}
			deltas, head, err = c.ListProjectionDeltas(ctx, projectID, after, limit)
			if err != nil || len(deltas) > 0 {
				return deltas, head, projectionWatchError(err)
			}
		}
	}
}

// closeProjectionDeltaWatcher waits for the backend read loop to finish so a
// completed watch owns no asynchronous operating-system resources when it
// returns to its caller.
func closeProjectionDeltaWatcher(watcher *fsnotify.Watcher) {
	_ = watcher.Close()
	for range watcher.Errors {
	}
}

func projectionDBEvent(dbPath, eventPath string) bool {
	dbPath, eventPath = filepath.Clean(dbPath), filepath.Clean(eventPath)
	return eventPath == dbPath || eventPath == dbPath+"-wal" || eventPath == dbPath+"-shm"
}

func projectionWatchError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &domain.ProjectionCanceledError{Cause: err}
	}
	return err
}

// ProjectionSnapshotAt returns the keyed materialization exactly as of cursor.
// It is a pure read transaction and never advances authority or consumer state.
func (c *Client) ProjectionSnapshotAt(ctx context.Context, projectID string, cursor uint64) (domain.ProjectionSnapshot, error) {
	db, err := c.projectionReadDBHandle()
	if err != nil {
		return domain.ProjectionSnapshot{}, err
	}
	projectID = normalizeProjectionProjectID(projectID)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ProjectionSnapshot{}, c.projectionReadError("begin projection snapshot", err)
	}
	defer tx.Rollback()
	head, err := projectionHead(ctx, tx, projectID)
	if err != nil {
		return domain.ProjectionSnapshot{}, c.projectionReadError("read projection snapshot head", err)
	}
	if cursor > head {
		return domain.ProjectionSnapshot{}, &domain.ProjectionGapError{ProjectID: projectID, Expected: head, Actual: cursor}
	}
	var materializedCount uint64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projection_deltas WHERE project_id=? AND cursor<=?`, projectID, cursor).Scan(&materializedCount); err != nil {
		return domain.ProjectionSnapshot{}, c.projectionReadError("validate projection snapshot cursor", err)
	}
	if materializedCount != cursor {
		return domain.ProjectionSnapshot{}, &domain.ProjectionGapError{ProjectID: projectID, Expected: cursor, Actual: materializedCount}
	}
	rows, err := tx.QueryContext(ctx, `WITH ranked AS (
		SELECT kind,key,operation,payload_json,ROW_NUMBER() OVER (PARTITION BY kind,key ORDER BY cursor DESC) AS rank
		FROM projection_deltas WHERE project_id=? AND cursor<=? AND kind<>?
	) SELECT kind,key,payload_json FROM ranked WHERE rank=1 AND operation='upsert' ORDER BY kind,key`, projectID, cursor, domain.ProjectionKindSourceAdvance)
	if err != nil {
		return domain.ProjectionSnapshot{}, c.projectionReadError("read projection snapshot", err)
	}
	defer rows.Close()
	values := []domain.ProjectionValue{}
	for rows.Next() {
		var value domain.ProjectionValue
		var payload string
		if err := rows.Scan(&value.Kind, &value.Key, &payload); err != nil {
			return domain.ProjectionSnapshot{}, c.projectionReadError("scan projection snapshot", err)
		}
		value.Payload = json.RawMessage(payload)
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.ProjectionSnapshot{}, c.projectionReadError("iterate projection snapshot", err)
	}
	if err := rows.Close(); err != nil {
		return domain.ProjectionSnapshot{}, c.projectionReadError("close projection snapshot values", err)
	}
	sourceRows, err := tx.QueryContext(ctx, `SELECT project_id,cursor,kind,key,operation,idempotency_key,payload_json,committed_at FROM projection_deltas WHERE project_id=? AND cursor<=? ORDER BY cursor`, projectID, cursor)
	if err != nil {
		return domain.ProjectionSnapshot{}, c.projectionReadError("read projection snapshot sources", err)
	}
	var sourceReader projectionDeltaRows = sourceRows
	if c.projectionSnapshotSourceRowsHook != nil {
		sourceReader = c.projectionSnapshotSourceRowsHook(sourceReader)
	}
	defer sourceReader.Close()
	var sourceDeltas []domain.ProjectionDelta
	for sourceReader.Next() {
		delta, err := scanProjectionDelta(sourceReader)
		if err != nil {
			sourceReader.Close()
			return domain.ProjectionSnapshot{}, c.projectionReadError("scan projection snapshot source", err)
		}
		sourceDeltas = append(sourceDeltas, delta)
	}
	if err := sourceReader.Err(); err != nil {
		return domain.ProjectionSnapshot{}, c.projectionReadError("iterate projection snapshot sources", err)
	}
	if err := sourceReader.Close(); err != nil {
		return domain.ProjectionSnapshot{}, c.projectionReadError("close projection snapshot sources", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.ProjectionSnapshot{}, c.projectionReadError("commit projection snapshot read", err)
	}
	return domain.ProjectionSnapshot{ProjectID: projectID, Cursor: cursor, Head: head, Values: values, Sources: domain.MergeProjectionSourceRanges(sourceDeltas)}, nil
}

func (c *Client) ProjectionConsumerCursor(ctx context.Context, projectID, consumer string) (uint64, error) {
	db, err := c.projectionReadDBHandle()
	if err != nil {
		return 0, err
	}
	projectID, consumer = normalizeProjectionProjectID(projectID), strings.TrimSpace(consumer)
	if consumer == "" {
		return 0, errors.New("projection consumer is required")
	}
	var cursor uint64
	err = db.QueryRowContext(ctx, `SELECT cursor FROM projection_consumer_cursors WHERE project_id=? AND consumer=?`, projectID, consumer).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, c.projectionReadError("read projection consumer cursor", err)
	}
	return cursor, nil
}

// OpenProjectionDeltaStore is the explicit initialization boundary for delta
// readers. Read methods never call migrations, normalization, repair, or seed
// paths; callers must open the project store during startup or through a write.
func (c *Client) OpenProjectionDeltaStore() error {
	_, err := c.dbHandle()
	return err
}

func (c *Client) projectionReadDBHandle() (*sql.DB, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.corruptionError(); err != nil {
		return nil, fmt.Errorf("projection delta store quarantined: %w", err)
	}
	if c.db == nil {
		return nil, fmt.Errorf("projection delta store is not open: %w", domain.ErrProjectionRetryable)
	}
	return c.db, nil
}

// AdvanceProjectionConsumerCursor records exactly-once consumption after
// proving every cursor in the requested interval exists.
func (c *Client) AdvanceProjectionConsumerCursor(ctx context.Context, projectID, consumer string, expected, next uint64) error {
	projectID, consumer = normalizeProjectionProjectID(projectID), strings.TrimSpace(consumer)
	if consumer == "" {
		return errors.New("projection consumer is required")
	}
	if next < expected {
		return &domain.ProjectionGapError{ProjectID: projectID, Expected: expected, Actual: next}
	}
	return c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `INSERT INTO projection_streams(project_id,head_cursor,updated_at) VALUES(?,0,?) ON CONFLICT(project_id) DO NOTHING`, projectID, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO projection_consumer_cursors(project_id,consumer,cursor,updated_at) VALUES(?,?,0,?) ON CONFLICT(project_id,consumer) DO NOTHING`, projectID, consumer, now); err != nil {
				return err
			}
			var current, head uint64
			if err := tx.QueryRowContext(ctx, `SELECT c.cursor,s.head_cursor FROM projection_consumer_cursors c JOIN projection_streams s USING(project_id) WHERE c.project_id=? AND c.consumer=?`, projectID, consumer).Scan(&current, &head); err != nil {
				return err
			}
			if current != expected {
				return &domain.ProjectionGapError{ProjectID: projectID, Expected: current, Actual: expected}
			}
			if next > head {
				return &domain.ProjectionGapError{ProjectID: projectID, Expected: head, Actual: next}
			}
			var count uint64
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projection_deltas WHERE project_id=? AND cursor>? AND cursor<=?`, projectID, expected, next).Scan(&count); err != nil {
				return err
			}
			if count != next-expected {
				return &domain.ProjectionGapError{ProjectID: projectID, Expected: next - expected, Actual: count}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE projection_consumer_cursors SET cursor=?,updated_at=? WHERE project_id=? AND consumer=? AND cursor=?`, next, now, projectID, consumer, expected); err != nil {
				return err
			}
			return tx.Commit()
		})
	})
}

func normalizeProjectionDeltaParams(params ProjectionDeltaParams) (ProjectionDeltaParams, error) {
	params.ProjectID = normalizeProjectionProjectID(params.ProjectID)
	params.Kind = domain.ProjectionKind(strings.TrimSpace(string(params.Kind)))
	params.Key, params.IdempotencyKey = strings.TrimSpace(params.Key), strings.TrimSpace(params.IdempotencyKey)
	if params.Kind == "" || params.Key == "" || params.IdempotencyKey == "" {
		return ProjectionDeltaParams{}, errors.New("projection kind, key, and idempotency key are required")
	}
	if !params.Operation.Valid() {
		return ProjectionDeltaParams{}, fmt.Errorf("invalid projection operation %q", params.Operation)
	}
	params.Payload = normalizedProjectionPayload(params.Payload)
	if !json.Valid(params.Payload) {
		return ProjectionDeltaParams{}, errors.New("projection payload must be valid JSON")
	}
	if params.CommittedAt.IsZero() {
		params.CommittedAt = time.Now().UTC()
	}
	return params, nil
}

func normalizeProjectionProjectID(projectID string) string {
	if projectID = strings.TrimSpace(projectID); projectID != "" {
		return projectID
	}
	return "default"
}

func normalizedProjectionPayload(payload json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(payload)) == 0 {
		return json.RawMessage(`{}`)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		return append(json.RawMessage(nil), payload...)
	}
	return append(json.RawMessage(nil), compact.Bytes()...)
}

func projectionHead(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID string) (uint64, error) {
	var head uint64
	err := q.QueryRowContext(ctx, `SELECT head_cursor FROM projection_streams WHERE project_id=?`, projectID).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return head, err
}

type projectionDeltaScanner interface{ Scan(...any) error }

type projectionDeltaRows interface {
	projectionDeltaScanner
	Next() bool
	Err() error
	Close() error
}

func scanProjectionDelta(scanner projectionDeltaScanner) (domain.ProjectionDelta, error) {
	var delta domain.ProjectionDelta
	var operation, payload, committedAt string
	if err := scanner.Scan(&delta.ProjectID, &delta.Cursor, &delta.Kind, &delta.Key, &operation, &delta.IdempotencyKey, &payload, &committedAt); err != nil {
		return delta, err
	}
	delta.Operation = domain.ProjectionDeltaOperation(operation)
	delta.CommittedAt = parseTimestamp(committedAt)
	delta.Payload = json.RawMessage(payload)
	delta.Source = domain.ProjectionSourceForDelta(delta)
	return delta, nil
}

func findProjectionDeltaByIdempotencyKey(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, key string) (domain.ProjectionDelta, bool, error) {
	delta, err := scanProjectionDelta(q.QueryRowContext(ctx, `SELECT project_id,cursor,kind,key,operation,idempotency_key,payload_json,committed_at FROM projection_deltas WHERE project_id=? AND idempotency_key=?`, projectID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectionDelta{}, false, nil
	}
	return delta, err == nil, err
}

func projectionDeltaMatches(delta domain.ProjectionDelta, params ProjectionDeltaParams) bool {
	return delta.Kind == params.Kind && delta.Key == params.Key && delta.Operation == params.Operation && bytes.Equal(bytes.TrimSpace(delta.Payload), bytes.TrimSpace(normalizedProjectionPayload(params.Payload)))
}

func (c *Client) projectionReadError(op string, err error) error {
	wrapped := sqliteutil.WrapError(c.dbPath, op, err)
	if IsSQLiteCorrupt(err) {
		return c.markSQLiteCorrupt(wrapped)
	}
	if IsSQLiteBusy(err) || sqliteutil.IsIOErrorShortRead(err) {
		return &domain.ProjectionRetryableError{Cause: wrapped}
	}
	return wrapped
}
