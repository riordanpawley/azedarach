package tmuxselector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

type fakeSnapshotLoader struct {
	snapshot Snapshot
	err      error
}

func TestLoadedTypedViewOwnsSelectorLayoutAndTabOverrideIsTransient(t *testing.T) {
	view := domain.DefaultBoardView()
	view.Layout = domain.BoardViewLayoutTreeList
	model := New(SnapshotLoaderFunc(func(context.Context) (Snapshot, error) { return Snapshot{}, nil }))
	next, _ := model.Update(LoadedMsg{Snapshot: Snapshot{View: view}})
	loaded := next.(Model)
	if loaded.activeTab != selectorTabTree {
		t.Fatalf("active tab = %v, want tree from typed view", loaded.activeTab)
	}
	next, cmd := loaded.Update(tea.KeyMsg{Type: tea.KeyTab})
	overridden := next.(Model)
	if overridden.activeTab != selectorTabGrid {
		t.Fatalf("active tab = %v, want transient grid override", overridden.activeTab)
	}
	if cmd != nil {
		t.Fatal("typed-view tab override unexpectedly persisted selector tab")
	}
}

func TestTypedColumnBoardRendersProjectedGroups(t *testing.T) {
	view := domain.DefaultBoardView()
	model := New(SnapshotLoaderFunc(func(context.Context) (Snapshot, error) { return Snapshot{}, nil }))
	model.loading = false
	model.snapshot = Snapshot{View: view, Entries: []InventoryEntry{
		{IssueID: "one", TaskTitle: "One", ViewProjected: true, ViewGroupID: domain.BoardColumnOpen, ViewGroupTitle: "Open"},
		{IssueID: "two", TaskTitle: "Two", ViewProjected: true, ViewGroupID: domain.BoardColumnActive, ViewGroupTitle: "In Progress"},
	}}
	rendered := model.View()
	if !strings.Contains(rendered, "Open (1)") || !strings.Contains(rendered, "In Progress (1)") {
		t.Fatalf("column board missing projected groups:\n%s", rendered)
	}
}

func TestTypedColumnBoardKeepsSelectedGroupVisibleOnNarrowViewport(t *testing.T) {
	view := domain.DefaultBoardView()
	model := New(SnapshotLoaderFunc(func(context.Context) (Snapshot, error) { return Snapshot{}, nil }))
	model.loading, model.width, model.height, model.cursor = false, 60, 20, 2
	model.snapshot = Snapshot{View: view, Entries: []InventoryEntry{
		{IssueID: "one", TaskTitle: "One", ViewGroupID: "one", ViewGroupTitle: "One"},
		{IssueID: "two", TaskTitle: "Two", ViewGroupID: "two", ViewGroupTitle: "Two"},
		{IssueID: "three", TaskTitle: "Three", ViewGroupID: "three", ViewGroupTitle: "Three"},
	}}
	rendered := model.View()
	if !strings.Contains(rendered, "Three (1)") {
		t.Fatalf("selected group not visible:\n%s", rendered)
	}
	if strings.Contains(rendered, "One (1)") {
		t.Fatalf("narrow viewport rendered off-screen group:\n%s", rendered)
	}
}

func TestTypedColumnBoardScrollsSelectedCardIntoView(t *testing.T) {
	view := domain.DefaultBoardView()
	entries := make([]InventoryEntry, 8)
	for i := range entries {
		id := string(rune('a' + i))
		entries[i] = InventoryEntry{
			IssueID: id, TaskTitle: "Card " + id, HasTmuxSession: true,
			ViewGroupID: domain.BoardColumnOpen, ViewGroupTitle: "Open",
		}
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{View: view, Entries: entries}})
	model.loading, model.width, model.height, model.cursor = false, 72, 14, len(entries)-1
	model.snapshot = Snapshot{View: view, Entries: entries}

	rendered := ansi.Strip(model.View())
	if !strings.Contains(rendered, "Card h") {
		t.Fatalf("selected card was not scrolled into view:\n%s", rendered)
	}
	if !strings.Contains(rendered, "more ^") {
		t.Fatalf("scrolled column missing overflow indicator:\n%s", rendered)
	}
}

func TestTypedColumnBoardPageNavigationKeepsSelectionVisible(t *testing.T) {
	view := domain.DefaultBoardView()
	entries := make([]InventoryEntry, 10)
	for i := range entries {
		id := string(rune('a' + i))
		entries[i] = InventoryEntry{
			IssueID: id, TaskTitle: "Card " + id, HasTmuxSession: true,
			ViewGroupID: domain.BoardColumnOpen, ViewGroupTitle: "Open",
		}
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{View: view, Entries: entries}})
	model.loading, model.width, model.height = false, 72, 14
	model.snapshot = Snapshot{View: view, Entries: entries}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	model = updated.(Model)
	if model.cursor <= 0 {
		t.Fatalf("PageDown cursor = %d, want movement", model.cursor)
	}
	selected, ok := model.selectedEntry()
	if !ok || !strings.Contains(ansi.Strip(model.View()), selected.TaskTitle) {
		t.Fatalf("paged selection is not visible: cursor=%d selected=%#v\n%s", model.cursor, selected, ansi.Strip(model.View()))
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	model = updated.(Model)
	if model.cursor != 0 {
		t.Fatalf("PageUp cursor = %d, want 0", model.cursor)
	}
}

func TestTypedColumnBoardMouseWheelScrollsSelection(t *testing.T) {
	view := domain.DefaultBoardView()
	entries := make([]InventoryEntry, 8)
	for i := range entries {
		id := string(rune('a' + i))
		entries[i] = InventoryEntry{
			IssueID: id, TaskTitle: "Card " + id, HasTmuxSession: true,
			ViewGroupID: domain.BoardColumnOpen, ViewGroupTitle: "Open",
		}
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{View: view, Entries: entries}})
	model.loading, model.width, model.height = false, 72, 14
	model.snapshot = Snapshot{View: view, Entries: entries}

	for range len(entries) - 1 {
		updated, _ := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
		model = updated.(Model)
	}
	if model.cursor != len(entries)-1 || !strings.Contains(ansi.Strip(model.View()), "Card h") {
		t.Fatalf("mouse wheel did not scroll selected card into view: cursor=%d\n%s", model.cursor, ansi.Strip(model.View()))
	}
}

func TestTypedViewStatusExplainsSelectorConfiguration(t *testing.T) {
	view := domain.DefaultBoardView()
	status := formatSnapshotStatus(Snapshot{View: view}, 3)
	for _, want := range []string{"view default", "az view select --project global --consumer tmux_selector VIEW"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status %q missing %q", status, want)
		}
	}
}

func TestTypedColumnBoardHorizontalMovementClampsAcrossUnevenColumns(t *testing.T) {
	view := domain.DefaultBoardView()
	entries := []InventoryEntry{
		{IssueID: "left-1", TaskTitle: "Left one", ViewGroupID: "left", ViewGroupTitle: "Left"},
		{IssueID: "left-2", TaskTitle: "Left two", ViewGroupID: "left", ViewGroupTitle: "Left"},
		{IssueID: "left-3", TaskTitle: "Left three", ViewGroupID: "left", ViewGroupTitle: "Left"},
		{IssueID: "middle-1", TaskTitle: "Middle one", ViewGroupID: "middle", ViewGroupTitle: "Middle"},
		{IssueID: "right-1", TaskTitle: "Right one", ViewGroupID: "right", ViewGroupTitle: "Right"},
		{IssueID: "right-2", TaskTitle: "Right two", ViewGroupID: "right", ViewGroupTitle: "Right"},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{View: view, Entries: entries}})
	model.loading, model.width, model.height, model.cursor = false, 90, 14, 2
	model.snapshot = Snapshot{View: view, Entries: entries}

	model = updateKey(t, model, "right")
	if model.cursor != 3 {
		t.Fatalf("right into shorter column cursor = %d, want clamped middle row 0", model.cursor)
	}
	model = updateKey(t, model, "right")
	if model.cursor != 4 {
		t.Fatalf("right into next column cursor = %d, want right row 0", model.cursor)
	}
	model = updateKey(t, model, "down")
	if model.cursor != 5 {
		t.Fatalf("down cursor = %d, want column-local right row 1", model.cursor)
	}
	model = updateKey(t, model, "left")
	if model.cursor != 3 {
		t.Fatalf("left into shorter column cursor = %d, want clamped middle row 0", model.cursor)
	}

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 50, Height: 10})
	model = updated.(Model)
	rendered := model.View()
	if !strings.Contains(rendered, "Middle one") {
		t.Fatalf("selected card lost after resize/scroll:\n%s", rendered)
	}
}

func (f fakeSnapshotLoader) ListTasksSnapshot(context.Context) (Snapshot, error) {
	return f.snapshot, f.err
}

type fakeLiveSnapshotLoader struct {
	liveCalls   int
	enrichCalls int
	live        Snapshot
	enriched    Snapshot
}

func (f *fakeLiveSnapshotLoader) ListTasksSnapshot(context.Context) (Snapshot, error) {
	return f.enriched, nil
}

func (f *fakeLiveSnapshotLoader) ListLiveSnapshot(context.Context) (Snapshot, error) {
	f.liveCalls++
	return f.live, nil
}

func (f *fakeLiveSnapshotLoader) EnrichSnapshot(_ context.Context, _ Snapshot) (Snapshot, error) {
	f.enrichCalls++
	return f.enriched, nil
}

type fakeSwitcher struct {
	sessionID string
	err       error
	events    *[]string
}

func (f *fakeSwitcher) SwitchClient(_ context.Context, sessionID string) error {
	if f.events != nil {
		*f.events = append(*f.events, "switch "+sessionID)
	}
	f.sessionID = sessionID
	return f.err
}

type fakeKiller struct {
	killed []InventoryEntry
	err    error
}

func (f *fakeKiller) KillSession(_ context.Context, entry InventoryEntry) error {
	f.killed = append(f.killed, entry)
	return f.err
}

type fakePopupCloser struct {
	closed int
	err    error
	events *[]string
}

func (f *fakePopupCloser) ClosePopup(context.Context) error {
	if f.events != nil {
		*f.events = append(*f.events, "close-popup")
	}
	f.closed++
	return f.err
}

type fakeFullAzSwitcher struct {
	hasSession bool
	commands   []string
}

func (f *fakeFullAzSwitcher) HasSession(_ context.Context, sessionID string) (bool, error) {
	f.commands = append(f.commands, "has "+sessionID)
	return f.hasSession, nil
}

func (f *fakeFullAzSwitcher) NewSessionWithCommand(_ context.Context, sessionID, workdir, command string) error {
	f.commands = append(f.commands, "new "+sessionID+" "+workdir+" "+command)
	return nil
}

