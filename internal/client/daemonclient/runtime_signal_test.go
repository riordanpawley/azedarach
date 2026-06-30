package daemonclient

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestRuntimeSignalIngestCommand(t *testing.T) {
	const wantProjectID = "proj-signals"
	transport := &taskRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			assertTaskProjectID(t, req, wantProjectID)
			if req.Command != protocol.CommandRuntimeSignalIngest {
				t.Fatalf("command = %q, want %q", req.Command, protocol.CommandRuntimeSignalIngest)
			}
			var body protocol.RuntimeSignalIngestCommandBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal runtime signal body: %v", err)
			}
			if body.Source != protocol.RuntimeSignalSourceGitHook ||
				body.Kind != protocol.RuntimeSignalKindGitWorktreeChanged ||
				body.Worktree != "/tmp/wt" ||
				body.Hook != "post-commit" ||
				!body.Log {
				t.Fatalf("runtime signal body = %+v", body)
			}
			return responseWithJSON(t, req, protocol.RuntimeSignalIngestResponseBody{
				Accepted:         true,
				SignalID:         "sig-test",
				EnrichmentQueued: true,
				Stages: []protocol.RuntimeSignalStageOutcome{{
					Name: "git_status_fast",
					OK:   true,
				}},
			}), nil
		},
	}

	resp, err := New(transport).WithProjectID(wantProjectID).RuntimeSignalIngest(context.Background(), protocol.RuntimeSignalIngestCommandBody{
		Source:   protocol.RuntimeSignalSourceGitHook,
		Kind:     protocol.RuntimeSignalKindGitWorktreeChanged,
		Worktree: "/tmp/wt",
		Hook:     "post-commit",
		Log:      true,
	})
	if err != nil {
		t.Fatalf("RuntimeSignalIngest error: %v", err)
	}
	if !resp.Accepted || resp.SignalID != "sig-test" || !resp.EnrichmentQueued {
		t.Fatalf("response = %+v", resp)
	}
}
