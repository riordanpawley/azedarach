package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type fakeSpecService struct {
	listRequirementsFn  func(context.Context, protocol.SpecRequirementListRequestBody) (protocol.SpecRequirementListResponseBody, error)
	getRequirementFn    func(context.Context, protocol.SpecRequirementGetRequestBody) (protocol.SpecRequirementGetResponseBody, error)
	createRequirementFn func(context.Context, protocol.SpecRequirementCreateRequestBody) (protocol.SpecRequirementCreateResponseBody, error)
	updateRequirementFn func(context.Context, protocol.SpecRequirementUpdateRequestBody) (protocol.SpecRequirementUpdateResponseBody, error)
	deleteRequirementFn func(context.Context, protocol.SpecRequirementDeleteRequestBody) (protocol.SpecRequirementDeleteResponseBody, error)
	listLinksFn         func(context.Context, protocol.SpecLinkListRequestBody) (protocol.SpecLinkListResponseBody, error)
	addLinkFn           func(context.Context, protocol.SpecLinkAddRequestBody) (protocol.SpecLinkAddResponseBody, error)
	removeLinkFn        func(context.Context, protocol.SpecLinkRemoveRequestBody) (protocol.SpecLinkRemoveResponseBody, error)
	readFn              func(context.Context, protocol.SpecReadRequestBody) (protocol.SpecReadResponseBody, error)
	lintFn              func(context.Context, protocol.SpecLintRequestBody) (protocol.SpecLintResponseBody, error)
	parityFn            func(context.Context, protocol.SpecParityRequestBody) (protocol.SpecParityResponseBody, error)
	syncMDFn            func(context.Context, protocol.SpecSyncMDRequestBody) (protocol.SpecSyncMDResponseBody, error)
}

func (f *fakeSpecService) ListRequirements(ctx context.Context, req protocol.SpecRequirementListRequestBody) (protocol.SpecRequirementListResponseBody, error) {
	if f.listRequirementsFn != nil {
		return f.listRequirementsFn(ctx, req)
	}
	return protocol.SpecRequirementListResponseBody{}, nil
}

func (f *fakeSpecService) GetRequirement(ctx context.Context, req protocol.SpecRequirementGetRequestBody) (protocol.SpecRequirementGetResponseBody, error) {
	if f.getRequirementFn != nil {
		return f.getRequirementFn(ctx, req)
	}
	return protocol.SpecRequirementGetResponseBody{}, nil
}

func (f *fakeSpecService) CreateRequirement(ctx context.Context, req protocol.SpecRequirementCreateRequestBody) (protocol.SpecRequirementCreateResponseBody, error) {
	if f.createRequirementFn != nil {
		return f.createRequirementFn(ctx, req)
	}
	return protocol.SpecRequirementCreateResponseBody{}, nil
}

func (f *fakeSpecService) UpdateRequirement(ctx context.Context, req protocol.SpecRequirementUpdateRequestBody) (protocol.SpecRequirementUpdateResponseBody, error) {
	if f.updateRequirementFn != nil {
		return f.updateRequirementFn(ctx, req)
	}
	return protocol.SpecRequirementUpdateResponseBody{}, nil
}

func (f *fakeSpecService) DeleteRequirement(ctx context.Context, req protocol.SpecRequirementDeleteRequestBody) (protocol.SpecRequirementDeleteResponseBody, error) {
	if f.deleteRequirementFn != nil {
		return f.deleteRequirementFn(ctx, req)
	}
	return protocol.SpecRequirementDeleteResponseBody{}, nil
}

func (f *fakeSpecService) ListLinks(ctx context.Context, req protocol.SpecLinkListRequestBody) (protocol.SpecLinkListResponseBody, error) {
	if f.listLinksFn != nil {
		return f.listLinksFn(ctx, req)
	}
	return protocol.SpecLinkListResponseBody{}, nil
}

func (f *fakeSpecService) AddLink(ctx context.Context, req protocol.SpecLinkAddRequestBody) (protocol.SpecLinkAddResponseBody, error) {
	if f.addLinkFn != nil {
		return f.addLinkFn(ctx, req)
	}
	return protocol.SpecLinkAddResponseBody{}, nil
}

func (f *fakeSpecService) RemoveLink(ctx context.Context, req protocol.SpecLinkRemoveRequestBody) (protocol.SpecLinkRemoveResponseBody, error) {
	if f.removeLinkFn != nil {
		return f.removeLinkFn(ctx, req)
	}
	return protocol.SpecLinkRemoveResponseBody{}, nil
}

