package overlay

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

const OperationQueueHotkey = "Q"

func OperationQueueHotkeyHint() string {
	return OperationQueueHotkey + ": ops"
}

type OperationQueueOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	snapshot   protocol.OperationQueueResponseBody
	loading    bool
	errMessage string
	treeMode   bool
	scroll     int
	maxScroll  int
	viewHeight int
	styles     *Styles
}

func NewOperationQueueOverlay(snapshot protocol.OperationQueueResponseBody) *OperationQueueOverlay {
	return &OperationQueueOverlay{
		snapshot:   cloneOperationQueueSnapshot(snapshot),
		treeMode:   true,
		viewHeight: 18,
		styles:     New(),
	}
}

func NewLoadingOperationQueueOverlay(projectID string) *OperationQueueOverlay {
	return &OperationQueueOverlay{
		snapshot: protocol.OperationQueueResponseBody{
			ProjectID: naming.ProjectID(protocol.NormalizeProjectID(projectID)),
		},
		loading:    true,
		treeMode:   true,
		viewHeight: 18,
		styles:     New(),
	}
}

func (o *OperationQueueOverlay) Init() tea.Cmd {
	return nil
}

func (o *OperationQueueOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		o.ApplyWindowSize(msg)
		return o, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyDown:
			o.scrollDown()
			return o, nil
		case tea.KeyUp:
			o.scrollUp()
			return o, nil
		case tea.KeyCtrlD:
			o.scroll = min(o.maxScroll, o.scroll+o.halfPageStep())
			return o, nil
		case tea.KeyCtrlU:
			o.scroll = max(0, o.scroll-o.halfPageStep())
			return o, nil
		}
		switch msg.String() {
		case "esc", "q", "backspace":
			return o, func() tea.Msg { return CloseOverlayMsg{} }
		case "r":
			o.loading = true
			o.errMessage = ""
			return o, func() tea.Msg { return SelectionMsg{Key: "operation_queue_refresh"} }
		case "t", "tab":
			o.treeMode = !o.treeMode
			o.scroll = 0
			return o, nil
		case "j", "down":
			o.scrollDown()
			return o, nil
		case "k", "up":
			o.scrollUp()
			return o, nil
		case "g":
			o.scroll = 0
			return o, nil
		case "G":
			o.scroll = o.maxScroll
			return o, nil
		}
	}
	return o, nil
}

func (o *OperationQueueOverlay) View() string {
	width, height := o.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            o.styles,
		width:             width,
		height:            height,
		title:             o.Title(),
		rightSectionTitle: "Keys",
		breakpoint:        82,
		gap:               3,
		minLeft:           50,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			o.viewHeight = max(1, height)
			return o.renderScrollableContent(width)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(o.styles, o.actionBindings(o.maxScroll > 0), width)
		},
	})
}

func (o *OperationQueueOverlay) Title() string {
	return "Operation Queue"
}

func (o *OperationQueueOverlay) Size() (width, height int) {
	const (
		minViewHeight = 8
		maxViewHeight = 24
		chromeHeight  = 4
	)
	neededViewHeight := len(o.contentLines(92)) + 1
	o.viewHeight = min(maxViewHeight, max(minViewHeight, neededViewHeight))
	return o.ClampResponsive(96, o.viewHeight+chromeHeight)
}

func (o *OperationQueueOverlay) StatusBindings() []keybinds.Binding {
	return o.actionBindings(o.maxScroll > 0)
}

func (o *OperationQueueOverlay) SetSnapshot(snapshot protocol.OperationQueueResponseBody) {
	o.snapshot = cloneOperationQueueSnapshot(snapshot)
	o.loading = false
	o.errMessage = ""
	o.clampScroll()
}

func (o *OperationQueueOverlay) SetError(err error) {
	o.loading = false
	if err == nil {
		o.errMessage = ""
		return
	}
	o.errMessage = err.Error()
	o.clampScroll()
}

func (o *OperationQueueOverlay) renderScrollableContent(width int) string {
	lines := o.contentLines(width)
	o.maxScroll = max(0, len(lines)-o.viewHeight)
	o.clampScroll()
	if len(lines) == 0 {
		return ""
	}
	start := min(o.scroll, len(lines))
	end := min(len(lines), start+o.viewHeight)
	return strings.Join(lines[start:end], "\n")
}

