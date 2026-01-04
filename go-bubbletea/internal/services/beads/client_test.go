package beads

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner implements CommandRunner for testing
type mockRunner struct {
	output []byte
	err    error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
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
				var beadsErr *domain.BeadsError
				assert.ErrorAs(t, err, &beadsErr)
				assert.Equal(t, "list", beadsErr.Op)
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
				var beadsErr *domain.BeadsError
				assert.ErrorAs(t, err, &beadsErr)
				assert.Equal(t, "search", beadsErr.Op)
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
				var beadsErr *domain.BeadsError
				assert.ErrorAs(t, err, &beadsErr)
				assert.Equal(t, "ready", beadsErr.Op)
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
				var beadsErr *domain.BeadsError
				assert.ErrorAs(t, err, &beadsErr)
				assert.Equal(t, "update", beadsErr.Op)
				assert.Equal(t, tt.id, beadsErr.BeadID)
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
				var beadsErr *domain.BeadsError
				assert.ErrorAs(t, err, &beadsErr)
				assert.Equal(t, "close", beadsErr.Op)
				assert.Equal(t, tt.id, beadsErr.BeadID)
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

		var beadsErr *domain.BeadsError
		require.ErrorAs(t, err, &beadsErr)
		assert.Equal(t, "list", beadsErr.Op)
		assert.Contains(t, err.Error(), "beads list")
	})

	t.Run("update error contains bead id", func(t *testing.T) {
		runner := &mockRunner{err: errors.New("cmd failed")}
		client := NewClient(runner, slog.Default())

		err := client.Update(context.Background(), "az-123", domain.StatusDone)
		require.Error(t, err)

		var beadsErr *domain.BeadsError
		require.ErrorAs(t, err, &beadsErr)
		assert.Equal(t, "update", beadsErr.Op)
		assert.Equal(t, "az-123", beadsErr.BeadID)
		assert.Contains(t, err.Error(), "az-123")
	})
}
