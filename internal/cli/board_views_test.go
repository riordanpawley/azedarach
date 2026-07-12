package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestParseBoardViewArgs(t *testing.T) {
	opts, err := ParseBoardViewArgs("select", []string{"--project", "proj-a", "--json", "activity"})
	if err != nil {
		t.Fatalf("ParseBoardViewArgs select error: %v", err)
	}
	if opts.Project != "proj-a" || !opts.JSON || opts.ViewID != "activity" {
		t.Fatalf("select opts = %+v", opts)
	}

	if _, err := ParseBoardViewArgs("delete", []string{"--confirm"}); err == nil {
		t.Fatal("ParseBoardViewArgs delete without view id error = nil")
	}
	if _, err := ParseBoardViewArgs("create", []string{}); err == nil {
		t.Fatal("ParseBoardViewArgs create without file error = nil")
	}
}

func TestParseGlobalViewScope(t *testing.T) {
	selected, err := parseGlobalViewScope("selected_projects", "alpha,beta")
	if err != nil {
		t.Fatalf("selected scope: %v", err)
	}
	if selected.Kind != protocol.GlobalViewScopeSelectedProjects || len(selected.ProjectIDs) != 2 {
		t.Fatalf("selected scope = %+v", selected)
	}
	current, err := parseGlobalViewScope("current_project", "alpha")
	if err != nil || current.CurrentProjectID != "alpha" {
		t.Fatalf("current scope = %+v, err=%v", current, err)
	}
	if _, err := parseGlobalViewScope("selected_projects", ""); err == nil {
		t.Fatal("empty selected scope accepted")
	}
}

func TestFormatGlobalViewScope(t *testing.T) {
	got := formatGlobalViewScope(protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeSelectedProjects, ProjectIDs: []naming.ProjectID{"alpha", "beta"}})
	if got != "selected_projects (alpha,beta)" {
		t.Fatalf("scope = %q", got)
	}
}

func TestBoardViewCommandUsesTypedDaemonClient(t *testing.T) {
	updatedAt := time.Date(2026, time.July, 9, 12, 0, 0, 0, time.UTC)
	transport := &fakeDaemonTransport{
		commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Meta.ProjectID != naming.ProjectID("proj-board") {
				t.Fatalf("project id = %q, want proj-board", req.Meta.ProjectID)
			}
			switch req.Command {
			case protocol.CommandBoardViewList:
				return responseWithJSON(req, protocol.BoardViewListResponseBody{
					ProjectID:      "proj-board",
					SelectedViewID: domain.DefaultBoardViewID,
					Views: []domain.BoardViewRecord{{
						ProjectID: "proj-board",
						View:      domain.DefaultBoardView(),
						BuiltIn:   true,
					}},
				}), nil
			case protocol.CommandBoardViewSelect:
				var body protocol.BoardViewSelectRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("decode select body: %v", err)
				}
				if body.ViewID != "activity" {
					t.Fatalf("select view id = %q, want activity", body.ViewID)
				}
				return responseWithJSON(req, protocol.BoardViewSelectResponseBody{
					ProjectID: "proj-board",
					ViewID:    body.ViewID,
					UpdatedAt: updatedAt,
				}), nil
			case protocol.CommandBoardViewDelete:
				var body protocol.BoardViewDeleteRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("decode delete body: %v", err)
				}
				if body.ViewID != "activity" {
					t.Fatalf("delete view id = %q, want activity", body.ViewID)
				}
				return responseWithJSON(req, map[string]any{"deleted": true}), nil
			default:
				t.Fatalf("unexpected command %q", req.Command)
				return protocol.ResponseEnvelope{}, nil
			}
		},
	}
	deps := &Dependencies{DaemonClient: daemonclient.New(transport).WithProjectID("proj-board")}

	if err := BoardViewCommand(deps, BoardViewOptions{Command: "list", Project: "proj-board"}); err != nil {
		t.Fatalf("list error: %v", err)
	}
	if err := BoardViewCommand(deps, BoardViewOptions{Command: "select", Project: "proj-board", ViewID: "activity"}); err != nil {
		t.Fatalf("select error: %v", err)
	}
	if err := BoardViewCommand(deps, BoardViewOptions{Command: "delete", Project: "proj-board", ViewID: "activity", Confirm: true}); err != nil {
		t.Fatalf("delete error: %v", err)
	}
}

func TestBoardViewCreateReadsValidatedDefinition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "view.json")
	view := domain.DefaultBoardView()
	view.ID = "active-only"
	view.Title = "Active Only"
	view.Columns = view.Columns[2:3]
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write view: %v", err)
	}

	transport := &fakeDaemonTransport{
		commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandBoardViewSave {
				t.Fatalf("command = %q, want board.view.save", req.Command)
			}
			var body protocol.BoardViewSaveRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("decode save body: %v", err)
			}
			if body.View.ID != "active-only" || len(body.View.Columns) != 1 {
				t.Fatalf("save body view = %+v", body.View)
			}
			return responseWithJSON(req, protocol.BoardViewResponseBody{
				ProjectID: "proj-board",
				View: domain.BoardViewRecord{
					ProjectID: "proj-board",
					View:      body.View,
				},
			}), nil
		},
	}
	deps := &Dependencies{DaemonClient: daemonclient.New(transport).WithProjectID("proj-board")}
	if err := BoardViewCommand(deps, BoardViewOptions{Command: "create", File: path, JSON: true}); err != nil {
		t.Fatalf("create error: %v", err)
	}
}

func TestLoadBoardViewDefinitionRejectsUnsupportedVersionedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "view.json")
	view := domain.DefaultBoardView()
	data, err := json.Marshal(map[string]any{
		"schema_version": 999,
		"id":             view.ID,
		"title":          view.Title,
		"columns":        view.Columns,
	})
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write view: %v", err)
	}

	if _, err := loadBoardViewDefinition(path); err == nil {
		t.Fatal("loadBoardViewDefinition error = nil, want unsupported schema_version")
	}
}