func (f *fakeSpecService) Read(ctx context.Context, req protocol.SpecReadRequestBody) (protocol.SpecReadResponseBody, error) {
	if f.readFn != nil {
		return f.readFn(ctx, req)
	}
	return protocol.SpecReadResponseBody{}, nil
}

func (f *fakeSpecService) Lint(ctx context.Context, req protocol.SpecLintRequestBody) (protocol.SpecLintResponseBody, error) {
	if f.lintFn != nil {
		return f.lintFn(ctx, req)
	}
	return protocol.SpecLintResponseBody{}, nil
}

func (f *fakeSpecService) Parity(ctx context.Context, req protocol.SpecParityRequestBody) (protocol.SpecParityResponseBody, error) {
	if f.parityFn != nil {
		return f.parityFn(ctx, req)
	}
	return protocol.SpecParityResponseBody{}, nil
}

func (f *fakeSpecService) SyncMD(ctx context.Context, req protocol.SpecSyncMDRequestBody) (protocol.SpecSyncMDResponseBody, error) {
	if f.syncMDFn != nil {
		return f.syncMDFn(ctx, req)
	}
	return protocol.SpecSyncMDResponseBody{}, nil
}

func specRequest(t *testing.T, command string, body any) protocol.RequestEnvelope {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("req-" + command),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         command,
		Body:            payload,
	}
}

func TestSpecHandlerRequirementCommands(t *testing.T) {
	var gotList protocol.SpecRequirementListRequestBody
	var gotGet protocol.SpecRequirementGetRequestBody
	var gotCreate protocol.SpecRequirementCreateRequestBody
	var gotUpdate protocol.SpecRequirementUpdateRequestBody
	var gotDelete protocol.SpecRequirementDeleteRequestBody

	handler := NewSpecHandler(&fakeSpecService{
		listRequirementsFn: func(_ context.Context, req protocol.SpecRequirementListRequestBody) (protocol.SpecRequirementListResponseBody, error) {
			gotList = req
			return protocol.SpecRequirementListResponseBody{
				Requirements: []protocol.SpecRequirement{{ID: "REQ-1", Status: protocol.SpecRequirementStatusOpen}},
			}, nil
		},
		getRequirementFn: func(_ context.Context, req protocol.SpecRequirementGetRequestBody) (protocol.SpecRequirementGetResponseBody, error) {
			gotGet = req
			return protocol.SpecRequirementGetResponseBody{
				Requirement: protocol.SpecRequirement{ID: req.ID, Title: "Requirement"},
			}, nil
		},
		createRequirementFn: func(_ context.Context, req protocol.SpecRequirementCreateRequestBody) (protocol.SpecRequirementCreateResponseBody, error) {
			gotCreate = req
			return protocol.SpecRequirementCreateResponseBody{
				Requirement: protocol.SpecRequirement{
					ID:          req.ID,
					Title:       req.Title,
					Description: req.Description,
					IssueID:     req.IssueID,
					Status:      protocol.SpecRequirementStatusOpen,
				},
			}, nil
		},
		updateRequirementFn: func(_ context.Context, req protocol.SpecRequirementUpdateRequestBody) (protocol.SpecRequirementUpdateResponseBody, error) {
			gotUpdate = req
			return protocol.SpecRequirementUpdateResponseBody{
				Requirement: protocol.SpecRequirement{
					ID:     req.ID,
					Title:  *req.Title,
					Status: *req.Status,
				},
			}, nil
		},
		deleteRequirementFn: func(_ context.Context, req protocol.SpecRequirementDeleteRequestBody) (protocol.SpecRequirementDeleteResponseBody, error) {
			gotDelete = req
			return protocol.SpecRequirementDeleteResponseBody{ID: req.ID, Deleted: true}, nil
		},
	})

	resp := handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecRequirementList, protocol.SpecRequirementListRequestBody{
		IssueID: " az-1 ",
		Status:  protocol.SpecRequirementStatusOpen,
		IDs: []naming.RequirementID{
			" REQ-2 ",
			"REQ-1",
			"REQ-2",
		},
	}))
	if !resp.OK {
		t.Fatalf("list response error: %+v", resp.Error)
	}
	if gotList.IssueID != "az-1" || len(gotList.IDs) != 2 || gotList.IDs[0] != "REQ-2" || gotList.IDs[1] != "REQ-1" {
		t.Fatalf("normalized list request = %+v", gotList)
	}

	resp = handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecRequirementGet, protocol.SpecRequirementGetRequestBody{ID: " REQ-1 "}))
	if !resp.OK {
		t.Fatalf("get response error: %+v", resp.Error)
	}
	if gotGet.ID != "REQ-1" {
		t.Fatalf("get request = %+v", gotGet)
	}

	resp = handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecRequirementCreate, protocol.SpecRequirementCreateRequestBody{
		ID:          " REQ-3 ",
		Title:       " Need daemon routing ",
		Description: " typed contract ",
		IssueID:     " az-3 ",
	}))
	if !resp.OK {
		t.Fatalf("create response error: %+v", resp.Error)
	}
	if gotCreate.ID != "REQ-3" || gotCreate.Title != "Need daemon routing" || gotCreate.IssueID != "az-3" {
		t.Fatalf("create request = %+v", gotCreate)
	}

	title := " Updated title "
	status := protocol.SpecRequirementStatusAccepted
	resp = handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecRequirementUpdate, protocol.SpecRequirementUpdateRequestBody{
		ID:     " REQ-3 ",
		Title:  &title,
		Status: &status,
	}))
	if !resp.OK {
		t.Fatalf("update response error: %+v", resp.Error)
	}
	if gotUpdate.ID != "REQ-3" || gotUpdate.Title == nil || *gotUpdate.Title != "Updated title" || gotUpdate.Status == nil || *gotUpdate.Status != protocol.SpecRequirementStatusAccepted {
		t.Fatalf("update request = %+v", gotUpdate)
	}

	resp = handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecRequirementDelete, protocol.SpecRequirementDeleteRequestBody{
		ID:      " REQ-3 ",
		Confirm: true,
	}))
	if !resp.OK {
		t.Fatalf("delete response error: %+v", resp.Error)
	}
	if gotDelete.ID != "REQ-3" || !gotDelete.Confirm {
		t.Fatalf("delete request = %+v", gotDelete)
	}
}

