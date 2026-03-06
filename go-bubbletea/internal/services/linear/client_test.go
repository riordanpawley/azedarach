package linear

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner implements CommandRunner for testing
type mockRunner struct {
	output  []byte
	err     error
	outputs [][]byte
	errs    []error
	calls   int
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	callIdx := m.calls
	m.calls++

	if len(m.outputs) > 0 || len(m.errs) > 0 {
		var out []byte
		var err error
		if len(m.outputs) > 0 {
			idx := callIdx
			if idx >= len(m.outputs) {
				idx = len(m.outputs) - 1
			}
			out = m.outputs[idx]
		}
		if len(m.errs) > 0 {
			idx := callIdx
			if idx >= len(m.errs) {
				idx = len(m.errs) - 1
			}
			err = m.errs[idx]
		}
		return out, err
	}

	return m.output, m.err
}

func TestClient_List(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		runErr    error
		wantCount int
		wantErr   bool
	}{
		{
			name: "valid response with multiple tasks",
			output: `[
				{"id": "az-1", "title": "Task 1", "status": "open", "priority": 1, "type": "task", "created_at": "2025-01-01T00:00:00Z", "updated_at": "2025-01-01T00:00:00Z"},
				{"id": "az-2", "title": "Task 2", "status": "in_progress", "priority": 0, "type": "bug", "created_at": "2025-01-01T00:00:00Z", "updated_at": "2025-01-01T00:00:00Z"}
			]`,
			wantCount: 2,
		},
		{
			name:      "empty response",
			output:    `[]`,
			wantCount: 0,
		},
		{
			name:    "invalid json",
			output:  `not json`,
			wantErr: true,
		},
		{
			name:    "runner error",
			runErr:  errors.New("command failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{
				output: []byte(tt.output),
				err:    tt.runErr,
			}
			client := NewClient(runner, slog.Default())

			tasks, err := client.List(context.Background())

			if tt.wantErr {
				require.Error(t, err)
				var trackerErr *domain.IssueTrackerError
				assert.ErrorAs(t, err, &trackerErr)
				assert.Equal(t, "list", trackerErr.Op)
				return
			}

			require.NoError(t, err)
			assert.Len(t, tasks, tt.wantCount)
		})
	}
}

func TestClient_Search(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		output    string
		runErr    error
		wantCount int
		wantErr   bool
	}{
		{
			name:  "valid search results",
			query: "authentication",
			output: `[
				{"id": "az-5", "title": "Add auth", "status": "open", "priority": 1, "type": "feature", "created_at": "2025-01-01T00:00:00Z", "updated_at": "2025-01-01T00:00:00Z"}
			]`,
			wantCount: 1,
		},
		{
			name:      "no results",
			query:     "nonexistent",
			output:    `[]`,
			wantCount: 0,
		},
		{
			name:    "invalid json",
			query:   "test",
			output:  `invalid`,
			wantErr: true,
		},
		{
			name:    "runner error",
			query:   "test",
			runErr:  errors.New("search failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{
				output: []byte(tt.output),
				err:    tt.runErr,
			}
			client := NewClient(runner, slog.Default())

			tasks, err := client.Search(context.Background(), tt.query)

			if tt.wantErr {
				require.Error(t, err)
				var trackerErr *domain.IssueTrackerError
				assert.ErrorAs(t, err, &trackerErr)
				assert.Equal(t, "search", trackerErr.Op)
				return
			}

			require.NoError(t, err)
			assert.Len(t, tasks, tt.wantCount)
		})
	}
}

func TestClient_Ready(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		runErr    error
		wantCount int
		wantErr   bool
	}{
		{
			name: "ready tasks available",
			output: `[
				{"id": "az-3", "title": "Ready task", "status": "open", "priority": 0, "type": "task", "created_at": "2025-01-01T00:00:00Z", "updated_at": "2025-01-01T00:00:00Z"}
			]`,
			wantCount: 1,
		},
		{
			name:      "no ready tasks",
			output:    `[]`,
			wantCount: 0,
		},
		{
			name:    "invalid json",
			output:  `{bad json}`,
			wantErr: true,
		},
		{
			name:    "runner error",
			runErr:  errors.New("ready command failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{
				output: []byte(tt.output),
				err:    tt.runErr,
			}
			client := NewClient(runner, slog.Default())

			tasks, err := client.Ready(context.Background())

			if tt.wantErr {
				require.Error(t, err)
				var trackerErr *domain.IssueTrackerError
				assert.ErrorAs(t, err, &trackerErr)
				assert.Equal(t, "ready", trackerErr.Op)
				return
			}

			require.NoError(t, err)
			assert.Len(t, tasks, tt.wantCount)
		})
	}
}

