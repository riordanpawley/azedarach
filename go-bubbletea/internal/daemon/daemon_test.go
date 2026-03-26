package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
)

func TestNewWiresSessionHandlerAndStore(t *testing.T) {
	repoDir := t.TempDir()
	d := New(Config{
		RepoDir:    repoDir,
		SocketPath: filepath.Join(repoDir, "daemon.sock"),
		LockPath:   filepath.Join(repoDir, "daemon.lock"),
		BaseBranch: "main",
		CLITool:    "claude",
	})

	if d.session == nil {
		t.Fatal("expected daemon.New to wire a session handler")
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-session",
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: "proj",
		},
	}

	if err := d.applySessionLifecycleTransition(context.Background(), req, "proj", "s1", "issue-1", daemonhandlers.CommandSessionStart); err != nil {
		t.Fatalf("start transition failed: %v", err)
	}
	if err := d.applySessionLifecycleTransition(context.Background(), req, "proj", "s1", "issue-1", daemonhandlers.CommandSessionAttach); err != nil {
		t.Fatalf("attach transition failed: %v", err)
	}
	if err := d.applySessionLifecycleTransition(context.Background(), req, "proj", "s1", "issue-1", daemonhandlers.CommandSessionPause); err != nil {
		t.Fatalf("pause transition failed: %v", err)
	}
	if err := d.applySessionLifecycleTransition(context.Background(), req, "proj", "s1", "issue-1", daemonhandlers.CommandSessionStop); err != nil {
		t.Fatalf("stop transition failed: %v", err)
	}

	if err := d.applySessionLifecycleTransition(context.Background(), req, "proj", "s1", "issue-1", daemonhandlers.CommandSessionStart); err != nil {
		t.Fatalf("restart transition after stop failed: %v", err)
	}
}

func TestApplySessionLifecycleTransitionRequiresHandler(t *testing.T) {
	d := &Daemon{}
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-session",
		Kind:            protocol.EnvelopeKindCommand,
	}

	if err := d.applySessionLifecycleTransition(context.Background(), req, "proj", "s1", "issue-1", daemonhandlers.CommandSessionStart); err == nil {
		t.Fatal("expected missing session handler to return an error")
	}
}
