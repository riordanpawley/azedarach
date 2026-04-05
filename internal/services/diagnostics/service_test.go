package diagnostics

import (
	"context"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/linearsync"
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

func TestCollectDiagnosticsIncludesOperationSummaryAndErrorGuidance(t *testing.T) {
	now := time.Now().Add(-3 * time.Minute)
	tmux := &mockTmuxClient{sessions: []string{}}
	ports := &mockPortAllocator{}
	network := &mockNetworkChecker{online: true, lastCheck: time.Now()}
	service := NewService(tmux, ports, network)

	diag := service.CollectDiagnostics(context.Background(), map[string]*domain.Session{
		"test-busy": {
			IssueID:   "test-busy",
			State:     domain.SessionBusy,
			StartedAt: &now,
			Worktree:  "/worktree/busy",
		},
		"test-wait": {
			IssueID: "test-wait",
			State:   domain.SessionWaiting,
		},
		"test-done": {
			IssueID: "test-done",
			State:   domain.SessionDone,
		},
		"test-error": {
			IssueID:   "test-error",
			State:     domain.SessionError,
			StartedAt: &now,
			Worktree:  "/worktree/error",
		},
		"test-paused": {
			IssueID: "test-paused",
			State:   domain.SessionPaused,
		},
	}, nil)

	if got, want := diag.Operations.Total, 5; got != want {
		t.Fatalf("Operations.Total = %d, want %d", got, want)
	}
	if got, want := diag.Operations.Busy, 1; got != want {
		t.Fatalf("Operations.Busy = %d, want %d", got, want)
	}
	if got, want := diag.Operations.Waiting, 1; got != want {
		t.Fatalf("Operations.Waiting = %d, want %d", got, want)
	}
	if got, want := diag.Operations.Done, 1; got != want {
		t.Fatalf("Operations.Done = %d, want %d", got, want)
	}
	if got, want := diag.Operations.Error, 1; got != want {
		t.Fatalf("Operations.Error = %d, want %d", got, want)
	}
	if got, want := diag.Operations.Paused, 1; got != want {
		t.Fatalf("Operations.Paused = %d, want %d", got, want)
	}
	if got, want := diag.Operations.Cancelable, 2; got != want {
		t.Fatalf("Operations.Cancelable = %d, want %d", got, want)
	}
	if diag.OverallState != HealthCritical {
		t.Fatalf("OverallState = %v, want %v", diag.OverallState, HealthCritical)
	}
	if len(diag.Errors) == 0 {
		t.Fatal("expected error guidance for SessionError state")
	}
	if got := diag.Errors[0]; got != "Session test-error is in error state (worktree: /worktree/error); started 3m 0s ago; inspect recent output or restart the session" {
		t.Fatalf("Errors[0] = %q", got)
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
			name: "orphaned tmux session ignored in projection-only diagnostics",
			sessions: map[string]*domain.Session{
				"test-1": {
					IssueID: "test-1",
				},
			},
			tmuxSessions: []string{"test-1", "orphaned-session"},
			online:       true,
			wantHealthy:  true,
			wantWarnings: 0,
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

func TestFormatDiagnosticsIncludesWebhookFallbackStatus(t *testing.T) {
	now := time.Now()
	status := &linearsync.WebhookFallbackStatus{
		Mode:    "misconfigured",
		Healthy: false,
		Reason:  "Linear webhook SDK mode requires a public webhook URL. Set issueTracker.linear.webhooks.url, export LINEAR_WEBHOOK_PUBLIC_URL, or run \"tailscale funnel --bg --yes 9000\"",
	}
	diag := &SystemDiagnostics{
		Timestamp:    now,
		OverallState: HealthDegraded,
		Operations:   OperationInfo{},
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
			MemoryUsage:  1024 * 1024,
		},
		WebhookFallback: status,
		Warnings:        []string{},
		Errors:          []string{},
	}

	tmux := &mockTmuxClient{}
	ports := &mockPortAllocator{}
	network := &mockNetworkChecker{}

	service := NewService(tmux, ports, network)

	output := service.FormatDiagnostics(diag)

	if !contains(output, "LINEAR WEBHOOK FALLBACK:") {
		t.Fatalf("FormatDiagnostics() missing webhook fallback section: %s", output)
	}
	if !contains(output, status.ToastMessage()) {
		t.Fatalf("FormatDiagnostics() missing fallback toast message: %s", output)
	}
	if !contains(output, status.HealthMessage()) {
		t.Fatalf("FormatDiagnostics() missing fallback health message: %s", output)
	}
	if !contains(output, "LINEAR_WEBHOOK_PUBLIC_URL") {
		t.Fatalf("FormatDiagnostics() missing concrete cause detail: %s", output)
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
