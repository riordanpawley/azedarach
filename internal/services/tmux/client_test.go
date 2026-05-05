package tmux

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
	output string
	err    error
}

func (m *mockRunner) Run(ctx context.Context, args ...string) (string, error) {
	return m.output, m.err
}

type recordingRunner struct {
	err      error
	commands [][]string
}

func (r *recordingRunner) Run(ctx context.Context, args ...string) (string, error) {
	r.commands = append(r.commands, append([]string(nil), args...))
	return "", r.err
}

type recordingOutputRunner struct {
	outputs  []string
	err      error
	commands [][]string
}

func (r *recordingOutputRunner) Run(ctx context.Context, args ...string) (string, error) {
	r.commands = append(r.commands, append([]string(nil), args...))
	if r.err != nil {
		return "", r.err
	}
	if len(r.outputs) == 0 {
		return "", nil
	}
	out := r.outputs[0]
	r.outputs = r.outputs[1:]
	return out, nil
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

func TestClient_NewSessionWithCommand(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(runner, slog.Default())

	err := client.NewSessionWithCommand(context.Background(), "az", "/repo", "az --open-issue bxn")

	require.NoError(t, err)
	assert.Equal(t, [][]string{{"new-session", "-d", "-s", "az", "-c", "/repo", "az --open-issue bxn"}}, runner.commands)
}

func TestClient_EnsureWindow(t *testing.T) {
	tests := []struct {
		name        string
		listOutput  string
		runErr      error
		wantReused  bool
		wantErr     bool
		wantCommand [][]string
	}{
		{
			name:       "reuses existing window",
			listOutput: "shell\nresolve-conflict\n",
			wantReused: true,
			wantCommand: [][]string{
				{"list-windows", "-t", "test-session", "-F", "#{window_name}"},
			},
		},
		{
			name:       "creates missing window with workdir",
			listOutput: "shell\n",
			wantCommand: [][]string{
				{"list-windows", "-t", "test-session", "-F", "#{window_name}"},
				{"new-window", "-d", "-t", "test-session", "-n", "resolve-conflict", "-c", "/tmp/worktree"},
			},
		},
		{
			name:       "list error",
			listOutput: "",
			runErr:     errors.New("list failed"),
			wantErr:    true,
			wantCommand: [][]string{
				{"list-windows", "-t", "test-session", "-F", "#{window_name}"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingOutputRunner{
				outputs: []string{tt.listOutput},
				err:     tt.runErr,
			}
			client := NewClient(runner, slog.Default())

			reused, err := client.EnsureWindow(context.Background(), "test-session", "resolve-conflict", "/tmp/worktree")
			if tt.wantErr {
				require.Error(t, err)
				var tmuxErr *domain.TmuxError
				assert.ErrorAs(t, err, &tmuxErr)
				assert.Equal(t, "list-windows", tmuxErr.Op)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantReused, reused)
			}
			assert.Equal(t, tt.wantCommand, runner.commands)
		})
	}
}

func TestClient_SendKey(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(runner, slog.Default())

	err := client.SendKey(context.Background(), "az", "C-c")

	require.NoError(t, err)
	assert.Equal(t, [][]string{{"send-keys", "-t", "az", "C-c"}}, runner.commands)
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

func TestClient_ListSessionInfos(t *testing.T) {
	createdOne := time.Unix(1775209200, 0).UTC()
	tests := []struct {
		name    string
		output  string
		runErr  error
		want    []SessionInfo
		wantErr bool
	}{
		{
			name:   "parses names and created at",
			output: "session1\t1775209200\nsession2\t\nsession3\tgarbage\n",
			want: []SessionInfo{
				{Name: "session1", CreatedAt: &createdOne},
				{Name: "session2"},
				{Name: "session3"},
			},
		},
		{
			name:   "parses name-only format",
			output: "legacy-session\n",
			want:   []SessionInfo{{Name: "legacy-session"}},
		},
		{
			name:   "no sessions",
			runErr: errors.New("no sessions"),
			want:   []SessionInfo{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{output: tt.output, err: tt.runErr}
			client := NewClient(runner, slog.Default())

			got, err := client.ListSessionInfos(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, len(tt.want))
			for i := range tt.want {
				assert.Equal(t, tt.want[i].Name, got[i].Name)
				if tt.want[i].CreatedAt == nil {
					assert.Nil(t, got[i].CreatedAt)
					continue
				}
				require.NotNil(t, got[i].CreatedAt)
				assert.True(t, got[i].CreatedAt.Equal(*tt.want[i].CreatedAt))
			}
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

func TestClient_SwitchClient(t *testing.T) {
	t.Run("switches target session", func(t *testing.T) {
		runner := &recordingRunner{}
		client := NewClient(runner, slog.Default())

		err := client.SwitchClient(context.Background(), "ch-em")
		require.NoError(t, err)
		require.Len(t, runner.commands, 1)
		assert.Equal(t, []string{"switch-client", "-t", "ch-em"}, runner.commands[0])
	})

	t.Run("wraps switch error", func(t *testing.T) {
		runner := &recordingRunner{err: errors.New("switch failed")}
		client := NewClient(runner, slog.Default())

		err := client.SwitchClient(context.Background(), "ch-em")
		require.Error(t, err)
		var tmuxErr *domain.TmuxError
		require.ErrorAs(t, err, &tmuxErr)
		assert.Equal(t, "switch-client", tmuxErr.Op)
		assert.Equal(t, "ch-em", tmuxErr.Session)
	})
}

func TestClient_DisplayPopup(t *testing.T) {
	t.Run("builds popup command with title", func(t *testing.T) {
		runner := &recordingRunner{}
		client := NewClient(runner, slog.Default())

		err := client.DisplayPopup(context.Background(), "az.log", "90%", "90%", "less +F az.log")
		require.NoError(t, err)
		require.Len(t, runner.commands, 1)
		assert.Equal(t, []string{
			"display-popup", "-E", "-w", "90%", "-h", "90%", "-T", "az.log", "less +F az.log",
		}, runner.commands[0])
	})

	t.Run("omits title when empty", func(t *testing.T) {
		runner := &recordingRunner{}
		client := NewClient(runner, slog.Default())

		err := client.DisplayPopup(context.Background(), "", "80%", "70%", "echo hi")
		require.NoError(t, err)
		require.Len(t, runner.commands, 1)
		assert.Equal(t, []string{
			"display-popup", "-E", "-w", "80%", "-h", "70%", "echo hi",
		}, runner.commands[0])
	})

	t.Run("wraps popup error", func(t *testing.T) {
		runner := &recordingRunner{err: errors.New("popup failed")}
		client := NewClient(runner, slog.Default())

		err := client.DisplayPopup(context.Background(), "az.log", "90%", "90%", "less +F az.log")
		require.Error(t, err)
		var tmuxErr *domain.TmuxError
		require.ErrorAs(t, err, &tmuxErr)
		assert.Equal(t, "display-popup", tmuxErr.Op)
	})
}
