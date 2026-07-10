package daemonclient

import (
	"context"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"strings"
	"testing"
)

func TestResolveInteractionTranslatesConflictEnvelope(t *testing.T) {
	c := New(&fakeTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		if req.Command != protocol.CommandInteractionResolve {
			t.Fatalf("command=%q", req.Command)
		}
		return protocol.ResponseEnvelope{Error: &protocol.ErrorEnvelope{Code: protocol.ErrorCodeConflict, Message: "stale interaction revision"}}, nil
	}})
	_, err := c.ResolveInteraction(context.Background(), protocol.InteractionResolveRequestBody{InteractionMutationRequestBody: protocol.InteractionMutationRequestBody{ID: "req", ExpectedRevision: 1, Answer: domain.InteractionAnswerPayload{SelectedOption: "yes", Rationale: "Proceed.", SignificanceRecommendation: domain.InteractionSignificanceRoutine, Revision: 1}, Actor: "human"}})
	if err == nil || !strings.Contains(err.Error(), "stale interaction revision") {
		t.Fatalf("error=%v", err)
	}
}