func (f *fakeFullAzSwitcher) SendKey(_ context.Context, sessionID, key string) error {
	f.commands = append(f.commands, "key "+sessionID+" "+key)
	return nil
}

func (f *fakeFullAzSwitcher) SendKeys(_ context.Context, sessionID, keys string) error {
	f.commands = append(f.commands, "keys "+sessionID+" "+keys)
	return nil
}

func (f *fakeFullAzSwitcher) SwitchClient(_ context.Context, sessionID string) error {
	f.commands = append(f.commands, "switch "+sessionID)
	return nil
}

type fakeDetailOpener struct {
	entries      []InventoryEntry
	drillEntries []InventoryEntry
	err          error
	events       *[]string
}

func (f *fakeDetailOpener) OpenDetail(_ context.Context, entry InventoryEntry) error {
	if f.events != nil {
		*f.events = append(*f.events, "open "+entryIssueID(entry))
	}
	f.entries = append(f.entries, entry)
	return f.err
}

func (f *fakeDetailOpener) OpenDrillDown(_ context.Context, entry InventoryEntry) error {
	f.drillEntries = append(f.drillEntries, entry)
	return f.err
}

type fakeUIStateStore struct {
	values map[string]string
	gets   []string
	sets   []string
	err    error
}

func (f *fakeUIStateStore) GetUIStateForProject(_ context.Context, _ string, key string) (protocol.UIStateResponseBody, error) {
	f.gets = append(f.gets, key)
	if f.err != nil {
		return protocol.UIStateResponseBody{}, f.err
	}
	value, found := f.values[key]
	return protocol.UIStateResponseBody{Key: key, Value: value, Found: found}, nil
}

func (f *fakeUIStateStore) SetUIStateForProject(_ context.Context, _ string, key string, value string) (protocol.UIStateResponseBody, error) {
	f.sets = append(f.sets, key+"="+value)
	if f.err != nil {
		return protocol.UIStateResponseBody{}, f.err
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[key] = value
	return protocol.UIStateResponseBody{Key: key, Value: value, Found: true}, nil
}

func TestModelUsesFakeInventoryAndSwitchesSessionID(t *testing.T) {
	switcher := &fakeSwitcher{}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", State: domain.SessionWaiting},
		{SessionID: "ch-two", IssueID: "two", TaskTitle: "Two", State: domain.SessionBusy},
	}}}, WithSwitcher(switcher))

	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: model.loader.(fakeSnapshotLoader).snapshot})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	if !strings.Contains(model.View(), "az-one") || !strings.Contains(model.View(), "ch-two") {
		t.Fatalf("view missing inventory: %q", model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	model = updated.(Model)
	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatalf("a did not produce switch command")
	}
	msg := cmd()
	if _, ok := msg.(switchCompleteMsg); !ok {
		t.Fatalf("switch msg = %T", msg)
	}
	if switcher.sessionID != "ch-two" {
		t.Fatalf("switched session = %q, want ch-two", switcher.sessionID)
	}
}

func TestModelTreeRendersHookActivityInsteadOfLifecycleBusy(t *testing.T) {
	tests := []struct {
		name     string
		activity string
		want     string
		reject   string
	}{
		{name: "hook idle", activity: "idle", want: "az-cmd [idle] False busy flag", reject: "az-cmd [busy] False busy flag"},
		{name: "agent working", activity: "working", want: "az-cmd [working] False busy flag", reject: "az-cmd [waiting] False busy flag"},
		{name: "agent waiting", activity: "waiting", want: "az-cmd [waiting] False busy flag", reject: "az-cmd [busy] False busy flag"},
		{name: "session no agent", activity: "no-agent", want: "az-cmd [no-agent] False busy flag", reject: "az-cmd [unknown] False busy flag"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := []InventoryEntry{{
				SessionID:      "az-cmd",
				IssueID:        "cmd",
				TaskTitle:      "False busy flag",
				State:          domain.SessionBusy,
				Activity:       tt.activity,
				ActivitySource: "hooks",
				HasTmuxSession: true,
			}}
			model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
			updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
			model = updated.(Model)
			if cmd != nil {
				t.Fatalf("snapshot update returned command")
			}
			model.activeTab = selectorTabTree

			view := ansi.Strip(model.View())
			if !strings.Contains(view, tt.want) {
				t.Fatalf("view missing activity label %q:\n%s", tt.want, view)
			}
			if strings.Contains(view, tt.reject) {
				t.Fatalf("view rendered wrong activity label %q:\n%s", tt.reject, view)
			}
		})
	}
}

func TestModelTreeRendersIssueStatusBesideAgentStatus(t *testing.T) {
	entries := []InventoryEntry{{
		SessionID:      "az-coh",
		IssueID:        "coh",
		TaskTitle:      "Show issue status in selector",
		State:          domain.SessionBusy,
		IssueStatus:    domain.StatusInReview,
		HasTmuxSession: true,
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	model.activeTab = selectorTabTree

	view := ansi.Strip(model.View())
	if !strings.Contains(view, "az-coh [busy] [in_review] Show issue status in selector") {
		t.Fatalf("tree view missing separate agent and issue statuses:\n%s", view)
	}
}

func TestModelTreeRendersHumanReadableUpdatedAge(t *testing.T) {
	updatedAt := time.Now().Add(-5 * time.Minute).UTC()
	entries := []InventoryEntry{{
		SessionID:     "az-cqg",
		IssueID:       "cqg",
		TaskTitle:     "Human readable selector duration",
		State:         domain.SessionBusy,
		IssueStatus:   domain.StatusInProgress,
		LastUpdatedAt: &updatedAt,
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	model.activeTab = selectorTabTree

	view := ansi.Strip(model.View())
	if !strings.Contains(view, "updated 5m") {
		t.Fatalf("tree view missing updated duration:\n%s", view)
	}
}

func TestModelTreeColorizesSessionAndIssueStatus(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	entries := []InventoryEntry{{
		SessionID:         "az-coh",
		IssueID:           "coh",
		TaskTitle:         "Show issue status in selector",
		State:             domain.SessionBusy,
		IssueStatus:       domain.StatusInProgress,
		HasTmuxSession:    true,
		TmuxAttachedCount: 1,
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	model.activeTab = selectorTabTree

	view := model.View()
	stripped := ansi.Strip(view)
	if !strings.Contains(stripped, "az-coh [busy] [in_progress] Show issue status in selector") {
		t.Fatalf("tree view text changed after colorization:\n%s", stripped)
	}
	for _, want := range []string{
		model.styles.SessionBusy.Render("busy"),
		issueStatusStyle("in_progress").Render("in_progress"),
		lipgloss.NewStyle().Foreground(styles.Teal).Bold(true).Render("tmux az-coh  attached"),
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("tree view missing styled token %q:\n%s", want, view)
		}
	}
}

func TestModelTreePrefersPrefixedSessionIDForIssueRows(t *testing.T) {
	entries := []InventoryEntry{{
		SessionID:      "ch-enw",
		IssueID:        "enw",
		TaskTitle:      "Store DevTools split focus inspector",
		State:          domain.SessionBusy,
		IssueStatus:    domain.StatusInProgress,
		HasTmuxSession: true,
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	model.activeTab = selectorTabTree

	view := ansi.Strip(model.View())
	if !strings.Contains(view, "ch-enw [busy] [in_progress] Store DevTools split focus inspector") {
		t.Fatalf("tree view missing prefixed row identity:\n%s", view)
	}
	if strings.Contains(view, "> enw [busy] [in_progress]") || strings.Contains(view, "`- enw [busy] [in_progress]") {
		t.Fatalf("tree view rendered bare issue id before prefixed id:\n%s", view)
	}
}

func TestRenderSessionRowIncludesIssueStatusInCardMeta(t *testing.T) {
	updatedAt := time.Now().Add(-3 * time.Hour).UTC()
	row := SessionRow{
		SessionID:      "az-coh",
		IssueID:        "coh",
		TaskTitle:      "Show issue status in selector",
		State:          domain.SessionBusy,
		IssueStatus:    domain.StatusInProgress,
		HasTmuxSession: true,
		LastUpdatedAt:  &updatedAt,
	}

	view := ansi.Strip(RenderSessionRow(row, false, 64, lipgloss.Style{}, lipgloss.Style{}, lipgloss.Style{}, styles.New()))
	if !strings.Contains(view, "issue in_progress") {
		t.Fatalf("card missing issue status meta:\n%s", view)
	}
	if !strings.Contains(view, "updated 3h") {
		t.Fatalf("card missing updated duration meta:\n%s", view)
	}
	if !strings.Contains(view, "tmux az-coh") {
		t.Fatalf("card missing tmux meta:\n%s", view)
	}
}

func TestFormatShortDuration(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{name: "seconds", in: 20 * time.Second, want: "20s"},
		{name: "minutes", in: 3*time.Minute + 45*time.Second, want: "3m"},
		{name: "hours", in: 5*time.Hour + 59*time.Minute, want: "5h"},
		{name: "days", in: 2*24*time.Hour + time.Hour, want: "2d"},
		{name: "weeks", in: 15 * 24 * time.Hour, want: "2w"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatShortDuration(tt.in); got != tt.want {
				t.Fatalf("formatShortDuration(%s) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatEntryUpdatedAgeIgnoresSessionProjectionRefresh(t *testing.T) {
	now := time.Now().UTC()
	issueUpdatedAt := now.Add(-6 * time.Hour)
	sessionUpdatedAt := now.Add(-2 * time.Second)
	entry := InventoryEntry{
		Task: domain.Task{
			UpdatedAt: issueUpdatedAt,
			Session: &domain.Session{
				UpdatedAt: sessionUpdatedAt,
			},
		},
	}

	if got := formatEntryUpdatedAge(entry, now); got != "6h" {
		t.Fatalf("formatEntryUpdatedAge = %q, want 6h from issue update, not 2s from projection refresh", got)
	}
}

func TestRenderSessionRowColorizesCardMeta(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	row := SessionRow{
		SessionID:         "az-coh",
		IssueID:           "coh",
		TaskTitle:         "Show issue status in selector",
		State:             domain.SessionBusy,
		IssueStatus:       domain.StatusInProgress,
		HasTmuxSession:    true,
		TmuxAttachedCount: 1,
		ProjectPath:       "/tmp/project",
	}
	s := styles.New()

	view := RenderSessionRow(row, false, 64, lipgloss.Style{}, lipgloss.Style{}, lipgloss.Style{}, s)
	stripped := ansi.Strip(view)
	for _, want := range []string{"issue in_progress", "tmux az-coh  attached", "/tmp/project"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("card meta text changed, missing %q:\n%s", want, stripped)
		}
	}
	for _, want := range []string{
		issueStatusStyle("in_progress").Render("in_progress"),
		lipgloss.NewStyle().Foreground(styles.Teal).Bold(true).Render("tmux az-coh  attached"),
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("card meta missing styled token %q:\n%s", want, view)
		}
	}
}

func TestParseAzedarachSessionNameDecodesEscapedIssueID(t *testing.T) {
	parsed, ok := ParseAzedarachSessionName("az-native_x2e_id_x5f_1")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if parsed.Project != "az" {
		t.Fatalf("project = %q, want az", parsed.Project)
	}
	if got, want := parsed.IssueID.String(), "native.id_1"; got != want {
		t.Fatalf("issue id = %q, want %q", got, want)
	}
}

func TestModelOpenDetailSupportsOAndSpaceKeysWithoutOpenIssueCommand(t *testing.T) {
	for _, tt := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "o", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}}},
		{name: "space rune", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}},
		{name: "key space", key: tea.KeyMsg{Type: tea.KeySpace}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			switcher := &fakeFullAzSwitcher{hasSession: true}
			opener := &fakeDetailOpener{}
			entries := []InventoryEntry{{
				SessionID:   "az-one",
				IssueID:     "one",
				TaskTitle:   "One",
				ProjectPath: "/tmp/project one",
			}}
			model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}}, WithSwitcher(switcher), WithDetailOpener(opener))
			updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
			model = updated.(Model)
			if cmd != nil {
				t.Fatalf("snapshot update returned command")
			}

			_, cmd = model.Update(tt.key)
			if cmd == nil {
				t.Fatal("open key did not produce detail command")
			}
			msg, ok := cmd().(DetailOpenResultMsg)
			if !ok {
				t.Fatalf("open msg = %T, want DetailOpenResultMsg", msg)
			}
			if msg.Err != nil {
				t.Fatalf("open detail returned error: %v", msg.Err)
			}

			want := []string{"has az", "switch az"}
			if got := strings.Join(switcher.commands, "\n"); got != strings.Join(want, "\n") {
				t.Fatalf("commands:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
			}
			if len(opener.entries) != 1 || opener.entries[0].IssueID != "one" {
				t.Fatalf("opener entries = %+v, want issue one", opener.entries)
			}
		})
	}
}

