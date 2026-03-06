package worktree

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

// mockTmuxClient is a mock implementation of the tmux client
type mockTmuxClient struct {
	sessions map[string]bool
}

func newMockTmuxClient() *mockTmuxClient {
	return &mockTmuxClient{
		sessions: make(map[string]bool),
	}
}

func (m *mockTmuxClient) NewSession(ctx context.Context, name string, workdir string) error {
	m.sessions[name] = true
	return nil
}

func (m *mockTmuxClient) HasSession(ctx context.Context, name string) (bool, error) {
	return m.sessions[name], nil
}

func (m *mockTmuxClient) KillSession(ctx context.Context, name string) error {
	delete(m.sessions, name)
	return nil
}

func (m *mockTmuxClient) SendKeys(ctx context.Context, name string, keys string) error {
	return nil
}

func (m *mockTmuxClient) CapturePane(ctx context.Context, name string, lines int) (string, error) {
	return "", nil
}

// mockGitClient is a mock implementation of the git client (placeholder)
type mockGitClient struct{}

type branchOriginGitRunner struct {
	commands []string
}

func (r *branchOriginGitRunner) Run(_ context.Context, args ...string) (string, error) {
	r.commands = append(r.commands, strings.Join(args, " "))
	if len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
		return "", nil
	}
	return "", nil
}

func (r *branchOriginGitRunner) hasCommand(fragment string) bool {
	for _, cmd := range r.commands {
		if strings.Contains(cmd, fragment) {
			return true
		}
	}
	return false
}

type branchOriginTmuxRunner struct {
	commands []string
}

func (r *branchOriginTmuxRunner) Run(_ context.Context, args ...string) (string, error) {
	r.commands = append(r.commands, strings.Join(args, " "))
	return "", nil
}

func newBranchOriginService(t *testing.T, baseBranch string) (*WorktreeSessionService, *branchOriginGitRunner, *branchOriginTmuxRunner) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = baseBranch

	gitRunner := &branchOriginGitRunner{}
	tmuxRunner := &branchOriginTmuxRunner{}

	service := NewWorktreeSessionService(
		tmux.NewClient(tmuxRunner, slog.Default()),
		nil,
		git.NewWorktreeManager(gitRunner, "/home/user/test-repo", slog.Default()),
		"/home/user/test-repo",
		cfg,
		slog.Default(),
	)

	return service, gitRunner, tmuxRunner
}

// mockWorktreeManager is a mock implementation of the worktree manager
type mockWorktreeManager struct {
	worktrees map[string]*mockWorktree
}

type mockWorktree struct {
	Path    string
	Branch  string
	IssueID string
}

func newMockWorktreeManager() *mockWorktreeManager {
	return &mockWorktreeManager{
		worktrees: make(map[string]*mockWorktree),
	}
}

func (m *mockWorktreeManager) Create(ctx context.Context, issueID string, baseBranch string) (*mockWorktree, error) {
	wt := &mockWorktree{
		Path:    "/tmp/test-" + issueID,
		Branch:  "az/" + issueID,
		IssueID: issueID,
	}
	m.worktrees[issueID] = wt
	return wt, nil
}

func (m *mockWorktreeManager) Delete(ctx context.Context, issueID string) error {
	delete(m.worktrees, issueID)
	return nil
}

func TestSessionStatusConversion(t *testing.T) {
	tests := []struct {
		status   SessionStatus
		expected domain.SessionState
	}{
		{SessionIdle, domain.SessionIdle},
		{SessionActive, domain.SessionBusy},
		{SessionWaiting, domain.SessionWaiting},
		{SessionDone, domain.SessionDone},
		{SessionError, domain.SessionError},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			result := ConvertStatus(tt.status)
			if result != tt.expected {
				t.Errorf("ConvertStatus(%v) = %v, want %v", tt.status, result, tt.expected)
			}
		})
	}
}

func TestDomainStateConversion(t *testing.T) {
	tests := []struct {
		state    domain.SessionState
		expected SessionStatus
	}{
		{domain.SessionIdle, SessionIdle},
		{domain.SessionBusy, SessionActive},
		{domain.SessionWaiting, SessionWaiting},
		{domain.SessionDone, SessionDone},
		{domain.SessionError, SessionError},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			result := ConvertFromDomainState(tt.state)
			if result != tt.expected {
				t.Errorf("ConvertFromDomainState(%v) = %v, want %v", tt.state, result, tt.expected)
			}
		})
	}
}

