package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
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
	twoPaneDialogChrome
	servers        []DevServerInfo
	cursor         int
	issueID        string
	onToggle       func(serverID string) tea.Cmd
	onView         func(serverID string) tea.Cmd
	onRestart      func(serverID string) tea.Cmd
	onClose        func() tea.Cmd
	styles         *Styles
	viewportWidth  int
	viewportHeight int
}

// NewDevServerOverlay creates a new dev server overlay
func NewDevServerOverlay(
	servers []DevServerInfo,
	issueID string,
	onToggle func(serverID string) tea.Cmd,
	onView func(serverID string) tea.Cmd,
	onRestart func(serverID string) tea.Cmd,
	onClose func() tea.Cmd,
) *DevServerOverlay {
	return &DevServerOverlay{
		servers:   servers,
		cursor:    0,
		issueID:   issueID,
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
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.viewportWidth = msg.Width
		}
		if msg.Height > 0 {
			m.viewportHeight = msg.Height
		}
	}

	return m, nil
}

// View renders the overlay
func (m *DevServerOverlay) View() string {
	width, height := clampDialogSize(72, m.viewHeight(), m.viewportWidth, m.viewportHeight)
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            m.styles,
		width:             width,
		height:            height,
		title:             "DEV SERVERS",
		rightSectionTitle: "Actions",
		breakpoint:        76,
		gap:               3,
		minLeft:           36,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return m.renderServerList()
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(m.styles, []keybinds.Binding{
				{Key: "Enter", Description: "toggle"},
				{Key: "v", Description: "view output"},
				{Key: "r", Description: "restart"},
				{Key: "Esc", Description: "close"},
			})
		},
	})
}

// Title returns the overlay title
func (m *DevServerOverlay) Title() string {
	return "Dev Servers"
}

// Size returns the overlay dimensions
func (m *DevServerOverlay) Size() (width, height int) {
	view := m.View()
	return lipgloss.Width(view), lipgloss.Height(view)
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

func (m *DevServerOverlay) renderServerList() string {
	var b strings.Builder
	if len(m.servers) == 0 {
		b.WriteString(m.styles.MenuItemDisabled.Render("No dev servers configured"))
		return b.String()
	}

	for i, server := range m.servers {
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

		nameStyle := m.styles.MenuItem
		if i == m.cursor {
			nameStyle = m.styles.MenuItemActive
		}

		uptimeStr := "—"
		if server.Status == "running" && server.Uptime > 0 {
			uptimeStr = formatUptime(server.Uptime)
		}

		line := fmt.Sprintf("%s %s :%d  %s",
			statusStyle.Render(statusText),
			nameStyle.Render(server.Name),
			server.Port,
			m.styles.MenuItemDisabled.Render(uptimeStr),
		)
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *DevServerOverlay) viewHeight() int {
	if len(m.servers) == 0 {
		return 12
	}
	return max(12, len(m.servers)+8)
}