func TestModelOpenDrillDownSupportsEnterKeyWithoutOpenIssueCommand(t *testing.T) {
	switcher := &fakeFullAzSwitcher{hasSession: true}
	opener := &fakeDetailOpener{}
	entries := []InventoryEntry{{
		SessionID:   "az-one",
		IssueID:     "one",
		TaskTitle:   "One",
		ProjectPath: "/tmp/project one",
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}}, WithSwitcher(switcher), WithDetailOpener(opener))
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not produce drill-down command")
	}
	msg, ok := cmd().(DetailOpenResultMsg)
	if !ok {
		t.Fatalf("drill-down msg = %T, want DetailOpenResultMsg", msg)
	}
	if msg.Err != nil {
		t.Fatalf("open drill-down returned error: %v", msg.Err)
	}

	want := []string{"has az", "switch az"}
	if got := strings.Join(switcher.commands, "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if len(opener.drillEntries) != 1 || opener.drillEntries[0].IssueID != "one" {
		t.Fatalf("drill entries = %+v, want issue one", opener.drillEntries)
	}
	if len(opener.entries) != 0 {
		t.Fatalf("detail entries = %+v, want none", opener.entries)
	}
}

func TestModelASwitchesSelectedIssueSessionThenOpensDetail(t *testing.T) {
	for _, tt := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "a", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := []string{}
			switcher := &fakeSwitcher{events: &events}
			opener := &fakeDetailOpener{events: &events}
			entries := []InventoryEntry{{
				SessionID:   "az-one",
				IssueID:     "one",
				TaskTitle:   "One",
				ProjectPath: "/tmp/project one",
			}}
			model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}}, WithSwitcher(switcher), WithDetailOpener(opener))
			updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
			model = updated.(Model)
			if cmd != nil {
				t.Fatalf("snapshot update returned command")
			}

			_, cmd = model.Update(tt.key)
			if cmd == nil {
				t.Fatal("switch key did not produce command")
			}
			msg, ok := cmd().(SwitchResultMsg)
			if !ok {
				t.Fatalf("switch msg = %T, want SwitchResultMsg", msg)
			}
			if msg.Err != nil {
				t.Fatalf("switch command error = %v", msg.Err)
			}
			if switcher.sessionID != "az-one" {
				t.Fatalf("switched session = %q, want selected session az-one", switcher.sessionID)
			}
			if len(opener.entries) != 1 || opener.entries[0].IssueID != "one" {
				t.Fatalf("opener entries = %+v, want issue one", opener.entries)
			}
			if got, want := strings.Join(events, "\n"), "switch az-one\nopen one"; got != want {
				t.Fatalf("events:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestModelAttachKeepsSwitchSuccessWhenDetailOpenFails(t *testing.T) {
	switcher := &fakeSwitcher{}
	opener := &fakeDetailOpener{err: errors.New("daemon busy")}
	closer := &fakePopupCloser{}
	entries := []InventoryEntry{{
		SessionID:   "az-one",
		IssueID:     "one",
		TaskTitle:   "One",
		ProjectPath: "/tmp/project one",
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}}, WithSwitcher(switcher), WithPopupCloser(closer), WithDetailOpener(opener))
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("a did not produce attach command")
	}
	msg, ok := cmd().(SwitchResultMsg)
	if !ok {
		t.Fatalf("attach msg = %T, want SwitchResultMsg", msg)
	}
	if msg.Err != nil {
		t.Fatalf("attach error = %v, want nil after switch success", msg.Err)
	}
	if switcher.sessionID != "az-one" {
		t.Fatalf("switched session = %q, want az-one", switcher.sessionID)
	}
	if len(opener.entries) != 1 {
		t.Fatalf("opener calls = %d, want 1", len(opener.entries))
	}
	if closer.closed != 1 {
		t.Fatalf("popup close calls = %d, want 1", closer.closed)
	}
}

func TestModelAttachClosesPopupBeforeWorkspaceOpen(t *testing.T) {
	events := []string{}
	switcher := &fakeSwitcher{events: &events}
	closer := &fakePopupCloser{events: &events}
	opener := &fakeDetailOpener{events: &events}
	entries := []InventoryEntry{{
		SessionID:   "az-one",
		IssueID:     "one",
		TaskTitle:   "One",
		ProjectPath: "/tmp/project one",
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}}, WithSwitcher(switcher), WithPopupCloser(closer), WithDetailOpener(opener))
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("a did not produce attach command")
	}
	msg, ok := cmd().(SwitchResultMsg)
	if !ok {
		t.Fatalf("attach msg = %T, want SwitchResultMsg", msg)
	}
	if msg.Err != nil {
		t.Fatalf("attach error = %v", msg.Err)
	}
	if got, want := strings.Join(events, "\n"), "switch az-one\nclose-popup\nopen one"; got != want {
		t.Fatalf("events:\n%s\nwant:\n%s", got, want)
	}
}

func TestModelOpenDetailErrorsWhenFullAzSessionMissing(t *testing.T) {
	switcher := &fakeFullAzSwitcher{}
	entries := []InventoryEntry{{
		SessionID:   "az-two",
		IssueID:     "two",
		TaskTitle:   "Two",
		ProjectPath: "/tmp/project",
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}}, WithSwitcher(switcher), WithDetailOpener(&fakeDetailOpener{}))
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if cmd == nil {
		t.Fatal("o did not produce detail command")
	}
	msg := cmd().(DetailOpenResultMsg)
	if msg.Err == nil || !strings.Contains(msg.Err.Error(), "not found") {
		t.Fatalf("open detail error = %v, want missing full az session", msg.Err)
	}
	want := []string{"has az"}
	if got := strings.Join(switcher.commands, "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestModelOpenDetailRequiresIssueID(t *testing.T) {
	switcher := &fakeFullAzSwitcher{hasSession: true}
	entries := []InventoryEntry{{
		SessionID:      "az",
		TaskTitle:      "az",
		HasTmuxSession: true,
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}}, WithSwitcher(switcher), WithDetailOpener(&fakeDetailOpener{}))
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if cmd == nil {
		t.Fatal("o did not produce detail command")
	}
	msg := cmd().(DetailOpenResultMsg)
	if msg.Err == nil || !strings.Contains(msg.Err.Error(), "no issue id") {
		t.Fatalf("open detail error = %v, want missing issue id", msg.Err)
	}
	if len(switcher.commands) != 0 {
		t.Fatalf("commands = %v, want none", switcher.commands)
	}
}

func TestModelTabTogglesTreeViewAndPersistsGlobally(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	entries := []InventoryEntry{
		{
			SessionID:      "az-proj-parent",
			IssueID:        parentID.String(),
			TaskTitle:      "Parent session",
			State:          domain.SessionBusy,
			HasTmuxSession: true,
			Task: domain.Task{
				ID:     parentID,
				Title:  "Parent session",
				Status: domain.StatusInProgress,
			},
		},
		{
			SessionID:      "az-proj-child",
			IssueID:        "az-child",
			TaskTitle:      "Child session",
			State:          domain.SessionWaiting,
			HasTmuxSession: true,
			Task: domain.Task{
				ID:       naming.IssueID("az-child"),
				Title:    "Child session",
				Status:   domain.StatusInProgress,
				ParentID: &parentID,
			},
		},
	}
	store := &fakeUIStateStore{}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}}, WithUIStateStore(store))
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("snapshot update did not defer selector tab load")
	}
	msg := cmd()
	if msg.(selectorTabLoadedMsg).err != nil {
		t.Fatalf("selector tab load msg = %+v", msg)
	}
	updated, _ = model.Update(msg)
	model = updated.(Model)

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	if model.activeTab != selectorTabTree {
		t.Fatalf("active tab = %v, want tree", model.activeTab)
	}
	if cmd == nil {
		t.Fatal("tab did not persist active selector tab")
	}
	if msg := cmd(); msg.(selectorTabSavedMsg).err != nil {
		t.Fatalf("persist tab msg = %+v", msg)
	}
	if got := strings.Join(store.sets, "\n"); got != protocol.UIStateKeyTMUXSelectorLastActiveTab+"=tree" {
		t.Fatalf("persisted keys = %q", got)
	}
	view := ansi.Strip(model.View())
	if !strings.Contains(view, "[ Tree ]") || !strings.Contains(view, "`- az-proj-child") {
		t.Fatalf("tree view missing tab or child hierarchy:\n%s", view)
	}

	model = updateKey(t, model, "down")
	if model.cursor != 1 {
		t.Fatalf("tree down cursor = %d, want child index 1", model.cursor)
	}
}

