package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestMailWatchGenericCommandLogsAtDebugOnSuccess(t *testing.T) {
	repoDir := t.TempDir()
	body := mustMarshal(t, protocol.MailWatchCommandBody{
		RepoDir:     repoDir,
		ParentIssue: "az-parent",
	})
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-watch",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: "proj-mail"},
		Command:         protocol.CommandMailWatch,
		Body:            body,
	}

	var infoLogs bytes.Buffer
	infoDaemon := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(&infoLogs, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	resp, err := infoDaemon.command(context.Background(), req)
	if err != nil {
		t.Fatalf("mail.watch command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("mail.watch response error: %+v", resp.Error)
	}
	if strings.Contains(infoLogs.String(), "daemon command received") || strings.Contains(infoLogs.String(), "daemon command completed") {
		t.Fatalf("mail.watch success should not emit generic info command logs, got %q", infoLogs.String())
	}

	var debugLogs bytes.Buffer
	debugDaemon := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(&debugLogs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	resp, err = debugDaemon.command(context.Background(), req)
	if err != nil {
		t.Fatalf("debug mail.watch command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("debug mail.watch response error: %+v", resp.Error)
	}
	output := debugLogs.String()
	if !strings.Contains(output, "daemon command received") || !strings.Contains(output, "daemon command completed") {
		t.Fatalf("mail.watch success should emit generic command logs at debug, got %q", output)
	}
}

func TestMailWatchFailureStillWarnsAtInfo(t *testing.T) {
	repoDir := t.TempDir()
	var logs bytes.Buffer
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-watch-invalid",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: "proj-mail"},
		Command:         protocol.CommandMailWatch,
		Body:            mustMarshal(t, protocol.MailWatchCommandBody{RepoDir: repoDir}),
	})
	if err != nil {
		t.Fatalf("mail.watch command error: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected mail.watch error response, got ok=%v", resp.OK)
	}
	output := logs.String()
	if strings.Contains(output, "daemon command received") {
		t.Fatalf("mail.watch failure should not emit generic received info log, got %q", output)
	}
	if !strings.Contains(output, "daemon command failed") {
		t.Fatalf("mail.watch failure should still emit warn log, got %q", output)
	}
}

func TestMailSendSerializesSequenceNumbers(t *testing.T) {
	repoDir := t.TempDir()
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	parent := "az-parent"
	start := make(chan struct{})
	errs := make(chan error, 2)
	send := func(issue string) {
		<-start
		_, err := d.command(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID(fmt.Sprintf("req-%s", issue)),
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: "proj-mail"},
			Command:         protocol.CommandMailSend,
			Body: mustMarshal(t, protocol.MailSendCommandBody{
				RepoDir:     repoDir,
				ParentIssue: parent,
				IssueID:     naming.IssueID(issue),
				Type:        "handoff",
				Body:        issue,
			}),
		})
		errs <- err
	}

	go send("az-1")
	go send("az-2")
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("mail.send command error: %v", err)
		}
	}

	events, err := readMailboxEvents(repoDir, parent)
	if err != nil {
		t.Fatalf("readMailboxEvents error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("seqs = [%d,%d], want [1,2]", events[0].Seq, events[1].Seq)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
