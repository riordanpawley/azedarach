package overlay

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
)

const (
	attachmentPreviewMaxBytes = 16 * 1024
	attachmentPreviewMaxLines = 6
	attachmentPreviewTimeout  = 200 * time.Millisecond
)

type attachmentPreviewState struct {
	attachmentID string
	title        string
	lines        []string
	err          string
}

type attachmentPreviewLoadedMsg struct {
	attachmentID string
	preview      attachmentPreviewState
	err          error
}

func loadAttachmentPreview(att attachment.Attachment) tea.Cmd {
	attachmentID := strings.TrimSpace(att.ID)
	return func() tea.Msg {
		preview, err := buildAttachmentPreview(att)
		if err != nil {
			return attachmentPreviewLoadedMsg{attachmentID: attachmentID, err: err}
		}
		return attachmentPreviewLoadedMsg{attachmentID: attachmentID, preview: preview}
	}
}

func buildAttachmentPreview(att attachment.Attachment) (attachmentPreviewState, error) {
	state := attachmentPreviewState{
		attachmentID: att.ID,
		title:        previewTitle(att),
	}
	if isPreviewableImageAttachment(att) {
		state.lines = imagePreviewLines(att)
		return state, nil
	}
	if !isReadableDocumentAttachment(att) {
		state.lines = []string{"Preview unavailable for this file type.", "Open in viewer to inspect the attachment."}
		return state, nil
	}

	source, truncated, err := readAttachmentPreviewSource(att.Path)
	if err != nil {
		return state, err
	}
	if strings.TrimSpace(source) == "" {
		state.lines = []string{"Document is empty."}
		return state, nil
	}
	if attachment.IsMarkdown(att) {
		ctx, cancel := context.WithTimeout(context.Background(), attachmentPreviewTimeout)
		defer cancel()
		rendered, err := renderMarkdownDocument(ctx, source, 48)
		if err == nil {
			source = rendered
		}
	}
	state.lines = firstNonEmptyPreviewLines(source, attachmentPreviewMaxLines)
	if truncated {
		state.lines = append(state.lines, "...")
	}
	if len(state.lines) == 0 {
		state.lines = []string{"Document is empty."}
	}
	return state, nil
}

func previewTitle(att attachment.Attachment) string {
	switch {
	case isPreviewableImageAttachment(att):
		return "Image Preview"
	case attachment.IsMarkdown(att):
		return "Markdown Preview"
	case isReadableDocumentAttachment(att):
		return "Document Preview"
	default:
		return "Attachment Preview"
	}
}

func isPreviewableImageAttachment(att attachment.Attachment) bool {
	if attachment.IsImage(att) {
		return true
	}
	switch strings.ToLower(filepath.Ext(att.Filename)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".tif", ".tiff", ".bmp":
		return true
	default:
		return false
	}
}

func imagePreviewLines(att attachment.Attachment) []string {
	lines := []string{"Inline image rendering is not available in this terminal."}
	if width, height, format := imageDimensions(att.Path); width > 0 && height > 0 {
		if format != "" {
			lines = append(lines, fmt.Sprintf("%dx%d %s image", width, height, strings.ToUpper(format)))
		} else {
			lines = append(lines, fmt.Sprintf("%dx%d image", width, height))
		}
	}
	lines = append(lines, "Press Enter/v for full preview or o to open externally.")
	return lines
}

func imageDimensions(path string) (int, int, string) {
	if strings.TrimSpace(path) == "" {
		return 0, 0, ""
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, ""
	}
	defer file.Close()
	cfg, format, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0, ""
	}
	return cfg.Width, cfg.Height, format
}

func isReadableDocumentAttachment(att attachment.Attachment) bool {
	if attachment.IsMarkdown(att) {
		return true
	}
	mimeType := strings.ToLower(strings.TrimSpace(att.MimeType))
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	switch mimeType {
	case "application/json", "application/yaml", "application/x-yaml":
		return true
	}
	switch strings.ToLower(filepath.Ext(att.Filename)) {
	case ".txt", ".log", ".json", ".csv", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func readAttachmentPreviewSource(path string) (string, bool, error) {
	if strings.TrimSpace(path) == "" {
		return "", false, fmt.Errorf("attachment has no local path")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("read preview: %w", err)
	}
	defer file.Close()

	var buf bytes.Buffer
	limited := io.LimitReader(file, attachmentPreviewMaxBytes+1)
	if _, err := io.Copy(&buf, limited); err != nil {
		return "", false, fmt.Errorf("read preview: %w", err)
	}
	data := buf.Bytes()
	truncated := len(data) > attachmentPreviewMaxBytes
	if truncated {
		data = data[:attachmentPreviewMaxBytes]
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		for trim := 0; trim < utf8.UTFMax; trim++ {
			if len(data) == 0 || utf8.Valid(data) {
				break
			}
			data = data[:len(data)-1]
		}
	}
	if !utf8.Valid(data) {
		return "", false, fmt.Errorf("preview is not valid UTF-8")
	}
	return string(data), truncated, nil
}

func firstNonEmptyPreviewLines(source string, limit int) []string {
	limit = max(1, limit)
	rawLines := strings.Split(strings.TrimRight(source, "\n"), "\n")
	lines := make([]string, 0, min(limit, len(rawLines)))
	for _, line := range rawLines {
		trimmed := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmed) == "" && len(lines) == 0 {
			continue
		}
		lines = append(lines, trimmed)
		if len(lines) >= limit {
			break
		}
	}
	return lines
}

func renderAttachmentPreviewBlock(styles *Styles, preview attachmentPreviewState, width int) string {
	if styles == nil {
		styles = New()
	}
	title := strings.TrimSpace(preview.title)
	if title == "" {
		title = "Attachment Preview"
	}
	width = max(12, width)
	lines := []string{styles.MenuHeader.Render(truncateToCellWidth(title, width))}
	body := preview.lines
	if preview.err != "" {
		body = []string{"Preview unavailable: " + preview.err}
	}
	if len(body) == 0 {
		body = []string{"Preview loading..."}
	}
	bodyStyle := styles.MenuItemDisabled
	for _, line := range body {
		for _, wrapped := range wrapDescriptionLines(line, width) {
			lines = append(lines, bodyStyle.Render(truncateToCellWidth(wrapped, width)))
		}
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true).
		BorderForeground(lipgloss.Color("#45475a")).
		Padding(0, 1).
		Width(width + 2).
		MaxWidth(width + 2).
		Render(strings.Join(lines, "\n"))
}

func truncateToCellWidth(value string, width int) string {
	width = max(1, width)
	return ansi.Truncate(value, width, "...")
}
