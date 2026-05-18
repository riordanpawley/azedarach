package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

type fakeDecisionService struct {
	listFn       func(context.Context, protocol.DecisionListRequestBody) (protocol.DecisionListResponseBody, error)
	getFn        func(context.Context, protocol.DecisionGetRequestBody) (protocol.DecisionGetResponseBody, error)
	createFn     func(context.Context, protocol.DecisionCreateRequestBody) (protocol.DecisionCreateResponseBody, error)
	updateFn     func(context.Context, protocol.DecisionUpdateRequestBody) (protocol.DecisionUpdateResponseBody, error)
	deleteFn     func(context.Context, protocol.DecisionDeleteRequestBody) (protocol.DecisionDeleteResponseBody, error)
	listLinksFn  func(context.Context, protocol.DecisionLinkListRequestBody) (protocol.DecisionLinkListResponseBody, error)
	addLinkFn    func(context.Context, protocol.DecisionLinkAddRequestBody) (protocol.DecisionLinkAddResponseBody, error)
	removeLinkFn func(context.Context, protocol.DecisionLinkRemoveRequestBody) (protocol.DecisionLinkRemoveResponseBody, error)
}

func (f *fakeDecisionService) ListDecisions(ctx context.Context, req protocol.DecisionListRequestBody) (protocol.DecisionListResponseBody, error) {
	if f.listFn != nil {
		return f.listFn(ctx, req)
	}
	return protocol.DecisionListResponseBody{}, nil
}
func (f *fakeDecisionService) GetDecision(ctx context.Context, req protocol.DecisionGetRequestBody) (protocol.DecisionGetResponseBody, error) {
	if f.getFn != nil {
		return f.getFn(ctx, req)
	}
	return protocol.DecisionGetResponseBody{}, nil
}
func (f *fakeDecisionService) CreateDecision(ctx context.Context, req protocol.DecisionCreateRequestBody) (protocol.DecisionCreateResponseBody, error) {
	if f.createFn != nil {
		return f.createFn(ctx, req)
	}
	return protocol.DecisionCreateResponseBody{}, nil
}
func (f *fakeDecisionService) UpdateDecision(ctx context.Context, req protocol.DecisionUpdateRequestBody) (protocol.DecisionUpdateResponseBody, error) {
	if f.updateFn != nil {
		return f.updateFn(ctx, req)
	}
	return protocol.DecisionUpdateResponseBody{}, nil
}
func (f *fakeDecisionService) DeleteDecision(ctx context.Context, req protocol.DecisionDeleteRequestBody) (protocol.DecisionDeleteResponseBody, error) {
	if f.deleteFn != nil {
		return f.deleteFn(ctx, req)
	}
	return protocol.DecisionDeleteResponseBody{ID: req.ID, Deleted: true}, nil
}
func (f *fakeDecisionService) ListDecisionLinks(ctx context.Context, req protocol.DecisionLinkListRequestBody) (protocol.DecisionLinkListResponseBody, error) {
	if f.listLinksFn != nil {
		return f.listLinksFn(ctx, req)
	}
	return protocol.DecisionLinkListResponseBody{}, nil
}
func (f *fakeDecisionService) AddDecisionLink(ctx context.Context, req protocol.DecisionLinkAddRequestBody) (protocol.DecisionLinkAddResponseBody, error) {
	if f.addLinkFn != nil {
		return f.addLinkFn(ctx, req)
	}
	return protocol.DecisionLinkAddResponseBody{}, nil
}
func (f *fakeDecisionService) RemoveDecisionLink(ctx context.Context, req protocol.DecisionLinkRemoveRequestBody) (protocol.DecisionLinkRemoveResponseBody, error) {
	if f.removeLinkFn != nil {
		return f.removeLinkFn(ctx, req)
	}
	return protocol.DecisionLinkRemoveResponseBody{}, nil
}