func TestUpdateStatus(t *testing.T) {
	cfg := config.DefaultConfig()
	service := NewWorktreeSessionService(
		nil, // tmux
		nil, // git
		nil, // worktree
		"/tmp/test-project",
		cfg,
		nil, // logger (will use default)
	)

	// Create a test session
	issueID := "test-123"
	service.sessions[issueID] = &WorktreeSession{
		IssueID:      issueID,
		WorktreePath: "/tmp/test-123",
		TmuxSession:  issueID,
		Branch:       "az/test-123",
		Status:       SessionIdle,
		CreatedAt:    time.Now(),
	}

	// Update status
	service.UpdateStatus(issueID, SessionActive)

	// Verify status was updated
	session := service.sessions[issueID]
	if session.Status != SessionActive {
		t.Errorf("Expected status to be %v, got %v", SessionActive, session.Status)
	}
}

func TestList(t *testing.T) {
	cfg := config.DefaultConfig()
	service := NewWorktreeSessionService(
		nil, // tmux
		nil, // git
		nil, // worktree
		"/tmp/test-project",
		cfg,
		nil, // logger (will use default)
	)

	// Create test sessions
	sessions := []*WorktreeSession{
		{
			IssueID:      "test-1",
			WorktreePath: "/tmp/test-1",
			TmuxSession:  "test-1",
			Branch:       "az/test-1",
			Status:       SessionIdle,
			CreatedAt:    time.Now(),
		},
		{
			IssueID:      "test-2",
			WorktreePath: "/tmp/test-2",
			TmuxSession:  "test-2",
			Branch:       "az/test-2",
			Status:       SessionActive,
			CreatedAt:    time.Now(),
		},
	}

	for _, s := range sessions {
		service.sessions[s.IssueID] = s
	}

	// List sessions
	result := service.List()

	// Verify count
	if len(result) != 2 {
		t.Errorf("Expected 2 sessions, got %d", len(result))
	}

	// Verify sessions are copies (not references)
	for _, s := range result {
		if s == service.sessions[s.IssueID] {
			t.Errorf("Expected session copy, got reference")
		}
	}
}

