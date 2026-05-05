package tmuxselector

import (
	"context"
	"errors"
	"strings"
	"testing"

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
