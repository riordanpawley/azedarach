package diff

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

type DiffClient interface {
	Status(ctx context.Context, worktree string) (*gitservice.GitStatus, error)
	ChangedFiles(ctx context.Context, worktree, baseBranch string) ([]gitservice.ChangedFile, error)
	MergeBase(ctx context.Context, worktree, baseBranch string) (string, error)
}

type PopupOpener func(ctx context.Context, title, command string) error

type loadChangedFilesMsg struct {
	Files      []gitservice.ChangedFile
	DirtyFiles []gitservice.ChangedFile
	DirtyErr   error
	Err        error
}

type popupResultMsg struct {
	Err error
}

// ExternalRefreshMsg triggers a full reload of changed files for an already-open viewer.
type ExternalRefreshMsg struct{}

// DiffViewer displays changed files and opens difftastic popups for selected diffs.
type DiffViewer struct {
	worktree    string
	baseBranch  string
	issueID     string
	gitClient   DiffClient
	openPopup   PopupOpener
	files       []gitservice.ChangedFile
	dirtyFiles  []gitservice.ChangedFile
	cursor      int
	scrollY     int
	styles      *Styles
	viewHeight  int
	viewportW   int
	viewportH   int
	loading     bool
	searchMode  bool
	filterText  string
	err         error
	dirtyErr    error
	popupStatus string
	activeTab   diffViewerTab
}

type diffViewerTab int

const (
	diffViewerTabCommitted diffViewerTab = iota
	diffViewerTabDirty
)

// NewDiffViewer creates a new diff viewer for the specified worktree.
func NewDiffViewer(worktree, baseBranch string, gitClient DiffClient, openPopup PopupOpener) *DiffViewer {
	return &DiffViewer{
		worktree:   strings.TrimSpace(worktree),
		baseBranch: strings.TrimSpace(baseBranch),
		gitClient:  gitClient,
		openPopup:  openPopup,
		files:      []gitservice.ChangedFile{},
		dirtyFiles: []gitservice.ChangedFile{},
		cursor:     0,
		scrollY:    0,
		styles:     New(),
		viewHeight: 20,
		activeTab:  diffViewerTabCommitted,
	}
}

// WithIssueID annotates the viewer title with the issue/task being inspected.
func (d *DiffViewer) WithIssueID(issueID string) *DiffViewer {
	d.issueID = strings.TrimSpace(issueID)
	return d
}

func (d *DiffViewer) loadChangedFilesCmd() tea.Cmd {
	return func() tea.Msg {
		if d.gitClient == nil {
			return loadChangedFilesMsg{Err: fmt.Errorf("git client unavailable")}
		}
		files, err := d.gitClient.ChangedFiles(context.Background(), d.worktree, d.effectiveBaseBranch())
		if err != nil {
			return loadChangedFilesMsg{Err: err}
		}
		status, statusErr := d.gitClient.Status(context.Background(), d.worktree)
		return loadChangedFilesMsg{
			Files:      files,
			DirtyFiles: statusChangedFiles(status),
			DirtyErr:   statusErr,
			Err:        err,
		}
	}
}

func (d *DiffViewer) openSelectedDiffCmd() tea.Cmd {
	filtered := d.filteredFiles()
	if d.cursor < 0 || d.cursor >= len(filtered) {
		return nil
	}
	filePath := strings.TrimSpace(filtered[d.cursor].Path)
	if filePath == "" {
		return nil
	}
	return d.openPopupCmd(filePath, false)
}