func TestModelTreeViewShowsNonSelectableAncestorsForActiveLeaves(t *testing.T) {
	rootID := naming.IssueID("az-root")
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	entries := []InventoryEntry{
		{
			SessionID:      "az-child",
			IssueID:        childID.String(),
			TaskTitle:      "Worker session",
			State:          domain.SessionBusy,
			HasTmuxSession: true,
			Task: domain.Task{
				ID:       childID,
				Title:    "Worker session",
				Status:   domain.StatusInProgress,
				ParentID: &parentID,
			},
		},
	}
	snapshot := Snapshot{
		Entries: entries,
		TreeTasks: []domain.Task{
			{
				ID:       rootID,
				Title:    "Root orchestration",
				Status:   domain.StatusInProgress,
				Priority: domain.P2,
				Type:     domain.TypeEpic,
			},
			{
				ID:       parentID,
				Title:    "Parent design",
				Status:   domain.StatusInProgress,
				Priority: domain.P1,
				Type:     domain.TypeTask,
				ParentID: &rootID,
			},
			{
				ID:       childID,
				Title:    "Worker session",
				Status:   domain.StatusInProgress,
				ParentID: &parentID,
			},
		},
	}
	model := New(fakeSnapshotLoader{snapshot: snapshot})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 18})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: snapshot})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	model = updateKey(t, model, "tab")

	view := ansi.Strip(model.View())
	for _, want := range []string{"az-root", "`- az-parent", "`- az-child"} {
		if !strings.Contains(view, want) {
			t.Fatalf("tree view missing %q:\n%s", want, view)
		}
	}
	if model.cursor != 0 {
		t.Fatalf("initial cursor = %d, want active child entry index 0", model.cursor)
	}
	model = updateKey(t, model, "down")
	if model.cursor != 0 {
		t.Fatalf("tree cursor moved to ancestor/nonexistent row: %d", model.cursor)
	}
}

func TestModelCardsOrderActiveSessionsByGraphSlice(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	entries := []InventoryEntry{
		{
			SessionID:      "az-child",
			IssueID:        childID.String(),
			TaskTitle:      "Child session",
			HasTmuxSession: true,
			Task: domain.Task{
				ID:       childID,
				Title:    "Child session",
				Status:   domain.StatusInProgress,
				ParentID: &parentID,
			},
		},
		{
			SessionID:      "az-parent",
			IssueID:        parentID.String(),
			TaskTitle:      "Parent session",
			HasTmuxSession: true,
			Task: domain.Task{
				ID:     parentID,
				Title:  "Parent session",
				Status: domain.StatusInProgress,
			},
		},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	ordered := model.filteredEntries()
	if got := []string{ordered[0].SessionID, ordered[1].SessionID}; strings.Join(got, ",") != "az-parent,az-child" {
		t.Fatalf("card order = %v, want parent before child", got)
	}
	view := ansi.Strip(model.View())
	parentPos := strings.Index(view, "Parent session")
	childPos := strings.Index(view, "Child session")
	if parentPos < 0 || childPos < 0 || parentPos > childPos {
		t.Fatalf("cards view did not render parent before child:\n%s", view)
	}
	if !strings.Contains(view, "under az-parent Parent session") {
		t.Fatalf("child card missing ancestor ribbon:\n%s", view)
	}

	model = updateKey(t, model, "1")
	selected, ok := model.selectedEntry()
	if !ok || selected.SessionID != "az-child" {
		t.Fatalf("digit hotkey selected %+v, want graph-ordered child card", selected)
	}
}

func TestModelCardsDefaultCurrentSessionUsesGraphOrder(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	snapshot := Snapshot{
		CurrentSessionID: "az-child",
		Entries: []InventoryEntry{
			{
				SessionID:      "az-child",
				IssueID:        childID.String(),
				TaskTitle:      "Child session",
				HasTmuxSession: true,
				Task: domain.Task{
					ID:       childID,
					Title:    "Child session",
					Status:   domain.StatusInProgress,
					ParentID: &parentID,
				},
			},
			{
				SessionID:      "az-parent",
				IssueID:        parentID.String(),
				TaskTitle:      "Parent session",
				HasTmuxSession: true,
				Task: domain.Task{
					ID:     parentID,
					Title:  "Parent session",
					Status: domain.StatusInProgress,
				},
			},
		},
	}
	model := New(fakeSnapshotLoader{snapshot: snapshot})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: snapshot})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	selected, ok := model.selectedEntry()
	if !ok || selected.SessionID != "az-child" {
		t.Fatalf("selected entry = %+v, want graph-ordered current child session", selected)
	}
	if model.cursor != 1 {
		t.Fatalf("cursor = %d, want child graph-order index 1", model.cursor)
	}
}

func TestModelCardsGroupSessionsUnderInactiveAncestor(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	firstChildID := naming.IssueID("az-first-child")
	secondChildID := naming.IssueID("az-second-child")
	entries := []InventoryEntry{
		{
			SessionID:      "az-first-child",
			IssueID:        firstChildID.String(),
			TaskTitle:      "First child",
			HasTmuxSession: true,
			Task: domain.Task{
				ID:       firstChildID,
				Title:    "First child",
				Status:   domain.StatusInProgress,
				ParentID: &parentID,
			},
		},
		{SessionID: "plain-tmux", TaskTitle: "Plain tmux", HasTmuxSession: true},
		{
			SessionID:      "az-second-child",
			IssueID:        secondChildID.String(),
			TaskTitle:      "Second child",
			HasTmuxSession: true,
			Task: domain.Task{
				ID:       secondChildID,
				Title:    "Second child",
				Status:   domain.StatusInProgress,
				ParentID: &parentID,
			},
		},
	}
	snapshot := Snapshot{
		Entries: entries,
		TreeTasks: []domain.Task{
			{ID: parentID, Title: "Inactive parent", Status: domain.StatusInProgress},
		},
	}
	model := New(fakeSnapshotLoader{snapshot: snapshot})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: snapshot})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	ordered := model.filteredEntries()
	if got := []string{ordered[0].SessionID, ordered[1].SessionID, ordered[2].SessionID}; strings.Join(got, ",") != "az-first-child,az-second-child,plain-tmux" {
		t.Fatalf("card order = %v, want related children grouped before unrelated session", got)
	}
	view := ansi.Strip(model.View())
	if !strings.Contains(view, "under az-parent Inactive parent") {
		t.Fatalf("cards view missing inactive ancestor ribbon:\n%s", view)
	}
}

func TestModelSlashSearchMatchesCardAncestorRibbon(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	snapshot := Snapshot{
		Entries: []InventoryEntry{
			{
				SessionID:      "az-child",
				IssueID:        childID.String(),
				TaskTitle:      "Worker session",
				HasTmuxSession: true,
				Task: domain.Task{
					ID:       childID,
					Title:    "Worker session",
					Status:   domain.StatusInProgress,
					ParentID: &parentID,
				},
			},
		},
		TreeTasks: []domain.Task{
			{ID: parentID, Title: "Inactive parent", Status: domain.StatusInProgress},
		},
	}
	model := New(fakeSnapshotLoader{snapshot: snapshot})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: snapshot})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	model = updateKey(t, model, "/")
	for _, r := range "inactive" {
		updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = updated.(Model)
		if cmd != nil {
			t.Fatalf("search rune %q returned command", r)
		}
	}

	if got := len(model.filteredEntries()); got != 1 {
		t.Fatalf("filtered sessions = %d, want child matched by ancestor ribbon", got)
	}
	view := ansi.Strip(model.View())
	if !strings.Contains(view, "under az-parent Inactive parent") || !strings.Contains(view, "Worker session") {
		t.Fatalf("ancestor-ribbon search did not keep descendant card visible:\n%s", view)
	}
}

func TestModelCardsKeepOriginalOrderForDuplicateIssueSessions(t *testing.T) {
	issueID := naming.IssueID("az-dup")
	parentID := naming.IssueID("az-parent")
	entries := []InventoryEntry{
		{
			SessionID:      "az-dup-one",
			IssueID:        issueID.String(),
			TaskTitle:      "Duplicate one",
			HasTmuxSession: true,
			Task: domain.Task{
				ID:       issueID,
				Title:    "Duplicate one",
				Status:   domain.StatusInProgress,
				ParentID: &parentID,
			},
		},
		{
			SessionID:      "az-dup-two",
			IssueID:        issueID.String(),
			TaskTitle:      "Duplicate two",
			HasTmuxSession: true,
			Task: domain.Task{
				ID:       issueID,
				Title:    "Duplicate two",
				Status:   domain.StatusInProgress,
				ParentID: &parentID,
			},
		},
	}
	snapshot := Snapshot{
		Entries:   entries,
		TreeTasks: []domain.Task{{ID: parentID, Title: "Parent", Status: domain.StatusInProgress}},
	}
	model := New(fakeSnapshotLoader{snapshot: snapshot})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: snapshot})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	ordered := model.filteredEntries()
	if got := []string{ordered[0].SessionID, ordered[1].SessionID}; strings.Join(got, ",") != "az-dup-one,az-dup-two" {
		t.Fatalf("card order = %v, want original order for duplicate issue sessions", got)
	}
}

func TestRenderSessionRowIncludesGraphAncestorRibbon(t *testing.T) {
	row := SessionRow{
		SessionID:      "az-child",
		IssueID:        "child",
		TaskTitle:      "Child session",
		HasTmuxSession: true,
		GraphAncestors: []string{"az-root Root", "az-parent Parent"},
	}

	view := ansi.Strip(RenderSessionRow(row, false, 72, lipgloss.Style{}, lipgloss.Style{}, lipgloss.Style{}, styles.New()))
	if !strings.Contains(view, "under az-root Root > az-parent Parent") {
		t.Fatalf("card missing graph ancestor ribbon:\n%s", view)
	}
	if !strings.Contains(view, "tmux az-child") {
		t.Fatalf("card missing tmux metadata after adding graph ribbon:\n%s", view)
	}
}

