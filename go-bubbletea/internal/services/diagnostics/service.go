// Package diagnostics provides system health monitoring and diagnostics
package diagnostics

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
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

// SessionMismatchKind identifies a divergence between board session indicators and tmux discovery.
type SessionMismatchKind string

const (
	SessionMismatchKindOrphanTmux     SessionMismatchKind = "orphan_tmux"
	SessionMismatchKindStaleIndicator SessionMismatchKind = "stale_indicator"
)

// SessionMismatch captures one session/tmux divergence requiring reconciliation.
type SessionMismatch struct {
	IssueID          string
	TmuxSession      string
	Kind             SessionMismatchKind
	IndicatorPresent bool
	TmuxPresent      bool
}

// Warning returns a user-facing warning string for the mismatch.
func (m SessionMismatch) Warning() string {
	switch m.Kind {
	case SessionMismatchKindOrphanTmux:
		return fmt.Sprintf("Session/tmux mismatch: orphan tmux session without indicator: %s", m.IssueID)
	case SessionMismatchKindStaleIndicator:
		return fmt.Sprintf("Session/tmux mismatch: indicator without tmux session: %s", m.IssueID)
	default:
		return fmt.Sprintf("Session/tmux mismatch detected: %s", m.IssueID)
	}
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
	Timestamp         time.Time
	OverallState      HealthStatus
	Startup           StartupGate
	Ports             []PortInfo
	Sessions          []SessionInfo
	SessionMismatches []SessionMismatch
	Worktrees         []WorktreeInfo
	Network           NetworkInfo
	System            SystemInfo
	Warnings          []string
	Errors            []string
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

// ToolChecker resolves external tools in PATH.
type ToolChecker interface {
	LookPath(file string) (string, error)
}

type execToolChecker struct{}

func (execToolChecker) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

// ToolRequirement configures startup checks for one external tool.
type ToolRequirement struct {
	Name           string
	Mandatory      bool
	BlockedActions []string
}

// ToolDiagnostic captures startup health for one tool.
type ToolDiagnostic struct {
	Name           string
	Mandatory      bool
	Available      bool
	Path           string
	Error          string
	BlockedActions []string
}

// StartupGateConfig defines the required tool checks at startup.
type StartupGateConfig struct {
	Tools []ToolRequirement
}

// StartupGate captures startup diagnostics and blocked actions.
type StartupGate struct {
	CheckedAt        time.Time
	OverallState     HealthStatus
	ToolDiagnostics  []ToolDiagnostic
	MissingMandatory []string
	MissingOptional  []string
	BlockedActions   map[string]string
	Warnings         []string
	Errors           []string
}

// Service provides system diagnostics and health monitoring
type Service struct {
	mu sync.RWMutex

	// Dependencies
	tmuxClient     TmuxClient
	portAllocator  PortAllocator
	networkChecker NetworkChecker
	toolChecker    ToolChecker

	// Cached diagnostics
	lastDiagnostics *SystemDiagnostics
	lastUpdate      time.Time
	startupGate     StartupGate
}

// Option configures diagnostics service behavior.
type Option func(*Service)

// WithToolChecker injects tool resolution behavior for startup checks.
func WithToolChecker(checker ToolChecker) Option {
	return func(s *Service) {
		if checker != nil {
			s.toolChecker = checker
		}
	}
}

// NewService creates a new diagnostics service.
func NewService(tmux TmuxClient, ports PortAllocator, network NetworkChecker, opts ...Option) *Service {
	service := &Service{
		tmuxClient:     tmux,
		portAllocator:  ports,
		networkChecker: network,
		toolChecker:    execToolChecker{},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}

	return service
}

// DefaultStartupGateConfig returns the default mandatory tool checks.
func DefaultStartupGateConfig(cliTool string) StartupGateConfig {
	tool := strings.TrimSpace(cliTool)
	if tool == "" {
		tool = "claude"
	}

	return StartupGateConfig{
		Tools: []ToolRequirement{
			{Name: "az", Mandatory: true},
			{Name: "git", Mandatory: true, BlockedActions: []string{"s", "S", "u", "m", "P", "f", "c", "M"}},
			{Name: "tmux", Mandatory: true, BlockedActions: []string{"s", "S", "a", "p", "x", "R"}},
			{Name: tool, Mandatory: true, BlockedActions: []string{"s", "S"}},
		},
	}
}

// RunStartupGate validates required tools and computes blocked actions.
func (s *Service) RunStartupGate(cfg StartupGateConfig) StartupGate {
	s.mu.Lock()
	defer s.mu.Unlock()

	requirements := normalizeToolRequirements(cfg)
	now := time.Now()

	toolDiagnostics := make([]ToolDiagnostic, 0, len(requirements))
	missingMandatory := make([]string, 0, len(requirements))
	missingOptional := make([]string, 0, len(requirements))
	warnings := make([]string, 0, len(requirements))
	errors := make([]string, 0, len(requirements))
	actionReasons := make(map[string][]string)

	for _, req := range requirements {
		path, err := s.toolChecker.LookPath(req.Name)
		available := err == nil && path != ""

		diag := ToolDiagnostic{
			Name:           req.Name,
			Mandatory:      req.Mandatory,
			Available:      available,
			BlockedActions: sortedUniqueStrings(req.BlockedActions),
		}
		if available {
			diag.Path = path
		}
		if err != nil {
			diag.Error = err.Error()
		}

		if !available {
			if req.Mandatory {
				missingMandatory = append(missingMandatory, req.Name)
				errors = append(errors, fmt.Sprintf("Missing mandatory tool '%s'. Install '%s' and restart.", req.Name, req.Name))
				reason := fmt.Sprintf("requires %s", req.Name)
				for _, action := range diag.BlockedActions {
					actionReasons[action] = append(actionReasons[action], reason)
				}
			} else {
				missingOptional = append(missingOptional, req.Name)
				warnings = append(warnings, fmt.Sprintf("Optional tool '%s' not found.", req.Name))
			}
		}

		toolDiagnostics = append(toolDiagnostics, diag)
	}

	blockedActions := make(map[string]string, len(actionReasons))
	actionKeys := make([]string, 0, len(actionReasons))
	for action := range actionReasons {
		actionKeys = append(actionKeys, action)
	}
	sort.Strings(actionKeys)
	for _, action := range actionKeys {
		reasons := sortedUniqueStrings(actionReasons[action])
		blockedActions[action] = strings.Join(reasons, "; ")
	}

	sort.Strings(missingMandatory)
	sort.Strings(missingOptional)

	overallState := HealthHealthy
	if len(errors) > 0 {
		overallState = HealthCritical
	} else if len(warnings) > 0 {
		overallState = HealthDegraded
	}

	gate := StartupGate{
		CheckedAt:        now,
		OverallState:     overallState,
		ToolDiagnostics:  toolDiagnostics,
		MissingMandatory: missingMandatory,
		MissingOptional:  missingOptional,
		BlockedActions:   blockedActions,
		Warnings:         warnings,
		Errors:           errors,
	}

	s.startupGate = gate
	return gate
}

// GetSystemStatus returns the overall system health status
func (s *Service) GetSystemStatus(ctx context.Context, sessions map[string]*domain.Session) HealthStatus {
	diag := s.CollectDiagnostics(ctx, sessions, nil)
	return diag.OverallState
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
	startupGate := s.startupGate
	if len(startupGate.Warnings) > 0 {
		warnings = append(warnings, startupGate.Warnings...)
	}
	if len(startupGate.Errors) > 0 {
		errors = append(errors, startupGate.Errors...)
	}

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

	// Collect tmux session names
	tmuxSessions, err := s.tmuxClient.ListSessions(ctx)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("Failed to list tmux sessions: %v", err))
	}

	sessionMismatches := DetectSessionMismatches(sessions, tmuxSessions)
	for _, mismatch := range sessionMismatches {
		warnings = append(warnings, mismatch.Warning())
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
		Timestamp:         now,
		OverallState:      overallState,
		Startup:           startupGate,
		Ports:             ports,
		Sessions:          sessionInfos,
		SessionMismatches: sessionMismatches,
		Worktrees:         worktreeInfos,
		Network:           network,
		System:            system,
		Warnings:          warnings,
		Errors:            errors,
	}

	s.lastDiagnostics = diag
	s.lastUpdate = now

	return diag
}

