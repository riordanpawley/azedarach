package overlay

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/diagnostics"
)

// DiagnosticsCollector defines the interface for collecting diagnostics
type DiagnosticsCollector interface {
	CollectDiagnostics(ctx context.Context, sessions map[string]*domain.Session, beadsPath *string) *diagnostics.SystemDiagnostics
}

// DiagnosticsSection represents a section in the diagnostics panel
type DiagnosticsSection int

const (
	SectionOverview DiagnosticsSection = iota
	SectionPorts
	SectionSessions
	SectionWorktrees
	SectionNetwork
	SectionSystem
)

// DiagnosticsRefreshMsg is sent when diagnostics should be refreshed
type DiagnosticsRefreshMsg struct {
	Diagnostics *diagnostics.SystemDiagnostics
}

// DiagnosticsPanel displays system diagnostics and health information
type DiagnosticsPanel struct {
	diagnosticsService DiagnosticsCollector
	sessions           map[string]*domain.Session
	currentDiagnostics *diagnostics.SystemDiagnostics

	// UI state
	activeSection DiagnosticsSection
	scrollY       int
	contentHeight int
	viewHeight    int
	styles        *Styles

	// Auto-refresh
	lastRefresh time.Time
}

// NewDiagnosticsPanel creates a new diagnostics panel
func NewDiagnosticsPanel(
	diagService DiagnosticsCollector,
	sessions map[string]*domain.Session,
) *DiagnosticsPanel {
	return &DiagnosticsPanel{
		diagnosticsService: diagService,
		sessions:           sessions,
		activeSection:      SectionOverview,
		scrollY:            0,
		viewHeight:         20,
		styles:             New(),
	}
}

// Init initializes the diagnostics panel and loads initial data
func (d *DiagnosticsPanel) Init() tea.Cmd {
	return d.refreshCmd()
}

// Update handles messages
func (d *DiagnosticsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return d, func() tea.Msg { return CloseOverlayMsg{} }

		case "r":
			// Manual refresh
			return d, d.refreshCmd()

		case "j", "down":
			if d.scrollY < d.maxScroll() {
				d.scrollY++
			}
			return d, nil

		case "k", "up":
			if d.scrollY > 0 {
				d.scrollY--
			}
			return d, nil

		case "g":
			// Jump to top
			d.scrollY = 0
			return d, nil

		case "G":
			// Jump to bottom
			d.scrollY = d.maxScroll()
			return d, nil

		case "tab":
			// Switch to next section
			d.activeSection = (d.activeSection + 1) % 6
			d.scrollY = 0
			return d, nil

		case "1":
			d.activeSection = SectionOverview
			d.scrollY = 0
			return d, nil

		case "2":
			d.activeSection = SectionPorts
			d.scrollY = 0
			return d, nil

		case "3":
			d.activeSection = SectionSessions
			d.scrollY = 0
			return d, nil

		case "4":
			d.activeSection = SectionWorktrees
			d.scrollY = 0
			return d, nil

		case "5":
			d.activeSection = SectionNetwork
			d.scrollY = 0
			return d, nil

		case "6":
			d.activeSection = SectionSystem
			d.scrollY = 0
			return d, nil
		}

	case DiagnosticsRefreshMsg:
		d.currentDiagnostics = msg.Diagnostics
		d.lastRefresh = time.Now()
		return d, nil
	}

	return d, nil
}

// View renders the diagnostics panel
func (d *DiagnosticsPanel) View() string {
	if d.currentDiagnostics == nil {
		return d.styles.MenuItem.Render("Loading diagnostics...")
	}

	var content strings.Builder

	// Render the active section
	switch d.activeSection {
	case SectionOverview:
		d.renderOverview(&content)
	case SectionPorts:
		d.renderPorts(&content)
	case SectionSessions:
		d.renderSessions(&content)
	case SectionWorktrees:
		d.renderWorktrees(&content)
	case SectionNetwork:
		d.renderNetwork(&content)
	case SectionSystem:
		d.renderSystem(&content)
	}

	// Count lines for scrolling
	lines := strings.Split(content.String(), "\n")
	d.contentHeight = len(lines)

	// Apply scrolling
	start := d.scrollY
	end := min(d.scrollY+d.viewHeight, len(lines))

	visibleLines := lines[start:end]
	result := strings.Join(visibleLines, "\n")

	// Add footer with navigation hints and scroll info
	footer := d.renderFooter()
	result += "\n\n" + footer

	return result
}

