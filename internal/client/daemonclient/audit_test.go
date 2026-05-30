package daemonclient

import (
	"context"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestCommandPopulatesClientAuditMetadataFromEnv(t *testing.T) {
	t.Setenv(auditEnvInvocationID, "inv-1")
	t.Setenv(auditEnvCommandShape, "session stop")
	t.Setenv(auditEnvArgv, `["session","stop","ckf"]`)
	t.Setenv(auditEnvExecutable, "az")
	t.Setenv(auditEnvPID, "123")
	t.Setenv(auditEnvPPID, "45")
	t.Setenv(auditEnvCWD, "/repo/wt")
	t.Setenv(auditEnvPWD, "/logical/wt")
	t.Setenv(auditEnvActor, "riordan")
	t.Setenv(auditEnvUID, "501")
	t.Setenv(auditEnvActiveIssue, "ckf")

	var got protocol.RequestEnvelope
	client := New(&fakeTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		got = req
		return protocol.ResponseEnvelope{OK: true}, nil
	}})
	if _, err := client.Command(context.Background(), protocol.RequestEnvelope{Command: "session.stop"}); err != nil {
		t.Fatalf("Command() error = %v", err)
	}

	if got.Meta.ClientInvocationID != "inv-1" ||
		got.Meta.ClientCommandShape != "session stop" ||
		got.Meta.ClientExecutable != "az" ||
		got.Meta.ClientPID != 123 ||
		got.Meta.ClientPPID != 45 ||
		got.Meta.ClientCWD != "/repo/wt" ||
		got.Meta.ClientPWD != "/logical/wt" ||
		got.Meta.ClientActor != "riordan" ||
		got.Meta.ClientUID != "501" ||
		got.Meta.ClientActiveIssue != "ckf" {
		t.Fatalf("audit metadata = %+v", got.Meta)
	}
	if len(got.Meta.ClientArgv) != 3 || got.Meta.ClientArgv[0] != "session" || got.Meta.ClientArgv[2] != "ckf" {
		t.Fatalf("client argv = %#v", got.Meta.ClientArgv)
	}
}

func TestCommandPreservesExplicitClientAuditMetadata(t *testing.T) {
	t.Setenv(auditEnvInvocationID, "env-inv")
	var got protocol.RequestEnvelope
	client := New(&fakeTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		got = req
		return protocol.ResponseEnvelope{OK: true}, nil
	}})
	if _, err := client.Command(context.Background(), protocol.RequestEnvelope{
		Command: "task.list",
		Meta: protocol.Metadata{
			ClientInvocationID: "explicit",
		},
	}); err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	if got.Meta.ClientInvocationID != "explicit" {
		t.Fatalf("client invocation id = %q, want explicit", got.Meta.ClientInvocationID)
	}
}
