package handlers

import (
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestCommandSpecRegistryProjectIDPolicy(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{command: CommandGitFetch, want: true},
		{command: protocol.CommandOperationList, want: true},
		{command: CommandSessionStart, want: true},
		{command: protocol.CommandIssueFanout, want: true},
		{command: protocol.CommandSpecRead, want: false},
		{command: "unknown.command", want: false},
	}

	for _, tt := range tests {
		if got := CommandRequiresProjectID(tt.command); got != tt.want {
			t.Fatalf("CommandRequiresProjectID(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

func TestCommandSpecRegistrySyncBootstrapPolicy(t *testing.T) {
	taskResp := CommandRequiresSyncBootstrap(protocol.RequestEnvelope{Command: "task.list"})
	if !taskResp {
		t.Fatal("expected task.list to require sync bootstrap")
	}

	sessionResp := CommandRequiresSyncBootstrap(protocol.RequestEnvelope{Command: CommandSessionPause})
	if !sessionResp {
		t.Fatal("expected session.pause to require sync bootstrap")
	}

	nonSyncResp := CommandRequiresSyncBootstrap(protocol.RequestEnvelope{Command: CommandGitStatus})
	if nonSyncResp {
		t.Fatal("expected git.status not to require sync bootstrap")
	}

	syncBody, err := json.Marshal(protocol.OperationSubmitRequestBody{
		Kind: " session.start ",
	})
	if err != nil {
		t.Fatalf("marshal sync body: %v", err)
	}
	if !CommandRequiresSyncBootstrap(protocol.RequestEnvelope{
		Command: protocol.CommandOperationSubmit,
		Body:    syncBody,
	}) {
		t.Fatal("expected operation.submit(session.start) to require sync bootstrap")
	}

	nonSyncBody, err := json.Marshal(protocol.OperationSubmitRequestBody{
		Kind: "worktree.create",
	})
	if err != nil {
		t.Fatalf("marshal non-sync body: %v", err)
	}
	if CommandRequiresSyncBootstrap(protocol.RequestEnvelope{
		Command: protocol.CommandOperationSubmit,
		Body:    nonSyncBody,
	}) {
		t.Fatal("expected operation.submit(worktree.create) not to require sync bootstrap")
	}
}

func TestCommandSpecRegistryDispatcherTargets(t *testing.T) {
	tests := []struct {
		command string
		want    CommandDispatchTarget
		ok      bool
	}{
		{command: CommandSessionStart, want: CommandDispatchSession, ok: true},
		{command: protocol.CommandOperationGet, want: CommandDispatchOperation, ok: true},
		{command: CommandPRCreate, want: CommandDispatchPR, ok: true},
		{command: CommandGitBranchBehind, want: CommandDispatchPR, ok: true},
		{command: protocol.CommandSpecRead, want: CommandDispatchSpec, ok: true},
		{command: CommandGitCheckpoint, want: CommandDispatchGit, ok: true},
		{command: CommandWorktreeRemove, want: CommandDispatchWorktree, ok: true},
		{command: CommandDevServerList, want: CommandDispatchDevServer, ok: true},
		{command: "task.list", want: CommandDispatchNone, ok: false},
		{command: "unknown.command", want: CommandDispatchNone, ok: false},
	}

	for _, tt := range tests {
		got, ok := DispatcherTarget(tt.command)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("DispatcherTarget(%q) = (%v, %v), want (%v, %v)", tt.command, got, ok, tt.want, tt.ok)
		}
	}
}

func TestCommandSpecRegistryDaemonDispatchOwnership(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{command: CommandGitFetch, want: true},
		{command: protocol.CommandOperationCancel, want: true},
		{command: protocol.CommandSpecRead, want: true},
		{command: CommandSessionStart, want: false},
		{command: "task.list", want: false},
		{command: "unknown.command", want: false},
	}

	for _, tt := range tests {
		if got := DaemonRoutesThroughDispatcher(tt.command); got != tt.want {
			t.Fatalf("DaemonRoutesThroughDispatcher(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}
