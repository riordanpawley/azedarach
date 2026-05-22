package protocol

// CommandDaemonShutdown requests graceful daemon shutdown over IPC.
const CommandDaemonShutdown = "daemon.shutdown"

// DaemonShutdownCommandBody carries caller intent for shutdown attribution.
type DaemonShutdownCommandBody struct {
	Reason string `json:"reason,omitempty"`
}
