package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"testing"
)

type fakeInteractionService struct{ err error }

func (f fakeInteractionService) CreateInteraction(context.Context, protocol.InteractionCreateRequestBody) (protocol.InteractionResponseBody, error) {
	return protocol.InteractionResponseBody{}, f.err
}
func (f fakeInteractionService) ListInteractions(context.Context, protocol.InteractionListRequestBody) (protocol.InteractionListResponseBody, error) {
	return protocol.InteractionListResponseBody{}, f.err
}
func (f fakeInteractionService) GetInteraction(context.Context, protocol.InteractionGetRequestBody) (protocol.InteractionResponseBody, error) {
	return protocol.InteractionResponseBody{}, f.err
}
func (f fakeInteractionService) MutateInteraction(context.Context, string, protocol.InteractionMutationRequestBody) (protocol.InteractionResponseBody, error) {
	return protocol.InteractionResponseBody{}, f.err
}
func (f fakeInteractionService) ResolveInteraction(context.Context, protocol.InteractionResolveRequestBody) (protocol.InteractionResponseBody, error) {
	return protocol.InteractionResponseBody{}, f.err
}
func TestInteractionHandlerStaleRevisionIsConflict(t *testing.T) {
	body, _ := json.Marshal(protocol.InteractionMutationRequestBody{ID: "req", ExpectedRevision: 1, Answer: "yes", Actor: "human"})
	h := NewInteractionHandler(fakeInteractionService{err: fmt.Errorf("wrapped: %w", domain.ErrStaleInteractionRevision)})
	resp := h.Handle(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, Command: protocol.CommandInteractionResolve, Body: body})
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeConflict || resp.Error.Retryable {
		t.Fatalf("response=%+v", resp)
	}
}
func TestInteractionHandlerValidatesTypedAuthorityShape(t *testing.T) {
	body, _ := json.Marshal(protocol.InteractionMutationRequestBody{ID: "req", ExpectedRevision: 1, Answer: "yes"})
	resp := NewInteractionHandler(fakeInteractionService{}).Handle(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, Command: protocol.CommandInteractionAnswer, Body: body})
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("response=%+v", resp)
	}
}

func TestInteractionDiscussAllowsDaemonOwnedSessionIdentity(t *testing.T) {
	body, _ := json.Marshal(protocol.InteractionMutationRequestBody{ID: "req", ExpectedRevision: 1, Actor: "human"})
	resp := NewInteractionHandler(fakeInteractionService{}).Handle(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, Command: protocol.CommandInteractionDiscuss, Body: body})
	if resp.Error != nil {
		t.Fatalf("response error = %+v", resp.Error)
	}
}
