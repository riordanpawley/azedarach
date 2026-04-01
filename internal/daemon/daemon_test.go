package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestDrainInFlightCommandsWaitsForCompletionAndRejectsNewIntake(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			IdleTimeout: 100 * time.Millisecond,
		},
	}

	if err := d.beginCommand(); err != nil {
		t.Fatalf("beginCommand error: %v", err)
	}

	drained := make(chan struct{})
	go func() {
		d.drainInFlightCommands()
		close(drained)
	}()

	select {
	case <-drained:
		t.Fatal("drain returned before in-flight command finished")
	case <-time.After(20 * time.Millisecond):
	}

	if err := d.beginCommand(); err == nil {
		t.Fatal("expected beginCommand to reject new intake while draining")
	}

	d.endCommand()

	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drain did not finish after in-flight command completed")
	}
}

func TestCommandLogsFailure(t *testing.T) {
	var logs bytes.Buffer
	d := &Daemon{
		cfg: Config{
			Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
		},
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-1",
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: "demo",
		},
		Command: "task.unsupported",
		SentAt:  time.Now().UTC(),
	}

	resp, err := d.command(context.Background(), req)
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected command error response, got ok=%v", resp.OK)
	}

	output := logs.String()
	if !strings.Contains(output, "daemon command received") {
		t.Fatalf("expected start command log entry, got %q", output)
	}
	if !strings.Contains(output, "daemon command failed") {
		t.Fatalf("expected failure command log entry, got %q", output)
	}
	if !strings.Contains(output, "command=task.unsupported") {
		t.Fatalf("expected command field in logs, got %q", output)
	}
	if !strings.Contains(output, "request_id=req-1") {
		t.Fatalf("expected request_id field in logs, got %q", output)
	}
}
