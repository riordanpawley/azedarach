// Package diagnostics provides system health monitoring and diagnostics
package diagnostics

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/linearsync"
)

// HealthStatus represents the overall health state
type HealthStatus string

const (
	HealthHealthy  HealthStatus = "healthy"
	HealthDegraded HealthStatus = "degraded"
	HealthCritical HealthStatus = "critical"
)

// PortInfo represents information about a port allocation
type PortInfo struct {
	Port      int
	IssueID   string
	InUse     bool
	Available bool
}

// SessionInfo represents information about a tmux session
type SessionInfo struct {
	Name      string
	IssueID   string
	State     domain.SessionState
	StartedAt *time.Time
	Worktree  string
	Uptime    time.Duration
}

// OperationInfo summarizes the background operation mix inferred from sessions.
type OperationInfo struct {
	Total      int
	Busy       int
	Waiting    int
	Done       int
	Error      int
	Paused     int
	Cancelable int
}

// WorktreeInfo represents information about a git worktree
type WorktreeInfo struct {
	Path      string
	IssueID   string
	Branch    string
	IsDirty   bool
	Exists    bool
	IsHealthy bool
}

// NetworkInfo represents network connectivity status
type NetworkInfo struct {
	IsOnline    bool
	LastCheck   time.Time
	Latency     time.Duration
	HealthState HealthStatus
}

// SystemInfo represents overall system information
type SystemInfo struct {
	GoVersion    string
	OS           string
	Arch         string
	NumGoroutine int
	MemoryUsage  uint64 // Bytes
}

// SystemDiagnostics contains all diagnostic information
type SystemDiagnostics struct {
	Timestamp       time.Time
	OverallState    HealthStatus
	Operations      OperationInfo
	Ports           []PortInfo
	Sessions        []SessionInfo
	Worktrees       []WorktreeInfo
	Network         NetworkInfo
	System          SystemInfo
	ProjectSync     []ProjectSyncDiagnostics
	WebhookFallback *linearsync.WebhookFallbackStatus
	Warnings        []string
	Errors          []string
}

// TmuxClient interface for tmux operations
type TmuxClient interface {
	ListSessions(ctx context.Context) ([]string, error)
	HasSession(ctx context.Context, name string) (bool, error)
}

// GitClient interface for git operations
type GitClient interface {
	ListWorktrees(ctx context.Context) ([]string, error)
}

// PortAllocator interface for port management
type PortAllocator interface {
	GetPort(issueID string) (int, bool)
}

// NetworkChecker interface for network status
type NetworkChecker interface {
	IsOnline() bool
	LastCheck() time.Time
}

// Service provides system diagnostics and health monitoring
type Service struct {
	mu sync.RWMutex

	// Dependencies
	tmuxClient     TmuxClient
	portAllocator  PortAllocator
	networkChecker NetworkChecker

	// Cached diagnostics
	lastDiagnostics *SystemDiagnostics
	lastUpdate      time.Time
	projectSync     map[string]ProjectSyncDiagnostics
	webhookFallback *linearsync.WebhookFallbackStatus
}

// NewService creates a new diagnostics service
func NewService(tmux TmuxClient, ports PortAllocator, network NetworkChecker) *Service {
	return &Service{
		tmuxClient:     tmux,
		portAllocator:  ports,
		networkChecker: network,
		projectSync:    map[string]ProjectSyncDiagnostics{},
	}
}

// GetSystemStatus returns the overall system health status
func (s *Service) GetSystemStatus(ctx context.Context, sessions map[string]*domain.Session) HealthStatus {
	diag := s.CollectDiagnostics(ctx, sessions, nil)
	return diag.OverallState
}

// SetWebhookFallback stores the current Linear webhook fallback status for diagnostics emission.
func (s *Service) SetWebhookFallback(status *linearsync.WebhookFallbackStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webhookFallback = status
}

// SetProjectSyncDiagnostics stores a project-level sync diagnostics record.
func (s *Service) SetProjectSyncDiagnostics(diag ProjectSyncDiagnostics) {
	projectID := strings.TrimSpace(diag.ProjectID)
	if projectID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.projectSync == nil {
		s.projectSync = map[string]ProjectSyncDiagnostics{}
	}
	diag.ProjectID = projectID
	s.projectSync[projectID] = normalizeProjectSyncDiagnostics(diag)
}

