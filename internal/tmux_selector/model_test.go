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
	last := lines[len(lines)-1]
	if !strings.Contains(last, "h/j/k/l: move") || !strings.Contains(last, "q/Esc: close") {
		t.Fatalf("last line should be key toolbar, got %q\n%s", last, view)
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