// Title returns the overlay title
func (d *DiagnosticsPanel) Title() string {
	if d.currentDiagnostics == nil {
		return "System Diagnostics"
	}

	// Color-code title based on health status
	status := strings.ToUpper(string(d.currentDiagnostics.OverallState))
	sectionName := d.getSectionName()

	return fmt.Sprintf("System Diagnostics - %s [%s]", sectionName, status)
}

// Size returns the overlay dimensions
func (d *DiagnosticsPanel) Size() (width, height int) {
	d.viewHeight = 20
	return 80, 28
}

// refreshCmd returns a command to refresh diagnostics
func (d *DiagnosticsPanel) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		diag := d.diagnosticsService.CollectDiagnostics(ctx, d.sessions, nil)
		return DiagnosticsRefreshMsg{Diagnostics: diag}
	}
}

// Rendering helpers

func (d *DiagnosticsPanel) renderOverview(b *strings.Builder) {
	diag := d.currentDiagnostics

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Width(14).
		Align(lipgloss.Right)

	// Health status with color
	statusColor := d.getHealthColor(diag.OverallState)
	statusStyle := lipgloss.NewStyle().
		Foreground(statusColor).
		Bold(true)

	b.WriteString(headerStyle.Render("OVERVIEW"))
	b.WriteString("\n\n")

	b.WriteString(labelStyle.Render("Status:"))
	b.WriteString("  ")
	b.WriteString(statusStyle.Render(strings.ToUpper(string(diag.OverallState))))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Updated:"))
	b.WriteString("  ")
	b.WriteString(d.styles.MenuItem.Render(diag.Timestamp.Format("15:04:05")))
	b.WriteString("\n\n")

	// Errors
	if len(diag.Errors) > 0 {
		b.WriteString(headerStyle.Render("ERRORS"))
		b.WriteString("\n")
		for _, err := range diag.Errors {
			errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))
			b.WriteString(errStyle.Render(fmt.Sprintf("  ✗ %s", err)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Warnings
	if len(diag.Warnings) > 0 {
		b.WriteString(headerStyle.Render("WARNINGS"))
		b.WriteString("\n")
		for _, warn := range diag.Warnings {
			warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
			b.WriteString(warnStyle.Render(fmt.Sprintf("  ⚠ %s", warn)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Summary stats
	b.WriteString(headerStyle.Render("SUMMARY"))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Sessions:"))
	b.WriteString("  ")
	b.WriteString(d.styles.MenuItem.Render(fmt.Sprintf("%d active", len(diag.Sessions))))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Ports:"))
	b.WriteString("  ")
	b.WriteString(d.styles.MenuItem.Render(fmt.Sprintf("%d allocated", len(diag.Ports))))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Worktrees:"))
	b.WriteString("  ")
	b.WriteString(d.styles.MenuItem.Render(fmt.Sprintf("%d active", len(diag.Worktrees))))
	b.WriteString("\n")

	networkStatus := "Online"
	if !diag.Network.IsOnline {
		networkStatus = "Offline"
	}
	b.WriteString(labelStyle.Render("Network:"))
	b.WriteString("  ")
	b.WriteString(d.styles.MenuItem.Render(networkStatus))
	b.WriteString("\n")

	// If everything is healthy, show success message
	if diag.OverallState == diagnostics.HealthHealthy {
		b.WriteString("\n")
		successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1"))
		b.WriteString(successStyle.Render("  ✓ All systems operational"))
		b.WriteString("\n")
	}
}

func (d *DiagnosticsPanel) renderPorts(b *strings.Builder) {
	diag := d.currentDiagnostics

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	b.WriteString(headerStyle.Render(fmt.Sprintf("PORT ALLOCATION (%d)", len(diag.Ports))))
	b.WriteString("\n\n")

	if len(diag.Ports) == 0 {
		b.WriteString(d.styles.MenuItem.Render("  No ports allocated"))
		b.WriteString("\n")
		return
	}

	// Table header
	tableHeaderStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Bold(true)

	b.WriteString(tableHeaderStyle.Render("  PORT     BEAD ID          STATUS"))
	b.WriteString("\n")
	b.WriteString(d.styles.MenuItem.Render("  ───────────────────────────────────────────"))
	b.WriteString("\n")

	// Table rows
	for _, port := range diag.Ports {
		status := "available"
		statusColor := lipgloss.Color("#a6e3a1") // Green

		if port.InUse {
			status = "in use"
			statusColor = lipgloss.Color("#89b4fa") // Blue
		}
		if !port.Available {
			status = "CONFLICT"
			statusColor = lipgloss.Color("#f38ba8") // Red
		}

		statusStyle := lipgloss.NewStyle().Foreground(statusColor)

		line := fmt.Sprintf("  %-8d %-16s %s",
			port.Port,
			truncateDiagString(port.BeadID, 16),
			statusStyle.Render(status),
		)
		b.WriteString(line)
		b.WriteString("\n")
	}
}

func (d *DiagnosticsPanel) renderSessions(b *strings.Builder) {
	diag := d.currentDiagnostics

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	b.WriteString(headerStyle.Render(fmt.Sprintf("ACTIVE SESSIONS (%d)", len(diag.Sessions))))
	b.WriteString("\n\n")

	if len(diag.Sessions) == 0 {
		b.WriteString(d.styles.MenuItem.Render("  No active sessions"))
		b.WriteString("\n")
		return
	}

	// Table header
	tableHeaderStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Bold(true)

	b.WriteString(tableHeaderStyle.Render("  BEAD ID          STATE        UPTIME"))
	b.WriteString("\n")
	b.WriteString(d.styles.MenuItem.Render("  ─────────────────────────────────────────"))
	b.WriteString("\n")

	// Table rows
	for _, session := range diag.Sessions {
		stateIcon := session.State.Icon()
		uptimeStr := "-"
		if session.Uptime > 0 {
			uptimeStr = formatDuration(session.Uptime)
		}

		line := fmt.Sprintf("  %-16s %s %-7s  %s",
			truncateDiagString(session.BeadID, 16),
			stateIcon,
			session.State,
			uptimeStr,
		)
		b.WriteString(d.styles.MenuItem.Render(line))
		b.WriteString("\n")

		// Show worktree path if available
		if session.Worktree != "" {
			pathStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
			b.WriteString(pathStyle.Render(fmt.Sprintf("    └─ %s", session.Worktree)))
			b.WriteString("\n")
		}
	}
}

func (d *DiagnosticsPanel) renderWorktrees(b *strings.Builder) {
	diag := d.currentDiagnostics

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	b.WriteString(headerStyle.Render(fmt.Sprintf("WORKTREES (%d)", len(diag.Worktrees))))
	b.WriteString("\n\n")

	if len(diag.Worktrees) == 0 {
		b.WriteString(d.styles.MenuItem.Render("  No active worktrees"))
		b.WriteString("\n")
		return
	}

	// List worktrees
	for _, wt := range diag.Worktrees {
		healthIcon := "✓"
		healthColor := lipgloss.Color("#a6e3a1")

		if !wt.IsHealthy {
			healthIcon = "✗"
			healthColor = lipgloss.Color("#f38ba8")
		}

		healthStyle := lipgloss.NewStyle().Foreground(healthColor)

		b.WriteString(healthStyle.Render(fmt.Sprintf("  %s ", healthIcon)))
		b.WriteString(d.styles.MenuItem.Render(wt.BeadID))
		b.WriteString("\n")

		pathStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
		b.WriteString(pathStyle.Render(fmt.Sprintf("    %s", wt.Path)))
		b.WriteString("\n")

		if wt.Branch != "" {
			b.WriteString(pathStyle.Render(fmt.Sprintf("    branch: %s", wt.Branch)))
			b.WriteString("\n")
		}

		b.WriteString("\n")
	}
}

func (d *DiagnosticsPanel) renderNetwork(b *strings.Builder) {
	diag := d.currentDiagnostics

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Width(14).
		Align(lipgloss.Right)

	b.WriteString(headerStyle.Render("NETWORK STATUS"))
	b.WriteString("\n\n")

	// Status
	statusIcon := "✓"
	statusText := "Online"
	statusColor := lipgloss.Color("#a6e3a1")

	if !diag.Network.IsOnline {
		statusIcon = "✗"
		statusText = "Offline"
		statusColor = lipgloss.Color("#f38ba8")
	}

	statusStyle := lipgloss.NewStyle().Foreground(statusColor)

	b.WriteString(labelStyle.Render("Status:"))
	b.WriteString("  ")
	b.WriteString(statusStyle.Render(fmt.Sprintf("%s %s", statusIcon, statusText)))
	b.WriteString("\n")

	// Last check
	lastCheckStr := "Never"
	if !diag.Network.LastCheck.IsZero() {
		lastCheckStr = diag.Network.LastCheck.Format("15:04:05")
		elapsed := time.Since(diag.Network.LastCheck)
		lastCheckStr += fmt.Sprintf(" (%s ago)", formatDuration(elapsed))
	}

	b.WriteString(labelStyle.Render("Last Check:"))
	b.WriteString("  ")
	b.WriteString(d.styles.MenuItem.Render(lastCheckStr))
	b.WriteString("\n")

	// Latency
	if diag.Network.Latency > 0 {
		b.WriteString(labelStyle.Render("Latency:"))
		b.WriteString("  ")
		b.WriteString(d.styles.MenuItem.Render(fmt.Sprintf("%dms", diag.Network.Latency.Milliseconds())))
		b.WriteString("\n")
	}

	// Network-dependent features
	b.WriteString("\n")
	b.WriteString(headerStyle.Render("NETWORK FEATURES"))
	b.WriteString("\n\n")

	features := []struct {
		name     string
		requires bool
	}{
		{"GitHub PR Creation", true},
		{"Git Push/Pull", true},
		{"Package Installation", true},
		{"Claude API", true},
	}

	for _, feature := range features {
		icon := "✓"
		color := lipgloss.Color("#a6e3a1")

		if feature.requires && !diag.Network.IsOnline {
			icon = "✗"
			color = lipgloss.Color("#6c7086")
		}

		featureStyle := lipgloss.NewStyle().Foreground(color)
		b.WriteString(featureStyle.Render(fmt.Sprintf("  %s %s", icon, feature.name)))
		b.WriteString("\n")
	}
}

func (d *DiagnosticsPanel) renderSystem(b *strings.Builder) {
	diag := d.currentDiagnostics

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Width(16).
		Align(lipgloss.Right)

	b.WriteString(headerStyle.Render("SYSTEM INFORMATION"))
	b.WriteString("\n\n")

	// Runtime
	b.WriteString(labelStyle.Render("Go Version:"))
	b.WriteString("  ")
	b.WriteString(d.styles.MenuItem.Render(diag.System.GoVersion))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("OS/Arch:"))
	b.WriteString("  ")
	b.WriteString(d.styles.MenuItem.Render(fmt.Sprintf("%s/%s", diag.System.OS, diag.System.Arch)))
	b.WriteString("\n")

	// Resources
	b.WriteString("\n")
	b.WriteString(headerStyle.Render("RESOURCES"))
	b.WriteString("\n\n")

	b.WriteString(labelStyle.Render("Goroutines:"))
	b.WriteString("  ")
	b.WriteString(d.styles.MenuItem.Render(fmt.Sprintf("%d", diag.System.NumGoroutine)))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Memory:"))
	b.WriteString("  ")
	b.WriteString(d.styles.MenuItem.Render(formatBytes(diag.System.MemoryUsage)))
	b.WriteString("\n")
}

func (d *DiagnosticsPanel) renderFooter() string {
	hints := []string{
		"[Tab] Switch section",
		"[1-6] Jump to section",
		"[j/k] Scroll",
		"[r] Refresh",
		"[q/Esc] Close",
	}

	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))

	var parts []string
	for _, hint := range hints {
		// Split on brackets to style keys differently
		styledHint := strings.ReplaceAll(hint, "[", keyStyle.Render("["))
		styledHint = strings.ReplaceAll(styledHint, "]", keyStyle.Render("]"))
		parts = append(parts, styledHint)
	}

	footer := hintStyle.Render(strings.Join(parts, "  "))

	// Add scroll indicator if needed
	if d.maxScroll() > 0 {
		scrollInfo := fmt.Sprintf("  (line %d/%d)", d.scrollY+1, d.contentHeight)
		footer += hintStyle.Render(scrollInfo)
	}

	return footer
}

// Helper methods

func (d *DiagnosticsPanel) getSectionName() string {
	switch d.activeSection {
	case SectionOverview:
		return "Overview"
	case SectionPorts:
		return "Ports"
	case SectionSessions:
		return "Sessions"
	case SectionWorktrees:
		return "Worktrees"
	case SectionNetwork:
		return "Network"
	case SectionSystem:
		return "System"
	default:
		return "Unknown"
	}
}

func (d *DiagnosticsPanel) getHealthColor(status diagnostics.HealthStatus) lipgloss.Color {
	switch status {
	case diagnostics.HealthHealthy:
		return lipgloss.Color("#a6e3a1") // Green
	case diagnostics.HealthDegraded:
		return lipgloss.Color("#f9e2af") // Yellow
	case diagnostics.HealthCritical:
		return lipgloss.Color("#f38ba8") // Red
	default:
		return lipgloss.Color("#ffffff") // White
	}
}

func (d *DiagnosticsPanel) maxScroll() int {
	return max(0, d.contentHeight-d.viewHeight)
}

// Utility functions

func truncateDiagString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen < 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func formatDuration(dur time.Duration) string {
	if dur < time.Minute {
		return fmt.Sprintf("%ds", int(dur.Seconds()))
	}
	if dur < time.Hour {
		return fmt.Sprintf("%dm", int(dur.Minutes()))
	}
	hours := int(dur.Hours())
	minutes := int(dur.Minutes()) % 60
	if minutes == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, minutes)
}

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