func TestModelTreeSortUpdatedRanksBranchesByNewestDescendantSession(t *testing.T) {
	now := time.Now().UTC()
	alphaID := naming.IssueID("az-alpha")
	betaID := naming.IssueID("az-beta")
	alphaChildID := naming.IssueID("az-alpha-child")
	betaChildID := naming.IssueID("az-beta-child")
	oldUpdated := now.Add(-4 * time.Hour)
	newUpdated := now.Add(-2 * time.Minute)
	entries := []InventoryEntry{
		{
			SessionID:      "az-alpha-child",
			IssueID:        alphaChildID.String(),
			TaskTitle:      "Alpha child",
			LastUpdatedAt:  &oldUpdated,
			HasTmuxSession: true,
			Task: domain.Task{
				ID:       alphaChildID,
				Title:    "Alpha child",
				Status:   domain.StatusInProgress,
				ParentID: &alphaID,
			},
		},
		{
			SessionID:      "az-beta-child",
			IssueID:        betaChildID.String(),
			TaskTitle:      "Beta child",
			LastUpdatedAt:  &newUpdated,
			HasTmuxSession: true,
			Task: domain.Task{
				ID:       betaChildID,
				Title:    "Beta child",
				Status:   domain.StatusInProgress,
				ParentID: &betaID,
			},
		},
	}
	snapshot := Snapshot{
		Entries: entries,
		TreeTasks: []domain.Task{
			{ID: alphaID, Title: "Alpha parent", Status: domain.StatusInProgress},
			{ID: betaID, Title: "Beta parent", Status: domain.StatusInProgress},
		},
	}
	model := New(fakeSnapshotLoader{snapshot: snapshot})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: snapshot})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	model = updateKey(t, model, "tab")
	orderView := ansi.Strip(model.View())
	alphaPos := strings.Index(orderView, "az-alpha")
	betaPos := strings.Index(orderView, "az-beta")
	if alphaPos < 0 || betaPos < 0 || alphaPos > betaPos {
		t.Fatalf("structural tree order changed unexpectedly:\n%s", orderView)
	}

	model = updateKey(t, model, "u")
	if model.treeSort != selectorTreeSortUpdated {
		t.Fatalf("tree sort = %v, want updated", model.treeSort)
	}
	updatedView := ansi.Strip(model.View())
	if !strings.Contains(updatedView, "[ Tree:updated ]") || !strings.Contains(updatedView, "u: sort") {
		t.Fatalf("updated tree view missing sort controls:\n%s", updatedView)
	}
	alphaPos = strings.Index(updatedView, "az-alpha")
	betaPos = strings.Index(updatedView, "az-beta")
	if alphaPos < 0 || betaPos < 0 || betaPos > alphaPos {
		t.Fatalf("updated tree order did not rank newest descendant first:\n%s", updatedView)
	}
}

func TestModelRestoresPersistedSelectorTab(t *testing.T) {
	store := &fakeUIStateStore{values: map[string]string{
		protocol.UIStateKeyTMUXSelectorLastActiveTab: "tree",
	}}
	model := New(fakeSnapshotLoader{}, WithUIStateStore(store))
	msg := model.loadSelectorTabCmd()()
	loaded, ok := msg.(selectorTabLoadedMsg)
	if !ok {
		t.Fatalf("load selector tab msg = %T", msg)
	}
	if loaded.err != nil || !loaded.found || loaded.tab != selectorTabTree {
		t.Fatalf("loaded tab = %+v, want found tree", loaded)
	}
	updated, _ := model.Update(loaded)
	model = updated.(Model)
	if model.activeTab != selectorTabTree {
		t.Fatalf("active tab = %v, want restored tree", model.activeTab)
	}
}

func TestSelectorTabPersistenceMapping(t *testing.T) {
	if got, ok := persistedValueForSelectorTab(selectorTabGrid); !ok || got != "cards" {
		t.Fatalf("persistedValueForSelectorTab(cards) = %q,%v", got, ok)
	}
	if got, ok := persistedValueForSelectorTab(selectorTabTree); !ok || got != "tree" {
		t.Fatalf("persistedValueForSelectorTab(tree) = %q,%v", got, ok)
	}
	if tab, ok := selectorTabFromPersistedValue("tree"); !ok || tab != selectorTabTree {
		t.Fatalf("selectorTabFromPersistedValue(tree) = %v,%v", tab, ok)
	}
	if tab, ok := selectorTabFromPersistedValue("cards"); !ok || tab != selectorTabGrid {
		t.Fatalf("selectorTabFromPersistedValue(cards) = %v,%v", tab, ok)
	}
	if _, ok := selectorTabFromPersistedValue("bogus"); ok {
		t.Fatal("selectorTabFromPersistedValue(bogus) should be invalid")
	}
}

func TestModelInitialLoadUsesLiveSnapshotBeforeEnrichment(t *testing.T) {
	loader := &fakeLiveSnapshotLoader{
		live: Snapshot{
			Enriching: true,
			Entries: []InventoryEntry{{
				SessionID: "az-bxf",
				IssueID:   "bxf",
				TaskTitle: "bxf",
			}},
		},
		enriched: Snapshot{Entries: []InventoryEntry{{
			SessionID:     "az-bxf",
			IssueID:       "bxf",
			TaskTitle:     "Standalone selector performance",
			GitAheadCount: 3,
		}}},
	}
	model := New(loader)

	msg := model.Init()()
	loaded, ok := msg.(LoadedMsg)
	if !ok {
		t.Fatalf("init msg = %T, want LoadedMsg", msg)
	}
	if loader.liveCalls != 1 || loader.enrichCalls != 0 {
		t.Fatalf("calls after init cmd live=%d enrich=%d, want live=1 enrich=0", loader.liveCalls, loader.enrichCalls)
	}
	if loaded.Snapshot.Entries[0].TaskTitle != "bxf" {
		t.Fatalf("initial title = %q, want live fallback", loaded.Snapshot.Entries[0].TaskTitle)
	}

	updated, cmd := model.Update(loaded)
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected background enrichment command")
	}
	if !strings.Contains(model.View(), "az-bxf") {
		t.Fatalf("first interactive view missing live row: %q", model.View())
	}
	enrichedMsg, ok := cmd().(EnrichedMsg)
	if !ok {
		t.Fatalf("enrich cmd msg = %T, want EnrichedMsg", enrichedMsg)
	}
	if loader.enrichCalls != 1 {
		t.Fatalf("enrich calls = %d, want 1", loader.enrichCalls)
	}
	if enrichedMsg.Snapshot.Entries[0].TaskTitle != "Standalone selector performance" {
		t.Fatalf("enriched title = %q", enrichedMsg.Snapshot.Entries[0].TaskTitle)
	}
}

func TestModelInitialLoadDoesNotWaitForPersistedSelectorTab(t *testing.T) {
	store := &fakeUIStateStore{values: map[string]string{
		protocol.UIStateKeyTMUXSelectorLastActiveTab: "tree",
	}}
	loader := fakeSnapshotLoader{snapshot: Snapshot{Entries: []InventoryEntry{{
		SessionID: "az-ckx",
		IssueID:   "ckx",
		TaskTitle: "ckx",
	}}}}
	model := New(loader, WithUIStateStore(store))

	msg := model.Init()()
	if _, ok := msg.(LoadedMsg); !ok {
		t.Fatalf("init msg = %T, want LoadedMsg", msg)
	}
	if len(store.gets) != 0 {
		t.Fatalf("selector tab store gets during init = %v, want none before live snapshot", store.gets)
	}
}

func TestModelSnapshotLoadRendersBeforePersistedSelectorTab(t *testing.T) {
	store := &fakeUIStateStore{values: map[string]string{
		protocol.UIStateKeyTMUXSelectorLastActiveTab: "tree",
	}}
	snapshot := Snapshot{Entries: []InventoryEntry{{
		SessionID: "az-ckx",
		IssueID:   "ckx",
		TaskTitle: "ckx",
	}}}
	model := New(fakeSnapshotLoader{snapshot: snapshot}, WithUIStateStore(store))

	updated, cmd := model.Update(LoadedMsg{Snapshot: snapshot})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("snapshot load did not request persisted selector tab")
	}
	if model.loading {
		t.Fatal("model waited for persisted selector tab before rendering")
	}
	if model.activeTab != selectorTabGrid {
		t.Fatalf("active tab before persisted load = %v, want default grid", model.activeTab)
	}
	if view := ansi.Strip(model.View()); strings.Contains(view, "Loading tmux sessions") || !strings.Contains(view, "az-ckx") {
		t.Fatalf("view before tab load should render snapshot, got:\n%s", view)
	}

	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	if model.loading {
		t.Fatal("model stayed loading after persisted selector tab loaded")
	}
	if model.activeTab != selectorTabTree {
		t.Fatalf("active tab = %v, want restored tree", model.activeTab)
	}
	if view := ansi.Strip(model.View()); !strings.Contains(view, "[ Tree ]") {
		t.Fatalf("view after tab load missing restored tree tab:\n%s", view)
	}
}

func TestModelUserTabChangeWinsOverLatePersistedSelectorTabLoad(t *testing.T) {
	store := &fakeUIStateStore{values: map[string]string{
		protocol.UIStateKeyTMUXSelectorLastActiveTab: "cards",
	}}
	snapshot := Snapshot{Entries: []InventoryEntry{{
		SessionID: "az-ckx",
		IssueID:   "ckx",
		TaskTitle: "ckx",
	}}}
	model := New(fakeSnapshotLoader{snapshot: snapshot}, WithUIStateStore(store))

	updated, cmd := model.Update(LoadedMsg{Snapshot: snapshot})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("snapshot load did not request persisted selector tab")
	}
	lateLoaded := cmd()

	updated, persistCmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	if model.activeTab != selectorTabTree {
		t.Fatalf("active tab after user tab = %v, want tree", model.activeTab)
	}
	if persistCmd == nil {
		t.Fatal("user tab did not persist active selector tab")
	}

	updated, _ = model.Update(lateLoaded)
	model = updated.(Model)
	if model.activeTab != selectorTabTree {
		t.Fatalf("active tab after late persisted load = %v, want user-selected tree", model.activeTab)
	}
}

func TestModelUserTabBeforePersistedSelectorTabLoadWins(t *testing.T) {
	store := &fakeUIStateStore{values: map[string]string{
		protocol.UIStateKeyTMUXSelectorLastActiveTab: "cards",
	}}
	snapshot := Snapshot{Entries: []InventoryEntry{{
		SessionID: "az-ckx",
		IssueID:   "ckx",
		TaskTitle: "ckx",
	}}}
	model := New(fakeSnapshotLoader{snapshot: snapshot}, WithUIStateStore(store))

	updated, cmd := model.Update(LoadedMsg{Snapshot: snapshot})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("snapshot load did not request persisted selector tab")
	}
	lateLoaded := cmd()

	updated, persistCmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	if persistCmd == nil {
		t.Fatal("tab key did not persist a value before persisted selector tab loaded")
	}
	if model.activeTab != selectorTabTree {
		t.Fatalf("active tab before persisted load = %v, want user-selected tree", model.activeTab)
	}
	if model.loading {
		t.Fatal("model stayed loading while persisted selector tab was pending")
	}

	updated, _ = model.Update(lateLoaded)
	model = updated.(Model)
	if model.activeTab != selectorTabTree {
		t.Fatalf("active tab after persisted load = %v, want user-selected tree", model.activeTab)
	}
}

