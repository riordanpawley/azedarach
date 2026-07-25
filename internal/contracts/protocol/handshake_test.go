package protocol

import "testing"

func TestNegotiateHelloCompatibilityMatrix(t *testing.T) {
	tests := []struct {
		name           string
		clientVersion  Version
		wantAccepted   bool
		wantErrorCode  ErrorCode
		wantRetryAfter bool
	}{
		{
			name:          "match version accepted",
			clientVersion: CurrentVersion,
			wantAccepted:  true,
		},
		{
			name:          "unknown version rejected",
			clientVersion: 0,
			wantAccepted:  false,
			wantErrorCode: ErrorCodeInvalidRequest,
		},
		{
			name:          "previous installed generation rejected before payload exchange",
			clientVersion: CurrentVersion - 1,
			wantAccepted:  false,
			wantErrorCode: ErrorCodeUpgradeRequired,
		},
		{
			name:           "forward-incompatible requests restart retry",
			clientVersion:  MaxSupportedVersion + 1,
			wantAccepted:   false,
			wantErrorCode:  ErrorCodeIncompatible,
			wantRetryAfter: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ack := NegotiateHello(Hello{
				ProtocolVersion: tc.clientVersion,
				ClientName:      "tui",
				ClientVersion:   "dev",
			}, "daemon-dev")

			if ack.Accepted != tc.wantAccepted {
				t.Fatalf("Accepted = %v, want %v", ack.Accepted, tc.wantAccepted)
			}
			if ack.ErrorCode != tc.wantErrorCode {
				t.Fatalf("ErrorCode = %q, want %q", ack.ErrorCode, tc.wantErrorCode)
			}
			if ack.RetryAfterRestart != tc.wantRetryAfter {
				t.Fatalf("RetryAfterRestart = %v, want %v", ack.RetryAfterRestart, tc.wantRetryAfter)
			}
		})
	}
}

func TestNegotiateHelloRequiresExplicitCommandSupport(t *testing.T) {
	hello := Hello{
		ProtocolVersion:  CurrentVersion,
		RequiredCommands: []string{CommandDecisionAcknowledge},
	}
	ack := NegotiateHelloWithCommands(hello, "old-daemon", nil)
	if ack.Accepted || ack.ErrorCode != ErrorCodeIncompatible || !ack.RetryAfterRestart {
		t.Fatalf("missing command ack = %+v, want replaceable incompatibility", ack)
	}

	ack = NegotiateHelloWithCommands(hello, "current-daemon", []string{CommandDecisionAcknowledge})
	if !ack.Accepted || len(ack.NegotiatedCommands) != 1 || ack.NegotiatedCommands[0] != CommandDecisionAcknowledge {
		t.Fatalf("supported command ack = %+v, want explicit negotiation", ack)
	}
}
