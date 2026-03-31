package pr

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner implements CommandRunner for testing
type mockRunner struct {
	output []byte
	err    error
	// For multi-call mocks
	callCount int
	outputs   [][]byte
	errors    []error
	calls     [][]string
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, append([]string{name}, args...))

	// If we have multiple outputs configured, use them based on call count
	if len(m.outputs) > 0 {
		idx := m.callCount
		m.callCount++
		if idx < len(m.outputs) {
			out := m.outputs[idx]
			var err error
			if idx < len(m.errors) {
				err = m.errors[idx]
			}
			return out, err
		}
	}
	return m.output, m.err
}

func TestBuildPRBody(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		beadID string
		title  string
		want   string
	}{
		{
			name:   "appends footer to body",
			body:   "This PR adds feature X",
			beadID: "az-123",
			title:  "Add feature X",
			want:   "This PR adds feature X\n\nResolves az-123: Add feature X",
		},
		{
			name:   "keeps existing footer idempotent",
			body:   "This PR adds feature X\n\nResolves az-123: Add feature X",
			beadID: "az-123",
			title:  "Add feature X",
			want:   "This PR adds feature X\n\nResolves az-123: Add feature X",
		},
		{
			name:   "returns footer for empty body",
			body:   "  ",
			beadID: "az-123",
			title:  "Add feature X",
			want:   "Resolves az-123: Add feature X",
		},
		{
			name:   "skips footer when bead id is missing",
			body:   "This PR adds feature X",
			beadID: "",
			title:  "Add feature X",
			want:   "This PR adds feature X",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, buildPRBody(tt.body, tt.beadID, tt.title))
		})
	}
}

func TestPRWorkflow_Create(t *testing.T) {
	tests := []struct {
		name       string
		params     CreatePRParams
		createOut  string
		getOut     string
		createErr  error
		getErr     error
		wantNumber int
		wantDraft  bool
		wantErr    bool
		wantErrContains string
	}{
		{
			name: "successful draft PR creation",
			params: CreatePRParams{
				Title:      "Add feature X",
				Body:       "This PR adds feature X",
				Branch:     "feature/x",
				BaseBranch: "main",
				Draft:      true,
				IssueID:    "az-123",
			},
			createOut: "https://github.com/owner/repo/pull/42\n",
			getOut: `{
				"number": 42,
				"title": "Add feature X",
				"url": "https://github.com/owner/repo/pull/42",
				"state": "open",
				"isDraft": true,
				"headRefName": "feature/x",
				"baseRefName": "main"
			}`,
			wantNumber: 42,
			wantDraft:  true,
		},
		{
			name: "successful ready PR creation",
			params: CreatePRParams{
				Title:      "Fix bug Y",
				Body:       "Fixes #456",
				Branch:     "fix/bug-y",
				BaseBranch: "main",
				Draft:      false,
				IssueID:    "az-456",
			},
			createOut: "https://github.com/owner/repo/pull/99\n",
			getOut: `{
				"number": 99,
				"title": "Fix bug Y",
				"url": "https://github.com/owner/repo/pull/99",
				"state": "open",
				"isDraft": false,
				"headRefName": "fix/bug-y",
				"baseRefName": "main"
			}`,
			wantNumber: 99,
			wantDraft:  false,
		},
		{
			name: "create command fails",
			params: CreatePRParams{
				Title:      "Test",
				Body:       "Test PR",
				Branch:     "test",
				BaseBranch: "main",
			},
			createErr: errors.New("gh command failed"),
			wantErr:   true,
			wantErrContains: "failed to create PR",
		},
		{
			name: "get command fails after create",
			params: CreatePRParams{
				Title:      "Test",
				Body:       "Test PR",
				Branch:     "test",
				BaseBranch: "main",
			},
			createOut: "https://github.com/owner/repo/pull/101\n",
			getErr:    errors.New("gh command failed"),
			wantErr:   true,
			wantErrContains: "failed to get PR info",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a multi-call mock runner
			runner := &mockRunner{
				outputs: [][]byte{
					[]byte(tt.createOut), // First call: create
					[]byte(tt.getOut),    // Second call: get
				},
				errors: []error{
					tt.createErr,
					tt.getErr,
				},
			}

			workflow := NewPRWorkflow(runner, slog.Default())

			info, err := workflow.Create(context.Background(), tt.params)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContains != "" {
					assert.Contains(t, err.Error(), tt.wantErrContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantNumber, info.Number)
			assert.Equal(t, tt.wantDraft, info.Draft)
			assert.Equal(t, tt.params.Branch, info.Branch)
			if tt.params.BeadID != "" && tt.params.Title != "" {
				expectedBody := buildPRBody(tt.params.Body, tt.params.BeadID, tt.params.Title)
				require.GreaterOrEqual(t, len(runner.calls), 1)
				expectedArgs := []string{
					"gh", "pr", "create",
					"--title", tt.params.Title,
					"--body", expectedBody,
					"--head", tt.params.Branch,
					"--base", tt.params.BaseBranch,
				}
				if tt.params.Draft {
					expectedArgs = append(expectedArgs, "--draft")
				}
				assert.Equal(t, expectedArgs, runner.calls[0])
			}
		})
	}
}

