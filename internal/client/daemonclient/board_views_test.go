package daemonclient

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestBoardViewClientCommandsUseTypedProtocol(t *testing.T) {
	updatedAt := time.Date(2026, time.July, 9, 12, 0, 0, 0, time.UTC)
	transport := &taskRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			assertTaskProjectID(t, req, "proj-board-client")
			switch req.Command {
			case protocol.CommandBoardViewSave:
				var body protocol.BoardViewSaveRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal save body: %v", err)
				}
				if body.View.ID != "custom" {
					t.Fatalf("save view id = %q, want custom", body.View.ID)
				}
				return responseWithJSON(t, req, protocol.BoardViewResponseBody{
					ProjectID: naming.ProjectID("proj-board-client"),
					View: domain.BoardViewRecord{
						ProjectID: "proj-board-client",
						View:      body.View,
					},
				}), nil
			case protocol.CommandBoardViewSelect:
				var body protocol.BoardViewSelectRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal select body: %v", err)
				}
				if body.ViewID != "custom" {
					t.Fatalf("select view id = %q, want custom", body.ViewID)
				}
				return responseWithJSON(t, req, protocol.BoardViewSelectResponseBody{
					ProjectID: naming.ProjectID("proj-board-client"),
					ViewID:    body.ViewID,
					UpdatedAt: updatedAt,
				}), nil
			default:
				t.Fatalf("unexpected command %q", req.Command)
				return protocol.ResponseEnvelope{}, nil
			}
		},
	}
	client := New(transport).WithProjectID("proj-board-client")
	view := domain.DefaultBoardView()
	view.ID = "custom"
	view.Name = "Custom"

	saveResp, err := client.SaveBoardView(context.Background(), view)
	if err != nil {
		t.Fatalf("SaveBoardView error: %v", err)
	}
	if saveResp.View.View.ID != "custom" {
		t.Fatalf("SaveBoardView response = %+v", saveResp)
	}
	selectResp, err := client.SelectBoardView(context.Background(), "custom")
	if err != nil {
		t.Fatalf("SelectBoardView error: %v", err)
	}
	if selectResp.ViewID != "custom" || !selectResp.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("SelectBoardView response = %+v", selectResp)
	}
}
