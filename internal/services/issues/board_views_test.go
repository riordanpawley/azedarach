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

func boardViewTestCustomView(id string) domain.BoardViewDefinition {
	return domain.BoardViewDefinition{
		SchemaVersion: domain.BoardViewDefinitionSchemaVersion,
		ID:            id,
		Name:          "Custom Active",
		Columns: []domain.BoardViewColumnDefinition{{
			ID:    "active",
			Title: "Active",
			Predicate: domain.BoardViewColumnPredicate{
				Type:         domain.BoardViewPredicateDisplayPhase,
				DisplayPhase: domain.IssueDisplayActive,
			},
		}},
	}
}

func boardViewTestHasView(views []domain.BoardViewRecord, id string) bool {
	for _, view := range views {
		if view.View.ID == id {
			return true
		}
	}
	return false
}
