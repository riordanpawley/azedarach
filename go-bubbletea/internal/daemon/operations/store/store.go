package store

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

var (
	ErrNotFound          = errors.New("operation not found")
	ErrConflict          = errors.New("operation conflict")
	ErrInvalidTransition = errors.New("invalid operation state transition")
)

type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateDone      State = "done"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

type Record struct {
	OperationID  string
	ProjectID    string
	IssueID      string
	Kind         string
	DedupeKey    string
	ResourceKeys []string
	State        State
	SubmittedAt  time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	ResultJSON   json.RawMessage
	ErrorJSON    json.RawMessage
	UpdatedAt    time.Time
}

type CreateParams struct {
	OperationID  string
	ProjectID    string
	IssueID      string
	Kind         string
	DedupeKey    string
	ResourceKeys []string
	State        State
	SubmittedAt  time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	ResultJSON   json.RawMessage
	ErrorJSON    json.RawMessage
}

type Query struct {
	OperationID string
	ProjectID   string
	IssueID     string
	DedupeKey   string
	Kind        string
	States      []State
	Limit       int
}

type TransitionParams struct {
	OperationID string
	ToState     State
	StartedAt   *time.Time
	FinishedAt  *time.Time
	ResultJSON  json.RawMessage
	ErrorJSON   json.RawMessage
}

