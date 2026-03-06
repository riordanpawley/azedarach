package diagnostics

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// Mock TmuxClient for testing
type mockTmuxClient struct {
	sessions   []string
	sessionErr error
	hasSession bool
	hasErr     error
}

func (m *mockTmuxClient) ListSessions(ctx context.Context) ([]string, error) {
	return m.sessions, m.sessionErr
}

func (m *mockTmuxClient) HasSession(ctx context.Context, name string) (bool, error) {
	return m.hasSession, m.hasErr
}

// Mock PortAllocator for testing
type mockPortAllocator struct {
	ports map[string]int
}

func (m *mockPortAllocator) GetPort(issueID string) (int, bool) {
	if m.ports == nil {
		return 0, false
	}
	port, ok := m.ports[issueID]
	return port, ok
}

// Mock NetworkChecker for testing
type mockNetworkChecker struct {
	online    bool
	lastCheck time.Time
}

func (m *mockNetworkChecker) IsOnline() bool {
	return m.online
}

func (m *mockNetworkChecker) LastCheck() time.Time {
	return m.lastCheck
}

type mockToolChecker struct {
	paths map[string]string
	errs  map[string]error
}

func (m *mockToolChecker) LookPath(file string) (string, error) {
	if m.errs != nil {
		if err, ok := m.errs[file]; ok {
			return "", err
		}
	}

	if m.paths != nil {
		if path, ok := m.paths[file]; ok {
			return path, nil
		}
	}

	return "", errors.New("not found")
}

func TestNewService(t *testing.T) {
	tmux := &mockTmuxClient{}
	ports := &mockPortAllocator{}
	network := &mockNetworkChecker{}

	service := NewService(tmux, ports, network)

	if service == nil {
		t.Fatal("NewService returned nil")
	}
	if service.tmuxClient != tmux {
		t.Error("tmuxClient not set correctly")
	}
	if service.portAllocator != ports {
		t.Error("portAllocator not set correctly")
	}
	if service.networkChecker != network {
		t.Error("networkChecker not set correctly")
	}
}

func TestGetSystemStatus(t *testing.T) {
	tests := []struct {
		name     string
		sessions map[string]*domain.Session
		online   bool
		want     HealthStatus
	}{
		{
			name:     "healthy system",
			sessions: map[string]*domain.Session{},
			online:   true,
			want:     HealthHealthy,
		},
		{
			name:     "offline network",
			sessions: map[string]*domain.Session{},
			online:   false,
			want:     HealthCritical,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmux := &mockTmuxClient{}
			ports := &mockPortAllocator{}
			network := &mockNetworkChecker{online: tt.online}

			service := NewService(tmux, ports, network)
			ctx := context.Background()

			status := service.GetSystemStatus(ctx, tt.sessions)
			if status != tt.want {
				t.Errorf("GetSystemStatus() = %v, want %v", status, tt.want)
			}
		})
	}
}

func TestGetPortConflicts(t *testing.T) {
	tests := []struct {
		name         string
		sessions     map[string]*domain.Session
		wantCount    int
		wantConflict bool
	}{
		{
			name:         "no ports allocated",
			sessions:     map[string]*domain.Session{},
			wantCount:    0,
			wantConflict: false,
		},
		{
			name: "port in use (no conflict)",
			sessions: map[string]*domain.Session{
				"test-1": {
					IssueID: "test-1",
					DevServer: &domain.DevServer{
						Port:    9999, // High port unlikely to conflict
						Running: false,
					},
				},
			},
			wantCount:    0,
			wantConflict: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmux := &mockTmuxClient{}
			ports := &mockPortAllocator{}
			network := &mockNetworkChecker{}

			service := NewService(tmux, ports, network)
			ctx := context.Background()

			conflicts := service.GetPortConflicts(ctx, tt.sessions)
			if len(conflicts) != tt.wantCount {
				t.Errorf("GetPortConflicts() count = %v, want %v", len(conflicts), tt.wantCount)
			}
		})
	}
}

