package notices

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/config"
)

type Repository interface {
	UpsertActive(context.Context, Candidate) (Record, bool, error)
	Get(context.Context, string, string) (Record, error)
	List(context.Context, Query) ([]Record, error)
	Update(context.Context, UpdateParams) (Record, bool, error)
	ExpireDue(context.Context, ExpireQuery) ([]Record, error)
	DeleteExpired(context.Context, ExpireQuery) ([]Record, error)
	Close() error
}

type SQLiteStore struct {
	dbPath string
	logger *slog.Logger

	mu sync.Mutex
	db *sql.DB
}

func New(repoDir string, logger *slog.Logger) *SQLiteStore {
	dbPath, err := resolveDBPath(repoDir)
	if err != nil {
		if logger != nil {
			logger.Warn("failed to resolve daemon notice database path", "repoDir", repoDir, "error", err)
		}
		fallbackRoot := strings.TrimSpace(repoDir)
		if normalizedRoot, normalizeErr := config.ResolveProjectRoot(repoDir); normalizeErr == nil {
			fallbackRoot = normalizedRoot
		}
		if fallbackRoot == "" {
			fallbackRoot = "."
		}
		dbPath = filepath.Join(fallbackRoot, ".azedarach", "azedarach.db")
	}
	return NewAtPath(dbPath, logger)
}

func NewAtPath(dbPath string, logger *slog.Logger) *SQLiteStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &SQLiteStore{dbPath: dbPath, logger: logger}
}

