package overlay

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

type dialogLayoutMode int

const (
	dialogLayoutSplit dialogLayoutMode = iota
	dialogLayoutStacked
)

const (
	dialogFullscreenWidthThreshold  = 120
	dialogFullscreenHeightThreshold = 30
)

type dialogLayoutConfig struct {
	styles            *Styles
	width             int
	height            int
	title             string
	rightSectionTitle string
	breakpoint        int
	gap               int
	minLeft           int
	minRight          int
	leftFocused       bool
	rightFocused      bool
	renderLeft        func(mode dialogLayoutMode, width, height int) string
	renderRight       func(mode dialogLayoutMode, width, height int) string
}

func clampDialogSize(desiredWidth, desiredHeight, viewportWidth, viewportHeight int) (int, int) {
	if viewportWidth > 0 && viewportHeight > 0 &&
		(viewportWidth <= dialogFullscreenWidthThreshold || viewportHeight <= dialogFullscreenHeightThreshold) {
		return max(1, viewportWidth-2), max(1, viewportHeight-2)
	}

	width := max(1, desiredWidth)
	height := max(1, desiredHeight)
	if viewportWidth > 0 {
		maxWidth := max(44, viewportWidth-4)
		if width > maxWidth {
			width = maxWidth
		}
	}
	if viewportHeight > 0 {
		maxHeight := max(10, viewportHeight-2)
		if height > maxHeight {
			height = maxHeight
		}
	}
	return width, height
}

func renderDialogTwoPane(cfg dialogLayoutConfig) string {
	contentWidth := max(1, cfg.width-2)
	contentHeight := max(1, cfg.height-2)
	titleLine := cfg.styles.MenuItemActive.Render(cfg.title)
	separator := cfg.styles.Separator.Render(strings.Repeat("─", max(6, contentWidth)))
	bodyHeight := max(6, contentHeight-2)

	if contentWidth < cfg.breakpoint {
		return renderDialogStacked(cfg, contentWidth, contentHeight, bodyHeight, titleLine, separator)
	}
	return renderDialogSplit(cfg, contentWidth, contentHeight, bodyHeight, titleLine, separator)
}

func renderDialogSplit(
	cfg dialogLayoutConfig,
	contentWidth, contentHeight, bodyHeight int,
	titleLine, separator string,
) string {
	usableWidth := max(16, contentWidth-cfg.gap)
	leftWidth := (usableWidth * 2) / 3
	maxLeft := max(cfg.minLeft, usableWidth-cfg.minRight)
	if leftWidth > maxLeft {
		leftWidth = maxLeft
	}
	if leftWidth < cfg.minLeft {
		leftWidth = cfg.minLeft
	}

	rightWidth := usableWidth - leftWidth
	if rightWidth < cfg.minRight {
		rightWidth = cfg.minRight
		leftWidth = max(cfg.minLeft, usableWidth-rightWidth)
	}

	leftStyle := lipgloss.NewStyle()
	if cfg.leftFocused {
		leftStyle = leftStyle.BorderLeft(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(styles.Blue).PaddingLeft(1)
	}
	leftView := leftStyle.
		Width(leftWidth).
		MaxWidth(leftWidth).
		Height(bodyHeight).
		MaxHeight(bodyHeight).
		Render(cfg.renderLeft(dialogLayoutSplit, leftWidth, bodyHeight))

	rightBody := lipgloss.JoinVertical(
		lipgloss.Left,
		cfg.styles.MenuItemActive.Render(cfg.rightSectionTitle),
		cfg.styles.Separator.Render(strings.Repeat("─", max(6, rightWidth))),
		cfg.renderRight(dialogLayoutSplit, rightWidth, bodyHeight),
	)
	rightStyle := lipgloss.NewStyle()
	if cfg.rightFocused {
		rightStyle = rightStyle.BorderLeft(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(styles.Blue).PaddingLeft(1)
	}
	rightView := rightStyle.
		Width(rightWidth).
		MaxWidth(rightWidth).
		Height(bodyHeight).
		MaxHeight(bodyHeight).
		Render(rightBody)

	body := lipgloss.JoinHorizontal(
		lipgloss.Top,
		leftView,
		lipgloss.NewStyle().Width(cfg.gap).Render(""),
		rightView,
	)
	content := lipgloss.JoinVertical(lipgloss.Left, titleLine, separator, body)
	return lipgloss.NewStyle().
		Width(contentWidth).
		Height(contentHeight).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.Surface2).
		Render(content)
}

func renderDialogStacked(
	cfg dialogLayoutConfig,
	contentWidth, contentHeight, bodyHeight int,
	titleLine, separator string,
) string {
	headerHeight := 2
	actionsHeight := max(8, bodyHeight/3)
	detailHeight := max(4, bodyHeight-actionsHeight-cfg.gap)

	leftView := lipgloss.NewStyle().
		Width(contentWidth).
		MaxWidth(contentWidth).
		Height(detailHeight).
		MaxHeight(detailHeight).
		Render(cfg.renderLeft(dialogLayoutStacked, contentWidth, detailHeight))

	rightBody := lipgloss.JoinVertical(
		lipgloss.Left,
		cfg.styles.MenuItemActive.Render(cfg.rightSectionTitle),
		cfg.styles.Separator.Render(strings.Repeat("─", max(6, contentWidth))),
		cfg.renderRight(dialogLayoutStacked, contentWidth, actionsHeight),
	)
	rightView := lipgloss.NewStyle().
		Width(contentWidth).
		MaxWidth(contentWidth).
		Height(actionsHeight).
		MaxHeight(actionsHeight).
		Render(rightBody)

	body := lipgloss.JoinVertical(
		lipgloss.Left,
		leftView,
		lipgloss.NewStyle().Height(cfg.gap).Render(""),
		rightView,
	)
	content := lipgloss.JoinVertical(lipgloss.Left, titleLine, separator, body)
	return lipgloss.NewStyle().
		Width(contentWidth).
		Height(max(6, contentHeight-headerHeight)).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.Surface2).
		Render(content)
}
