package cli

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestCompactOrchestrationTranscriptKeepsRecentSemanticEvents(t *testing.T) {
	input := strings.Join([]string{
		"╭────────────────────────╮",
		"│ >_ OpenAI Codex        │",
		"╰────────────────────────╯",
		"› original task prompt",
		"  more original prompt",
		"",
		"\x1b[32m• I’m checking the capture path\x1b[0m",
		"  before choosing a design.",
		"",
		"• Ran rg -n capture cmd",
		"  │ internal",
		"  └ internal/cli/commands.go: capture",
		"    ignored verbose output",
		"    … +116 lines (ctrl + t to view transcript)",
		"• Ran a command with fully collapsed output",
		"  └ … +200 lines (ctrl + t to view transcript)",
		"    must not leak into the command text",
		"",
		"• Working (49s • esc to interrupt)",
		"• Queued follow-up inputs",
		"  ↳ do another unrelated task",
		"",
		"⚠ Hook warning",
		"  missing optional metadata",
		"",
		"› a later user prompt",
		"  must not become worker output",
		"",
		"gpt-5.6-sol medium · Context 89% left",
	}, "\n")

	got := compactOrchestrationTranscript(input)
	want := "• I’m checking the capture path before choosing a design.\n" +
		"• Ran rg -n capture cmd internal → internal/cli/commands.go: capture\n" +
		"• Ran a command with fully collapsed output\n" +
		"⚠ Hook warning missing optional metadata\n"
	if got.Output != want {
		t.Fatalf("compact output = %q, want %q", got.Output, want)
	}
	if got.Events != 4 || got.OmittedEvents != 0 {
		t.Fatalf("metadata = events:%d omitted:%d, want 4/0", got.Events, got.OmittedEvents)
	}
}

func TestCompactOrchestrationTranscriptBoundsEventCountAndLength(t *testing.T) {
	var input strings.Builder
	for index := 0; index < compactCaptureMaxEvents+2; index++ {
		input.WriteString("• event ")
		input.WriteRune(rune('A' + index))
		input.WriteByte('\n')
	}
	input.WriteString("• ")
	input.WriteString(strings.Repeat("x", compactCaptureMaxEventRunes+20))
	input.WriteByte('\n')

	got := compactOrchestrationTranscript(input.String())
	if got.Events != compactCaptureMaxEvents || got.OmittedEvents != 3 {
		t.Fatalf("metadata = events:%d omitted:%d, want %d/3", got.Events, got.OmittedEvents, compactCaptureMaxEvents)
	}
	if strings.Contains(got.Output, "event A") || !strings.Contains(got.Output, "event D") {
		t.Fatalf("output does not contain only the most recent events: %q", got.Output)
	}
	lastLine := strings.TrimSpace(got.Output[strings.LastIndex(strings.TrimSuffix(got.Output, "\n"), "\n")+1:])
	if len([]rune(strings.TrimPrefix(lastLine, "• "))) != compactCaptureMaxEventRunes {
		t.Fatalf("last event rune count = %d, want %d", len([]rune(strings.TrimPrefix(lastLine, "• "))), compactCaptureMaxEventRunes)
	}
}

func TestCompactOrchestrationTranscriptGuidesRawFallbackWhenNoEventsExist(t *testing.T) {
	got := compactOrchestrationTranscript("╭──╮\n› prompt\ngpt-5 context left\n")
	if got.Output != compactCaptureEmptyOutput || got.Events != 0 || got.OmittedEvents != 0 {
		t.Fatalf("compact result = %+v, want empty-event fallback", got)
	}
}

func TestParseOrchestrateCaptureArgsSupportsRawWithoutChangingSessionCapture(t *testing.T) {
	opts, err := ParseOrchestrateCaptureArgs([]string{"--issue", "az-2", "--raw"})
	if err != nil {
		t.Fatalf("ParseOrchestrateCaptureArgs error = %v", err)
	}
	if !opts.Raw {
		t.Fatal("Raw = false, want true")
	}
	if _, err := ParseSessionCaptureArgs([]string{"--issue", "az-2", "--raw"}); err == nil {
		t.Fatal("ParseSessionCaptureArgs accepted orchestration-only --raw")
	}
}

func TestOrchestrateCaptureCommandPrintsCompactJSONMetadata(t *testing.T) {
	rawOutput := "╭──╮\n› prompt\n• Worker progress\n• Ran go test ./...\n  └ ok\n"
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := json.Marshal(protocol.SessionCaptureResponseBody{
					ProjectID: "proj",
					IssueID:   "az-2",
					SessionID: "proj-az-2",
					Lines:     120,
					Output:    rawOutput,
				})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					CompletedAt:     req.SentAt,
					OK:              true,
					Body:            body,
				}, nil
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return OrchestrateCaptureCommand(deps, SessionCaptureOptions{IssueID: "az-2", Lines: 120, JSON: true})
	})
	var got orchestrateCaptureResult
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, output)
	}
	if got.Mode != "compact" || got.RawBytes != len(rawOutput) || got.OutputBytes != len(got.Output) {
		t.Fatalf("metadata = %+v, want compact raw_bytes=%d output_bytes=%d", got, len(rawOutput), len(got.Output))
	}
	if got.Events != 2 || got.Output != "• Worker progress\n• Ran go test ./... → ok\n" {
		t.Fatalf("compact result = %+v", got)
	}
}