func TestGetSessionHealth(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		sessions  map[string]*domain.Session
		wantCount int
	}{
		{
			name:      "no sessions",
			sessions:  map[string]*domain.Session{},
			wantCount: 0,
		},
		{
			name: "one session",
			sessions: map[string]*domain.Session{
				"test-1": {
					IssueID:   "test-1",
					State:     domain.SessionBusy,
					StartedAt: &now,
					Worktree:  "/path/to/worktree",
				},
			},
			wantCount: 1,
		},
		{
			name: "multiple sessions",
			sessions: map[string]*domain.Session{
				"test-1": {
					IssueID: "test-1",
					State:   domain.SessionBusy,
				},
				"test-2": {
					IssueID: "test-2",
					State:   domain.SessionWaiting,
				},
			},
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmux := &mockTmuxClient{}
			ports := &mockPortAllocator{}
			network := &mockNetworkChecker{}

			service := NewService(tmux, ports, network)
			ctx := context.Background()

			health := service.GetSessionHealth(ctx, tt.sessions)
			if len(health) != tt.wantCount {
				t.Errorf("GetSessionHealth() count = %v, want %v", len(health), tt.wantCount)
			}

			// Verify session info is correct
			for _, info := range health {
				session, ok := tt.sessions[info.IssueID]
				if !ok {
					t.Errorf("Session %s not found in input", info.IssueID)
					continue
				}
				if info.State != session.State {
					t.Errorf("Session %s state = %v, want %v", info.IssueID, info.State, session.State)
				}
				if info.Worktree != session.Worktree {
					t.Errorf("Session %s worktree = %v, want %v", info.IssueID, info.Worktree, session.Worktree)
				}
			}
		})
	}
}

func TestGetWorktreeStatus(t *testing.T) {
	tests := []struct {
		name      string
		sessions  map[string]*domain.Session
		wantCount int
	}{
		{
			name:      "no worktrees",
			sessions:  map[string]*domain.Session{},
			wantCount: 0,
		},
		{
			name: "session without worktree",
			sessions: map[string]*domain.Session{
				"test-1": {
					IssueID:  "test-1",
					Worktree: "",
				},
			},
			wantCount: 0,
		},
		{
			name: "session with worktree",
			sessions: map[string]*domain.Session{
				"test-1": {
					IssueID:  "test-1",
					Worktree: "/path/to/worktree",
				},
			},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmux := &mockTmuxClient{}
			ports := &mockPortAllocator{}
			network := &mockNetworkChecker{}

			service := NewService(tmux, ports, network)
			ctx := context.Background()

			worktrees := service.GetWorktreeStatus(ctx, tt.sessions)
			if len(worktrees) != tt.wantCount {
				t.Errorf("GetWorktreeStatus() count = %v, want %v", len(worktrees), tt.wantCount)
			}
		})
	}
}

func TestCollectDiagnostics(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name         string
		sessions     map[string]*domain.Session
		tmuxSessions []string
		online       bool
		wantHealthy  bool
		wantWarnings int
		wantErrors   int
	}{
		{
			name:         "healthy system",
			sessions:     map[string]*domain.Session{},
			tmuxSessions: []string{},
			online:       true,
			wantHealthy:  true,
			wantWarnings: 0,
			wantErrors:   0,
		},
		{
			name:         "offline network",
			sessions:     map[string]*domain.Session{},
			tmuxSessions: []string{},
			online:       false,
			wantHealthy:  false,
			wantWarnings: 0,
			wantErrors:   1,
		},
		{
			name: "orphaned tmux session",
			sessions: map[string]*domain.Session{
				"test-1": {
					IssueID: "test-1",
				},
			},
			tmuxSessions: []string{"test-1", "orphaned-session"},
			online:       true,
			wantHealthy:  false,
			wantWarnings: 1,
			wantErrors:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmux := &mockTmuxClient{sessions: tt.tmuxSessions}
			ports := &mockPortAllocator{}
			network := &mockNetworkChecker{
				online:    tt.online,
				lastCheck: now,
			}

			service := NewService(tmux, ports, network)
			ctx := context.Background()

			diag := service.CollectDiagnostics(ctx, tt.sessions, nil)

			if diag == nil {
				t.Fatal("CollectDiagnostics() returned nil")
			}

			isHealthy := diag.OverallState == HealthHealthy
			if isHealthy != tt.wantHealthy {
				t.Errorf("CollectDiagnostics() healthy = %v, want %v", isHealthy, tt.wantHealthy)
			}

			if len(diag.Warnings) != tt.wantWarnings {
				t.Errorf("CollectDiagnostics() warnings = %v, want %v", len(diag.Warnings), tt.wantWarnings)
			}

			if len(diag.Errors) != tt.wantErrors {
				t.Errorf("CollectDiagnostics() errors = %v, want %v", len(diag.Errors), tt.wantErrors)
			}

			// Verify timestamp is recent
			if time.Since(diag.Timestamp) > time.Second {
				t.Error("CollectDiagnostics() timestamp is not recent")
			}

			// Verify system info is populated
			if diag.System.GoVersion == "" {
				t.Error("CollectDiagnostics() system.GoVersion is empty")
			}
			if diag.System.OS == "" {
				t.Error("CollectDiagnostics() system.OS is empty")
			}
		})
	}
}