// DetectSessionMismatches computes deterministic session/tmux mismatch records.
func DetectSessionMismatches(sessions map[string]*domain.Session, tmuxSessions []string) []SessionMismatch {
	indicatorIDs := make(map[string]bool, len(sessions))
	for issueID, session := range sessions {
		issueID = strings.TrimSpace(issueID)
		if issueID == "" || session == nil {
			continue
		}
		indicatorIDs[issueID] = true
	}

	tmuxIDs := make(map[string]bool, len(tmuxSessions))
	for _, tmuxSession := range tmuxSessions {
		tmuxSession = strings.TrimSpace(tmuxSession)
		if tmuxSession == "" || strings.HasPrefix(tmuxSession, "devserver-") {
			continue
		}
		tmuxIDs[tmuxSession] = true
	}

	mismatches := make([]SessionMismatch, 0, len(indicatorIDs)+len(tmuxIDs))
	for issueID := range indicatorIDs {
		if tmuxIDs[issueID] {
			continue
		}
		mismatches = append(mismatches, SessionMismatch{
			IssueID:          issueID,
			Kind:             SessionMismatchKindStaleIndicator,
			IndicatorPresent: true,
			TmuxPresent:      false,
		})
	}

	for issueID := range tmuxIDs {
		if indicatorIDs[issueID] {
			continue
		}
		mismatches = append(mismatches, SessionMismatch{
			IssueID:          issueID,
			TmuxSession:      issueID,
			Kind:             SessionMismatchKindOrphanTmux,
			IndicatorPresent: false,
			TmuxPresent:      true,
		})
	}

	sort.Slice(mismatches, func(i, j int) bool {
		if mismatches[i].IssueID == mismatches[j].IssueID {
			return mismatches[i].Kind < mismatches[j].Kind
		}
		return mismatches[i].IssueID < mismatches[j].IssueID
	})

	return mismatches
}