func (o *OperationQueueOverlay) contentLines(width int) []string {
	lines := []string{o.summaryLine(width)}
	if o.loading {
		lines = append(lines, o.styles.MenuItemDisabled.Render("Loading operation queue..."))
	}
	if o.errMessage != "" {
		lines = append(lines, o.styles.MenuItemDisabled.Render(ansi.Truncate("Error: "+o.errMessage, max(8, width), "...")))
	}
	if o.treeMode {
		lines = append(lines, o.treeLines(width)...)
	} else {
		lines = append(lines, o.tableLines(width)...)
	}
	if len(o.snapshot.Running) == 0 && len(o.snapshot.Queued) == 0 && !o.loading && o.errMessage == "" {
		lines = append(lines, o.styles.MenuItemDisabled.Render("No running or queued operations."))
	}
	return lines
}

func (o *OperationQueueOverlay) summaryLine(width int) string {
	mode := "tree"
	if !o.treeMode {
		mode = "table"
	}
	line := fmt.Sprintf("Project %s  running %d  queued %d  mode %s",
		emptyDefault(o.snapshot.ProjectID.String(), "-"),
		len(o.snapshot.Running),
		len(o.snapshot.Queued),
		mode,
	)
	return o.styles.MenuKey.Render(ansi.Truncate(line, operationQueueLineWidth(width), "..."))
}

func (o *OperationQueueOverlay) treeLines(width int) []string {
	waitingByBlocker := make(map[string][]protocol.OperationQueueEntry)
	var unblockedQueued []protocol.OperationQueueEntry
	for _, entry := range o.snapshot.Queued {
		if len(entry.BlockingOperationIDs) == 0 {
			unblockedQueued = append(unblockedQueued, entry)
			continue
		}
		for _, blocker := range entry.BlockingOperationIDs {
			waitingByBlocker[blocker.String()] = append(waitingByBlocker[blocker.String()], entry)
		}
	}

	lines := make([]string, 0, len(o.snapshot.Running)+len(o.snapshot.Queued)+4)
	for _, entry := range o.snapshot.Running {
		lines = append(lines, o.renderOperationLine("", entry, width, false))
		children := waitingByBlocker[entry.Operation.OperationID.String()]
		for idx, child := range children {
			connector := "`- "
			if idx < len(children)-1 {
				connector = "|- "
			}
			lines = append(lines, o.renderOperationLine(connector, child, width, true))
		}
		delete(waitingByBlocker, entry.Operation.OperationID.String())
	}
	for _, entry := range unblockedQueued {
		lines = append(lines, o.renderOperationLine("queued: ", entry, width, true))
	}
	blockerIDs := make([]string, 0, len(waitingByBlocker))
	for blockerID := range waitingByBlocker {
		blockerIDs = append(blockerIDs, blockerID)
	}
	sort.Strings(blockerIDs)
	for _, blockerID := range blockerIDs {
		lines = append(lines, o.styles.MenuItemDisabled.Render(ansi.Truncate("blocked by "+blockerID, operationQueueLineWidth(width), "...")))
		for _, entry := range waitingByBlocker[blockerID] {
			lines = append(lines, o.renderOperationLine("`- ", entry, width, true))
		}
	}
	return lines
}

func (o *OperationQueueOverlay) tableLines(width int) []string {
	lineWidth := operationQueueLineWidth(width)
	lines := []string{o.styles.MenuItemDisabled.Render(ansi.Truncate("ID  STATE  KIND  ISSUE  BLOCKED_BY  RESOURCES", lineWidth, "..."))}
	for _, entry := range appendOperationQueueEntries(o.snapshot.Running, o.snapshot.Queued) {
		line := fmt.Sprintf("%s  %s  %s  %s  %s  %s",
			entry.Operation.OperationID,
			entry.Operation.State,
			emptyDefault(entry.Operation.Kind, "-"),
			emptyDefault(entry.Operation.IssueID.String(), "-"),
			emptyDefault(joinOperationQueueIDs(entry.BlockingOperationIDs), "-"),
			emptyDefault(strings.Join(operationQueueResources(entry), ","), "-"),
		)
		lines = append(lines, o.styles.MenuItem.Render(ansi.Truncate(line, lineWidth, "...")))
	}
	return lines
}

func (o *OperationQueueOverlay) renderOperationLine(prefix string, entry protocol.OperationQueueEntry, width int, blocked bool) string {
	parts := []string{
		prefix + entry.Operation.OperationID.String(),
		string(entry.Operation.State),
	}
	if blockers := joinOperationQueueIDs(entry.BlockingOperationIDs); blockers != "" {
		parts = append(parts, "by="+blockers)
	}
	parts = append(parts, emptyDefault(entry.Operation.Kind, "-"))
	if entry.Operation.IssueID != "" {
		parts = append(parts, entry.Operation.IssueID.String())
	}
	if progress := operationQueueProgress(entry.Operation); progress != "" {
		parts = append(parts, progress)
	}
	if resources := operationQueueResources(entry); len(resources) > 0 {
		parts = append(parts, "res="+strings.Join(resources, ","))
	}
	line := ansi.Truncate(strings.Join(parts, " "), operationQueueLineWidth(width), "...")
	if blocked {
		return o.styles.MenuItem.Render(line)
	}
	return o.styles.MenuItemActive.Render(line)
}

