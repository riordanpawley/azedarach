package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestHookLogAppendAndList(t *testing.T) {
	repoDir := t.TempDir()
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	projectID := "proj-hooks"
	for i := 0; i < 3; i++ {
		msg := "hook run"
		if i == 2 {
			msg = "refresh failed"
		}
		resp, err := d.command(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "hook-append",
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Command:         protocol.CommandHookLogAppend,
			Body: mustMarshal(t, protocol.HookLogAppendCommandBody{
				Event: protocol.HookLogEvent{
					Hook:      "post-commit",
					Worktree:  "/tmp/wt",
					Source:    "githooks.hook",
					Level:     "info",
					Message:   msg,
					CreatedAt: time.Date(2026, time.April, 4, 9, 0, i, 0, time.UTC),
				},
			}),
		})
		if err != nil {
			t.Fatalf("hook.log.append command error: %v", err)
		}
		if !resp.OK {
			t.Fatalf("hook.log.append response not ok: %+v", resp.Error)
		}
		if resp.Revision == 0 {
			t.Fatalf("hook.log.append response revision = %d, want > 0", resp.Revision)
		}
	}

	listResp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "hook-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         protocol.CommandHookLogList,
		Body:            mustMarshal(t, protocol.HookLogListCommandBody{Limit: 2}),
	})
	if err != nil {
		t.Fatalf("hook.log.list command error: %v", err)
	}
	if !listResp.OK {
		t.Fatalf("hook.log.list response not ok: %+v", listResp.Error)
	}

	var events []protocol.HookLogEvent
	if err := json.Unmarshal(listResp.Body, &events); err != nil {
		t.Fatalf("unmarshal hook.log.list body: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("hook.log.list len = %d, want 2", len(events))
	}
	if events[0].Message != "hook run" || events[1].Message != "refresh failed" {
		t.Fatalf("hook.log.list events = %+v", events)
	}
	if events[0].ProjectID != projectID || events[1].ProjectID != projectID {
		t.Fatalf("hook.log.list project ids = [%q,%q], want %q", events[0].ProjectID, events[1].ProjectID, projectID)
	}
}
