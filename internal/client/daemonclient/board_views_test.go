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
	view.Title = "Custom"

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

func TestSelectGlobalViewCarriesTypedConsumerAndGlobalScope(t *testing.T) {
	transport := &taskRecordingTransport{replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		var body protocol.BoardViewSelectRequestBody
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Fatalf("unmarshal select body: %v", err)
		}
		if body.ProjectID != "global" || body.Consumer != protocol.GlobalViewConsumerTmuxSelector || body.ViewID != "orchestration" {
			t.Fatalf("select body = %+v", body)
		}
		return responseWithJSON(t, req, protocol.BoardViewSelectResponseBody{ProjectID: "global", ViewID: body.ViewID}), nil
	}}
	client := New(transport).WithProjectID("project-local")

	resp, err := client.SelectGlobalView(context.Background(), protocol.GlobalViewConsumerTmuxSelector, " orchestration ")
	if err != nil {
		t.Fatalf("SelectGlobalView error: %v", err)
	}
	if resp.ProjectID != "global" || resp.ViewID != "orchestration" {
		t.Fatalf("SelectGlobalView response = %+v", resp)
	}
}

func TestGlobalViewListAndDeleteUseExplicitGlobalProject(t *testing.T) {
	commands := []string{}
	transport := &taskRecordingTransport{replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		commands = append(commands, req.Command)
		switch req.Command {
		case protocol.CommandBoardViewList:
			var body protocol.BoardViewListRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil || body.ProjectID != "global" {
				t.Fatalf("global list body=%+v err=%v", body, err)
			}
			return responseWithJSON(t, req, protocol.BoardViewListResponseBody{ProjectID: "global"}), nil
		case protocol.CommandBoardViewDelete:
			var body protocol.BoardViewDeleteRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil || body.ProjectID != "global" || body.ViewID != "custom" {
				t.Fatalf("global delete body=%+v err=%v", body, err)
			}
			return responseWithJSON(t, req, struct{}{}), nil
		default:
			t.Fatalf("unexpected command %q", req.Command)
			return protocol.ResponseEnvelope{}, nil
		}
	}}
	client := New(transport).WithProjectID("local")
	if _, err := client.ListGlobalViews(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteGlobalView(context.Background(), " custom "); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 {
		t.Fatalf("commands=%v", commands)
	}
}
