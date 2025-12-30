package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// DevServerInfo represents a dev server with its status
type DevServerInfo struct {
	ID     string
	Name   string
	Port   int
	Status string // "running", "stopped", "error"
	Uptime time.Duration
}

// DevServerOverlay is a menu overlay for dev server management
type DevServerOverlay struct {
	servers  []DevServerInfo
	cursor   int
	beadID   string
	onToggle func(serverID string) tea.Cmd
	onView   func(serverID string) tea.Cmd
	onRestart func(serverID string) tea.Cmd
	onClose  func() tea.Cmd
	styles   *Styles
}

// NewDevServerOverlay creates a new dev server overlay
func NewDevServerOverlay(
	servers []DevServerInfo,
	beadID string,
	onToggle func(serverID string) tea.Cmd,
	onView func(serverID string) tea.Cmd,
	onRestart func(serverID string) tea.Cmd,
	onClose func() tea.Cmd,
) *DevServerOverlay {
	return &DevServerOverlay{
		servers:   servers,
		cursor:    0,
		beadID:    beadID,
		onToggle:  onToggle,
		onView:    onView,
		onRestart: onRestart,
		onClose:   onClose,
		styles:    New(),
	}
}

// Init initializes the overlay
func (m *DevServerOverlay) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m *DevServerOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			if m.onClose != nil {
				return m, m.onClose()
			}
			return m, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			m.moveCursorDown()
			return m, nil

		case "k", "up":
			m.moveCursorUp()
			return m, nil

		case "enter":
			// Toggle start/stop
			if m.cursor >= 0 && m.cursor < len(m.servers) && m.onToggle != nil {
				return m, m.onToggle(m.servers[m.cursor].ID)
			}
			return m, nil

		case "v":
			// View server output
			if m.cursor >= 0 && m.cursor < len(m.servers) && m.onView != nil {
				return m, m.onView(m.servers[m.cursor].ID)
			}
			return m, nil

		case "r":
			// Restart server
			if m.cursor >= 0 && m.cursor < len(m.servers) && m.onRestart != nil {
				return m, m.onRestart(m.servers[m.cursor].ID)
			}
			return m, nil
		}
	}

	return m, nil
}

// View renders the overlay
func (m *DevServerOverlay) View() string {
	var b strings.Builder

	if len(m.servers) == 0 {
		b.WriteString(m.styles.MenuItemDisabled.Render("No dev servers configured"))
		b.WriteString("\n\n")
		b.WriteString(m.styles.Footer.Render("Press Escape to close"))
		return b.String()
	}

	for i, server := range m.servers {
		// Determine status style and indicator
		var statusStyle lipgloss.Style
		var statusText string
		switch server.Status {
		case "running":
			statusStyle = lipgloss.NewStyle().Foreground(styles.Green).Bold(true)
			statusText = "●"
		case "stopped":
			statusStyle = lipgloss.NewStyle().Foreground(styles.Overlay0)
			statusText = "○"
		case "error":
			statusStyle = lipgloss.NewStyle().Foreground(styles.Red).Bold(true)
			statusText = "✗"
		default:
			statusStyle = lipgloss.NewStyle().Foreground(styles.Overlay0)
			statusText = "?"
		}

		// Determine item style based on cursor position
		var nameStyle lipgloss.Style
		if i == m.cursor {
			nameStyle = m.styles.MenuItemActive
		} else {
			nameStyle = m.styles.MenuItem
		}

		// Format uptime
		var uptimeStr string
		if server.Status == "running" && server.Uptime > 0 {
			uptimeStr = formatUptime(server.Uptime)
		} else {
			uptimeStr = "—"
		}

		// Format line: [status] name :port uptime
		line := fmt.Sprintf("%s %s :%d  %s",
			statusStyle.Render(statusText),
			nameStyle.Render(server.Name),
			server.Port,
			m.styles.MenuItemDisabled.Render(uptimeStr),
		)

		b.WriteString(line)
		b.WriteString("\n")
	}

	// Add footer with keybindings
	b.WriteString("\n")
	footer := m.styles.Footer.Render(
		"Enter: toggle • v: view output • r: restart • Esc: close",
	)
	b.WriteString(footer)

	return b.String()
}

// Title returns the overlay title
func (m *DevServerOverlay) Title() string {
	return "Dev Servers"
}

// Size returns the overlay dimensions
func (m *DevServerOverlay) Size() (width, height int) {
	// Width: enough for server info (name + port + uptime + padding)
	// Height: number of servers + footer + padding
	maxWidth := 50
	for _, server := range m.servers {
		lineWidth := len(server.Name) + 20 // name + port + uptime + decorations
		if lineWidth > maxWidth {
			maxWidth = lineWidth
		}
	}

	height = len(m.servers) + 4 // servers + footer + padding
	if len(m.servers) == 0 {
		height = 6
	}

	width = maxWidth
	return
}

// moveCursorDown moves the cursor to the next server
func (m *DevServerOverlay) moveCursorDown() {
	if len(m.servers) == 0 {
		return
	}
	m.cursor = (m.cursor + 1) % len(m.servers)
}

// moveCursorUp moves the cursor to the previous server
func (m *DevServerOverlay) moveCursorUp() {
	if len(m.servers) == 0 {
		return
	}
	m.cursor = (m.cursor - 1 + len(m.servers)) % len(m.servers)
}

// formatUptime formats a duration into a human-readable uptime string
func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if hours < 24 {
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
	days := hours / 24
	hours = hours % 24
	return fmt.Sprintf("%dd%dh", days, hours)
}
