package tmuxselector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

type fakeSnapshotLoader struct {
	snapshot Snapshot
	err      error
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
}

func (f *fakeSwitcher) SwitchClient(_ context.Context, sessionID string) error {
	f.sessionID = sessionID
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
	entries []InventoryEntry
	err     error
}

func (f *fakeDetailOpener) OpenDetail(_ context.Context, entry InventoryEntry) error {
	f.entries = append(f.entries, entry)
	return f.err
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
	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("enter did not produce switch command")
	}
	msg := cmd()
	if _, ok := msg.(switchCompleteMsg); !ok {
		t.Fatalf("switch msg = %T", msg)
	}
	if switcher.sessionID != "ch-two" {
		t.Fatalf("switched session = %q, want ch-two", switcher.sessionID)
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

func TestModelShowsLoaderError(t *testing.T) {
	model := New(fakeSnapshotLoader{})
	updated, _ := model.Update(snapshotLoadedMsg{err: errors.New("boom")})
	model = updated.(Model)
	if !strings.Contains(model.View(), "boom") {
		t.Fatalf("view missing loader error: %q", model.View())
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
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 96, Height: 18})
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
	if got, want := model.jumpMode.GetLabel(1), "b"; got != want {
		t.Fatalf("jump label 1 = %q, want %q", got, want)
	}
	if !strings.Contains(model.View(), "jump: type label") {
		t.Fatalf("view missing jump status:\n%s", model.View())
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
