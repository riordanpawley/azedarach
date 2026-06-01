package daemon

import "github.com/riordanpawley/azedarach/internal/contracts/protocol"

func daemonClientAuditAttrs(meta protocol.Metadata) []any {
	attrs := make([]any, 0, 24)
	appendString := func(key, value string) {
		if value != "" {
			attrs = append(attrs, key, value)
		}
	}
	appendInt := func(key string, value int) {
		if value != 0 {
			attrs = append(attrs, key, value)
		}
	}
	appendString("client_invocation_id", meta.ClientInvocationID)
	appendString("client_command_shape", meta.ClientCommandShape)
	if len(meta.ClientArgv) > 0 {
		attrs = append(attrs, "client_argv", meta.ClientArgv)
	}
	appendString("client_executable", meta.ClientExecutable)
	appendInt("client_pid", meta.ClientPID)
	appendInt("client_ppid", meta.ClientPPID)
	appendString("client_cwd", meta.ClientCWD)
	appendString("client_pwd", meta.ClientPWD)
	appendString("client_actor", meta.ClientActor)
	appendString("client_uid", meta.ClientUID)
	appendString("client_active_issue", meta.ClientActiveIssue)
	return attrs
}
