package issues

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestBoardViewsSeedDefaultsAndIsolateProjects(t *testing.T) {
	ctx := context.Background()
	client := NewClientAtPath(t.TempDir()+"/issues.db", nil)
	t.Cleanup(func() {
		if err := client.CloseDB(); err != nil {
			t.Fatalf("CloseDB error: %v", err)
		}
	})

	projectA := "proj-board-a"
	projectB := "proj-board-b"
	if _, err := client.SaveBoardView(ctx, projectA, boardViewTestCustomView("custom-active")); err != nil {
		t.Fatalf("SaveBoardView error: %v", err)
	}
	viewsA, err := client.ListBoardViews(ctx, projectA)
	if err != nil {
		t.Fatalf("ListBoardViews projectA error: %v", err)
	}
	viewsB, err := client.ListBoardViews(ctx, projectB)
	if err != nil {
		t.Fatalf("ListBoardViews projectB error: %v", err)
	}
	if !boardViewTestHasView(viewsA, domain.DefaultBoardViewID) || !boardViewTestHasView(viewsA, "custom-active") {
		t.Fatalf("projectA views = %+v, want default and custom", viewsA)
	}
	if !boardViewTestHasView(viewsB, domain.DefaultBoardViewID) || boardViewTestHasView(viewsB, "custom-active") {
		t.Fatalf("projectB views = %+v, want isolated default only", viewsB)
	}
	if _, err := client.SaveBoardView(ctx, projectA, domain.DefaultBoardView()); !errors.Is(err, ErrBoardViewBuiltIn) {
		t.Fatalf("SaveBoardView default error = %v, want ErrBoardViewBuiltIn", err)
	}
	legacyDefault, err := client.GetBoardView(ctx, projectA, "default")
	if err != nil {
		t.Fatalf("GetBoardView legacy default error: %v", err)
	}
	if legacyDefault.View.ID != domain.BoardViewCurrentID {
		t.Fatalf("legacy default view id = %q, want %q", legacyDefault.View.ID, domain.BoardViewCurrentID)
	}
}

func TestBoardViewCorruptDefinitionFailsSafely(t *testing.T) {
	ctx := context.Background()
	client := NewClientAtPath(t.TempDir()+"/issues.db", nil)
	t.Cleanup(func() {
		if err := client.CloseDB(); err != nil {
			t.Fatalf("CloseDB error: %v", err)
		}
	})
	projectID := "proj-corrupt-board"
	if _, err := client.SaveBoardView(ctx, projectID, boardViewTestCustomView("corrupt-me")); err != nil {
		t.Fatalf("SaveBoardView error: %v", err)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatalf("dbHandle error: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE board_views SET definition_json = ? WHERE project_id = ? AND id = ?`, `{"schema_version":999,"id":"corrupt-me"}`, projectID, "corrupt-me"); err != nil {
		t.Fatalf("corrupt board view: %v", err)
	}

	_, err = client.GetBoardView(ctx, projectID, "corrupt-me")
	if err == nil {
		t.Fatal("GetBoardView error = nil, want corrupt definition error")
	}
	if !strings.Contains(err.Error(), `decode board view "corrupt-me"`) {
		t.Fatalf("GetBoardView error = %v, want clear corrupt view id", err)
	}
}

func boardViewTestCustomView(id string) domain.BoardView {
	return domain.BoardView{
		ID:    domain.BoardViewID(id),
		Title: "Custom Active",
		Columns: []domain.BoardColumn{{
			ID:    domain.BoardColumnActive,
			Title: "Active",
			Predicates: []domain.BoardColumnPredicate{{
				Kind:          domain.BoardPredicateDisplayPhase,
				DisplayPhases: []domain.IssueDisplayPhase{domain.IssueDisplayActive},
			}},
		}},
	}
}

func boardViewTestHasView(views []domain.BoardViewRecord, id string) bool {
	for _, view := range views {
		if string(view.View.ID) == id {
			return true
		}
	}
	return false
}