// FormatDiagnostics returns a human-readable diagnostics report
func (s *Service) FormatDiagnostics(diag *SystemDiagnostics) string {
	var b strings.Builder

	// Overall status
	b.WriteString(fmt.Sprintf("System Status: %s\n", strings.ToUpper(string(diag.OverallState))))
	b.WriteString(fmt.Sprintf("Last Updated: %s\n\n", diag.Timestamp.Format("15:04:05")))

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

func normalizeToolRequirements(cfg StartupGateConfig) []ToolRequirement {
	requirements := cfg.Tools
	if len(requirements) == 0 {
		requirements = DefaultStartupGateConfig("claude").Tools
	}

	byName := make(map[string]ToolRequirement, len(requirements))
	for _, req := range requirements {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			continue
		}

		normalizedActions := sortedUniqueStrings(req.BlockedActions)
		if existing, ok := byName[name]; ok {
			merged := append(existing.BlockedActions, normalizedActions...)
			existing.BlockedActions = sortedUniqueStrings(merged)
			existing.Mandatory = existing.Mandatory || req.Mandatory
			byName[name] = existing
			continue
		}

		byName[name] = ToolRequirement{
			Name:           name,
			Mandatory:      req.Mandatory,
			BlockedActions: normalizedActions,
		}
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	normalized := make([]ToolRequirement, 0, len(names))
	for _, name := range names {
		normalized = append(normalized, byName[name])
	}

	return normalized
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
	}

	sort.Strings(unique)
	return unique
}
