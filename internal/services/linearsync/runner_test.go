package linearsync

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

type retryableTestError struct {
	msg string
}

func (e retryableTestError) Error() string {
	return e.msg
}

func (e retryableTestError) Retryable() bool {
	return true
}

type captureHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

type capturedRecord struct {
	level   slog.Level
	message string
	attrs   map[string]any
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	r.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.Any()
		return true
	})

	h.mu.Lock()
	h.records = append(h.records, capturedRecord{
		level:   r.Level,
		message: r.Message,
		attrs:   attrs,
	})
	h.mu.Unlock()

	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureHandler) WithGroup(string) slog.Handler {
	return h
}

func (h *captureHandler) Records() []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]capturedRecord(nil), h.records...)
}

func attrString(t *testing.T, attrs map[string]any, key string) string {
	t.Helper()
	value, ok := attrs[key]
	if !ok {
		t.Fatalf("missing attr %q", key)
	}
	return slogValueString(value)
}

func attrInt(t *testing.T, attrs map[string]any, key string) int {
	t.Helper()
	value, ok := attrs[key]
	if !ok {
		t.Fatalf("missing attr %q", key)
	}
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case uint64:
		return int(v)
	default:
		t.Fatalf("attr %q has unexpected type %T", key, value)
		return 0
	}
}

func slogValueString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case Operation:
		return string(v)
	default:
		return ""
	}
}

func TestRunnerFlushEmitsLifecycleLogs(t *testing.T) {
	handler := &captureHandler{}
	runner := NewRunnerWithMaxAttempts(slog.New(handler), 5)

	outcomes := runner.Flush(context.Background(), FlushOptions{
		RunID:       "run-1",
		ProjectPath: "/workspace/project",
	}, []DispatchItem{
		{
			IssueID:       "az-1",
			LinearIssueID: "lin-1",
			Operation:     OperationUpsert,
			Attempts:      0,
			Work: func(context.Context) error {
				return nil
			},
		},
		{
			IssueID:       "az-2",
			LinearIssueID: "lin-2",
			Operation:     OperationClose,
			Attempts:      0,
			Work: func(context.Context) error {
				return retryableTestError{msg: "temporary failure"}
			},
		},
	})

	if len(outcomes) != 2 {
		t.Fatalf("outcomes length = %d, want 2", len(outcomes))
	}
	if outcomes[0].Err != nil {
		t.Fatalf("first outcome error = %v, want nil", outcomes[0].Err)
	}
	if outcomes[1].Err == nil || !outcomes[1].Retried {
		t.Fatalf("second outcome = %+v, want retryable failure", outcomes[1])
	}

	records := handler.Records()
	if len(records) != 5 {
		t.Fatalf("record count = %d, want 5", len(records))
	}

	check := func(index int, wantMessage string) capturedRecord {
		t.Helper()
		record := records[index]
		if record.message != wantMessage {
			t.Fatalf("record[%d].message = %q, want %q", index, record.message, wantMessage)
		}
		return record
	}

	start := check(0, "Linear flush run start")
	if got := attrString(t, start.attrs, "run"); got != "run-1" {
		t.Fatalf("run = %q, want run-1", got)
	}
	if got := attrString(t, start.attrs, "project_path"); got != "/workspace/project" {
		t.Fatalf("project_path = %q, want /workspace/project", got)
	}
	if got := attrInt(t, start.attrs, "pending_items"); got != 2 {
		t.Fatalf("pending_items = %d, want 2", got)
	}

	firstStart := check(1, "Linear sync dispatch start")
	if got := attrString(t, firstStart.attrs, "issue_id"); got != "az-1" {
		t.Fatalf("issue_id = %q, want az-1", got)
	}
	if got := attrString(t, firstStart.attrs, "linear_issue_id"); got != "lin-1" {
		t.Fatalf("linear_issue_id = %q, want lin-1", got)
	}
	if got := attrString(t, firstStart.attrs, "operation"); got != string(OperationUpsert) {
		t.Fatalf("operation = %q, want upsert", got)
	}
	if got := attrInt(t, firstStart.attrs, "attempts"); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	if got := attrInt(t, firstStart.attrs, "max_attempts"); got != 5 {
		t.Fatalf("max_attempts = %d, want 5", got)
	}

	firstSuccess := check(2, "Linear sync dispatch success")
	if got := attrString(t, firstSuccess.attrs, "issue_id"); got != "az-1" {
		t.Fatalf("success issue_id = %q, want az-1", got)
	}

	secondStart := check(3, "Linear sync dispatch start")
	if got := attrString(t, secondStart.attrs, "issue_id"); got != "az-2" {
		t.Fatalf("second issue_id = %q, want az-2", got)
	}

	retry := check(4, "Linear sync dispatch retry scheduled")
	if got := attrString(t, retry.attrs, "issue_id"); got != "az-2" {
		t.Fatalf("retry issue_id = %q, want az-2", got)
	}
	if got := attrInt(t, retry.attrs, "delay_seconds"); got != 5 {
		t.Fatalf("delay_seconds = %d, want 5", got)
	}
	if got := attrString(t, retry.attrs, "reason"); got != "temporary failure" {
		t.Fatalf("reason = %q, want temporary failure", got)
	}
}