func TestBuildClaudeCommand(t *testing.T) {
	tests := []struct {
		name     string
		cliTool  string
		yolo     bool
		expected string
	}{
		{
			name:     "default command without yolo",
			cliTool:  "claude",
			yolo:     false,
			expected: "claude",
		},
		{
			name:     "default command with yolo",
			cliTool:  "claude",
			yolo:     true,
			expected: "claude --yolo",
		},
		{
			name:     "custom CLI tool",
			cliTool:  "my-claude",
			yolo:     false,
			expected: "my-claude",
		},
		{
			name:     "empty CLI tool defaults to claude",
			cliTool:  "",
			yolo:     false,
			expected: "claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.CLITool = tt.cliTool

			service := NewWorktreeSessionService(
				nil, // tmux
				nil, // git
				nil, // worktree
				"/tmp/test-project",
				cfg,
				nil, // logger (will use default)
			)

			result := service.buildClaudeCommand(tt.yolo)
			if result != tt.expected {
				t.Errorf("buildClaudeCommand() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestBuildBranchOriginChooser_IncludesBaseAndUpstreamOptions(t *testing.T) {
	service, _, _ := newBranchOriginService(t, "main")

	chooser := service.BuildBranchOriginChooser(
		"AZE-110",
		[]git.UpstreamBranchSource{{IssueID: "AZE-21", Branch: "az/AZE-21"}},
		false,
	)

	if chooser.IssueID != "AZE-110" {
		t.Fatalf("expected chooser issue AZE-110, got %q", chooser.IssueID)
	}
	if chooser.TargetBranch != "az/AZE-110" {
		t.Fatalf("expected target branch az/AZE-110, got %q", chooser.TargetBranch)
	}
	if len(chooser.Options) != 2 {
		t.Fatalf("expected 2 chooser options, got %d", len(chooser.Options))
	}
	if chooser.UpstreamUnavailableReason != "" {
		t.Fatalf("expected no unavailable reason when upstream exists, got %q", chooser.UpstreamUnavailableReason)
	}
}

func TestCreateWithBranchOrigin_RequiresExplicitSourceWhenMultipleUpstreams(t *testing.T) {
	service, gitRunner, _ := newBranchOriginService(t, "main")

	_, err := service.CreateWithBranchOrigin(
		context.Background(),
		"AZE-110",
		[]git.UpstreamBranchSource{
			{IssueID: "AZE-21", Branch: "az/AZE-21"},
			{IssueID: "AZE-22", Branch: "az/AZE-22"},
		},
		git.BranchOriginSelection{Kind: git.BranchOriginUpstream},
		false,
	)
	if err == nil {
		t.Fatal("expected explicit source selection error, got nil")
	}
	if !strings.Contains(err.Error(), "explicit source selection required") {
		t.Fatalf("expected explicit source error, got %v", err)
	}
	if gitRunner.hasCommand("worktree add -b") {
		t.Fatalf("expected no worktree add command on invalid selection, commands=%v", gitRunner.commands)
	}
}

func TestCreateWithBranchOrigin_UsesSelectedUpstreamAndCapturesMetadata(t *testing.T) {
	service, gitRunner, _ := newBranchOriginService(t, "main")

	session, err := service.CreateWithBranchOrigin(
		context.Background(),
		"AZE-110",
		[]git.UpstreamBranchSource{
			{IssueID: "AZE-21", Branch: "az/AZE-21"},
			{IssueID: "AZE-22", Branch: "az/AZE-22"},
		},
		git.BranchOriginSelection{Kind: git.BranchOriginUpstream, SourceIssueID: "AZE-22"},
		false,
	)
	if err != nil {
		t.Fatalf("expected successful create with upstream origin, got %v", err)
	}
	if session.BranchOriginKind != git.BranchOriginUpstream {
		t.Fatalf("expected branch origin kind upstream, got %q", session.BranchOriginKind)
	}
	if session.BranchOriginIssueID != "AZE-22" {
		t.Fatalf("expected branch origin issue AZE-22, got %q", session.BranchOriginIssueID)
	}
	if session.BranchOriginBranch != "az/AZE-22" {
		t.Fatalf("expected branch origin branch az/AZE-22, got %q", session.BranchOriginBranch)
	}
	if session.BranchRecreated {
		t.Fatal("expected non-recreate create flow")
	}
	if !gitRunner.hasCommand("worktree add -b az/AZE-110 /home/user/test-repo-AZE-110 az/AZE-22") {
		t.Fatalf("expected worktree add command with selected upstream origin, commands=%v", gitRunner.commands)
	}
}

func TestCreateWithBranchOrigin_RecreateFlowKeepsChooserContract(t *testing.T) {
	service, gitRunner, _ := newBranchOriginService(t, "develop")

	chooser := service.BuildBranchOriginChooser("AZE-111", nil, true)
	if !chooser.Recreate {
		t.Fatal("expected recreate chooser flag")
	}
	if chooser.UpstreamUnavailableReason == "" {
		t.Fatal("expected fallback reason when no upstream sources are available")
	}

	session, err := service.CreateWithBranchOrigin(
		context.Background(),
		"AZE-111",
		nil,
		git.BranchOriginSelection{Kind: git.BranchOriginBase},
		true,
	)
	if err != nil {
		t.Fatalf("expected recreate path create to succeed, got %v", err)
	}
	if !session.BranchRecreated {
		t.Fatal("expected recreated branch metadata flag")
	}
	if session.BranchOriginKind != git.BranchOriginBase {
		t.Fatalf("expected base origin kind, got %q", session.BranchOriginKind)
	}
	if session.BranchOriginBranch != "develop" {
		t.Fatalf("expected base origin branch develop, got %q", session.BranchOriginBranch)
	}
	if !gitRunner.hasCommand("worktree add -b az/AZE-111 /home/user/test-repo-AZE-111 develop") {
		t.Fatalf("expected recreate command from configured base branch, commands=%v", gitRunner.commands)
	}
}
