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
	recordFn     func(context.Context, protocol.DecisionRecordRequestBody) (protocol.DecisionRecordResponseBody, error)
	updateFn     func(context.Context, protocol.DecisionUpdateRequestBody) (protocol.DecisionUpdateResponseBody, error)
	deleteFn     func(context.Context, protocol.DecisionDeleteRequestBody) (protocol.DecisionDeleteResponseBody, error)
	listLinksFn  func(context.Context, protocol.DecisionLinkListRequestBody) (protocol.DecisionLinkListResponseBody, error)
	addLinkFn    func(context.Context, protocol.DecisionLinkAddRequestBody) (protocol.DecisionLinkAddResponseBody, error)
	removeLinkFn func(context.Context, protocol.DecisionLinkRemoveRequestBody) (protocol.DecisionLinkRemoveResponseBody, error)
	syncMDFn     func(context.Context, protocol.DecisionSyncMDRequestBody) (protocol.DecisionSyncMDResponseBody, error)
	importMDFn   func(context.Context, protocol.DecisionImportMDRequestBody) (protocol.DecisionImportMDResponseBody, error)
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
func (f *fakeDecisionService) RecordDecision(ctx context.Context, req protocol.DecisionRecordRequestBody) (protocol.DecisionRecordResponseBody, error) {
	if f.recordFn != nil {
		return f.recordFn(ctx, req)
	}
	return protocol.DecisionRecordResponseBody{Decision: protocol.Decision{ID: "dec-1", Title: req.Title, Rationale: req.Rationale, Context: req.Context}}, nil
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
func (f *fakeDecisionService) SyncMD(ctx context.Context, req protocol.DecisionSyncMDRequestBody) (protocol.DecisionSyncMDResponseBody, error) {
	if f.syncMDFn != nil {
		return f.syncMDFn(ctx, req)
	}
	return protocol.DecisionSyncMDResponseBody{Check: req.Check}, nil
}
func (f *fakeDecisionService) ImportMD(ctx context.Context, req protocol.DecisionImportMDRequestBody) (protocol.DecisionImportMDResponseBody, error) {
	if f.importMDFn != nil {
		return f.importMDFn(ctx, req)
	}
	return protocol.DecisionImportMDResponseBody{Check: req.Check, Force: req.Force}, nil
}

func TestDecisionHandler_RecordValidation(t *testing.T) {
	handler := NewDecisionHandler(&fakeDecisionService{})

	cases := []struct {
		name string
		body protocol.DecisionRecordRequestBody
		ok   bool
	}{
		{"missing title", protocol.DecisionRecordRequestBody{Rationale: "why"}, false},
		{"missing rationale", protocol.DecisionRecordRequestBody{Title: "what"}, false},
		{"both empty", protocol.DecisionRecordRequestBody{}, false},
		{"valid", protocol.DecisionRecordRequestBody{Title: "what", Rationale: "why"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, _ := json.Marshal(tc.body)
			resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
				Command: protocol.CommandDecisionRecord,
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
	handler := NewDecisionHandler(&fakeDecisionService{
		addLinkFn: func(ctx context.Context, req protocol.DecisionLinkAddRequestBody) (protocol.DecisionLinkAddResponseBody, error) {
			return protocol.DecisionLinkAddResponseBody{Link: protocol.DecisionLink{
				ID:         "dec-1:issue:az-1",
				DecisionID: req.DecisionID,
				TargetKind: req.TargetKind,
				TargetID:   req.TargetID,
				Relation:   req.Relation,
			}}, nil
		},
	})

	// Missing target kind: should fail.
	payload, _ := json.Marshal(protocol.DecisionLinkAddRequestBody{DecisionID: "dec-1", TargetID: "az-1"})
	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		Command: protocol.CommandDecisionLinkAdd,
		Body:    payload,
	})
	if resp.OK {
		t.Fatalf("expected validation failure for missing target kind")
	}

	// Decision-to-decision with revises relation succeeds (and relation default to applies-to).
	payload, _ = json.Marshal(protocol.DecisionLinkAddRequestBody{
		DecisionID: "dec-2",
		TargetKind: protocol.DecisionTargetDecision,
		TargetID:   "dec-1",
		Relation:   protocol.DecisionRelationRevises,
	})
	resp = handler.Handle(context.Background(), protocol.RequestEnvelope{
		Command: protocol.CommandDecisionLinkAdd,
		Body:    payload,
	})
	if !resp.OK || resp.Error != nil {
		t.Fatalf("expected success, got %+v", resp.Error)
	}
}

func TestDecisionHandler_DeleteRequiresConfirm(t *testing.T) {
	handler := NewDecisionHandler(&fakeDecisionService{})
	payload, _ := json.Marshal(protocol.DecisionDeleteRequestBody{ID: "dec-1"})
	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		Command: protocol.CommandDecisionDelete,
		Body:    payload,
	})
	if resp.OK {
		t.Fatalf("expected delete without confirm to fail")
	}

	payload, _ = json.Marshal(protocol.DecisionDeleteRequestBody{ID: "dec-1", Confirm: true})
	resp = handler.Handle(context.Background(), protocol.RequestEnvelope{
		Command: protocol.CommandDecisionDelete,
		Body:    payload,
	})
	if !resp.OK {
		t.Fatalf("expected delete with confirm to succeed, got %+v", resp.Error)
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