func TestDecisionHandler_CreateValidation(t *testing.T) {
	handler := NewDecisionHandler(&fakeDecisionService{})

	cases := []struct {
		name string
		body protocol.DecisionCreateRequestBody
		ok   bool
	}{
		{"empty id", protocol.DecisionCreateRequestBody{Title: "x"}, false},
		{"empty title", protocol.DecisionCreateRequestBody{ID: "x"}, false},
		{"bad status", protocol.DecisionCreateRequestBody{ID: "x", Title: "y", Status: "garbage"}, false},
		{"valid", protocol.DecisionCreateRequestBody{ID: "x", Title: "y"}, true},
		{"valid w/ accepted", protocol.DecisionCreateRequestBody{ID: "x", Title: "y", Status: protocol.DecisionStatusAccepted}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
				Command: protocol.CommandDecisionCreate,
				Body:    payload,
			})
			gotOK := resp.OK && resp.Error == nil
			if gotOK != tc.ok {
				t.Fatalf("ok=%v want=%v error=%+v", gotOK, tc.ok, resp.Error)
			}
		})
	}
}

func TestDecisionHandler_LinkAddValidation(t *testing.T) {
	called := false
	handler := NewDecisionHandler(&fakeDecisionService{
		addLinkFn: func(ctx context.Context, req protocol.DecisionLinkAddRequestBody) (protocol.DecisionLinkAddResponseBody, error) {
			called = true
			return protocol.DecisionLinkAddResponseBody{Link: protocol.DecisionLink{
				ID:         "d1:issue:az-1",
				DecisionID: req.DecisionID,
				TargetKind: req.TargetKind,
				TargetID:   req.TargetID,
				Relation:   req.Relation,
			}}, nil
		},
	})

	// Missing target kind: should fail.
	payload, _ := json.Marshal(protocol.DecisionLinkAddRequestBody{DecisionID: "d1", TargetID: "az-1"})
	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		Command: protocol.CommandDecisionLinkAdd,
		Body:    payload,
	})
	if resp.OK {
		t.Fatalf("expected validation failure for missing target kind")
	}
	if called {
		t.Fatalf("service should not have been called on validation failure")
	}

	// Valid: relation defaults to relates.
	payload, _ = json.Marshal(protocol.DecisionLinkAddRequestBody{
		DecisionID: "d1", TargetID: "az-1", TargetKind: protocol.DecisionTargetIssue,
	})
	resp = handler.Handle(context.Background(), protocol.RequestEnvelope{
		Command: protocol.CommandDecisionLinkAdd,
		Body:    payload,
	})
	if !resp.OK || resp.Error != nil {
		t.Fatalf("expected success, got error: %+v", resp.Error)
	}
	if !called {
		t.Fatalf("expected service call")
	}
}

func TestDecisionHandler_DeleteRequiresConfirm(t *testing.T) {
	handler := NewDecisionHandler(&fakeDecisionService{})

	payload, _ := json.Marshal(protocol.DecisionDeleteRequestBody{ID: "x", Confirm: false})
	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		Command: protocol.CommandDecisionDelete,
		Body:    payload,
	})
	if resp.OK {
		t.Fatalf("expected delete without confirm to fail")
	}

	payload, _ = json.Marshal(protocol.DecisionDeleteRequestBody{ID: "x", Confirm: true})
	resp = handler.Handle(context.Background(), protocol.RequestEnvelope{
		Command: protocol.CommandDecisionDelete,
		Body:    payload,
	})
	if !resp.OK {
		t.Fatalf("expected delete with confirm to succeed, got error: %+v", resp.Error)
	}
}

func TestDecisionHandler_UnsupportedCommand(t *testing.T) {
	handler := NewDecisionHandler(&fakeDecisionService{})
	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{Command: "decision.bogus"})
	if resp.OK || resp.Error == nil {
		t.Fatalf("expected unsupported-command error")
	}
	if resp.Error.Code != protocol.ErrorCodeUnsupportedCommand {
		t.Fatalf("expected ErrorCodeUnsupportedCommand, got %s", resp.Error.Code)
	}
}