func TestModelDefaultsCursorToCurrentTmuxSession(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{
		Entries:          entries,
		CurrentSessionID: "az-two",
	}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	if model.cursor != 1 {
		t.Fatalf("cursor = %d, want current session index 1", model.cursor)
	}

	model = updateKey(t, model, "down")
	if model.cursor != 2 {
		t.Fatalf("cursor after user move = %d, want 2", model.cursor)
	}
	updated, cmd = model.Update(EnrichedMsg{Snapshot: Snapshot{
		Entries: []InventoryEntry{
			entries[2],
			entries[1],
			entries[0],
		},
		CurrentSessionID: "az-two",
	}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("enriched update returned command")
	}
	if model.cursor != 0 {
		t.Fatalf("cursor after enrichment = %d, want moved session index 0", model.cursor)
	}
	selected, ok := model.selectedEntry()
	if !ok || selected.SessionID != "az-three" {
		t.Fatalf("selected after enrichment = %#v, want az-three", selected)
	}
}

func TestNormalizeSnapshotKeepsHumanAttentionAheadOfFullAzSession(t *testing.T) {
	waiting := InventoryEntry{
		SessionID: "az-waiting",
		Task: domain.Task{
			ID:      "waiting",
			Status:  domain.StatusInProgress,
			Session: &domain.Session{Activity: "waiting-for-human"},
		},
	}
	m := New(fakeSnapshotLoader{})
	m.snapshot.Entries = []InventoryEntry{waiting, {SessionID: defaultFullAzSession}}

	m.normalizeSnapshot()
	if got := m.snapshot.Entries[0].SessionID; got != waiting.SessionID {
		t.Fatalf("first session = %q, want human-attention session %q", got, waiting.SessionID)
	}
}

func TestModelDefaultsCursorToAttachedTmuxSessionWhenCurrentUnknown(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az", IssueID: "", TaskTitle: "Full az", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", HasTmuxSession: true, TmuxAttached: true, TmuxAttachedCount: 1},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	if model.cursor != 1 {
		t.Fatalf("cursor = %d, want attached session index 1 even with full az present", model.cursor)
	}
	selected, ok := model.selectedEntry()
	if !ok || selected.SessionID != "az-two" {
		t.Fatalf("selected = %#v, want az-two", selected)
	}
}

func TestModelShowsLoaderError(t *testing.T) {
	model := New(fakeSnapshotLoader{})
	updated, _ := model.Update(snapshotLoadedMsg{err: errors.New("boom")})
	model = updated.(Model)
	if !strings.Contains(model.View(), "boom") {
		t.Fatalf("view missing loader error: %q", model.View())
	}
}

func TestModelViewMarksAttachedTmuxSession(t *testing.T) {
	entries := []InventoryEntry{{
		SessionID:         "az-one",
		IssueID:           "one",
		TaskTitle:         "One",
		ProjectPath:       "/tmp/project",
		TmuxAttached:      true,
		TmuxAttachedCount: 2,
		HasTmuxSession:    true,
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 112, Height: 18})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	view := ansi.Strip(model.View())
	if !strings.Contains(view, "tmux az-one  attached x2") {
		t.Fatalf("view missing attached tmux metadata:\n%s", view)
	}
}

func TestModelViewUsesTmuxCopyAndBottomToolbar(t *testing.T) {
	entries := []InventoryEntry{{
		SessionID:      "az-one",
		IssueID:        "one",
		TaskTitle:      "One",
		ProjectPath:    "/tmp/project",
		HasTmuxSession: true,
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 112, Height: 18})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	view := model.View()
	if strings.Contains(view, "Tmux sessions") || strings.Contains(view, "Az tmux sessions") || strings.Contains(view, "Azedarach issue sessions") {
		t.Fatalf("view should not repeat popup title or use Az-only selector copy: %q", view)
	}
	if !strings.Contains(view, "tmux az-one") || !strings.Contains(view, "/tmp/project") {
		t.Fatalf("view missing tmux metadata inside card: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.HasPrefix(line, "  tmux ") {
			t.Fatalf("tmux metadata rendered outside card: %q\n%s", line, view)
		}
	}
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if strings.TrimSpace(lines[0]) == "" {
		t.Fatalf("view should not start with top padding:\n%q", view)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "h/j/k/l: move") || !strings.Contains(last, "q/Esc: close") {
		t.Fatalf("last line should be key toolbar, got %q\n%s", last, view)
	}
	if height := lipgloss.Height(view); height != 18 {
		t.Fatalf("view height = %d, want exactly 18\n%q", height, view)
	}
	if strings.HasSuffix(view, "\n") {
		t.Fatalf("view should not leave a blank row under toolbar:\n%q", view)
	}
}

func TestRenderSessionRowStylesInsertedMetaBordersLikeCardBorders(t *testing.T) {
	rendered := RenderSessionRow(SessionRow{
		SessionID:      "az-one",
		IssueID:        "one",
		TaskTitle:      "One",
		ProjectPath:    "/tmp/project",
		HasTmuxSession: true,
	}, true, 42, lipgloss.Style{}, lipgloss.Style{}, lipgloss.Style{}, styles.New())
	lines := strings.Split(rendered, "\n")
	if len(lines) < 4 {
		t.Fatalf("rendered card too short:\n%s", rendered)
	}
	metaLine := ""
	for _, line := range lines {
		if strings.Contains(ansi.Strip(line), "tmux az-one") {
			metaLine = line
			break
		}
	}
	if metaLine == "" {
		t.Fatalf("missing tmux metadata line:\n%s", rendered)
	}
	referenceLine := lines[len(lines)-3]
	width := ansi.StringWidth(referenceLine)
	if got, want := ansi.Cut(metaLine, 0, 1), ansi.Cut(referenceLine, 0, 1); got != want {
		t.Fatalf("left meta border should reuse styled card border:\ngot  %q\nwant %q\n%s", got, want, rendered)
	}
	if got, want := ansi.Cut(metaLine, width-1, width), ansi.Cut(referenceLine, width-1, width); got != want {
		t.Fatalf("right meta border should reuse styled card border:\ngot  %q\nwant %q\n%s", got, want, rendered)
	}
}

func TestRenderSessionRow_OriginBadgeOnMetaLine(t *testing.T) {
	cases := []struct {
		name     string
		origin   string
		want     string
		expected bool
	}{
		{name: "linear", origin: "linear", want: "lin", expected: true},
		{name: "local", origin: "local", want: "loc", expected: true},
		{name: "github", origin: "github", want: "gh", expected: true},
		{name: "empty_omits_badge", origin: "", want: "", expected: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := SessionRow{
				SessionID:      "az-proj-one",
				IssueID:        "one",
				TaskTitle:      "Origin badge test",
				HasTmuxSession: true,
				Task: domain.Task{
					ID:       naming.IssueID("one"),
					Title:    "Origin badge test",
					Status:   domain.StatusInProgress,
					Priority: domain.P2,
					Type:     domain.TypeTask,
					Origin:   tc.origin,
				},
			}
			rendered := RenderSessionRow(row, false, 50, lipgloss.Style{}, lipgloss.Style{}, lipgloss.Style{}, styles.New())
			lines := strings.Split(ansi.Strip(rendered), "\n")
			var metaLine string
			for _, line := range lines {
				if strings.Contains(line, "tmux az-proj-one") {
					metaLine = line
					break
				}
			}
			if metaLine == "" {
				t.Fatalf("missing tmux metadata line:\n%s", rendered)
			}
			trimmed := strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(metaLine, "│"), "│"), " ")
			if !tc.expected {
				for _, marker := range []string{"lin", "loc", "gh"} {
					if strings.HasSuffix(trimmed, marker) {
						t.Fatalf("expected no badge on meta line for empty origin, found %q in %q", marker, metaLine)
					}
				}
				return
			}
			if !strings.HasSuffix(trimmed, tc.want) {
				t.Fatalf("expected meta line to end with badge %q, got %q\nfull view:\n%s", tc.want, metaLine, rendered)
			}
		})
	}
}

func TestModelViewFitsViewportHeightWithWrappedCards(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One title wraps enough to make this card taller than a fixed row assumption", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two title wraps enough to make this card taller than a fixed row assumption", HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three title wraps enough to make this card taller than a fixed row assumption", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 50, Height: 20})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	view := model.View()
	if height := lipgloss.Height(view); height > 20 {
		t.Fatalf("view height = %d, want <= 20\n%s", height, view)
	}
}

func TestModelViewUsesRemainingHeightOnNarrowViewport(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three", HasTmuxSession: true},
		{SessionID: "az-four", IssueID: "four", TaskTitle: "Four", HasTmuxSession: true},
		{SessionID: "az-five", IssueID: "five", TaskTitle: "Five", HasTmuxSession: true},
		{SessionID: "az-six", IssueID: "six", TaskTitle: "Six", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 48, Height: 32})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	if columns := gridColumnCount(model.width); columns != 1 {
		t.Fatalf("columns = %d, want narrow one-column layout", columns)
	}
	if got, want := model.gridAvailableHeight(), 30; got != want {
		t.Fatalf("grid height budget = %d, want remaining viewport height %d", got, want)
	}
	view := model.View()
	for _, sessionID := range []string{"az-one", "az-two", "az-three"} {
		if !strings.Contains(view, sessionID) {
			t.Fatalf("narrow view should use vertical space and include %s:\n%s", sessionID, view)
		}
	}
	if height := lipgloss.Height(view); height != 32 {
		t.Fatalf("view height = %d, want exactly 32\n%s", height, view)
	}
}

func TestModelViewUsesCompactCardsOnMobileViewport(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", ProjectPath: "/tmp/project", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", ProjectPath: "/tmp/project", HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three", ProjectPath: "/tmp/project", HasTmuxSession: true},
		{SessionID: "az-four", IssueID: "four", TaskTitle: "Four", ProjectPath: "/tmp/project", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 14})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	availableHeight := model.gridAvailableHeight()
	columns := model.gridColumnCount(availableHeight)
	if columns != 1 {
		t.Fatalf("columns = %d, want mobile one-column layout", columns)
	}
	cardWidth := gridCardWidth(model.width, columns)
	visible := VisibleGridIndices(model.snapshot.Entries, model.cursor, columns, cardWidth, availableHeight, model.styles)
	if len(visible) < 2 {
		t.Fatalf("visible sessions = %d, want compact cards to fit multiple sessions; indices=%v", len(visible), visible)
	}

	view := model.View()
	if height := lipgloss.Height(view); height != 14 {
		t.Fatalf("view height = %d, want exactly 14\n%s", height, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 40 {
			t.Fatalf("line width = %d, want <= 40:\n%q\n\n%s", width, line, view)
		}
	}
	if !strings.Contains(view, "az-one") || !strings.Contains(view, "az-two") {
		t.Fatalf("mobile view should show multiple compact session cards:\n%s", view)
	}
}