func TestClient_Update(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		status  domain.Status
		runErr  error
		wantErr bool
	}{
		{
			name:   "successful update",
			id:     "az-1",
			status: domain.StatusInProgress,
		},
		{
			name:    "runner error",
			id:      "az-2",
			status:  domain.StatusDone,
			runErr:  errors.New("update failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{err: tt.runErr}
			client := NewClient(runner, slog.Default())

			err := client.Update(context.Background(), tt.id, tt.status)

			if tt.wantErr {
				require.Error(t, err)
				var trackerErr *domain.IssueTrackerError
				assert.ErrorAs(t, err, &trackerErr)
				assert.Equal(t, "update", trackerErr.Op)
				assert.Equal(t, tt.id, trackerErr.IssueID)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestClient_Close(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		reason  string
		runErr  error
		wantErr bool
	}{
		{
			name:   "close with reason",
			id:     "az-1",
			reason: "completed successfully",
		},
		{
			name: "close without reason",
			id:   "az-2",
		},
		{
			name:    "runner error",
			id:      "az-3",
			reason:  "test",
			runErr:  errors.New("close failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{err: tt.runErr}
			client := NewClient(runner, slog.Default())

			err := client.Close(context.Background(), tt.id, tt.reason)

			if tt.wantErr {
				require.Error(t, err)
				var trackerErr *domain.IssueTrackerError
				assert.ErrorAs(t, err, &trackerErr)
				assert.Equal(t, "close", trackerErr.Op)
				assert.Equal(t, tt.id, trackerErr.IssueID)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestClient_Create(t *testing.T) {
	tests := []struct {
		name    string
		params  CreateTaskParams
		output  string
		runErr  error
		wantID  string
		wantErr bool
	}{
		{
			name: "successful creation",
			params: CreateTaskParams{
				Title:    "New Task",
				Type:     domain.TypeTask,
				Priority: domain.P2,
			},
			output: `{"id": "az-123", "title": "New Task"}`,
			wantID: "az-123",
		},
		{
			name: "successful creation with parent",
			params: CreateTaskParams{
				Title:    "Subtask",
				Type:     domain.TypeTask,
				Priority: domain.P2,
				ParentID: stringPtr("az-1"),
			},
			output: `{"id": "az-124"}`,
			wantID: "az-124",
		},
		{
			name:    "runner error",
			params:  CreateTaskParams{Title: "Fail"},
			runErr:  errors.New("create failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{
				output: []byte(tt.output),
				err:    tt.runErr,
			}
			client := NewClient(runner, slog.Default())

			id, err := client.Create(context.Background(), tt.params)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

func stringPtr(s string) *string {
	return &s
}

func TestClient_ErrorWrapping(t *testing.T) {
	t.Run("list error contains op", func(t *testing.T) {
		runner := &mockRunner{err: errors.New("cmd failed")}
		client := NewClient(runner, slog.Default())

		_, err := client.List(context.Background())
		require.Error(t, err)

		var trackerErr *domain.IssueTrackerError
		require.ErrorAs(t, err, &trackerErr)
		assert.Equal(t, "list", trackerErr.Op)
		assert.Contains(t, err.Error(), "issue tracker list")
	})

	t.Run("update error contains issue id", func(t *testing.T) {
		runner := &mockRunner{err: errors.New("cmd failed")}
		client := NewClient(runner, slog.Default())

		err := client.Update(context.Background(), "az-123", domain.StatusDone)
		require.Error(t, err)

		var trackerErr *domain.IssueTrackerError
		require.ErrorAs(t, err, &trackerErr)
		assert.Equal(t, "update", trackerErr.Op)
		assert.Equal(t, "az-123", trackerErr.IssueID)
		assert.Contains(t, err.Error(), "az-123")
	})
}

func TestClient_ListRetriesLockContention(t *testing.T) {
	runner := &mockRunner{
		outputs: [][]byte{
			nil,
			[]byte(`[{"id":"az-1","title":"Task","status":"open","priority":1,"type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}]`),
		},
		errs: []error{
			errors.New("database is locked"),
			nil,
		},
	}
	client := NewClient(runner, slog.Default())
	client.retryDelay = 0
	client.sleep = func(context.Context, time.Duration) error { return nil }

	tasks, err := client.List(context.Background())
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, 2, runner.calls)
}

func TestClient_ListRetriesDatabaseTableLocked(t *testing.T) {
	runner := &mockRunner{
		outputs: [][]byte{
			nil,
			[]byte(`[{"id":"az-2","title":"Task","status":"open","priority":1,"type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}]`),
		},
		errs: []error{
			errors.New("database table is locked"),
			nil,
		},
	}
	client := NewClient(runner, slog.Default())
	client.retryDelay = 0
	client.sleep = func(context.Context, time.Duration) error { return nil }

	tasks, err := client.List(context.Background())
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, 2, runner.calls)
}

func TestClient_UpdateDoesNotRetryNonLockErrors(t *testing.T) {
	runner := &mockRunner{
		errs: []error{errors.New("permission denied")},
	}
	client := NewClient(runner, slog.Default())
	client.retryDelay = 0
	client.sleep = func(context.Context, time.Duration) error { return nil }

	err := client.Update(context.Background(), "AZE-136", domain.StatusDone)
	require.Error(t, err)
	assert.Equal(t, 1, runner.calls)
}

func TestClient_UpdateLockContentionExhaustedIncludesAttemptCount(t *testing.T) {
	runner := &mockRunner{
		errs: []error{
			errors.New("database is locked"),
			errors.New("database is locked"),
			errors.New("database is locked"),
		},
	}
	client := NewClient(runner, slog.Default())
	client.maxAttempts = 3
	client.retryDelay = 0
	client.sleep = func(context.Context, time.Duration) error { return nil }

	err := client.Update(context.Background(), "AZE-136", domain.StatusDone)
	require.Error(t, err)
	var trackerErr *domain.IssueTrackerError
	require.ErrorAs(t, err, &trackerErr)
	require.Error(t, trackerErr.Err)
	assert.Contains(t, trackerErr.Err.Error(), "after 3 attempts")
	assert.Equal(t, 3, runner.calls)
}

func TestClient_CreateDoesNotRetryOnLockContention(t *testing.T) {
	runner := &mockRunner{
		outputs: [][]byte{
			nil,
			[]byte(`{"id":"az-999"}`),
		},
		errs: []error{
			errors.New("database is locked"),
			nil,
		},
	}
	client := NewClient(runner, slog.Default())
	client.retryDelay = 0
	client.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := client.Create(context.Background(), CreateTaskParams{
		Title:    "Lock-sensitive create",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.Error(t, err)
	var trackerErr *domain.IssueTrackerError
	require.ErrorAs(t, err, &trackerErr)
	require.Error(t, trackerErr.Err)
	assert.Contains(t, trackerErr.Err.Error(), "prevent duplicate writes")
	assert.Equal(t, 1, runner.calls)
}

func TestClient_CreateDetectsDatabaseTableLockAsDuplicateRisk(t *testing.T) {
	runner := &mockRunner{
		outputs: [][]byte{
			nil,
			[]byte(`{"id":"az-1000"}`),
		},
		errs: []error{
			errors.New("database table is locked"),
			nil,
		},
	}
	client := NewClient(runner, slog.Default())
	client.retryDelay = 0
	client.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := client.Create(context.Background(), CreateTaskParams{
		Title:    "Lock-sensitive create variant",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.Error(t, err)
	var trackerErr *domain.IssueTrackerError
	require.ErrorAs(t, err, &trackerErr)
	require.Error(t, trackerErr.Err)
	assert.Contains(t, trackerErr.Err.Error(), "prevent duplicate writes")
	assert.Equal(t, 1, runner.calls)
}
