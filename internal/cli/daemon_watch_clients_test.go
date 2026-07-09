package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestDaemonWatchClientsCommandPrintsOrphanCandidates(t *testing.T) {
	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != protocol.CommandDaemonWatchClients {
					t.Fatalf("command = %s, want %s", req.Command, protocol.CommandDaemonWatchClients)
				}
				return responseWithJSON(req, protocol.DaemonWatchClientsResult{
					ActiveWindowSeconds: 5,
					Clients: []protocol.WatchClient{{
						ClientPID:       35559,
						ClientPPID:      1,
						ProjectID:       "chefy",
						CommandShape:    "orchestrate watch --...",
						ClientCWD:       "/repo",
						AgeSeconds:      120,
						IdleSeconds:     0,
						Active:          true,
						OrphanCandidate: true,
					}},
				}), nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return DaemonWatchClientsCommand(deps, DaemonWatchClientsOptions{})
	})
	for _, want := range []string{"Watch clients", "active,orphan", "35559", "chefy", "orchestrate watch --..."} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %s", want, output)
		}
	}
}
