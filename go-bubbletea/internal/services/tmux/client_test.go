package tmux

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
	output string
	err    error
}

func (m *mockRunner) Run(ctx context.Context, args ...string) (string, error) {
	return m.output, m.err
}

func TestClient_NewSession(t *testing.T) {
	tests := []struct {
		name     string
		session  string
		workdir  string
		runErr   error
		wantErr  bool
		wantArgs []string
	}{
		{
			name:     "create session with workdir",
			session:  "test-session",
			workdir:  "/home/user/project",
			wantArgs: []string{"new-session", "-d", "-s", "test-session", "-c", "/home/user/project"},
		},
		{
			name:     "create session without workdir",
			session:  "test-session",
			workdir:  "",
			wantArgs: []string{"new-session", "-d", "-s", "test-session"},
		},
		{
			name:    "runner error",
			session: "test-session",
			workdir: "/tmp",
			runErr:  errors.New("tmux command failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{err: tt.runErr}
			client := NewClient(runner, slog.Default())

			err := client.NewSession(context.Background(), tt.session, tt.workdir)

			if tt.wantErr {
				require.Error(t, err)
				var tmuxErr *domain.TmuxError
				assert.ErrorAs(t, err, &tmuxErr)
				assert.Equal(t, "new-session", tmuxErr.Op)
				assert.Equal(t, tt.session, tmuxErr.Session)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestClient_HasSession(t *testing.T) {
	tests := []struct {
		name     string
		session  string
		runErr   error
		wantBool bool
		wantErr  bool
	}{
		{
			name:     "session exists",
			session:  "existing-session",
			wantBool: true,
		},
		{
			name:     "session does not exist",
			session:  "missing-session",
			runErr:   errors.New("session not found"),
			wantBool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{err: tt.runErr}
			client := NewClient(runner, slog.Default())

			exists, err := client.HasSession(context.Background(), tt.session)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantBool, exists)
		})
	}
}

func TestClient_KillSession(t *testing.T) {
	tests := []struct {
		name    string
		session string
		runErr  error
		wantErr bool
	}{
		{
			name:    "successful kill",
			session: "test-session",
		},
		{
			name:    "runner error",
			session: "test-session",
			runErr:  errors.New("kill failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{err: tt.runErr}
			client := NewClient(runner, slog.Default())

			err := client.KillSession(context.Background(), tt.session)

			if tt.wantErr {
				require.Error(t, err)
				var tmuxErr *domain.TmuxError
				assert.ErrorAs(t, err, &tmuxErr)
				assert.Equal(t, "kill-session", tmuxErr.Op)
				assert.Equal(t, tt.session, tmuxErr.Session)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestClient_SendKeys(t *testing.T) {
	tests := []struct {
		name    string
		session string
		keys    string
		runErr  error
		wantErr bool
	}{
		{
			name:    "send simple command",
			session: "test-session",
			keys:    "echo hello",
		},
		{
			name:    "send complex command",
			session: "test-session",
			keys:    "cd /tmp && ls -la",
		},
		{
			name:    "runner error",
			session: "test-session",
			keys:    "test",
			runErr:  errors.New("send-keys failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{err: tt.runErr}
			client := NewClient(runner, slog.Default())

			err := client.SendKeys(context.Background(), tt.session, tt.keys)

			if tt.wantErr {
				require.Error(t, err)
				var tmuxErr *domain.TmuxError
				assert.ErrorAs(t, err, &tmuxErr)
				assert.Equal(t, "send-keys", tmuxErr.Op)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestClient_CapturePane(t *testing.T) {
	tests := []struct {
		name       string
		session    string
		lines      int
		output     string
		runErr     error
		wantOutput string
		wantErr    bool
	}{
		{
			name:       "capture last 10 lines",
			session:    "test-session",
			lines:      10,
			output:     "line1\nline2\nline3\n",
			wantOutput: "line1\nline2\nline3\n",
		},
		{
			name:       "capture last 100 lines",
			session:    "test-session",
			lines:      100,
			output:     "output here",
			wantOutput: "output here",
		},
		{
			name:    "runner error",
			session: "test-session",
			lines:   10,
			runErr:  errors.New("capture failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{
				output: tt.output,
				err:    tt.runErr,
			}
			client := NewClient(runner, slog.Default())

			output, err := client.CapturePane(context.Background(), tt.session, tt.lines)

			if tt.wantErr {
				require.Error(t, err)
				var tmuxErr *domain.TmuxError
				assert.ErrorAs(t, err, &tmuxErr)
				assert.Equal(t, "capture-pane", tmuxErr.Op)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantOutput, output)
		})
	}
}

func TestClient_ListSessions(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		runErr    error
		wantCount int
		wantNames []string
		wantErr   bool
	}{
		{
			name:      "multiple sessions",
			output:    "session1\nsession2\nsession3\n",
			wantCount: 3,
			wantNames: []string{"session1", "session2", "session3"},
		},
		{
			name:      "single session",
			output:    "only-session\n",
			wantCount: 1,
			wantNames: []string{"only-session"},
		},
		{
			name:      "no sessions",
			output:    "",
			runErr:    errors.New("no sessions"),
			wantCount: 0,
			wantNames: []string{},
		},
		{
			name:      "empty output",
			output:    "",
			wantCount: 0,
			wantNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{
				output: tt.output,
				err:    tt.runErr,
			}
			client := NewClient(runner, slog.Default())

			sessions, err := client.ListSessions(context.Background())

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Len(t, sessions, tt.wantCount)
			assert.Equal(t, tt.wantNames, sessions)
		})
	}
}

func TestClient_SetEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		session string
		key     string
		value   string
		runErr  error
		wantErr bool
	}{
		{
			name:    "set environment variable",
			session: "test-session",
			key:     "DATABASE_URL",
			value:   "postgresql://localhost:5432/db",
		},
		{
			name:    "set simple variable",
			session: "test-session",
			key:     "ENV",
			value:   "production",
		},
		{
			name:    "runner error",
			session: "test-session",
			key:     "VAR",
			value:   "value",
			runErr:  errors.New("set-environment failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{err: tt.runErr}
			client := NewClient(runner, slog.Default())

			err := client.SetEnvironment(context.Background(), tt.session, tt.key, tt.value)

			if tt.wantErr {
				require.Error(t, err)
				var tmuxErr *domain.TmuxError
				assert.ErrorAs(t, err, &tmuxErr)
				assert.Equal(t, "set-environment", tmuxErr.Op)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestClient_ErrorWrapping(t *testing.T) {
	t.Run("new-session error contains session name", func(t *testing.T) {
		runner := &mockRunner{err: errors.New("cmd failed")}
		client := NewClient(runner, slog.Default())

		err := client.NewSession(context.Background(), "my-session", "/tmp")
		require.Error(t, err)

		var tmuxErr *domain.TmuxError
		require.ErrorAs(t, err, &tmuxErr)
		assert.Equal(t, "new-session", tmuxErr.Op)
		assert.Equal(t, "my-session", tmuxErr.Session)
		assert.Contains(t, err.Error(), "my-session")
	})

	t.Run("capture-pane error contains session name", func(t *testing.T) {
		runner := &mockRunner{err: errors.New("cmd failed")}
		client := NewClient(runner, slog.Default())

		_, err := client.CapturePane(context.Background(), "session-123", 10)
		require.Error(t, err)

		var tmuxErr *domain.TmuxError
		require.ErrorAs(t, err, &tmuxErr)
		assert.Equal(t, "capture-pane", tmuxErr.Op)
		assert.Equal(t, "session-123", tmuxErr.Session)
		assert.Contains(t, err.Error(), "session-123")
	})
}