func TestFormatDiagnostics(t *testing.T) {
	now := time.Now()

	diag := &SystemDiagnostics{
		Timestamp:    now,
		OverallState: HealthHealthy,
		Ports:        []PortInfo{},
		Sessions:     []SessionInfo{},
		Worktrees:    []WorktreeInfo{},
		Network: NetworkInfo{
			IsOnline:  true,
			LastCheck: now,
		},
		System: SystemInfo{
			GoVersion:    "go1.21",
			OS:           "linux",
			Arch:         "amd64",
			NumGoroutine: 10,
			MemoryUsage:  1024 * 1024, // 1MB
		},
		Warnings: []string{},
		Errors:   []string{},
	}

	tmux := &mockTmuxClient{}
	ports := &mockPortAllocator{}
	network := &mockNetworkChecker{}

	service := NewService(tmux, ports, network)

	output := service.FormatDiagnostics(diag)

	// Verify output contains expected sections
	expectedSections := []string{
		"System Status",
		"NETWORK:",
		"SESSIONS:",
		"SYSTEM:",
		"Go:",
		"OS:",
		"Goroutines:",
		"Memory:",
	}

	for _, section := range expectedSections {
		if !contains(output, section) {
			t.Errorf("FormatDiagnostics() missing section: %s", section)
		}
	}

	// Verify health status is included
	if !contains(output, "HEALTHY") {
		t.Error("FormatDiagnostics() missing health status")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "30s"},
		{2 * time.Minute, "2m 0s"},
		{90 * time.Minute, "1h 30m"},
		{2 * time.Hour, "2h 0m"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %v, want %v", tt.duration, got, tt.want)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes uint64
		want  string
	}{
		{512, "512 B"},
		{1024, "1.00 KB"},
		{1024 * 1024, "1.00 MB"},
		{1024 * 1024 * 1024, "1.00 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatBytes(tt.bytes)
			if got != tt.want {
				t.Errorf("formatBytes(%v) = %v, want %v", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestRunStartupGate_MissingMandatoryToolBlocksActions(t *testing.T) {
	tmux := &mockTmuxClient{}
	ports := &mockPortAllocator{}
	network := &mockNetworkChecker{}
	tools := &mockToolChecker{
		paths: map[string]string{
			"az":     "/usr/local/bin/az",
			"git":    "/usr/bin/git",
			"claude": "/usr/local/bin/claude",
		},
		errs: map[string]error{
			"tmux": errors.New("executable file not found"),
		},
	}

	service := NewService(tmux, ports, network, WithToolChecker(tools))
	gate := service.RunStartupGate(DefaultStartupGateConfig("claude"))

	if gate.OverallState != HealthCritical {
		t.Fatalf("RunStartupGate() health = %v, want %v", gate.OverallState, HealthCritical)
	}

	if !reflect.DeepEqual(gate.MissingMandatory, []string{"tmux"}) {
		t.Fatalf("RunStartupGate() missing mandatory = %v, want [tmux]", gate.MissingMandatory)
	}

	reason, ok := gate.BlockedActions["s"]
	if !ok {
		t.Fatalf("RunStartupGate() expected action 's' to be blocked, got %v", gate.BlockedActions)
	}
	if !contains(reason, "tmux") {
		t.Fatalf("RunStartupGate() block reason = %q, expected tmux context", reason)
	}

	if len(gate.Errors) == 0 {
		t.Fatalf("RunStartupGate() expected startup errors, got none")
	}
	if !contains(gate.Errors[0], "tmux") {
		t.Fatalf("RunStartupGate() error = %q, expected tmux context", gate.Errors[0])
	}
}

func TestRunStartupGate_DeterministicFailureOrdering(t *testing.T) {
	tmux := &mockTmuxClient{}
	ports := &mockPortAllocator{}
	network := &mockNetworkChecker{}
	tools := &mockToolChecker{
		errs: map[string]error{
			"az":   errors.New("not found"),
			"git":  errors.New("not found"),
			"tmux": errors.New("not found"),
		},
	}

	service := NewService(tmux, ports, network, WithToolChecker(tools))
	cfg := StartupGateConfig{
		Tools: []ToolRequirement{
			{Name: "tmux", Mandatory: true, BlockedActions: []string{"s"}},
			{Name: "az", Mandatory: true, BlockedActions: []string{"o"}},
			{Name: "git", Mandatory: true, BlockedActions: []string{"s"}},
		},
	}

	first := service.RunStartupGate(cfg)
	second := service.RunStartupGate(cfg)

	if !reflect.DeepEqual(first.MissingMandatory, second.MissingMandatory) {
		t.Fatalf("RunStartupGate() missing mandatory not deterministic:\nfirst=%v\nsecond=%v", first.MissingMandatory, second.MissingMandatory)
	}

	if !reflect.DeepEqual(first.Errors, second.Errors) {
		t.Fatalf("RunStartupGate() errors not deterministic:\nfirst=%v\nsecond=%v", first.Errors, second.Errors)
	}

	toolOrder := make([]string, 0, len(first.ToolDiagnostics))
	for _, tool := range first.ToolDiagnostics {
		toolOrder = append(toolOrder, tool.Name)
	}
	expectedOrder := []string{"az", "git", "tmux"}
	if !reflect.DeepEqual(toolOrder, expectedOrder) {
		t.Fatalf("RunStartupGate() tool order = %v, want %v", toolOrder, expectedOrder)
	}
}

func TestCollectDiagnostics_IncludesStartupGateErrors(t *testing.T) {
	tmux := &mockTmuxClient{}
	ports := &mockPortAllocator{}
	network := &mockNetworkChecker{
		online:    true,
		lastCheck: time.Now(),
	}
	tools := &mockToolChecker{
		paths: map[string]string{
			"az":     "/usr/local/bin/az",
			"git":    "/usr/bin/git",
			"claude": "/usr/local/bin/claude",
		},
		errs: map[string]error{
			"tmux": errors.New("not found"),
		},
	}

	service := NewService(tmux, ports, network, WithToolChecker(tools))
	service.RunStartupGate(DefaultStartupGateConfig("claude"))

	diag := service.CollectDiagnostics(context.Background(), map[string]*domain.Session{}, nil)
	if diag.OverallState != HealthCritical {
		t.Fatalf("CollectDiagnostics() health = %v, want %v", diag.OverallState, HealthCritical)
	}

	if diag.Startup.OverallState != HealthCritical {
		t.Fatalf("CollectDiagnostics() startup health = %v, want %v", diag.Startup.OverallState, HealthCritical)
	}

	foundTmuxError := false
	for _, err := range diag.Errors {
		if contains(err, "tmux") {
			foundTmuxError = true
			break
		}
	}

	if !foundTmuxError {
		t.Fatalf("CollectDiagnostics() expected startup tmux error, got %v", diag.Errors)
	}
}

func TestCollectDiagnostics_ReportsSessionMismatches(t *testing.T) {
	tmux := &mockTmuxClient{
		sessions: []string{"az-1", "az-3", "devserver-az-2"},
	}
	ports := &mockPortAllocator{}
	network := &mockNetworkChecker{
		online:    true,
		lastCheck: time.Now(),
	}

	service := NewService(tmux, ports, network)
	diag := service.CollectDiagnostics(context.Background(), map[string]*domain.Session{
		"az-1": {IssueID: "az-1", State: domain.SessionBusy},
		"az-2": {IssueID: "az-2", State: domain.SessionIdle},
	}, nil)

	if len(diag.SessionMismatches) != 2 {
		t.Fatalf("CollectDiagnostics() mismatches = %d, want 2", len(diag.SessionMismatches))
	}

	first := diag.SessionMismatches[0]
	second := diag.SessionMismatches[1]

	if first.IssueID != "az-2" || first.Kind != SessionMismatchKindStaleIndicator {
		t.Fatalf("first mismatch = %+v, want stale indicator for az-2", first)
	}
	if second.IssueID != "az-3" || second.Kind != SessionMismatchKindOrphanTmux {
		t.Fatalf("second mismatch = %+v, want orphan tmux for az-3", second)
	}

	if len(diag.Warnings) < 2 {
		t.Fatalf("CollectDiagnostics() warnings = %v, expected mismatch warnings", diag.Warnings)
	}
	if !contains(diag.Warnings[0], "Session/tmux mismatch") {
		t.Fatalf("expected mismatch warning text, got %q", diag.Warnings[0])
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && stringContains(s, substr)
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