func TestPRWorkflow_Get(t *testing.T) {
	tests := []struct {
		name       string
		branch     string
		output     string
		runErr     error
		wantNumber int
		wantState  string
		wantErr    bool
		wantErrContains string
	}{
		{
			name:   "successful get",
			branch: "feature/auth",
			output: `{
				"number": 123,
				"title": "Add authentication",
				"url": "https://github.com/owner/repo/pull/123",
				"state": "open",
				"isDraft": false,
				"headRefName": "feature/auth",
				"baseRefName": "main"
			}`,
			wantNumber: 123,
			wantState:  "open",
		},
		{
			name:   "merged PR",
			branch: "hotfix/security",
			output: `{
				"number": 456,
				"title": "Security fix",
				"url": "https://github.com/owner/repo/pull/456",
				"state": "merged",
				"isDraft": false,
				"headRefName": "hotfix/security",
				"baseRefName": "main"
			}`,
			wantNumber: 456,
			wantState:  "merged",
		},
		{
			name:    "invalid json",
			branch:  "test",
			output:  `not json`,
			wantErr: true,
			wantErrContains: "failed to parse PR JSON",
		},
		{
			name:    "runner error",
			branch:  "test",
			runErr:  errors.New("gh command failed"),
			wantErr: true,
			wantErrContains: "failed to get PR info for branch test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{
				output: []byte(tt.output),
				err:    tt.runErr,
			}
			workflow := NewPRWorkflow(runner, slog.Default())

			info, err := workflow.Get(context.Background(), tt.branch)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContains != "" {
					assert.Contains(t, err.Error(), tt.wantErrContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantNumber, info.Number)
			assert.Equal(t, tt.wantState, info.State)
			assert.Equal(t, tt.branch, info.Branch)
		})
	}
}

func TestPRWorkflow_List(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		runErr    error
		wantCount int
		wantErr   bool
		wantErrContains string
	}{
		{
			name: "multiple PRs",
			output: `[
				{
					"number": 1,
					"title": "PR 1",
					"url": "https://github.com/owner/repo/pull/1",
					"state": "open",
					"isDraft": true,
					"headRefName": "feature/a",
					"baseRefName": "main"
				},
				{
					"number": 2,
					"title": "PR 2",
					"url": "https://github.com/owner/repo/pull/2",
					"state": "open",
					"isDraft": false,
					"headRefName": "feature/b",
					"baseRefName": "main"
				}
			]`,
			wantCount: 2,
		},
		{
			name:      "no PRs",
			output:    `[]`,
			wantCount: 0,
		},
		{
			name:    "invalid json",
			output:  `{invalid}`,
			wantErr: true,
			wantErrContains: "failed to parse PR list JSON",
		},
		{
			name:    "runner error",
			runErr:  errors.New("list failed"),
			wantErr: true,
			wantErrContains: "failed to list PRs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{
				output: []byte(tt.output),
				err:    tt.runErr,
			}
			workflow := NewPRWorkflow(runner, slog.Default())

			prs, err := workflow.List(context.Background())

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContains != "" {
					assert.Contains(t, err.Error(), tt.wantErrContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Len(t, prs, tt.wantCount)
		})
	}
}

func TestPRWorkflow_Merge(t *testing.T) {
	tests := []struct {
		name     string
		prNumber int
		strategy string
		runErr   error
		wantErr  bool
		wantErrContains string
	}{
		{
			name:     "squash merge",
			prNumber: 42,
			strategy: "squash",
		},
		{
			name:     "rebase merge",
			prNumber: 43,
			strategy: "rebase",
		},
		{
			name:     "regular merge",
			prNumber: 44,
			strategy: "merge",
		},
		{
			name:     "invalid strategy",
			prNumber: 45,
			strategy: "invalid",
			wantErr:  true,
			wantErrContains: "invalid merge strategy: invalid",
		},
		{
			name:     "runner error",
			prNumber: 46,
			strategy: "squash",
			runErr:   errors.New("merge failed"),
			wantErr:  true,
			wantErrContains: "failed to merge PR 46",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{err: tt.runErr}
			workflow := NewPRWorkflow(runner, slog.Default())

			err := workflow.Merge(context.Background(), tt.prNumber, tt.strategy)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContains != "" {
					assert.Contains(t, err.Error(), tt.wantErrContains)
				}
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestPRWorkflow_Close(t *testing.T) {
	tests := []struct {
		name     string
		prNumber int
		runErr   error
		wantErr  bool
		wantErrContains string
	}{
		{
			name:     "successful close",
			prNumber: 99,
		},
		{
			name:     "runner error",
			prNumber: 100,
			runErr:   errors.New("close failed"),
			wantErr:  true,
			wantErrContains: "failed to close PR 100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{err: tt.runErr}
			workflow := NewPRWorkflow(runner, slog.Default())

			err := workflow.Close(context.Background(), tt.prNumber)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContains != "" {
					assert.Contains(t, err.Error(), tt.wantErrContains)
				}
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestPRWorkflow_MarkReady(t *testing.T) {
	tests := []struct {
		name     string
		prNumber int
		runErr   error
		wantErr  bool
		wantErrContains string
	}{
		{
			name:     "successful mark ready",
			prNumber: 42,
		},
		{
			name:     "runner error",
			prNumber: 43,
			runErr:   errors.New("mark ready failed"),
			wantErr:  true,
			wantErrContains: "failed to mark PR 43 ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{err: tt.runErr}
			workflow := NewPRWorkflow(runner, slog.Default())

			err := workflow.MarkReady(context.Background(), tt.prNumber)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContains != "" {
					assert.Contains(t, err.Error(), tt.wantErrContains)
				}
				return
			}

			require.NoError(t, err)
		})
	}
}