func (d *DiffViewer) openPopupCmd(filePath string, all bool) tea.Cmd {
	return func() tea.Msg {
		if d.gitClient == nil {
			return popupResultMsg{Err: fmt.Errorf("git client unavailable")}
		}
		if d.openPopup == nil {
			return popupResultMsg{Err: fmt.Errorf("diff popup unavailable")}
		}

		baseBranch := shellSingleQuote(d.effectiveBaseBranch())
		resolveBaseRef := fmt.Sprintf("BASE_BRANCH=%s; BASE_REF=\"$BASE_BRANCH\"; git rev-parse --verify \"$BASE_REF\" >/dev/null 2>&1 || BASE_REF=\"origin/$BASE_BRANCH\";", baseBranch)

		var title string
		var command string
		if d.activeTab == diffViewerTabDirty {
			if all {
				title = " Dirty Files "
				command = "git status --short && echo \"\" && git diff HEAD --stat --color=always && echo \"\" && ( if command -v difft >/dev/null 2>&1; then DFT_COLOR=always GIT_EXTERNAL_DIFF=\"difft --display=side-by-side\" git diff HEAD; else git diff HEAD --color=always; fi ) | less -RS"
			} else {
				quotedFile := shellSingleQuote(filePath)
				title = " " + filePath + " "
				command = fmt.Sprintf(
					"( if git ls-files --error-unmatch -- %s >/dev/null 2>&1; then if command -v difft >/dev/null 2>&1; then DFT_COLOR=always GIT_EXTERNAL_DIFF=\"difft --display=side-by-side\" git diff HEAD -- %s; else git diff HEAD --color=always -- %s; fi; else printf 'Untracked file: %%s\\n\\n' %s; sed -n '1,240p' %s; fi ) | less -RS",
					quotedFile,
					quotedFile,
					quotedFile,
					quotedFile,
					quotedFile,
				)
			}
		} else if all {
			title = " All Changes "
			command = fmt.Sprintf(
				"%s git diff \"$BASE_REF\"...HEAD --stat --color=always && echo \"\" && ( if command -v difft >/dev/null 2>&1; then DFT_COLOR=always GIT_EXTERNAL_DIFF=\"difft --display=side-by-side\" git diff \"$BASE_REF\"...HEAD; else git diff \"$BASE_REF\"...HEAD --color=always; fi ) | less -RS",
				resolveBaseRef,
			)
		} else {
			title = " " + filePath + " "
			command = fmt.Sprintf(
				"%s ( if command -v difft >/dev/null 2>&1; then DFT_COLOR=always GIT_EXTERNAL_DIFF=\"difft --display=side-by-side\" git diff \"$BASE_REF\"...HEAD -- %s; else git diff \"$BASE_REF\"...HEAD --color=always -- %s; fi ) | less -RS",
				resolveBaseRef,
				shellSingleQuote(filePath),
				shellSingleQuote(filePath),
			)
		}

		if err := d.openPopup(context.Background(), title, command); err != nil {
			return popupResultMsg{Err: err}
		}
		return popupResultMsg{}
	}
}

func (d *DiffViewer) effectiveBaseBranch() string {
	baseBranch := strings.TrimSpace(d.baseBranch)
	if baseBranch == "" {
		return "main"
	}
	return baseBranch
}

func (d *DiffViewer) Init() tea.Cmd {
	d.loading = true
	return d.loadChangedFilesCmd()
}

// Worktree returns the viewer's target worktree path.
func (d *DiffViewer) Worktree() string {
	return strings.TrimSpace(d.worktree)
}