func (s *SQLiteStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *SQLiteStore) UpsertActive(ctx context.Context, candidate Candidate) (Record, bool, error) {
	incoming, err := NormalizeCandidate(candidate)
	if err != nil {
		return Record{}, false, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Record{}, false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, false, fmt.Errorf("upsert notice %s: %w", incoming.NoticeID, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	if incoming.DedupeKey != "" {
		existing, err := scanRecord(tx.QueryRowContext(ctx, selectNoticeSQL+`
            WHERE project_id = ? AND dedupe_key = ? AND state = ?
            ORDER BY updated_at DESC, notice_id ASC
            LIMIT 1
        `, incoming.ProjectID, incoming.DedupeKey, StateActive))
		if err == nil {
			next := ApplyDedupe(existing, incoming)
			if err := updateRecord(ctx, tx, next); err != nil {
				return Record{}, false, fmt.Errorf("upsert notice %s: %w", next.NoticeID, err)
			}
			if err := tx.Commit(); err != nil {
				return Record{}, false, fmt.Errorf("upsert notice %s: %w", next.NoticeID, err)
			}
			tx = nil
			return next, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Record{}, false, fmt.Errorf("upsert notice %s: %w", incoming.NoticeID, err)
		}
	}
	if err := insertRecord(ctx, tx, incoming); err != nil {
		return Record{}, false, fmt.Errorf("create notice %s: %w", incoming.NoticeID, err)
	}
	if err := tx.Commit(); err != nil {
		return Record{}, false, fmt.Errorf("create notice %s: %w", incoming.NoticeID, err)
	}
	tx = nil
	return incoming, true, nil
}

func (s *SQLiteStore) Get(ctx context.Context, projectID, noticeID string) (Record, error) {
	db, err := s.dbHandle()
	if err != nil {
		return Record{}, err
	}
	projectID = strings.TrimSpace(projectID)
	noticeID = strings.TrimSpace(noticeID)
	record, err := scanRecord(db.QueryRowContext(ctx, selectNoticeSQL+`
        WHERE project_id = ? AND notice_id = ?
    `, projectID, noticeID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, fmt.Errorf("get notice %s: %w", noticeID, ErrNotFound)
		}
		return Record{}, fmt.Errorf("get notice %s: %w", noticeID, err)
	}
	return record, nil
}

func (s *SQLiteStore) List(ctx context.Context, query Query) ([]Record, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	builder := strings.Builder{}
	builder.WriteString(selectNoticeSQL)
	where := []string{"project_id = ?"}
	args := []any{strings.TrimSpace(query.ProjectID)}
	if len(query.States) > 0 {
		placeholders := make([]string, 0, len(query.States))
		for _, state := range query.States {
			placeholders = append(placeholders, "?")
			args = append(args, string(state))
		}
		where = append(where, "state IN ("+strings.Join(placeholders, ",")+")")
	}
	if query.Read != nil {
		where = append(where, "read = ?")
		args = append(args, boolToInt(*query.Read))
	}
	if query.Severity != "" {
		where = append(where, "severity = ?")
		args = append(args, string(query.Severity))
	}
	if category := strings.TrimSpace(query.Category); category != "" {
		where = append(where, "category = ?")
		args = append(args, category)
	}
	if scopeType := strings.TrimSpace(query.ScopeType); scopeType != "" {
		where = append(where, "scope_type = ?")
		args = append(args, scopeType)
	}
	if scopeID := strings.TrimSpace(query.ScopeID); scopeID != "" {
		where = append(where, "scope_id = ?")
		args = append(args, scopeID)
	}
	if operationID := strings.TrimSpace(query.OperationID); operationID != "" {
		where = append(where, "source_operation_id = ?")
		args = append(args, operationID)
	}
	if dedupeKey := strings.TrimSpace(query.DedupeKey); dedupeKey != "" {
		where = append(where, "dedupe_key = ?")
		args = append(args, dedupeKey)
	}
	if query.UpdatedAfter != nil {
		where = append(where, "updated_at > ?")
		args = append(args, query.UpdatedAfter.UTC().Format(time.RFC3339Nano))
	}
	builder.WriteString(" WHERE ")
	builder.WriteString(strings.Join(where, " AND "))
	builder.WriteString(" ORDER BY updated_at DESC, notice_id ASC")
	if query.Limit > 0 {
		builder.WriteString(" LIMIT ?")
		args = append(args, query.Limit)
	}
	rows, err := db.QueryContext(ctx, builder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list notices: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("list notices: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notices: %w", err)
	}
	return records, nil
}

func (s *SQLiteStore) Update(ctx context.Context, params UpdateParams) (Record, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return Record{}, false, err
	}
	projectID := strings.TrimSpace(params.ProjectID)
	noticeID := strings.TrimSpace(params.NoticeID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, false, fmt.Errorf("update notice %s: %w", noticeID, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	current, err := scanRecord(tx.QueryRowContext(ctx, selectNoticeSQL+`
        WHERE project_id = ? AND notice_id = ?
    `, projectID, noticeID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, false, fmt.Errorf("update notice %s: %w", noticeID, ErrNotFound)
		}
		return Record{}, false, fmt.Errorf("update notice %s: %w", noticeID, err)
	}
	next, changed, err := ApplyLifecycle(current, params)
	if err != nil {
		return Record{}, false, fmt.Errorf("update notice %s: %w", noticeID, err)
	}
	if changed {
		if err := updateRecord(ctx, tx, next); err != nil {
			return Record{}, false, fmt.Errorf("update notice %s: %w", noticeID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Record{}, false, fmt.Errorf("update notice %s: %w", noticeID, err)
	}
	tx = nil
	return next, changed, nil
}

func (s *SQLiteStore) ExpireDue(ctx context.Context, query ExpireQuery) ([]Record, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	now := query.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	projectID := strings.TrimSpace(query.ProjectID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("expire notices: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	builder := strings.Builder{}
	builder.WriteString(selectNoticeSQL)
	builder.WriteString(`
        WHERE project_id = ?
          AND state IN (?, ?)
          AND expires_at IS NOT NULL
          AND expires_at <= ?
        ORDER BY expires_at ASC, notice_id ASC
    `)
	args := []any{projectID, StateResolved, StateDismissed, now.Format(time.RFC3339Nano)}
	if query.Limit > 0 {
		builder.WriteString(" LIMIT ?")
		args = append(args, query.Limit)
	}
	rows, err := tx.QueryContext(ctx, builder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("expire notices: %w", err)
	}
	records, err := scanRecords(rows)
	if err != nil {
		return nil, fmt.Errorf("expire notices: %w", err)
	}
	for i := range records {
		records[i].State = StateExpired
		records[i].UpdatedAt = now
		if err := updateRecord(ctx, tx, records[i]); err != nil {
			return nil, fmt.Errorf("expire notice %s: %w", records[i].NoticeID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("expire notices: %w", err)
	}
	tx = nil
	return records, nil
}

func (s *SQLiteStore) DeleteExpired(ctx context.Context, query ExpireQuery) ([]Record, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	now := query.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	projectID := strings.TrimSpace(query.ProjectID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("delete expired notices: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	builder := strings.Builder{}
	builder.WriteString(selectNoticeSQL)
	builder.WriteString(`
        WHERE project_id = ?
          AND state = ?
          AND expires_at IS NOT NULL
          AND expires_at <= ?
        ORDER BY expires_at ASC, notice_id ASC
    `)
	args := []any{projectID, StateExpired, now.Format(time.RFC3339Nano)}
	if query.Limit > 0 {
		builder.WriteString(" LIMIT ?")
		args = append(args, query.Limit)
	}
	rows, err := tx.QueryContext(ctx, builder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("delete expired notices: %w", err)
	}
	records, err := scanRecords(rows)
	if err != nil {
		return nil, fmt.Errorf("delete expired notices: %w", err)
	}
	for _, record := range records {
		if _, err := tx.ExecContext(ctx, `
            DELETE FROM daemon_notices
            WHERE project_id = ? AND notice_id = ?
        `, record.ProjectID, record.NoticeID); err != nil {
			return nil, fmt.Errorf("delete expired notice %s: %w", record.NoticeID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("delete expired notices: %w", err)
	}
	tx = nil
	return records, nil
}

func (s *SQLiteStore) dbHandle() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db, nil
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_txlock=immediate", filepath.ToSlash(s.dbPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open notice db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open notice db: %w", err)
	}
	if err := configureSQLite(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open notice db: %w", err)
	}
	if err := runMigrations(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open notice db: %w", err)
	}
	s.db = db
	return db, nil
}

func configureSQLite(db *sql.DB) error {
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

func resolveDBPath(repoDir string) (string, error) {
	if fromEnv := strings.TrimSpace(os.Getenv("AZEDARACH_DB_PATH")); fromEnv != "" {
		return fromEnv, nil
	}
	startDir := strings.TrimSpace(repoDir)
	if startDir == "" {
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
		return filepath.Join(baseRoot, ".azedarach", "azedarach.db"), nil
	}
	return "", fmt.Errorf("resolve notice db path from %s: %w", absStart, err)
}

const selectNoticeSQL = `
    SELECT
        notice_id,
        project_id,
        scope_type,
        scope_id,
        source_json,
        source_operation_id,
        severity,
        category,
        state,
        read,
        title,
        summary,
        detail,
        cause_json,
        actions_json,
        COALESCE(dedupe_key, ''),
        occurrence_count,
        first_occurrence_at,
        last_occurrence_at,
        created_at,
        updated_at,
        resolved_at,
        dismissed_at,
        expires_at,
        retention_class
    FROM daemon_notices
`

func insertRecord(ctx context.Context, exec sqlExecer, record Record) error {
	sourceJSON, sourceOperationID, err := marshalSource(record.Source)
	if err != nil {
		return err
	}
	causeJSON, err := marshalOptionalJSON(record.Cause)
	if err != nil {
		return fmt.Errorf("marshal cause: %w", err)
	}
	actionsJSON, err := marshalActions(record.Actions)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `
        INSERT INTO daemon_notices (
            notice_id, project_id, scope_type, scope_id, source_json, source_operation_id,
            severity, category, state, read, title, summary, detail, cause_json, actions_json,
            dedupe_key, occurrence_count, first_occurrence_at, last_occurrence_at, created_at,
            updated_at, resolved_at, dismissed_at, expires_at, retention_class
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		record.NoticeID,
		record.ProjectID,
		record.Scope.Type,
		record.Scope.ID,
		nullableString(sourceJSON),
		sourceOperationID,
		string(record.Severity),
		record.Category,
		string(record.State),
		boolToInt(record.Read),
		record.Title,
		record.Summary,
		record.Detail,
		nullableString(causeJSON),
		actionsJSON,
		nullableString(record.DedupeKey),
		record.OccurrenceCount,
		formatTime(record.FirstOccurrenceAt),
		formatTime(record.LastOccurrenceAt),
		formatTime(record.CreatedAt),
		formatTime(record.UpdatedAt),
		nullableTime(record.ResolvedAt),
		nullableTime(record.DismissedAt),
		nullableTime(record.ExpiresAt),
		string(record.RetentionClass),
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return fmt.Errorf("%w: %s", ErrConflict, err)
		}
		return err
	}
	return nil
}

func updateRecord(ctx context.Context, exec sqlExecer, record Record) error {
	sourceJSON, sourceOperationID, err := marshalSource(record.Source)
	if err != nil {
		return err
	}
	causeJSON, err := marshalOptionalJSON(record.Cause)
	if err != nil {
		return fmt.Errorf("marshal cause: %w", err)
	}
	actionsJSON, err := marshalActions(record.Actions)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `
        UPDATE daemon_notices
        SET scope_type = ?,
            scope_id = ?,
            source_json = ?,
            source_operation_id = ?,
            severity = ?,
            category = ?,
            state = ?,
            read = ?,
            title = ?,
            summary = ?,
            detail = ?,
            cause_json = ?,
            actions_json = ?,
            dedupe_key = ?,
            occurrence_count = ?,
            first_occurrence_at = ?,
            last_occurrence_at = ?,
            updated_at = ?,
            resolved_at = ?,
            dismissed_at = ?,
            expires_at = ?,
            retention_class = ?
        WHERE project_id = ? AND notice_id = ?
    `,
		record.Scope.Type,
		record.Scope.ID,
		nullableString(sourceJSON),
		sourceOperationID,
		string(record.Severity),
		record.Category,
		string(record.State),
		boolToInt(record.Read),
		record.Title,
		record.Summary,
		record.Detail,
		nullableString(causeJSON),
		actionsJSON,
		nullableString(record.DedupeKey),
		record.OccurrenceCount,
		formatTime(record.FirstOccurrenceAt),
		formatTime(record.LastOccurrenceAt),
		formatTime(record.UpdatedAt),
		nullableTime(record.ResolvedAt),
		nullableTime(record.DismissedAt),
		nullableTime(record.ExpiresAt),
		string(record.RetentionClass),
		record.ProjectID,
		record.NoticeID,
	)
	return err
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func scanRecords(rows *sql.Rows) ([]Record, error) {
	defer rows.Close()
	records := make([]Record, 0)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func scanRecord(scanner interface{ Scan(...any) error }) (Record, error) {
	var (
		record             Record
		sourceJSONRaw      sql.NullString
		sourceOperationID  string
		severityRaw        string
		stateRaw           string
		readRaw            int
		causeJSONRaw       sql.NullString
		actionsJSONRaw     string
		firstOccurrenceRaw string
		lastOccurrenceRaw  string
		createdAtRaw       string
		updatedAtRaw       string
		resolvedAtRaw      sql.NullString
		dismissedAtRaw     sql.NullString
		expiresAtRaw       sql.NullString
		retentionRaw       string
	)
	if err := scanner.Scan(
		&record.NoticeID,
		&record.ProjectID,
		&record.Scope.Type,
		&record.Scope.ID,
		&sourceJSONRaw,
		&sourceOperationID,
		&severityRaw,
		&record.Category,
		&stateRaw,
		&readRaw,
		&record.Title,
		&record.Summary,
		&record.Detail,
		&causeJSONRaw,
		&actionsJSONRaw,
		&record.DedupeKey,
		&record.OccurrenceCount,
		&firstOccurrenceRaw,
		&lastOccurrenceRaw,
		&createdAtRaw,
		&updatedAtRaw,
		&resolvedAtRaw,
		&dismissedAtRaw,
		&expiresAtRaw,
		&retentionRaw,
	); err != nil {
		return Record{}, err
	}
	if sourceJSONRaw.Valid {
		var source Source
		if err := json.Unmarshal([]byte(sourceJSONRaw.String), &source); err != nil {
			return Record{}, fmt.Errorf("decode notice source: %w", err)
		}
		record.Source = &source
	}
	if causeJSONRaw.Valid {
		var cause Cause
		if err := json.Unmarshal([]byte(causeJSONRaw.String), &cause); err != nil {
			return Record{}, fmt.Errorf("decode notice cause: %w", err)
		}
		record.Cause = &cause
	}
	if strings.TrimSpace(actionsJSONRaw) != "" {
		if err := json.Unmarshal([]byte(actionsJSONRaw), &record.Actions); err != nil {
			return Record{}, fmt.Errorf("decode notice actions: %w", err)
		}
	}
	record.Severity = Severity(severityRaw)
	record.State = State(stateRaw)
	record.Read = readRaw != 0
	record.RetentionClass = RetentionClass(retentionRaw)
	var err error
	if record.FirstOccurrenceAt, err = parseTime(firstOccurrenceRaw); err != nil {
		return Record{}, fmt.Errorf("decode first_occurrence_at: %w", err)
	}
	if record.LastOccurrenceAt, err = parseTime(lastOccurrenceRaw); err != nil {
		return Record{}, fmt.Errorf("decode last_occurrence_at: %w", err)
	}
	if record.CreatedAt, err = parseTime(createdAtRaw); err != nil {
		return Record{}, fmt.Errorf("decode created_at: %w", err)
	}
	if record.UpdatedAt, err = parseTime(updatedAtRaw); err != nil {
		return Record{}, fmt.Errorf("decode updated_at: %w", err)
	}
	if record.ResolvedAt, err = parseNullableTime(resolvedAtRaw); err != nil {
		return Record{}, fmt.Errorf("decode resolved_at: %w", err)
	}
	if record.DismissedAt, err = parseNullableTime(dismissedAtRaw); err != nil {
		return Record{}, fmt.Errorf("decode dismissed_at: %w", err)
	}
	if record.ExpiresAt, err = parseNullableTime(expiresAtRaw); err != nil {
		return Record{}, fmt.Errorf("decode expires_at: %w", err)
	}
	return record, nil
}

func marshalSource(source *Source) (string, string, error) {
	if source == nil {
		return "", "", nil
	}
	data, err := json.Marshal(source)
	if err != nil {
		return "", "", fmt.Errorf("marshal source: %w", err)
	}
	return string(data), strings.TrimSpace(source.OperationID.String()), nil
}

func marshalOptionalJSON(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func marshalActions(actions []Action) (string, error) {
	if actions == nil {
		actions = []Action{}
	}
	data, err := json.Marshal(actions)
	if err != nil {
		return "", fmt.Errorf("marshal actions: %w", err)
	}
	return string(data), nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