func TestSpecHandlerLinkReadLintParityAndSyncCommands(t *testing.T) {
	var gotAdd protocol.SpecLinkAddRequestBody
	var gotRead protocol.SpecReadRequestBody
	var gotLint protocol.SpecLintRequestBody
	var gotParity protocol.SpecParityRequestBody
	var gotSync protocol.SpecSyncMDRequestBody

	handler := NewSpecHandler(&fakeSpecService{
		listLinksFn: func(_ context.Context, req protocol.SpecLinkListRequestBody) (protocol.SpecLinkListResponseBody, error) {
			if len(req.IDs) != 2 || req.IDs[0] != "LINK-2" || req.IDs[1] != "LINK-1" {
				t.Fatalf("link list ids = %+v", req.IDs)
			}
			return protocol.SpecLinkListResponseBody{
				Links: []protocol.SpecLink{{IssueID: "az-1", ReqID: "REQ-1", Role: protocol.SpecLinkRoleImplements}},
			}, nil
		},
		addLinkFn: func(_ context.Context, req protocol.SpecLinkAddRequestBody) (protocol.SpecLinkAddResponseBody, error) {
			gotAdd = req
			return protocol.SpecLinkAddResponseBody{
				Link: protocol.SpecLink{IssueID: req.IssueID, ReqID: req.ReqID, Role: req.Role, Note: req.Note},
			}, nil
		},
		removeLinkFn: func(_ context.Context, req protocol.SpecLinkRemoveRequestBody) (protocol.SpecLinkRemoveResponseBody, error) {
			return protocol.SpecLinkRemoveResponseBody{IssueID: req.IssueID, ReqID: req.ReqID, Removed: true}, nil
		},
		readFn: func(_ context.Context, req protocol.SpecReadRequestBody) (protocol.SpecReadResponseBody, error) {
			gotRead = req
			return protocol.SpecReadResponseBody{
				Requirements: []protocol.SpecRequirement{{ID: "REQ-1", Status: protocol.SpecRequirementStatusOpen}},
				Links:        []protocol.SpecLink{{IssueID: req.IssueID, ReqID: req.ReqID, Role: protocol.SpecLinkRoleImplements}},
			}, nil
		},
		lintFn: func(_ context.Context, req protocol.SpecLintRequestBody) (protocol.SpecLintResponseBody, error) {
			gotLint = req
			return protocol.SpecLintResponseBody{OK: false}, nil
		},
		parityFn: func(_ context.Context, req protocol.SpecParityRequestBody) (protocol.SpecParityResponseBody, error) {
			gotParity = req
			return protocol.SpecParityResponseBody{OK: false}, nil
		},
		syncMDFn: func(_ context.Context, req protocol.SpecSyncMDRequestBody) (protocol.SpecSyncMDResponseBody, error) {
			gotSync = req
			return protocol.SpecSyncMDResponseBody{Target: "md", Check: req.Check, Changed: true, Files: []string{"docs/spec/reqs.md"}}, nil
		},
	})

	resp := handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecLinkList, protocol.SpecLinkListRequestBody{
		IssueID: " az-1 ",
		ReqID:   " REQ-1 ",
		IDs: []naming.SpecLinkID{
			" LINK-2 ",
			"LINK-1",
			"LINK-2",
		},
	}))
	if !resp.OK {
		t.Fatalf("link list response error: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecLinkAdd, protocol.SpecLinkAddRequestBody{
		IssueID: " az-1 ",
		ReqID:   " REQ-1 ",
		Note:    " handled by daemon ",
	}))
	if !resp.OK {
		t.Fatalf("link add response error: %+v", resp.Error)
	}
	if gotAdd.IssueID != "az-1" || gotAdd.ReqID != "REQ-1" || gotAdd.Role != protocol.SpecLinkRoleImplements || gotAdd.Note != "handled by daemon" {
		t.Fatalf("link add request = %+v", gotAdd)
	}

	resp = handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecLinkRemove, protocol.SpecLinkRemoveRequestBody{
		IssueID: "az-1",
		ReqID:   "REQ-1",
	}))
	if !resp.OK {
		t.Fatalf("link remove response error: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecRead, protocol.SpecReadRequestBody{
		IssueID: " az-1 ",
		ReqID:   " REQ-1 ",
	}))
	if !resp.OK {
		t.Fatalf("read response error: %+v", resp.Error)
	}
	if gotRead.IssueID != "az-1" || gotRead.ReqID != "REQ-1" {
		t.Fatalf("read request = %+v", gotRead)
	}

	resp = handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecLint, protocol.SpecLintRequestBody{Strict: true}))
	if !resp.OK {
		t.Fatalf("lint response error: %+v", resp.Error)
	}
	if !gotLint.Strict {
		t.Fatalf("lint request = %+v", gotLint)
	}

	resp = handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecParity, protocol.SpecParityRequestBody{FailOnOut: true}))
	if !resp.OK {
		t.Fatalf("parity response error: %+v", resp.Error)
	}
	if !gotParity.FailOnOut {
		t.Fatalf("parity request = %+v", gotParity)
	}

	resp = handler.Handle(context.Background(), specRequest(t, protocol.CommandSpecSync, protocol.SpecSyncMDRequestBody{Check: true}))
	if !resp.OK {
		t.Fatalf("sync response error: %+v", resp.Error)
	}
	if gotSync.Target != "md" || !gotSync.Check {
		t.Fatalf("sync request = %+v", gotSync)
	}
}

