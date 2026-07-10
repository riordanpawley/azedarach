package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type fakeAIAccountService struct {
	backup protocol.AIAccountBackupRequestBody
	delete protocol.AIAccountDeleteRequestBody
}

func (f *fakeAIAccountService) Backup(_ context.Context, req protocol.AIAccountBackupRequestBody) (protocol.AIAccountBackupResponseBody, error) {
	f.backup = req
	return protocol.AIAccountBackupResponseBody{Profile: protocol.AIAccountProfile{Provider: req.Provider, Name: req.Name, Active: true}}, nil
}

func (*fakeAIAccountService) List(context.Context, protocol.AIAccountListRequestBody) (protocol.AIAccountListResponseBody, error) {
	return protocol.AIAccountListResponseBody{Profiles: []protocol.AIAccountProfile{}}, nil
}

func (*fakeAIAccountService) Status(context.Context, protocol.AIAccountStatusRequestBody) (protocol.AIAccountStatusResponseBody, error) {
	return protocol.AIAccountStatusResponseBody{Providers: []protocol.AIAccountProviderStatus{}}, nil
}

func (*fakeAIAccountService) Activate(_ context.Context, req protocol.AIAccountActivateRequestBody) (protocol.AIAccountActivateResponseBody, error) {
	return protocol.AIAccountActivateResponseBody{Profile: protocol.AIAccountProfile{Provider: req.Provider, Name: req.Name, Active: true}, RestartExistingProcesses: true}, nil
}

func (f *fakeAIAccountService) Delete(_ context.Context, req protocol.AIAccountDeleteRequestBody) (protocol.AIAccountDeleteResponseBody, error) {
	f.delete = req
	return protocol.AIAccountDeleteResponseBody{Provider: req.Provider, Name: req.Name, Deleted: true}, nil
}

func TestAIAccountHandlerRoutesAndValidates(t *testing.T) {
	service := &fakeAIAccountService{}
	handler := NewAIAccountHandler(service)

	resp := handler.Handle(context.Background(), aiAccountRequest(t, protocol.CommandAIAccountBackup, protocol.AIAccountBackupRequestBody{
		Provider: protocol.AIAccountProviderClaude,
		Name:     "work@example.com",
		Force:    true,
	}))
	if !resp.OK || service.backup.Name != "work@example.com" || !service.backup.Force {
		t.Fatalf("backup response/service = %+v / %+v", resp, service.backup)
	}
	resp = handler.Handle(context.Background(), aiAccountRequest(t, protocol.CommandAIAccountBackup, protocol.AIAccountBackupRequestBody{
		Provider: protocol.AIAccountProviderClaude,
		Name:     "  personal  ",
	}))
	if !resp.OK || service.backup.Name != "personal" {
		t.Fatalf("trimmed backup response/service = %+v / %+v", resp, service.backup)
	}

	resp = handler.Handle(context.Background(), aiAccountRequest(t, protocol.CommandAIAccountDelete, protocol.AIAccountDeleteRequestBody{
		Provider: protocol.AIAccountProviderCodex,
		Name:     "personal",
	}))
	if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("unconfirmed delete response = %+v", resp)
	}

	resp = handler.Handle(context.Background(), aiAccountRequest(t, protocol.CommandAIAccountActivate, protocol.AIAccountActivateRequestBody{
		Provider: protocol.AIAccountProviderClaude,
		Name:     "../escape",
	}))
	if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("unsafe profile response = %+v", resp)
	}

	resp = handler.Handle(context.Background(), aiAccountRequest(t, protocol.CommandAIAccountBackup, protocol.AIAccountBackupRequestBody{
		Provider: protocol.AIAccountProviderClaude,
		Name:     "_original",
	}))
	if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("reserved profile response = %+v", resp)
	}
}

func TestAIAccountHandlerUnavailable(t *testing.T) {
	resp := NewAIAccountHandler(nil).Handle(context.Background(), aiAccountRequest(t, protocol.CommandAIAccountList, protocol.AIAccountListRequestBody{}))
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnavailable || !resp.Error.Retryable {
		t.Fatalf("response = %+v", resp)
	}
}

func aiAccountRequest(t *testing.T, command string, body any) protocol.RequestEnvelope {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("req-ai-account"),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         command,
		Body:            payload,
	}
}
