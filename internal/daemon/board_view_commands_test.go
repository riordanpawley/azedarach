package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestBoardViewMutationsPublishProjectionInvalidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	logger := slog.Default()
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("CloseDB error: %v", err)
		}
	})
	projectID := "proj-board-view-events"
	view := domain.DefaultBoardView()
	view.ID = "custom"
	view.Title = "Custom"

	d := &Daemon{
		cfg:                   Config{Logger: logger, RepoDir: t.TempDir()},
		issues:                issuesClient,
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		uiState:               map[string]string{},
		revision:              map[string]uint64{},
		hub:                   publish.NewHub(16, 8, logger),
	}
	events, cancel := d.hub.Subscribe(projectID, 0)
	defer cancel()

	request := func(command string, body any) protocol.RequestEnvelope {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s request: %v", command, err)
		}
		return protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID(command + "-request"),
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Command:         command,
			SentAt:          time.Now().UTC(),
			Body:            encoded,
		}
	}
	assertEvent := func(resp protocol.ResponseEnvelope, change protocol.BoardViewChange) {
		t.Helper()
		if !resp.OK {
			t.Fatalf("%s response = %+v", change, resp.Error)
		}
		select {
		case event := <-events:
			if event.Event != protocol.EventBoardViewChanged {
				t.Fatalf("event = %q, want %q", event.Event, protocol.EventBoardViewChanged)
			}
			if event.Revision != resp.Revision {
				t.Fatalf("event revision = %d, want response revision %d", event.Revision, resp.Revision)
			}
			var eventBody protocol.BoardViewChangedEventBody
			if err := json.Unmarshal(event.Body, &eventBody); err != nil {
				t.Fatalf("unmarshal event body: %v", err)
			}
			if eventBody.ProjectID.String() != projectID || eventBody.ViewID != "custom" || eventBody.Change != change {
				t.Fatalf("event body = %+v", eventBody)
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("timed out waiting for %s board-view projection invalidation", change)
		}
	}

	resp, err := d.handleBoardViewSave(ctx, request(protocol.CommandBoardViewSave, protocol.BoardViewSaveRequestBody{View: view}))
	if err != nil {
		t.Fatalf("handleBoardViewSave error: %v", err)
	}
	assertEvent(resp, protocol.BoardViewChangeSaved)

	resp, err = d.handleBoardViewSelect(ctx, request(protocol.CommandBoardViewSelect, protocol.BoardViewSelectRequestBody{ViewID: "custom"}))
	if err != nil {
		t.Fatalf("handleBoardViewSelect error: %v", err)
	}
	assertEvent(resp, protocol.BoardViewChangeSelected)

	resp, err = d.handleBoardViewDelete(ctx, request(protocol.CommandBoardViewDelete, protocol.BoardViewDeleteRequestBody{ViewID: "custom"}))
	if err != nil {
		t.Fatalf("handleBoardViewDelete error: %v", err)
	}
	assertEvent(resp, protocol.BoardViewChangeDeleted)
}