// SetProjectSyncProjection projects worker lifecycle state into diagnostics and stores it.
func (s *Service) SetProjectSyncProjection(projection ProjectSyncProjection) ProjectSyncDiagnostics {
	diag := ProjectSyncDiagnosticsFromLifecycle(projection)
	s.SetProjectSyncDiagnostics(diag)
	return diag
}

// GetPortConflicts returns a list of ports that are allocated but not available
func (s *Service) GetPortConflicts(ctx context.Context, sessions map[string]*domain.Session) []PortInfo {
	var conflicts []PortInfo

	for issueID, session := range sessions {
		if session.DevServer == nil {
			continue
		}

		port := session.DevServer.Port
		available := isPortAvailable(port)

		if !available && session.DevServer.Running {
			conflicts = append(conflicts, PortInfo{
				Port:      port,
				IssueID:   issueID,
				InUse:     true,
				Available: false,
			})
		}
	}

	return conflicts
}

// GetSessionHealth returns session status summary
func (s *Service) GetSessionHealth(ctx context.Context, sessions map[string]*domain.Session) []SessionInfo {
	var sessionInfos []SessionInfo

	for issueID, session := range sessions {
		info := SessionInfo{
			Name:      issueID,
			IssueID:   issueID,
			State:     session.State,
			StartedAt: session.StartedAt,
			Worktree:  session.Worktree,
		}

		if session.StartedAt != nil {
			info.Uptime = time.Since(*session.StartedAt)
		}

		sessionInfos = append(sessionInfos, info)
	}

	return sessionInfos
}

// GetWorktreeStatus returns worktree health information
func (s *Service) GetWorktreeStatus(ctx context.Context, sessions map[string]*domain.Session) []WorktreeInfo {
	var worktreeInfos []WorktreeInfo

	for issueID, session := range sessions {
		if session.Worktree == "" {
			continue
		}

		info := WorktreeInfo{
			Path:      session.Worktree,
			IssueID:   issueID,
			Exists:    true, // Assume exists if in session
			IsHealthy: true,
		}

		worktreeInfos = append(worktreeInfos, info)
	}

	return worktreeInfos
}

// CollectDiagnostics gathers all diagnostic information
func (s *Service) CollectDiagnostics(ctx context.Context, sessions map[string]*domain.Session, issuesPath *string) *SystemDiagnostics {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	var warnings []string
	var errors []string

	// Collect port information
	var ports []PortInfo
	seenPorts := make(map[int]bool)

	for issueID, session := range sessions {
		if session.DevServer != nil {
			port := session.DevServer.Port
			if !seenPorts[port] {
				available := isPortAvailable(port)
				ports = append(ports, PortInfo{
					Port:      port,
					IssueID:   issueID,
					InUse:     session.DevServer.Running,
					Available: available,
				})
				seenPorts[port] = true

				// Add warning if port is in use but not available
				if session.DevServer.Running && !available {
					warnings = append(warnings, fmt.Sprintf("Port %d allocated to %s but not available", port, issueID))
				}
			}
		}
	}

	// Collect session information
	sessionInfos := s.GetSessionHealth(ctx, sessions)
	ops := OperationInfo{Total: len(sessionInfos)}
	for _, session := range sessionInfos {
		switch session.State {
		case domain.SessionBusy:
			ops.Busy++
			ops.Cancelable++
		case domain.SessionWaiting:
			ops.Waiting++
			ops.Cancelable++
		case domain.SessionDone:
			ops.Done++
		case domain.SessionError:
			ops.Error++
			errMsg := fmt.Sprintf("Session %s is in error state", session.IssueID)
			if session.Worktree != "" {
				errMsg += fmt.Sprintf(" (worktree: %s)", session.Worktree)
			}
			if session.Uptime > 0 {
				errMsg += fmt.Sprintf("; started %s ago", formatDuration(session.Uptime))
			}
			errMsg += "; inspect recent output or restart the session"
			errors = append(errors, errMsg)
		case domain.SessionPaused:
			ops.Paused++
		}
	}

	// Collect worktree information
	worktreeInfos := s.GetWorktreeStatus(ctx, sessions)

	// Collect network information
	network := NetworkInfo{
		IsOnline:  s.networkChecker.IsOnline(),
		LastCheck: s.networkChecker.LastCheck(),
	}

	if !network.IsOnline {
		network.HealthState = HealthCritical
		errors = append(errors, "Network is offline")
	} else {
		network.HealthState = HealthHealthy
	}

	// Collect system information
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	system := SystemInfo{
		GoVersion:    runtime.Version(),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		NumGoroutine: runtime.NumGoroutine(),
		MemoryUsage:  memStats.Alloc,
	}

	// Determine overall health state
	overallState := HealthHealthy
	if len(errors) > 0 {
		overallState = HealthCritical
	} else if len(warnings) > 0 {
		overallState = HealthDegraded
	}

	diag := &SystemDiagnostics{
		Timestamp:       now,
		OverallState:    overallState,
		Operations:      ops,
		Ports:           ports,
		Sessions:        sessionInfos,
		Worktrees:       worktreeInfos,
		Network:         network,
		System:          system,
		ProjectSync:     s.projectSyncSnapshotLocked(),
		WebhookFallback: s.webhookFallback,
		Warnings:        warnings,
		Errors:          errors,
	}

	s.lastDiagnostics = diag
	s.lastUpdate = now

	return diag
}

