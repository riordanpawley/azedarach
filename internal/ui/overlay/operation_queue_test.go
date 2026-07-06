package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestOperationQueueOverlayRendersDependencyTree(t *testing.T) {
	overlay := NewOperationQueueOverlay(testOperationQueueSnapshot())
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	overlay = model.(*OperationQueueOverlay)

	view := overlay.View()
	for _, needle := range []string{
		"Operation Queue",
		"running 1",
		"queued 2",
		"op-running running git.merge az-1",
		"`- op-queued queued by=op-running",
		"worktree.cleanu",
		"by=op-running",
		"queued: op-free queued notice.sync az-3",
		"r refresh",
		"t/Tab tree/table",
	} {
		if !strings.Contains(view, needle) {
			t.Fatalf("View() missing %q:\n%s", needle, view)
		}
	}
}

func TestOperationQueueOverlayToggleTableAndRefresh(t *testing.T) {
	overlay := NewOperationQueueOverlay(testOperationQueueSnapshot())

	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	overlay = model.(*OperationQueueOverlay)
	view := overlay.View()
	if !strings.Contains(view, "ID  STATE  KIND  ISSUE  BLOCKED_BY  RESOURCES") {
		t.Fatalf("table view missing header:\n%s", view)
	}

	model, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	overlay = model.(*OperationQueueOverlay)
	if !overlay.loading {
		t.Fatal("refresh should mark overlay loading")
	}
	if cmd == nil {
		t.Fatal("refresh should emit command")
	}
	msg, ok := cmd().(SelectionMsg)
	if !ok || msg.Key != "operation_queue_refresh" {
		t.Fatalf("refresh message = %T/%+v, want operation_queue_refresh", msg, msg)
	}
}

func TestOperationQueueOverlayFitsSmallViewport(t *testing.T) {
	overlay := NewOperationQueueOverlay(testOperationQueueSnapshot())
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 64, Height: 18})
	overlay = model.(*OperationQueueOverlay)
	width, height := overlay.Size()
	if width > 64 || height > 18 {
		t.Fatalf("Size() = %dx%d, want within 64x18", width, height)
	}
	view := overlay.View()
	if !strings.Contains(view, "Operation Queue") || !strings.Contains(view, "Esc/q/backspace") {
		t.Fatalf("small view missing title/actions:\n%s", view)
	}
}

func testOperationQueueSnapshot() protocol.OperationQueueResponseBody {
	return protocol.OperationQueueResponseBody{
		ProjectID: "proj",
		Running: []protocol.OperationQueueEntry{{
			Operation: protocol.OperationRecord{
				OperationID:  "op-running",
				ProjectID:    "proj",
				Kind:         "git.merge",
				IssueID:      "az-1",
				ResourceKeys: []string{"worktree:/tmp/wt"},
				State:        protocol.OperationStateRunning,
				Progress:     &protocol.OperationProgress{Percent: 40},
			},
		}},
		Queued: []protocol.OperationQueueEntry{
			{
				Operation: protocol.OperationRecord{
					OperationID: "op-queued",
					ProjectID:   "proj",
					Kind:        "worktree.cleanup",
					IssueID:     "az-2",
					State:       protocol.OperationStateQueued,
				},
				QueueIndex:           1,
				BlockingOperationIDs: []naming.OperationID{"op-running"},
				BlockedResourceKeys:  []string{"worktree:/tmp/wt"},
			},
			{
				Operation: protocol.OperationRecord{
					OperationID:  "op-free",
					ProjectID:    "proj",
					Kind:         "notice.sync",
					IssueID:      "az-3",
					ResourceKeys: []string{"notice:proj"},
					State:        protocol.OperationStateQueued,
				},
				QueueIndex: 2,
			},
		},
	}
}