func TestSpecHandlerValidationAndErrorMapping(t *testing.T) {
	handler := NewSpecHandler(&fakeSpecService{
		getRequirementFn: func(context.Context, protocol.SpecRequirementGetRequestBody) (protocol.SpecRequirementGetResponseBody, error) {
			return protocol.SpecRequirementGetResponseBody{}, errSpecNotFound
		},
		createRequirementFn: func(context.Context, protocol.SpecRequirementCreateRequestBody) (protocol.SpecRequirementCreateResponseBody, error) {
			return protocol.SpecRequirementCreateResponseBody{}, ErrSpecUnavailable
		},
		deleteRequirementFn: func(context.Context, protocol.SpecRequirementDeleteRequestBody) (protocol.SpecRequirementDeleteResponseBody, error) {
			return protocol.SpecRequirementDeleteResponseBody{}, context.DeadlineExceeded
		},
		addLinkFn: func(context.Context, protocol.SpecLinkAddRequestBody) (protocol.SpecLinkAddResponseBody, error) {
			return protocol.SpecLinkAddResponseBody{}, errSpecConflict
		},
	})

	tests := []struct {
		name     string
		req      protocol.RequestEnvelope
		wantCode protocol.ErrorCode
	}{
		{
			name: "invalid list status",
			req: specRequest(t, protocol.CommandSpecRequirementList, protocol.SpecRequirementListRequestBody{
				Status: protocol.SpecRequirementStatus("draft"),
			}),
			wantCode: protocol.ErrorCodeInvalidRequest,
		},
		{
			name:     "missing get id",
			req:      specRequest(t, protocol.CommandSpecRequirementGet, protocol.SpecRequirementGetRequestBody{}),
			wantCode: protocol.ErrorCodeInvalidRequest,
		},
		{
			name:     "missing create title",
			req:      specRequest(t, protocol.CommandSpecRequirementCreate, protocol.SpecRequirementCreateRequestBody{ID: "REQ-1"}),
			wantCode: protocol.ErrorCodeInvalidRequest,
		},
		{
			name:     "missing update fields",
			req:      specRequest(t, protocol.CommandSpecRequirementUpdate, protocol.SpecRequirementUpdateRequestBody{ID: "REQ-1"}),
			wantCode: protocol.ErrorCodeInvalidRequest,
		},
		{
			name:     "delete missing confirm",
			req:      specRequest(t, protocol.CommandSpecRequirementDelete, protocol.SpecRequirementDeleteRequestBody{ID: "REQ-1"}),
			wantCode: protocol.ErrorCodeInvalidRequest,
		},
		{
			name: "invalid link role",
			req: specRequest(t, protocol.CommandSpecLinkAdd, protocol.SpecLinkAddRequestBody{
				IssueID: "az-1",
				ReqID:   "REQ-1",
				Role:    protocol.SpecLinkRole("blocks"),
			}),
			wantCode: protocol.ErrorCodeInvalidRequest,
		},
		{
			name:     "service not found",
			req:      specRequest(t, protocol.CommandSpecRequirementGet, protocol.SpecRequirementGetRequestBody{ID: "REQ-1"}),
			wantCode: protocol.ErrorCodeInvalidRequest,
		},
		{
			name:     "service unavailable",
			req:      specRequest(t, protocol.CommandSpecRequirementCreate, protocol.SpecRequirementCreateRequestBody{ID: "REQ-1", Title: "Title"}),
			wantCode: protocol.ErrorCodeUnavailable,
		},
		{
			name:     "service timeout",
			req:      specRequest(t, protocol.CommandSpecRequirementDelete, protocol.SpecRequirementDeleteRequestBody{ID: "REQ-1", Confirm: true}),
			wantCode: protocol.ErrorCodeTimeout,
		},
		{
			name:     "service conflict",
			req:      specRequest(t, protocol.CommandSpecLinkAdd, protocol.SpecLinkAddRequestBody{IssueID: "az-1", ReqID: "REQ-1"}),
			wantCode: protocol.ErrorCodeConflict,
		},
		{
			name:     "sync invalid target",
			req:      specRequest(t, protocol.CommandSpecSync, protocol.SpecSyncMDRequestBody{Target: "html"}),
			wantCode: protocol.ErrorCodeInvalidRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := handler.Handle(context.Background(), tc.req)
			if resp.OK {
				t.Fatal("expected error response")
			}
			if resp.Error == nil || resp.Error.Code != tc.wantCode {
				t.Fatalf("error = %+v, want code %q", resp.Error, tc.wantCode)
			}
		})
	}

	t.Run("nil service", func(t *testing.T) {
		resp := NewSpecHandler(nil).Handle(context.Background(), specRequest(t, protocol.CommandSpecRead, protocol.SpecReadRequestBody{}))
		if resp.OK {
			t.Fatal("expected unavailable response")
		}
		if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnavailable {
			t.Fatalf("error = %+v, want unavailable", resp.Error)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-invalid-json",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         protocol.CommandSpecRead,
			Body:            []byte("{bad"),
		})
		if resp.OK {
			t.Fatal("expected invalid request")
		}
		if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
			t.Fatalf("error = %+v, want invalid_request", resp.Error)
		}
	})

	t.Run("unsupported command", func(t *testing.T) {
		resp := handler.Handle(context.Background(), specRequest(t, "spec.unknown", map[string]any{}))
		if resp.OK {
			t.Fatal("expected unsupported command")
		}
		if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnsupportedCommand {
			t.Fatalf("error = %+v, want unsupported_command", resp.Error)
		}
	})
}

func TestMapSpecErrorDefault(t *testing.T) {
	got := mapSpecError(errors.New("boom"))
	if got == nil || got.Code != protocol.ErrorCodeInternal || got.Message != "boom" {
		t.Fatalf("mapSpecError = %+v", got)
	}
}