func TestRunnerFlushLogsTerminalFailure(t *testing.T) {
	handler := &captureHandler{}
	runner := NewRunnerWithMaxAttempts(slog.New(handler), 5)

	outcomes := runner.Flush(context.Background(), FlushOptions{
		RunID:       "run-2",
		ProjectPath: "/workspace/project",
	}, []DispatchItem{
		{
			IssueID:       "az-3",
			LinearIssueID: "lin-3",
			Operation:     OperationUpsert,
			Attempts:      4,
			Work: func(context.Context) error {
				return retryableTestError{msg: "still failing"}
			},
		},
	})

	if len(outcomes) != 1 {
		t.Fatalf("outcomes length = %d, want 1", len(outcomes))
	}
	if outcomes[0].Retried {
		t.Fatal("terminal failure outcome should not be retried")
	}

	records := handler.Records()
	if len(records) != 3 {
		t.Fatalf("record count = %d, want 3", len(records))
	}

	terminal := records[2]
	if terminal.message != "Linear sync dispatch terminal failure" {
		t.Fatalf("terminal message = %q, want terminal failure", terminal.message)
	}
	if got := attrString(t, terminal.attrs, "issue_id"); got != "az-3" {
		t.Fatalf("terminal issue_id = %q, want az-3", got)
	}
	if got := attrInt(t, terminal.attrs, "attempts"); got != 5 {
		t.Fatalf("terminal attempts = %d, want 5", got)
	}
	if got := attrInt(t, terminal.attrs, "max_attempts"); got != 5 {
		t.Fatalf("terminal max_attempts = %d, want 5", got)
	}
	if got := attrString(t, terminal.attrs, "reason"); got != "still failing" {
		t.Fatalf("terminal reason = %q, want still failing", got)
	}
}

func TestRunnerFlushLogsSkipWhenEmpty(t *testing.T) {
	handler := &captureHandler{}
	runner := NewRunner(slog.New(handler))

	outcomes := runner.Flush(context.Background(), FlushOptions{
		RunID:       "run-3",
		ProjectPath: "/workspace/project",
	}, nil)

	if len(outcomes) != 0 {
		t.Fatalf("outcomes length = %d, want 0", len(outcomes))
	}

	records := handler.Records()
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}

	skipped := records[1]
	if skipped.message != "Linear flush skipped" {
		t.Fatalf("skip message = %q, want skip", skipped.message)
	}
	if got := attrString(t, skipped.attrs, "reason"); got != "no_pending_items" {
		t.Fatalf("skip reason = %q, want no_pending_items", got)
	}
	if got := attrInt(t, skipped.attrs, "pending_items"); got != 0 {
		t.Fatalf("skip pending_items = %d, want 0", got)
	}
}