func (o *OperationQueueOverlay) actionBindings(scrollable bool) []keybinds.Binding {
	bindings := []keybinds.Binding{
		{Key: "r", Description: "refresh"},
		{Key: "t/Tab", Description: "tree/table"},
	}
	if scrollable {
		bindings = append(bindings,
			keybinds.Binding{Key: "j/k/up/down", Description: "scroll"},
			keybinds.Binding{Key: "ctrl+u/d", Description: "half-page"},
			keybinds.Binding{Key: "g/G", Description: "top/bottom"},
		)
	}
	bindings = append(bindings, keybinds.Binding{Key: "Esc/q/backspace", Description: "close"})
	return bindings
}

func (o *OperationQueueOverlay) scrollDown() {
	o.scroll = min(o.maxScroll, o.scroll+1)
}

func (o *OperationQueueOverlay) scrollUp() {
	o.scroll = max(0, o.scroll-1)
}

func (o *OperationQueueOverlay) clampScroll() {
	o.scroll = max(0, min(o.scroll, o.maxScroll))
}

func (o *OperationQueueOverlay) halfPageStep() int {
	if o.viewHeight <= 2 {
		return 1
	}
	return max(1, o.viewHeight/2)
}

func operationQueueLineWidth(width int) int {
	return max(8, width-8)
}

func appendOperationQueueEntries(running, queued []protocol.OperationQueueEntry) []protocol.OperationQueueEntry {
	out := make([]protocol.OperationQueueEntry, 0, len(running)+len(queued))
	out = append(out, running...)
	out = append(out, queued...)
	return out
}

func operationQueueResources(entry protocol.OperationQueueEntry) []string {
	if len(entry.BlockedResourceKeys) > 0 {
		return append([]string(nil), entry.BlockedResourceKeys...)
	}
	return append([]string(nil), entry.Operation.ResourceKeys...)
}

func operationQueueProgress(record protocol.OperationRecord) string {
	if record.Progress == nil {
		return ""
	}
	if record.Progress.Percent > 0 {
		return fmt.Sprintf("%d%%", record.Progress.Percent)
	}
	if strings.TrimSpace(record.Progress.Message) != "" {
		return strings.TrimSpace(record.Progress.Message)
	}
	return strings.TrimSpace(record.Progress.Phase)
}

func joinOperationQueueIDs(ids []naming.OperationID) string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		values = append(values, id.String())
	}
	return strings.Join(values, ",")
}

func cloneOperationQueueSnapshot(snapshot protocol.OperationQueueResponseBody) protocol.OperationQueueResponseBody {
	snapshot.Running = cloneOperationQueueEntries(snapshot.Running)
	snapshot.Queued = cloneOperationQueueEntries(snapshot.Queued)
	return snapshot
}

func cloneOperationQueueEntries(entries []protocol.OperationQueueEntry) []protocol.OperationQueueEntry {
	out := make([]protocol.OperationQueueEntry, 0, len(entries))
	for _, entry := range entries {
		entry.Operation.ResourceKeys = append([]string(nil), entry.Operation.ResourceKeys...)
		entry.Operation.Payload = append([]byte(nil), entry.Operation.Payload...)
		entry.Operation.Result = append([]byte(nil), entry.Operation.Result...)
		entry.BlockingOperationIDs = append([]naming.OperationID(nil), entry.BlockingOperationIDs...)
		entry.BlockedResourceKeys = append([]string(nil), entry.BlockedResourceKeys...)
		if entry.Operation.Progress != nil {
			progress := *entry.Operation.Progress
			entry.Operation.Progress = &progress
		}
		if entry.Operation.Error != nil {
			opErr := *entry.Operation.Error
			entry.Operation.Error = &opErr
		}
		if entry.Operation.StartedAt != nil {
			started := *entry.Operation.StartedAt
			entry.Operation.StartedAt = &started
		}
		if entry.Operation.FinishedAt != nil {
			finished := *entry.Operation.FinishedAt
			entry.Operation.FinishedAt = &finished
		}
		out = append(out, entry)
	}
	return out
}
