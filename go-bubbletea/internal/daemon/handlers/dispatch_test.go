package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type fakeWorktreeService struct{}

func (f *fakeWorktreeService) List(context.Context, string) ([]git.Worktree, error) {
	return []git.Worktree{{Path: "/tmp/wt", Branch: "main", IssueID: "afk"}}, nil
}
func (f *fakeWorktreeService) Create(context.Context, string, string, string) (*git.Worktree, error) {
	return &git.Worktree{Path: "/tmp/wt", Branch: "main", IssueID: "afk"}, nil
}
func (f *fakeWorktreeService) Delete(context.Context, string, string) error { return nil }
func (f *fakeWorktreeService) CleanupOrphaned(context.Context, string) (*CleanupOrphanedResult, error) {
	return &CleanupOrphanedResult{ProjectID: "proj"}, nil
}

type routeDevServerManager struct {
	servers map[string]*devserver.Server
}

func newRouteDevServerManager() *routeDevServerManager {
	return &routeDevServerManager{
		servers: map[string]*devserver.Server{
			"afl": {Name: "afl", Command: "run", Status: "stopped"},
		},
	}
}

func (m *routeDevServerManager) Start(ctx context.Context, issueID, name, command string) (*devserver.Server, error) {
	srv := &devserver.Server{Name: name, Command: command, Status: "running"}
	m.servers[issueID] = srv
	return srv, nil
}
func (m *routeDevServerManager) Stop(ctx context.Context, issueID string) error {
	if srv, ok := m.servers[issueID]; ok {
		srv.Status = "stopped"
	}
	return nil
}
func (m *routeDevServerManager) Get(issueID string) (*devserver.Server, bool) {
	srv, ok := m.servers[issueID]
	return srv, ok
}

func TestDispatcherMixedRouting(t *testing.T) {
	session := NewSessionHandler(daemonstate.NewStore())
	worktree := NewWorktreeHandler(&fakeWorktreeService{})
	devserverH := NewDevServerHandler(newRouteDevServerManager())
	dispatch := NewDispatcher(session, worktree, devserverH)

	mkReq := func(cmd string, body any) protocol.RequestEnvelope {
		b, _ := json.Marshal(body)
		return protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-" + cmd,
			Kind:            protocol.EnvelopeKindCommand,
			Command:         cmd,
			Body:            b,
		}
	}

	r1 := dispatch.Handle(context.Background(), mkReq("session.start", map[string]string{
		"project_id": "proj",
		"session_id": "s1",
		"issue_id":   "aey",
	}))
	if !r1.OK {
		t.Fatalf("session route failed: %+v", r1.Error)
	}

	r2 := dispatch.Handle(context.Background(), mkReq("worktree.list", map[string]string{
		"project_id": "/tmp/proj",
	}))
	if !r2.OK {
		t.Fatalf("worktree route failed: %+v", r2.Error)
	}

	r3 := dispatch.Handle(context.Background(), mkReq("devserver.status", map[string]string{
		"issue_id": "afl",
	}))
	if !r3.OK {
		t.Fatalf("devserver route failed: %+v", r3.Error)
	}

	r4 := dispatch.Handle(context.Background(), mkReq("worktree.cleanup_orphaned", map[string]string{
		"project_id": "proj",
	}))
	if !r4.OK {
		t.Fatalf("cleanup route failed: %+v", r4.Error)
	}
}

func TestDispatcherUnknownCommand(t *testing.T) {
	dispatch := NewDispatcher(nil, nil, nil)
	resp := dispatch.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-x",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "foo.bar",
	})
	if resp.OK {
		t.Fatalf("expected unsupported command response")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnsupportedCommand {
		t.Fatalf("unexpected error mapping: %+v", resp.Error)
	}
}