// FormatDiagnostics returns a human-readable diagnostics report
func (s *Service) FormatDiagnostics(diag *SystemDiagnostics) string {
	var b strings.Builder

	// Overall status
	b.WriteString(fmt.Sprintf("System Status: %s\n", strings.ToUpper(string(diag.OverallState))))
	b.WriteString(fmt.Sprintf("Last Updated: %s\n\n", diag.Timestamp.Format("15:04:05")))

	b.WriteString("OPERATIONS:\n")
	b.WriteString(fmt.Sprintf("  Total: %d\n", diag.Operations.Total))
	b.WriteString(fmt.Sprintf("  Busy: %d\n", diag.Operations.Busy))
	b.WriteString(fmt.Sprintf("  Waiting: %d\n", diag.Operations.Waiting))
	b.WriteString(fmt.Sprintf("  Done: %d\n", diag.Operations.Done))
	b.WriteString(fmt.Sprintf("  Error: %d\n", diag.Operations.Error))
	b.WriteString(fmt.Sprintf("  Paused: %d\n", diag.Operations.Paused))
	b.WriteString(fmt.Sprintf("  Cancelable: %d\n\n", diag.Operations.Cancelable))

	// Errors
	if len(diag.Errors) > 0 {
		b.WriteString("ERRORS:\n")
		for _, err := range diag.Errors {
			b.WriteString(fmt.Sprintf("  ✗ %s\n", err))
		}
		b.WriteString("\n")
	}

	// Warnings
	if len(diag.Warnings) > 0 {
		b.WriteString("WARNINGS:\n")
		for _, warn := range diag.Warnings {
			b.WriteString(fmt.Sprintf("  ⚠ %s\n", warn))
		}
		b.WriteString("\n")
	}

	if diag.WebhookFallback != nil {
		b.WriteString("LINEAR WEBHOOK FALLBACK:\n")
		b.WriteString(fmt.Sprintf("  Mode: %s\n", diag.WebhookFallback.Mode))
		b.WriteString(fmt.Sprintf("  Healthy: %t\n", diag.WebhookFallback.Healthy))
		if reason := diag.WebhookFallback.NormalizedReason(); reason != "" {
			b.WriteString(fmt.Sprintf("  Reason: %s\n", reason))
		}
		b.WriteString(fmt.Sprintf("  Toast: %s\n", diag.WebhookFallback.ToastMessage()))
		b.WriteString(fmt.Sprintf("  Health: %s\n\n", diag.WebhookFallback.HealthMessage()))
	}

	// Network
	b.WriteString("NETWORK:\n")
	if diag.Network.IsOnline {
		b.WriteString("  ✓ Online\n")
	} else {
		b.WriteString("  ✗ Offline\n")
	}
	b.WriteString(fmt.Sprintf("  Last Check: %s\n\n", diag.Network.LastCheck.Format("15:04:05")))

	// Sessions
	b.WriteString(fmt.Sprintf("SESSIONS: %d active\n", len(diag.Sessions)))
	if len(diag.Sessions) == 0 {
		b.WriteString("  (none)\n")
	} else {
		for _, session := range diag.Sessions {
			b.WriteString(fmt.Sprintf("  %s: %s", session.IssueID, session.State))
			if session.Uptime > 0 {
				b.WriteString(fmt.Sprintf(" (uptime: %s)", formatDuration(session.Uptime)))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	// Ports
	if len(diag.Ports) > 0 {
		b.WriteString(fmt.Sprintf("PORTS: %d allocated\n", len(diag.Ports)))
		for _, port := range diag.Ports {
			status := "available"
			if port.InUse {
				status = "in use"
			}
			if !port.Available {
				status = "UNAVAILABLE"
			}
			b.WriteString(fmt.Sprintf("  :%d → %s (%s)\n", port.Port, port.IssueID, status))
		}
		b.WriteString("\n")
	}

	// Worktrees
	if len(diag.Worktrees) > 0 {
		b.WriteString(fmt.Sprintf("WORKTREES: %d active\n", len(diag.Worktrees)))
		for _, wt := range diag.Worktrees {
			b.WriteString(fmt.Sprintf("  %s: %s\n", wt.IssueID, wt.Path))
		}
		b.WriteString("\n")
	}

	// System
	b.WriteString("SYSTEM:\n")
	b.WriteString(fmt.Sprintf("  Go: %s\n", diag.System.GoVersion))
	b.WriteString(fmt.Sprintf("  OS: %s/%s\n", diag.System.OS, diag.System.Arch))
	b.WriteString(fmt.Sprintf("  Goroutines: %d\n", diag.System.NumGoroutine))
	b.WriteString(fmt.Sprintf("  Memory: %s\n", formatBytes(diag.System.MemoryUsage)))

	if len(diag.ProjectSync) > 0 {
		b.WriteString("\n")
		b.WriteString("PROJECT SYNC:\n")
		for _, project := range diag.ProjectSync {
			b.WriteString(fmt.Sprintf("  %s: state=%s transport=%s fallback=%t retry=%d backoff=%s\n",
				project.ProjectID,
				project.State,
				project.Transport,
				project.FallbackActive,
				project.RetryAttempts,
				project.RetryBackoff,
			))
			if project.FallbackReason != "" {
				b.WriteString(fmt.Sprintf("    fallback reason: %s\n", project.FallbackReason))
			}
			b.WriteString(fmt.Sprintf("    last success: %s\n", formatProjectSyncTime(project.LastSuccessAt)))
			if project.LastError != "" {
				b.WriteString(fmt.Sprintf("    last error: %s\n", project.LastError))
			} else {
				b.WriteString("    last error: none\n")
			}
			b.WriteString(fmt.Sprintf("    last error at: %s\n", formatProjectSyncTime(project.LastErrorAt)))
		}
	}

	return b.String()
}

// GetCachedDiagnostics returns the last collected diagnostics without refresh
func (s *Service) GetCachedDiagnostics() *SystemDiagnostics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastDiagnostics
}

// Helper functions

// isPortAvailable checks if a port is available by attempting to listen on it
func isPortAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// formatDuration formats a duration in a human-readable format
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", hours, minutes)
}

// formatBytes formats bytes in a human-readable format
func formatBytes(bytes uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func (s *Service) projectSyncSnapshotLocked() []ProjectSyncDiagnostics {
	if len(s.projectSync) == 0 {
		return nil
	}

	projects := make([]ProjectSyncDiagnostics, 0, len(s.projectSync))
	for _, diag := range s.projectSync {
		projects = append(projects, cloneProjectSyncDiagnostics(diag))
	}

	sort.SliceStable(projects, func(i, j int) bool {
		if projects[i].ProjectID != projects[j].ProjectID {
			return projects[i].ProjectID < projects[j].ProjectID
		}
		if projects[i].State != projects[j].State {
			return projects[i].State < projects[j].State
		}
		return projects[i].Transport < projects[j].Transport
	})

	return projects
}
