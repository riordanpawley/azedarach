package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

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
