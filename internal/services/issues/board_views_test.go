package issues

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	if legacyDefault.View.ID != domain.BoardViewDefaultID {
		t.Fatalf("legacy default view id = %q, want %q", legacyDefault.View.ID, domain.BoardViewDefaultID)
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

func TestBoardViewInvalidTypedDefinitionFailsSafely(t *testing.T) {
	ctx := context.Background()
	client := NewClientAtPath(t.TempDir()+"/issues.db", nil)
	t.Cleanup(func() {
		if err := client.CloseDB(); err != nil {
			t.Fatalf("CloseDB error: %v", err)
		}
	})
	projectID := "proj-invalid-board"
	if _, err := client.SaveBoardView(ctx, projectID, boardViewTestCustomView("invalid-me")); err != nil {
		t.Fatalf("SaveBoardView error: %v", err)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatalf("dbHandle error: %v", err)
	}
	invalidDefinition := `{"schema_version":1,"id":"invalid-me","title":"Invalid","columns":[{"id":"active","title":"Active","predicates":[{"kind":"display_phase","display_phases":["active"],"unexpected":true}]}]}`
	if _, err := db.ExecContext(ctx, `UPDATE board_views SET definition_json = ? WHERE project_id = ? AND id = ?`, invalidDefinition, projectID, "invalid-me"); err != nil {
		t.Fatalf("corrupt board view: %v", err)
	}

	_, err = client.GetBoardView(ctx, projectID, "invalid-me")
	if err == nil {
		t.Fatal("GetBoardView error = nil, want typed definition decode error")
	}
	if !strings.Contains(err.Error(), `decode board view "invalid-me"`) || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("GetBoardView error = %v, want typed decode error with view id", err)
	}
}

func TestBoardViewsReseedBuiltInDefinitions(t *testing.T) {
	ctx := context.Background()
	client := NewClientAtPath(t.TempDir()+"/issues.db", nil)
	t.Cleanup(func() {
		if err := client.CloseDB(); err != nil {
			t.Fatalf("CloseDB error: %v", err)
		}
	})
	projectID := "proj-reseed-board"
	if _, err := client.ListBoardViews(ctx, projectID); err != nil {
		t.Fatalf("ListBoardViews seed error: %v", err)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatalf("dbHandle error: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE board_views
		SET name = 'Broken', definition_json = '{"schema_version":999}', deleted_at = '2026-07-09T00:00:00Z'
		WHERE project_id = ? AND id = ?
	`, projectID, domain.DefaultBoardViewID); err != nil {
		t.Fatalf("break default view: %v", err)
	}

	record, err := client.GetBoardView(ctx, projectID, domain.DefaultBoardViewID)
	if err != nil {
		t.Fatalf("GetBoardView after reseed error: %v", err)
	}
	if record.View.ID != domain.BoardViewDefaultID || record.View.Title != domain.DefaultBoardView().Title {
		t.Fatalf("reseeded view = %+v, want built-in default", record.View)
	}
	if !record.BuiltIn {
		t.Fatal("reseeded default BuiltIn = false, want true")
	}
}

func TestBoardViewsMigrateLegacyBuiltInsAndPreserveCustomIDConflict(t *testing.T) {
	ctx := context.Background()
	client := NewClientAtPath(t.TempDir()+"/issues.db", nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	projectID := "proj-board-upgrade"
	if _, err := client.ListBoardViews(ctx, projectID); err != nil {
		t.Fatalf("initial seed: %v", err)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatalf("dbHandle: %v", err)
	}
	custom := boardViewTestCustomView(string(domain.BoardViewDefaultID))
	definition, err := domain.EncodeBoardViewDefinitionJSON(custom)
	if err != nil {
		t.Fatalf("encode custom conflict: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `DELETE FROM board_views WHERE project_id = ? AND id = ?`, projectID, domain.BoardViewDefaultID); err != nil {
		t.Fatalf("remove seeded default: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO board_views (project_id, id, name, definition_json, built_in, created_at, updated_at, deleted_at)
		VALUES (?, ?, 'Legacy Current', ?, 1, ?, ?, NULL)
	`, projectID, domain.BoardViewCurrentID, mustEncodeBoardView(t, domain.DefaultBoardView()), now, now); err != nil {
		t.Fatalf("seed legacy built-in: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO board_views (project_id, id, name, definition_json, built_in, created_at, updated_at, deleted_at)
		VALUES (?, ?, 'Conflicting Custom', ?, 0, ?, ?, NULL)
	`, projectID, domain.BoardViewDefaultID, string(definition), now, now); err != nil {
		t.Fatalf("seed upgraded catalog: %v", err)
	}

	views, err := client.ListBoardViews(ctx, projectID)
	if err != nil {
		t.Fatalf("ListBoardViews upgrade: %v", err)
	}
	if boardViewTestHasView(views, string(domain.BoardViewCurrentID)) {
		t.Fatal("legacy current built-in remains after upgrade")
	}
	if !boardViewTestHasView(views, string(domain.BoardViewDefaultID)) || !boardViewTestHasView(views, "default-custom") {
		t.Fatalf("upgraded views = %+v, want default and preserved default-custom", views)
	}
	preserved, err := client.GetBoardView(ctx, projectID, "default-custom")
	if err != nil {
		t.Fatalf("GetBoardView preserved custom: %v", err)
	}
	if preserved.BuiltIn || preserved.View.Title != custom.Title {
		t.Fatalf("preserved custom = %+v", preserved)
	}
	if got := preserved.UpdatedAt.Format(time.RFC3339Nano); got != now {
		t.Fatalf("preserved custom updated_at = %q, want %q", got, now)
	}
}

func TestBoardViewsCatalogMigrationRollsBackOnCorruptIDConflict(t *testing.T) {
	ctx := context.Background()
	client := NewClientAtPath(t.TempDir()+"/issues.db", nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	projectID := "proj-board-corrupt-upgrade"
	if _, err := client.ListBoardViews(ctx, projectID); err != nil {
		t.Fatalf("initial seed: %v", err)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatalf("dbHandle: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE board_views
		SET built_in = 0, name = 'Corrupt Custom', definition_json = '{"schema_version":999}'
		WHERE project_id = ? AND id = ?
	`, projectID, domain.BoardViewOrchestrationID); err != nil {
		t.Fatalf("seed corrupt conflict: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE board_views SET name = 'Sentinel Default'
		WHERE project_id = ? AND id = ?
	`, projectID, domain.BoardViewDefaultID); err != nil {
		t.Fatalf("seed rollback sentinel: %v", err)
	}

	_, err = client.ListBoardViews(ctx, projectID)
	if err == nil || !strings.Contains(err.Error(), `conflicting with built-in "orchestration"`) {
		t.Fatalf("ListBoardViews error = %v, want corrupt conflict", err)
	}
	var name string
	if err := db.QueryRowContext(ctx, `
		SELECT name FROM board_views WHERE project_id = ? AND id = ?
	`, projectID, domain.BoardViewDefaultID).Scan(&name); err != nil {
		t.Fatalf("read rollback sentinel: %v", err)
	}
	if name != "Sentinel Default" {
		t.Fatalf("default name = %q, want transaction rollback sentinel", name)
	}
}

func mustEncodeBoardView(t *testing.T, view domain.BoardView) string {
	t.Helper()
	data, err := domain.EncodeBoardViewDefinitionJSON(view)
	if err != nil {
		t.Fatalf("encode board view: %v", err)
	}
	return string(data)
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
