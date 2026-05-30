package daemon

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestDaemonClientAuditAttrsIncludesAttribution(t *testing.T) {
	attrs := daemonClientAuditAttrs(protocol.Metadata{
		ClientInvocationID: "inv-1",
		ClientCommandShape: "session stop",
		ClientArgv:         []string{"session", "stop", "ckf"},
		ClientExecutable:   "az",
		ClientPID:          123,
		ClientPPID:         45,
		ClientCWD:          "/repo/wt",
		ClientPWD:          "/logical/wt",
		ClientActor:        "riordan",
		ClientUID:          "501",
		ClientActiveIssue:  "ckf",
	})
	for _, key := range []string{
		"client_invocation_id",
		"client_command_shape",
		"client_argv",
		"client_executable",
		"client_pid",
		"client_ppid",
		"client_cwd",
		"client_pwd",
		"client_actor",
		"client_uid",
		"client_active_issue",
	} {
		if !attrsContainKey(attrs, key) {
			t.Fatalf("attrs missing %q: %#v", key, attrs)
		}
	}
}

func attrsContainKey(attrs []any, key string) bool {
	for i := 0; i < len(attrs)-1; i += 2 {
		if attrs[i] == key {
			return true
		}
	}
	return false
}