func TestModelGridSmallViewportKeepsIssueAndSessionMetadataVisible(t *testing.T) {
	entries := []InventoryEntry{{
		SessionID:         "az-one",
		IssueID:           "one",
		TaskTitle:         "One",
		IssueStatus:       domain.StatusInProgress,
		State:             domain.SessionBusy,
		Activity:          "working",
		HasTmuxSession:    true,
		TmuxAttached:      true,
		TmuxAttachedCount: 2,
	}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 9})
	model = updated.(Model)
	updated, _ = model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)

	view := ansi.Strip(model.View())
	for _, want := range []string{"one", "issue in_progress", "tmux az-one", "attached x2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("small grid view missing projected metadata %q:\n%s", want, view)
		}
	}
}

func TestModelGridSmallViewportNavigationKeepsSelectionVisibleAfterResize(t *testing.T) {
	entries := make([]InventoryEntry, 12)
	for i := range entries {
		id := string(rune('a' + i))
		entries[i] = InventoryEntry{SessionID: "az-" + id, IssueID: id, TaskTitle: "Issue " + id, HasTmuxSession: true}
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	model = updated.(Model)
	updated, _ = model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)

	for _, key := range []tea.KeyType{tea.KeyRight, tea.KeyDown, tea.KeyDown} {
		updated, _ = model.Update(tea.KeyMsg{Type: key})
		model = updated.(Model)
		selected, ok := model.selectedEntry()
		if !ok || !strings.Contains(ansi.Strip(model.View()), selected.SessionID) {
			t.Fatalf("selection not visible after %s: cursor=%d selected=%#v\n%s", key, model.cursor, selected, ansi.Strip(model.View()))
		}
	}

	updated, _ = model.Update(tea.WindowSizeMsg{Width: 32, Height: 8})
	model = updated.(Model)
	selected, ok := model.selectedEntry()
	resizedView := model.View()
	if !ok || !strings.Contains(ansi.Strip(resizedView), selected.SessionID) {
		t.Fatalf("selection not visible after narrow resize: cursor=%d selected=%#v\n%s", model.cursor, selected, ansi.Strip(model.View()))
	}
	for _, line := range strings.Split(resizedView, "\n") {
		if got := ansi.StringWidth(line); got > model.width {
			t.Fatalf("narrow resize left content horizontally off-screen: line width=%d viewport=%d\n%s", got, model.width, ansi.Strip(resizedView))
		}
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	selected, ok = model.selectedEntry()
	if !ok || !strings.Contains(ansi.Strip(model.View()), selected.SessionID) {
		t.Fatalf("selection not visible after vertical scroll: cursor=%d selected=%#v\n%s", model.cursor, selected, ansi.Strip(model.View()))
	}
}

func TestModelViewUsesGridOnWideViewport(t *testing.T) {
	started := time.Unix(1775209200, 0).UTC()
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", StartedAt: &started, HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", StartedAt: &started, HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three", StartedAt: &started, HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 28})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	view := model.View()
	lineWithCards := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "one") && strings.Contains(line, "two") && strings.Contains(line, "three") {
			lineWithCards = line
			break
		}
	}
	if lineWithCards == "" {
		t.Fatalf("wide view did not render issue cards in a horizontal grid:\n%s", view)
	}
}

func TestModelViewUsesCompactCardsToFillTallViewport(t *testing.T) {
	entries := make([]InventoryEntry, 21)
	for i := range entries {
		issueID := string(rune('a' + i))
		entries[i] = InventoryEntry{
			SessionID:      "az-" + issueID,
			IssueID:        issueID,
			TaskTitle:      "Dashboard: investigate a moderately long task title that wraps in narrow cards",
			ProjectPath:    "/tmp/project",
			HasTmuxSession: true,
			GitAdditions:   100 + i,
			GitDeletions:   i,
		}
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 181, Height: 51})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	model.cursor = 10

	availableHeight := model.gridAvailableHeight()
	if got := gridColumnCount(model.width); got != 3 {
		t.Fatalf("baseline columns = %d, want width to allow 3 columns", got)
	}
	if got := model.gridColumnCount(availableHeight); got != 3 {
		t.Fatalf("height-aware columns = %d, want 3 columns when compact cards fit", got)
	}
	cardWidth := gridCardWidth(model.width, model.gridColumnCount(availableHeight))
	visible := VisibleGridIndices(model.snapshot.Entries, model.cursor, model.gridColumnCount(availableHeight), cardWidth, availableHeight, model.styles)
	if len(visible) != len(entries) {
		t.Fatalf("visible sessions = %d, want all %d to use tall viewport; indices=%v", len(visible), len(entries), visible)
	}
	if height := lipgloss.Height(model.View()); height != 51 {
		t.Fatalf("view height = %d, want exactly 51", height)
	}
}

func TestModelGridNavigationFollowsVisualRowsAndColumns(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three", HasTmuxSession: true},
		{SessionID: "az-four", IssueID: "four", TaskTitle: "Four", HasTmuxSession: true},
		{SessionID: "az-five", IssueID: "five", TaskTitle: "Five", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 28})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	model = updateKey(t, model, "right")
	if model.cursor != 1 {
		t.Fatalf("right cursor = %d, want 1", model.cursor)
	}
	model = updateKey(t, model, "down")
	if model.cursor != 4 {
		t.Fatalf("down cursor = %d, want 4", model.cursor)
	}
	model = updateKey(t, model, "right")
	if model.cursor != 4 {
		t.Fatalf("right from incomplete bottom row cursor = %d, want 4", model.cursor)
	}
	model = updateKey(t, model, "left")
	if model.cursor != 3 {
		t.Fatalf("left cursor = %d, want 3", model.cursor)
	}
	model = updateKey(t, model, "up")
	if model.cursor != 0 {
		t.Fatalf("up cursor = %d, want 0", model.cursor)
	}
}

func TestModelDigitHotkeysSelectFirstTenCardsOnly(t *testing.T) {
	entries := make([]InventoryEntry, 10)
	for i := range entries {
		issueID := string(rune('a' + i))
		entries[i] = InventoryEntry{
			SessionID:      "az-" + issueID,
			IssueID:        issueID,
			TaskTitle:      issueID,
			HasTmuxSession: true,
		}
	}
	switcher := &fakeSwitcher{}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}}, WithSwitcher(switcher))
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	for _, tt := range []struct {
		key  string
		want int
	}{
		{key: "0", want: 0},
		{key: "1", want: 1},
		{key: "2", want: 2},
		{key: "9", want: 9},
	} {
		updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)})
		model = updated.(Model)
		if cmd != nil {
			t.Fatalf("digit key %q returned command", tt.key)
		}
		if model.cursor != tt.want {
			t.Fatalf("digit key %q cursor = %d, want %d", tt.key, model.cursor, tt.want)
		}
		if switcher.sessionID != "" {
			t.Fatalf("digit key %q switched session %q; want selection only", tt.key, switcher.sessionID)
		}
	}
}

func TestModelDigitHotkeysSelectAzAsFirstCard(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "aa-task", IssueID: "task-a", TaskTitle: "Task A", HasTmuxSession: true},
		{SessionID: "az", IssueID: "az", TaskTitle: "az", HasTmuxSession: true},
		{SessionID: "ab-task", IssueID: "task-b", TaskTitle: "Task B", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	if model.snapshot.Entries[0].SessionID != "az" {
		t.Fatalf("first entry = %q, want az", model.snapshot.Entries[0].SessionID)
	}
	if model.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", model.cursor)
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("digit key returned command")
	}
	if model.cursor != 0 {
		t.Fatalf("cursor after 0 = %d, want 0", model.cursor)
	}
}

func TestModelDigitHotkeyOutOfRangeKeepsCurrentSelection(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	model.cursor = 1

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'9'}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("out-of-range digit returned command")
	}
	if model.cursor != 1 {
		t.Fatalf("cursor = %d, want unchanged index 1", model.cursor)
	}
}

func TestModelSlashSearchFiltersSessionsLive(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "Alpha task", ProjectPath: "/tmp/alpha", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Beta task", ProjectPath: "/tmp/beta", HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Gamma task", ProjectPath: "/tmp/gamma", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	model = updateKey(t, model, "/")
	if !model.searchMode {
		t.Fatal("expected / to enter search mode")
	}
	for _, r := range "beta" {
		updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = updated.(Model)
		if cmd != nil {
			t.Fatalf("search rune %q returned command", r)
		}
	}

	if got := len(model.filteredEntries()); got != 1 {
		t.Fatalf("filtered sessions = %d, want 1", got)
	}
	selected, ok := model.selectedEntry()
	if !ok || selected.SessionID != "az-two" {
		t.Fatalf("selected after search = %+v, want az-two", selected)
	}
	view := ansi.Strip(model.View())
	if !strings.Contains(view, "1/3 sessions match /beta") {
		t.Fatalf("view missing search status:\n%s", view)
	}
	if strings.Contains(view, "Alpha task") || strings.Contains(view, "Gamma task") {
		t.Fatalf("view should only include matching session:\n%s", view)
	}

	model = updateKey(t, model, "enter")
	if model.searchMode {
		t.Fatal("expected Enter to leave search mode")
	}
	if got := model.searchQuery; got != "beta" {
		t.Fatalf("search query after Enter = %q, want beta", got)
	}
	model = updateKey(t, model, "/")
	model = updateKey(t, model, "esc")
	if model.searchMode || model.searchQuery != "" {
		t.Fatalf("Esc should clear search mode/query, mode=%v query=%q", model.searchMode, model.searchQuery)
	}
	if got := len(model.filteredEntries()); got != 3 {
		t.Fatalf("filtered sessions after Esc = %d, want 3", got)
	}
}

func TestModelSlashSearchTreatsQAsQueryRune(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-ecq", IssueID: "ecq", TaskTitle: "ECQ task", HasTmuxSession: true},
		{SessionID: "az-else", IssueID: "else", TaskTitle: "Else task", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	model = updateKey(t, model, "/")
	for _, r := range "ecq" {
		updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = updated.(Model)
		if cmd != nil {
			t.Fatalf("search rune %q returned command", r)
		}
	}

	if !model.searchMode {
		t.Fatal("expected q to keep search mode active")
	}
	if model.searchQuery != "ecq" {
		t.Fatalf("search query = %q, want ecq", model.searchQuery)
	}
	if got := len(model.filteredEntries()); got != 1 {
		t.Fatalf("filtered sessions = %d, want 1", got)
	}
}

func TestModelSlashSearchFiltersTreeView(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	entries := []InventoryEntry{
		{
			SessionID:      "az-parent",
			IssueID:        parentID.String(),
			TaskTitle:      "Parent session",
			HasTmuxSession: true,
			Task: domain.Task{
				ID:     parentID,
				Title:  "Parent session",
				Status: domain.StatusInProgress,
			},
		},
		{
			SessionID:      "az-child",
			IssueID:        "az-child",
			TaskTitle:      "Needle child",
			HasTmuxSession: true,
			Task: domain.Task{
				ID:       naming.IssueID("az-child"),
				Title:    "Needle child",
				Status:   domain.StatusInProgress,
				ParentID: &parentID,
			},
		},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 18})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	model = updateKey(t, model, "tab")
	model = updateKey(t, model, "/")
	for _, r := range "needle" {
		updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = updated.(Model)
		if cmd != nil {
			t.Fatalf("search rune %q returned command", r)
		}
	}

	view := ansi.Strip(model.View())
	if !strings.Contains(view, "Needle child") || strings.Contains(view, "Parent session") {
		t.Fatalf("tree search did not filter rows:\n%s", view)
	}
}