type Repository interface {
	Create(context.Context, CreateParams) (Record, error)
	Get(context.Context, string) (Record, error)
	List(context.Context, Query) ([]Record, error)
	Transition(context.Context, TransitionParams) (Record, error)
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
			logger.Warn("failed to resolve daemon operation database path", "repoDir", repoDir, "error", err)
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

func (s *SQLiteStore) Create(ctx context.Context, params CreateParams) (Record, error) {
	db, err := s.dbHandle()
	if err != nil {
		return Record{}, err
	}
	record, err := normalizeCreateParams(params)
	if err != nil {
		return Record{}, err
	}
	resourceKeysJSON, err := marshalResourceKeys(record.ResourceKeys)
	if err != nil {
		return Record{}, err
	}
	_, err = db.ExecContext(ctx, `
        INSERT INTO daemon_operations (
            operation_id,
            project_id,
            issue_id,
            kind,
            dedupe_key,
            resource_keys_json,
            state,
            submitted_at,
            started_at,
            finished_at,
            result_json,
            error_json,
            updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		record.OperationID,
		record.ProjectID,
		record.IssueID,
		record.Kind,
		nullableString(record.DedupeKey),
		resourceKeysJSON,
		string(record.State),
		record.SubmittedAt.UTC().Format(time.RFC3339Nano),
		nullableTime(record.StartedAt),
		nullableTime(record.FinishedAt),
		nullableJSON(record.ResultJSON),
		nullableJSON(record.ErrorJSON),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Record{}, fmt.Errorf("create operation %s: %w", record.OperationID, ErrConflict)
		}
		return Record{}, fmt.Errorf("create operation %s: %w", record.OperationID, err)
	}
	return record, nil
}

func (s *SQLiteStore) Get(ctx context.Context, operationID string) (Record, error) {
	db, err := s.dbHandle()
	if err != nil {
		return Record{}, err
	}
	row := db.QueryRowContext(ctx, `
        SELECT
            operation_id,
            project_id,
            issue_id,
            kind,
            COALESCE(dedupe_key, ''),
            resource_keys_json,
            state,
            submitted_at,
            started_at,
            finished_at,
            result_json,
            error_json,
            updated_at
        FROM daemon_operations
        WHERE operation_id = ?
    `, strings.TrimSpace(operationID))
	record, err := scanRecord(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, fmt.Errorf("get operation %s: %w", operationID, ErrNotFound)
		}
		return Record{}, fmt.Errorf("get operation %s: %w", operationID, err)
	}
	return record, nil
}

func (s *SQLiteStore) List(ctx context.Context, query Query) ([]Record, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	builder := strings.Builder{}
	builder.WriteString(`
        SELECT
            operation_id,
            project_id,
            issue_id,
            kind,
            COALESCE(dedupe_key, ''),
            resource_keys_json,
            state,
            submitted_at,
            started_at,
            finished_at,
            result_json,
            error_json,
            updated_at
        FROM daemon_operations
    `)
	var where []string
	args := make([]any, 0, 8)
	if id := strings.TrimSpace(query.OperationID); id != "" {
		where = append(where, "operation_id = ?")
		args = append(args, id)
	}
	if projectID := strings.TrimSpace(query.ProjectID); projectID != "" {
		where = append(where, "project_id = ?")
		args = append(args, projectID)
	}
	if issueID := strings.TrimSpace(query.IssueID); issueID != "" {
		where = append(where, "issue_id = ?")
		args = append(args, issueID)
	}
	if dedupeKey := strings.TrimSpace(query.DedupeKey); dedupeKey != "" {
		where = append(where, "dedupe_key = ?")
		args = append(args, dedupeKey)
	}
	if kind := strings.TrimSpace(query.Kind); kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
	}
	if len(query.States) > 0 {
		placeholders := make([]string, 0, len(query.States))
		for _, state := range query.States {
			placeholders = append(placeholders, "?")
			args = append(args, string(state))
		}
		where = append(where, "state IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(where) > 0 {
		builder.WriteString(" WHERE ")
		builder.WriteString(strings.Join(where, " AND "))
	}
	builder.WriteString(" ORDER BY updated_at DESC, operation_id ASC")
	if query.Limit > 0 {
		builder.WriteString(" LIMIT ?")
		args = append(args, query.Limit)
	}
	rows, err := db.QueryContext(ctx, builder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list operations: %w", err)
	}
	defer rows.Close()

	records := make([]Record, 0)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("list operations: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list operations: %w", err)
	}
	return records, nil
}

func (s *SQLiteStore) Transition(ctx context.Context, params TransitionParams) (Record, error) {
	db, err := s.dbHandle()
	if err != nil {
		return Record{}, err
	}
	operationID := strings.TrimSpace(params.OperationID)
	if operationID == "" {
		return Record{}, fmt.Errorf("transition operation: missing operation id")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("transition operation %s: %w", operationID, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	current, err := scanRecord(tx.QueryRowContext(ctx, `
        SELECT
            operation_id,
            project_id,
            issue_id,
            kind,
            COALESCE(dedupe_key, ''),
            resource_keys_json,
            state,
            submitted_at,
            started_at,
            finished_at,
            result_json,
            error_json,
            updated_at
        FROM daemon_operations
        WHERE operation_id = ?
    `, operationID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, fmt.Errorf("transition operation %s: %w", operationID, ErrNotFound)
		}
		return Record{}, fmt.Errorf("transition operation %s: %w", operationID, err)
	}
	if err := ValidateTransition(current.State, params.ToState); err != nil {
		return Record{}, fmt.Errorf("transition operation %s: %w", operationID, err)
	}
	next := current
	next.State = params.ToState
	now := time.Now().UTC()
	next.UpdatedAt = now
	if params.StartedAt != nil {
		startedAt := params.StartedAt.UTC()
		next.StartedAt = &startedAt
	} else if params.ToState == StateRunning && next.StartedAt == nil {
		next.StartedAt = &now
	}
	if params.FinishedAt != nil {
		finishedAt := params.FinishedAt.UTC()
		next.FinishedAt = &finishedAt
	} else if isTerminalState(params.ToState) {
		next.FinishedAt = &now
	}
	if params.ResultJSON != nil {
		next.ResultJSON = cloneJSON(params.ResultJSON)
	}
	if params.ErrorJSON != nil {
		next.ErrorJSON = cloneJSON(params.ErrorJSON)
	}

	_, err = tx.ExecContext(ctx, `
        UPDATE daemon_operations
        SET state = ?,
            started_at = ?,
            finished_at = ?,
            result_json = ?,
            error_json = ?,
            updated_at = ?
        WHERE operation_id = ?
    `,
		string(next.State),
		nullableTime(next.StartedAt),
		nullableTime(next.FinishedAt),
		nullableJSON(next.ResultJSON),
		nullableJSON(next.ErrorJSON),
		next.UpdatedAt.UTC().Format(time.RFC3339Nano),
		next.OperationID,
	)
	if err != nil {
		return Record{}, fmt.Errorf("transition operation %s: %w", operationID, err)
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("transition operation %s: %w", operationID, err)
	}
	tx = nil
	return next, nil
}

func ValidateTransition(from, to State) error {
	if from == to {
		return nil
	}
	allowed, ok := allowedTransitions[from]
	if !ok {
		return fmt.Errorf("%w: unknown current state %q", ErrInvalidTransition, from)
	}
	if _, ok := allowed[to]; !ok {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	return nil
}

var allowedTransitions = map[State]map[State]struct{}{
	StateQueued: {
		StateRunning:   {},
		StateCancelled: {},
		StateFailed:    {},
	},
	StateRunning: {
		StateDone:      {},
		StateFailed:    {},
		StateCancelled: {},
	},
	StateDone:      {},
	StateFailed:    {},
	StateCancelled: {},
}

func isTerminalState(state State) bool {
	switch state {
	case StateDone, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

func normalizeCreateParams(params CreateParams) (Record, error) {
	submittedAt := params.SubmittedAt.UTC()
	if submittedAt.IsZero() {
		submittedAt = time.Now().UTC()
	}
	state := params.State
	if state == "" {
		state = StateQueued
	}
	if state != StateQueued && state != StateRunning && state != StateDone && state != StateFailed && state != StateCancelled {
		return Record{}, fmt.Errorf("create operation %s: unsupported state %q", params.OperationID, state)
	}
	operationID := strings.TrimSpace(params.OperationID)
	if operationID == "" {
		return Record{}, fmt.Errorf("create operation: missing operation id")
	}
	projectID := strings.TrimSpace(params.ProjectID)
	if projectID == "" {
		return Record{}, fmt.Errorf("create operation %s: missing project id", operationID)
	}
	issueID := strings.TrimSpace(params.IssueID)
	if issueID == "" {
		return Record{}, fmt.Errorf("create operation %s: missing issue id", operationID)
	}
	kind := strings.TrimSpace(params.Kind)
	if kind == "" {
		return Record{}, fmt.Errorf("create operation %s: missing kind", operationID)
	}
	resourceKeys := make([]string, 0, len(params.ResourceKeys))
	for _, key := range params.ResourceKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		resourceKeys = append(resourceKeys, key)
	}
	if len(resourceKeys) == 0 {
		return Record{}, fmt.Errorf("create operation %s: missing resource keys", operationID)
	}
	updatedAt := submittedAt
	record := Record{
		OperationID:  operationID,
		ProjectID:    projectID,
		IssueID:      issueID,
		Kind:         kind,
		DedupeKey:    strings.TrimSpace(params.DedupeKey),
		ResourceKeys: resourceKeys,
		State:        state,
		SubmittedAt:  submittedAt,
		ResultJSON:   cloneJSON(params.ResultJSON),
		ErrorJSON:    cloneJSON(params.ErrorJSON),
		UpdatedAt:    updatedAt,
	}
	if params.StartedAt != nil {
		startedAt := params.StartedAt.UTC()
		record.StartedAt = &startedAt
		record.UpdatedAt = startedAt
	}
	if params.FinishedAt != nil {
		finishedAt := params.FinishedAt.UTC()
		record.FinishedAt = &finishedAt
		record.UpdatedAt = finishedAt
	}
	return record, nil
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
		return nil, fmt.Errorf("open operation db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open operation db: %w", err)
	}
	if err := configureSQLite(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open operation db: %w", err)
	}
	if err := runMigrations(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open operation db: %w", err)
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
	return "", fmt.Errorf("resolve operation db path from %s: %w", absStart, err)
}

func marshalResourceKeys(keys []string) (string, error) {
	encoded, err := json.Marshal(keys)
	if err != nil {
		return "", fmt.Errorf("marshal resource keys: %w", err)
	}
	return string(encoded), nil
}

func scanRecord(scanner interface{ Scan(...any) error }) (Record, error) {
	var (
		record           Record
		resourceKeysJSON string
		stateRaw         string
		submittedAtRaw   string
		startedAtRaw     sql.NullString
		finishedAtRaw    sql.NullString
		resultJSONRaw    sql.NullString
		errorJSONRaw     sql.NullString
		updatedAtRaw     string
	)
	if err := scanner.Scan(
		&record.OperationID,
		&record.ProjectID,
		&record.IssueID,
		&record.Kind,
		&record.DedupeKey,
		&resourceKeysJSON,
		&stateRaw,
		&submittedAtRaw,
		&startedAtRaw,
		&finishedAtRaw,
		&resultJSONRaw,
		&errorJSONRaw,
		&updatedAtRaw,
	); err != nil {
		return Record{}, err
	}
	record.State = State(stateRaw)
	if err := json.Unmarshal([]byte(resourceKeysJSON), &record.ResourceKeys); err != nil {
		return Record{}, fmt.Errorf("decode resource keys: %w", err)
	}
	submittedAt, err := time.Parse(time.RFC3339Nano, submittedAtRaw)
	if err != nil {
		return Record{}, fmt.Errorf("parse submitted_at: %w", err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, updatedAtRaw)
	if err != nil {
		return Record{}, fmt.Errorf("parse updated_at: %w", err)
	}
	record.SubmittedAt = submittedAt.UTC()
	record.UpdatedAt = updatedAt.UTC()
	if startedAtRaw.Valid && strings.TrimSpace(startedAtRaw.String) != "" {
		startedAt, err := time.Parse(time.RFC3339Nano, startedAtRaw.String)
		if err != nil {
			return Record{}, fmt.Errorf("parse started_at: %w", err)
		}
		startedAt = startedAt.UTC()
		record.StartedAt = &startedAt
	}
	if finishedAtRaw.Valid && strings.TrimSpace(finishedAtRaw.String) != "" {
		finishedAt, err := time.Parse(time.RFC3339Nano, finishedAtRaw.String)
		if err != nil {
			return Record{}, fmt.Errorf("parse finished_at: %w", err)
		}
		finishedAt = finishedAt.UTC()
		record.FinishedAt = &finishedAt
	}
	if resultJSONRaw.Valid && strings.TrimSpace(resultJSONRaw.String) != "" {
		record.ResultJSON = json.RawMessage(resultJSONRaw.String)
	}
	if errorJSONRaw.Valid && strings.TrimSpace(errorJSONRaw.String) != "" {
		record.ErrorJSON = json.RawMessage(errorJSONRaw.String)
	}
	return record, nil
}

func nullableString(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	copied := make([]byte, len(value))
	copy(copied, value)
	return json.RawMessage(copied)
}
