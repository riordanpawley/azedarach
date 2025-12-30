package diff

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// Styles holds all diff viewer-specific styles using Catppuccin Macchiato theme
type Styles struct {
	// Container styles
	Overlay lipgloss.Style
	Title   lipgloss.Style

	// File list styles
	FileHeader         lipgloss.Style
	FileHeaderSelected lipgloss.Style
	FileHeaderExpanded lipgloss.Style
	FilePath           lipgloss.Style
	FilePathSelected   lipgloss.Style
	FileStats          lipgloss.Style
	FileStatsAdd       lipgloss.Style
	FileStatsDel       lipgloss.Style

	// File status badges
	StatusModified lipgloss.Style
	StatusAdded    lipgloss.Style
	StatusDeleted  lipgloss.Style
	StatusRenamed  lipgloss.Style

	// Diff content styles
	AddLine     lipgloss.Style
	DeleteLine  lipgloss.Style
	ContextLine lipgloss.Style
	HunkHeader  lipgloss.Style
	LineNumber  lipgloss.Style

	// Navigation hints
	Footer   lipgloss.Style
	KeyHint  lipgloss.Style
	Dimmed   lipgloss.Style
}

// New creates a new Styles instance using Catppuccin Macchiato colors
func New() *Styles {
	return &Styles{
		Overlay: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(styles.Surface2).
			Background(styles.Base).
			Padding(1, 2),

		Title: lipgloss.NewStyle().
			Foreground(styles.Text).
			Bold(true).
			MarginBottom(1),

		FileHeader: lipgloss.NewStyle().
			Foreground(styles.Text).
			Bold(true),

		FileHeaderSelected: lipgloss.NewStyle().
			Foreground(styles.Blue).
			Bold(true),

		FileHeaderExpanded: lipgloss.NewStyle().
			Foreground(styles.Mauve).
			Bold(true),

		FilePath: lipgloss.NewStyle().
			Foreground(styles.Text),

		FilePathSelected: lipgloss.NewStyle().
			Foreground(styles.Blue),

		FileStats: lipgloss.NewStyle().
			Foreground(styles.Subtext0),

		FileStatsAdd: lipgloss.NewStyle().
			Foreground(styles.Green).
			Bold(true),

		FileStatsDel: lipgloss.NewStyle().
			Foreground(styles.Red).
			Bold(true),

		StatusModified: lipgloss.NewStyle().
			Foreground(styles.Yellow).
			Bold(true),

		StatusAdded: lipgloss.NewStyle().
			Foreground(styles.Green).
			Bold(true),

		StatusDeleted: lipgloss.NewStyle().
			Foreground(styles.Red).
			Bold(true),

		StatusRenamed: lipgloss.NewStyle().
			Foreground(styles.Blue).
			Bold(true),

		AddLine: lipgloss.NewStyle().
			Foreground(styles.Green),

		DeleteLine: lipgloss.NewStyle().
			Foreground(styles.Red),

		ContextLine: lipgloss.NewStyle().
			Foreground(styles.Subtext0),

		HunkHeader: lipgloss.NewStyle().
			Foreground(styles.Blue).
			Bold(true),

		LineNumber: lipgloss.NewStyle().
			Foreground(styles.Overlay1).
			Width(5).
			Align(lipgloss.Right),

		Footer: lipgloss.NewStyle().
			Foreground(styles.Subtext0).
			MarginTop(1),

		KeyHint: lipgloss.NewStyle().
			Foreground(styles.Yellow).
			Bold(true),

		Dimmed: lipgloss.NewStyle().
			Foreground(styles.Overlay0),
	}
}

// FileStatusStyle returns the appropriate style for a file status
func (s *Styles) FileStatusStyle(status FileStatus) lipgloss.Style {
	switch status {
	case FileAdded:
		return s.StatusAdded
	case FileDeleted:
		return s.StatusDeleted
	case FileRenamed:
		return s.StatusRenamed
	case FileModified:
		return s.StatusModified
	default:
		return s.StatusModified
	}
}

// FileStatusBadge returns a styled badge for a file status
func (s *Styles) FileStatusBadge(status FileStatus) string {
	style := s.FileStatusStyle(status)

	var badge string
	switch status {
	case FileAdded:
		badge = "A"
	case FileDeleted:
		badge = "D"
	case FileRenamed:
		badge = "R"
	case FileModified:
		badge = "M"
	default:
		badge = "?"
	}

	return style.Render(badge)
}