func TestModelViewShowsNumericSelectionLabelsForFirstTenCards(t *testing.T) {
	entries := make([]InventoryEntry, 10)
	for i := range entries {
		entries[i] = InventoryEntry{
			SessionID:      "az-" + string(rune('a'+i)),
			IssueID:        "issue-" + string(rune('a'+i)),
			TaskTitle:      "issue " + string(rune('a'+i)),
			HasTmuxSession: true,
		}
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(tea.WindowSizeMsg{Width: 220, Height: 40})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("window resize returned command")
	}
	updated, cmd = model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	view := ansi.Strip(model.View())
	for i := 0; i <= 9; i++ {
		want := string(rune('0' + i))
		if !strings.Contains(view, want) {
			t.Fatalf("view missing hotkey %q\n%s", want, view)
		}
	}
}

func TestModelTreeViewShowsNumericSelectionIndexColumn(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 18})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	model = updateKey(t, model, "tab")

	view := ansi.Strip(model.View())
	if !strings.Contains(view, ">0 az-one") {
		t.Fatalf("selected tree row missing numeric index column:\n%s", view)
	}
	if !strings.Contains(view, " 1 az-two") {
		t.Fatalf("unselected tree row missing numeric index column:\n%s", view)
	}
}

func TestModelGotoWordJumpSelectsVisibleSession(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 140, Height: 24})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("g returned command")
	}
	if !model.gotoArmed {
		t.Fatal("expected g to arm goto mode")
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("w returned command")
	}
	if model.jumpMode == nil {
		t.Fatal("expected gw to start jump mode")
	}
	if got, want := model.jumpMode.GetLabel(1), "ab"; got != want {
		t.Fatalf("jump label 1 = %q, want %q", got, want)
	}
	if !strings.Contains(model.View(), "jump: type label") {
		t.Fatalf("view missing jump status:\n%s", model.View())
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("first jump key returned command")
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("jump label did not return selection command")
	}
	updated, cmd = model.Update(cmd())
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("jump selected returned command")
	}
	if model.cursor != 1 {
		t.Fatalf("cursor = %d, want selected entry 1", model.cursor)
	}
	if model.jumpMode != nil || model.gotoArmed {
		t.Fatal("expected jump mode to clear after selection")
	}
}

func TestModelGotoWordJumpLabelsKeepGridWithinViewport(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three", HasTmuxSession: true},
		{SessionID: "az-four", IssueID: "four", TaskTitle: "Four", HasTmuxSession: true},
		{SessionID: "az-five", IssueID: "five", TaskTitle: "Five", HasTmuxSession: true},
		{SessionID: "az-six", IssueID: "six", TaskTitle: "Six", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 140, Height: 24})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	model = updateKey(t, model, "g")
	model = updateKey(t, model, "w")

	view := model.View()
	if !strings.Contains(view, "jump: type label") {
		t.Fatalf("view missing jump status:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(line); got > model.width {
			t.Fatalf("gw line width = %d, want <= %d:\n%q\n\n%s", got, model.width, line, view)
		}
	}
}

func updateKey(t *testing.T, model Model, key string) Model {
	t.Helper()
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	if cmd != nil {
		t.Fatalf("key %q returned command", key)
	}
	return updated.(Model)
}

func TestRenderVisibleRowsFitsMeasuredHeight(t *testing.T) {
	rows := []SessionRow{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One title wraps enough to make this card taller than a fixed row assumption", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two title wraps enough to make this card taller than a fixed row assumption", HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three title wraps enough to make this card taller than a fixed row assumption", HasTmuxSession: true},
	}
	rowHeight := lipgloss.Height(RenderSessionRow(rows[0], true, 42, lipgloss.Style{}, lipgloss.Style{}, lipgloss.Style{}, styles.New())) + 1
	availableHeight := rowHeight

	rendered := RenderVisibleRows(rows, 0, 42, availableHeight, styles.New())
	if len(rendered) == 0 {
		t.Fatal("expected selected row to render")
	}
	if len(rendered) != 1 {
		t.Fatalf("rendered rows = %d, want 1 row within measured height budget", len(rendered))
	}
	if !strings.Contains(rendered[0], "az-one") {
		t.Fatalf("selected row missing from rendered rows: %q", strings.Join(rendered, "\n"))
	}
	totalHeight := 0
	for _, row := range rendered {
		totalHeight += lipgloss.Height(row) + 1
	}
	if totalHeight > availableHeight {
		t.Fatalf("rendered rows height = %d, want <= %d\n%s", totalHeight, availableHeight, strings.Join(rendered, "\n"))
	}
}

func TestModelHidesEntriesWithoutTmuxSession(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true},
		{SessionID: "", IssueID: "stale-task", TaskTitle: "Stale", HasTmuxSession: false},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", HasTmuxSession: true},
	}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	if got := len(model.snapshot.Entries); got != 2 {
		t.Fatalf("snapshot entries after normalization = %d, want 2 (issue-only entry dropped)", got)
	}
	for _, entry := range model.snapshot.Entries {
		if strings.TrimSpace(entry.SessionID) == "" {
			t.Fatalf("normalization left an entry with empty SessionID: %+v", entry)
		}
	}
	view := model.View()
	if strings.Contains(view, "Stale") {
		t.Fatalf("view should not show issue-only entries: %s", view)
	}
}

func TestModelXKillsSelectedSessionAndRefreshes(t *testing.T) {
	entries := []InventoryEntry{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two", HasTmuxSession: true},
	}
	killer := &fakeKiller{}
	loader := fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}}
	model := New(loader, WithKiller(killer))
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: loader.snapshot})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	model = updated.(Model)

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Fatal("x did not produce kill command")
	}
	msg, ok := cmd().(KillResultMsg)
	if !ok {
		t.Fatalf("kill msg = %T, want KillResultMsg", msg)
	}
	if msg.Err != nil {
		t.Fatalf("kill returned error: %v", msg.Err)
	}
	if got := killer.killed; len(got) != 1 || got[0].SessionID != "az-two" {
		t.Fatalf("killed entries = %+v, want one entry with SessionID az-two", got)
	}

	updated, cmd = model.Update(msg)
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("kill success did not trigger a refresh load command")
	}
	if !model.loading {
		t.Fatalf("model should re-enter loading state after kill, got loading=%v", model.loading)
	}
	if !strings.Contains(model.status, "killed az-two") {
		t.Fatalf("status = %q, want killed az-two announcement", model.status)
	}
}

func TestModelXFailsGracefullyWhenKillerMissing(t *testing.T) {
	entries := []InventoryEntry{{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true}}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}})
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Fatal("x did not produce kill command even without killer")
	}
	msg, ok := cmd().(KillResultMsg)
	if !ok {
		t.Fatalf("kill msg = %T, want KillResultMsg", msg)
	}
	if msg.Err == nil || !strings.Contains(msg.Err.Error(), "unavailable") {
		t.Fatalf("kill err = %v, want unavailable", msg.Err)
	}
	updated, _ = model.Update(msg)
	model = updated.(Model)
	if model.err == nil {
		t.Fatal("expected model err to be populated after kill failure")
	}
}

func TestModelXSurfacesKillerError(t *testing.T) {
	entries := []InventoryEntry{{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true}}
	killer := &fakeKiller{err: errors.New("kill boom")}
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: entries}}, WithKiller(killer))
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: entries}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Fatal("x did not produce kill command")
	}
	msg := cmd().(KillResultMsg)
	if msg.Err == nil {
		t.Fatal("expected killer error to surface")
	}
	updated, refresh := model.Update(msg)
	model = updated.(Model)
	if refresh != nil {
		t.Fatalf("error path should not trigger refresh, got cmd=%v", refresh)
	}
	if !strings.Contains(model.status, "kill az-one failed") {
		t.Fatalf("status = %q, want failure note", model.status)
	}
}

func TestModelFooterAdvertisesKillBinding(t *testing.T) {
	model := New(fakeSnapshotLoader{snapshot: Snapshot{Entries: []InventoryEntry{{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true}}}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	model = updated.(Model)
	updated, cmd := model.Update(snapshotLoadedMsg{snapshot: Snapshot{Entries: []InventoryEntry{{SessionID: "az-one", IssueID: "one", TaskTitle: "One", HasTmuxSession: true}}}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("snapshot update returned command")
	}
	if got := ansi.Strip(model.View()); !strings.Contains(got, "x: kill") {
		t.Fatalf("view missing kill binding hint:\n%s", got)
	}
}

func TestRenderVisibleGridFitsMeasuredHeight(t *testing.T) {
	rows := []SessionRow{
		{SessionID: "az-one", IssueID: "one", TaskTitle: "One title wraps enough to make this card taller than a fixed row assumption", HasTmuxSession: true},
		{SessionID: "az-two", IssueID: "two", TaskTitle: "Two title wraps enough to make this card taller than a fixed row assumption", HasTmuxSession: true},
		{SessionID: "az-three", IssueID: "three", TaskTitle: "Three title wraps enough to make this card taller than a fixed row assumption", HasTmuxSession: true},
		{SessionID: "az-four", IssueID: "four", TaskTitle: "Four title wraps enough to make this card taller than a fixed row assumption", HasTmuxSession: true},
	}
	firstGridRow := RenderVisibleGrid(rows, 0, 2, 42, 100, styles.New())[0]
	availableHeight := lipgloss.Height(firstGridRow) + 1

	rendered := RenderVisibleGrid(rows, 0, 2, 42, availableHeight, styles.New())
	if len(rendered) != 1 {
		t.Fatalf("grid rows = %d, want only one visible grid row", len(rendered))
	}
	if !strings.Contains(rendered[0], "az-one") || !strings.Contains(rendered[0], "az-two") {
		t.Fatalf("selected grid row missing expected cells:\n%s", rendered[0])
	}
	totalHeight := 0
	for _, row := range rendered {
		totalHeight += lipgloss.Height(row) + 1
	}
	if totalHeight > availableHeight {
		t.Fatalf("rendered grid height = %d, want <= %d\n%s", totalHeight, availableHeight, strings.Join(rendered, "\n"))
	}
}