func (d *DiffViewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			d.viewportW = msg.Width
		}
		if msg.Height > 0 {
			d.viewportH = msg.Height
		}
		d.Size()
		return d, nil

	case ExternalRefreshMsg:
		if d.loading {
			return d, nil
		}
		d.loading = true
		d.err = nil
		d.dirtyErr = nil
		d.popupStatus = ""
		return d, d.loadChangedFilesCmd()

	case loadChangedFilesMsg:
		d.loading = false
		d.cursor = 0
		d.scrollY = 0
		d.files = nil
		d.dirtyFiles = nil
		d.searchMode = false
		d.filterText = ""
		d.popupStatus = ""
		if msg.Err != nil || msg.Files == nil {
			if msg.Err != nil {
				d.err = msg.Err
			} else {
				d.err = fmt.Errorf("failed to load changed files")
			}
			return d, nil
		}
		d.err = nil
		d.dirtyErr = msg.DirtyErr
		d.files = msg.Files
		d.dirtyFiles = msg.DirtyFiles
		return d, nil

	case popupResultMsg:
		if msg.Err != nil {
			d.popupStatus = "Popup error: " + msg.Err.Error()
		} else {
			d.popupStatus = "Opened diff popup"
		}
		return d, nil

	case tea.KeyMsg:
		if d.loading {
			return d, nil
		}
		filtered := d.filteredFiles()
		clampCursor := func() {
			filteredNow := d.filteredFiles()
			if len(filteredNow) == 0 {
				d.cursor = 0
				d.scrollY = 0
				return
			}
			if d.cursor >= len(filteredNow) {
				d.cursor = len(filteredNow) - 1
			}
		}
		if d.searchMode {
			switch msg.String() {
			case "esc":
				d.searchMode = false
				d.filterText = ""
				clampCursor()
				return d, nil
			case "enter":
				d.searchMode = false
				clampCursor()
				return d, nil
			case "backspace":
				if len(d.filterText) > 0 {
					d.filterText = d.filterText[:len(d.filterText)-1]
				}
				clampCursor()
				d.ensureCursorVisible()
				return d, nil
			case "j", "down":
				if d.cursor < len(filtered)-1 {
					d.cursor++
					d.ensureCursorVisible()
				}
				return d, nil
			case "k", "up":
				if d.cursor > 0 {
					d.cursor--
					d.ensureCursorVisible()
				}
				return d, nil
			default:
				if len(msg.Runes) == 1 && !msg.Alt {
					d.filterText += string(msg.Runes)
					clampCursor()
					d.ensureCursorVisible()
				}
				return d, nil
			}
		}

		switch msg.String() {
		case "esc", "q":
			return d, func() tea.Msg { return overlay.CloseOverlayMsg{} }

		case "tab":
			d.toggleTab()
			return d, nil

		case "j", "down":
			if d.cursor < len(filtered)-1 {
				d.cursor++
				d.ensureCursorVisible()
			}
			return d, nil

		case "k", "up":
			if d.cursor > 0 {
				d.cursor--
				d.ensureCursorVisible()
			}
			return d, nil

		case "g":
			d.cursor = 0
			d.scrollY = 0
			return d, nil

		case "G":
			if len(filtered) > 0 {
				d.cursor = len(filtered) - 1
				d.ensureCursorVisible()
			}
			return d, nil

		case "r":
			d.loading = true
			d.err = nil
			d.popupStatus = ""
			return d, d.loadChangedFilesCmd()

		case "enter":
			return d, d.openSelectedDiffCmd()

		case "a":
			return d, d.openPopupCmd("", true)

		case "/":
			d.searchMode = true
			return d, nil
		}
	}

	return d, nil
}

func (d *DiffViewer) View() string {
	d.Size()
	if d.loading {
		return d.styles.Dimmed.Render("Loading changed files...")
	}

	if d.err != nil {
		return d.styles.DeleteLine.Render(fmt.Sprintf("Error loading changed files: %v", d.err))
	}

	if len(d.files) == 0 && len(d.dirtyFiles) == 0 {
		return d.styles.Dimmed.Render(fmt.Sprintf("No changes vs %s", d.effectiveBaseBranch()))
	}
	filtered := d.filteredFiles()
	if len(filtered) == 0 {
		if strings.TrimSpace(d.filterText) != "" {
			return lipgloss.JoinVertical(lipgloss.Left, d.renderTabs(), d.styles.Dimmed.Render(fmt.Sprintf("No matches for %q", d.filterText)), d.renderFooter())
		}
		return lipgloss.JoinVertical(lipgloss.Left, d.renderTabs(), d.styles.Dimmed.Render(d.emptyTabMessage()), d.renderFooter())
	}

	var content strings.Builder

	content.WriteString(d.renderTabs())
	content.WriteString("\n")
	if d.dirtyErr != nil && d.activeTab == diffViewerTabDirty {
		content.WriteString(d.styles.DeleteLine.Render(fmt.Sprintf("Could not load dirty files: %v", d.dirtyErr)))
		content.WriteString("\n")
	}

	lines := d.renderFiles(filtered)

	start := d.scrollY
	end := min(d.scrollY+d.viewHeight, len(lines))

	for i := start; i < end; i++ {
		content.WriteString(lines[i])
		content.WriteString("\n")
	}

	if d.popupStatus != "" {
		content.WriteString(d.styles.Footer.Render(d.popupStatus))
		content.WriteString("\n\n")
	}

	footer := d.renderFooter()
	content.WriteString(footer)

	return content.String()
}

func (d *DiffViewer) Title() string {
	scope := ""
	if d.issueID != "" {
		scope = fmt.Sprintf(" | %s", d.issueID)
	}
	filterSuffix := ""
	if strings.TrimSpace(d.filterText) != "" {
		filterSuffix = fmt.Sprintf(" | %d/%d match", len(d.filteredFiles()), len(d.activeFiles()))
	}
	return fmt.Sprintf("Diff%s vs %s (%d committed, %d dirty%s)", scope, d.effectiveBaseBranch(), len(d.files), len(d.dirtyFiles), filterSuffix)
}

func (d *DiffViewer) Size() (width, height int) {
	width, height = overlay.ClampResponsiveDialogSize(100, 30, d.viewportW, d.viewportH)
	d.viewHeight = max(1, height-7)
	return width, height
}

func (d *DiffViewer) renderFiles(files []gitservice.ChangedFile) []string {
	lines := make([]string, 0, len(files))
	for i, file := range files {
		isSelected := i == d.cursor

		var headerStyle lipgloss.Style
		if isSelected {
			headerStyle = d.styles.FileHeaderSelected
		} else {
			headerStyle = d.styles.FileHeader
		}

		cursorMarker := " "
		if isSelected {
			cursorMarker = "▶"
		}

		fileStatus := toViewerFileStatus(file.Status)
		badge := d.styles.FileStatusBadge(fileStatus)
		path := file.Path
		if fileStatus == FileRenamed && file.OldPath != file.Path {
			path = fmt.Sprintf("%s → %s", file.OldPath, file.Path)
		}

		line := lipgloss.JoinHorizontal(
			lipgloss.Left,
			cursorMarker,
			" ",
			badge,
			" ",
			headerStyle.Render(path),
		)
		lines = append(lines, line)
	}
	return lines
}

func (d *DiffViewer) renderFooter() string {
	hints := []keybinds.Binding{
		{Key: "Tab", Description: "switch section"},
		{Key: "j/k", Description: "navigate files"},
		{Key: "Enter", Description: "popup selected"},
		{Key: "a", Description: "popup all"},
		{Key: "r", Description: "refresh"},
		{Key: "q/Esc", Description: "close"},
	}

	status := ""
	filteredCount := len(d.filteredFiles())
	if filteredCount > 0 {
		status = d.styles.Footer.Render(fmt.Sprintf("  [File %d/%d]", d.cursor+1, filteredCount))
	}
	if d.searchMode {
		status += d.styles.Footer.Render(fmt.Sprintf("  /%s", d.filterText))
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		keybinds.RenderInline(hints, " • ", keybinds.Theme{
			KeyStyle:         d.styles.KeyHint,
			DescriptionStyle: d.styles.Footer,
			FooterStyle:      d.styles.Footer,
		}),
		status,
	)
}

func (d *DiffViewer) ensureCursorVisible() {
	if d.cursor < d.scrollY {
		d.scrollY = d.cursor
	} else if d.cursor >= d.scrollY+d.viewHeight {
		d.scrollY = d.cursor - d.viewHeight + 1
	}
}

func (d *DiffViewer) filteredFiles() []gitservice.ChangedFile {
	query := strings.ToLower(strings.TrimSpace(d.filterText))
	files := d.activeFiles()
	if query == "" {
		return files
	}
	filtered := make([]gitservice.ChangedFile, 0, len(files))
	for _, file := range files {
		path := strings.ToLower(file.Path)
		oldPath := strings.ToLower(file.OldPath)
		if strings.Contains(path, query) || strings.Contains(oldPath, query) {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

func (d *DiffViewer) activeFiles() []gitservice.ChangedFile {
	if d.activeTab == diffViewerTabDirty {
		return d.dirtyFiles
	}
	return d.files
}

func (d *DiffViewer) toggleTab() {
	if d.activeTab == diffViewerTabDirty {
		d.activeTab = diffViewerTabCommitted
	} else {
		d.activeTab = diffViewerTabDirty
	}
	d.cursor = 0
	d.scrollY = 0
	d.searchMode = false
	d.filterText = ""
}

func (d *DiffViewer) renderTabs() string {
	committed := fmt.Sprintf("Committed diff (%d)", len(d.files))
	dirty := fmt.Sprintf("Dirty files (%d)", len(d.dirtyFiles))
	if d.activeTab == diffViewerTabCommitted {
		committed = d.styles.FileHeaderSelected.Render(committed)
		dirty = d.styles.Dimmed.Render(dirty)
	} else {
		committed = d.styles.Dimmed.Render(committed)
		dirty = d.styles.FileHeaderSelected.Render(dirty)
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, committed, d.styles.Footer.Render("  |  "), dirty)
}

func (d *DiffViewer) emptyTabMessage() string {
	if d.activeTab == diffViewerTabDirty {
		return "No dirty files"
	}
	return fmt.Sprintf("No committed diff vs %s", d.effectiveBaseBranch())
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func shellSingleQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func statusChangedFiles(status *gitservice.GitStatus) []gitservice.ChangedFile {
	if status == nil {
		return []gitservice.ChangedFile{}
	}

	byPath := make(map[string]gitservice.ChangedFile)
	rank := make(map[string]int)

	put := func(paths []string, status gitservice.DiffFileStatus, priority int) {
		for _, path := range paths {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			if currentPriority, exists := rank[path]; exists && currentPriority >= priority {
				continue
			}
			byPath[path] = gitservice.ChangedFile{
				Path:   path,
				Status: status,
			}
			rank[path] = priority
		}
	}

	put(status.Modified, gitservice.DiffFileModified, 1)
	put(status.Staged, gitservice.DiffFileModified, 1)
	put(status.Added, gitservice.DiffFileAdded, 2)
	put(status.Untracked, gitservice.DiffFileAdded, 2)
	put(status.Deleted, gitservice.DiffFileDeleted, 3)

	out := make([]gitservice.ChangedFile, 0, len(byPath))
	for _, file := range byPath {
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out
}

func mergeChangedFileLists(primary, secondary []gitservice.ChangedFile) []gitservice.ChangedFile {
	merged := make(map[string]gitservice.ChangedFile, len(primary)+len(secondary))
	for _, file := range secondary {
		if strings.TrimSpace(file.Path) == "" {
			continue
		}
		merged[file.Path] = file
	}
	for _, file := range primary {
		if strings.TrimSpace(file.Path) == "" {
			continue
		}
		merged[file.Path] = file
	}

	out := make([]gitservice.ChangedFile, 0, len(merged))
	for _, file := range merged {
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func toViewerFileStatus(status gitservice.DiffFileStatus) FileStatus {
	switch status {
	case gitservice.DiffFileAdded:
		return FileAdded
	case gitservice.DiffFileDeleted:
		return FileDeleted
	case gitservice.DiffFileRenamed:
		return FileRenamed
	default:
		return FileModified
	}
}
