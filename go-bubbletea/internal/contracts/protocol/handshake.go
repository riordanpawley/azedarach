package protocol

// Supported protocol window for this daemon/client build.
const (
	MinSupportedVersion Version = 1
	MaxSupportedVersion Version = CurrentVersion
)

// Hello is the initial client handshake request.
type Hello struct {
	ProtocolVersion Version  `json:"protocol_version" msgpack:"protocol_version"`
	ClientName      string   `json:"client_name" msgpack:"client_name"`
	ClientVersion   string   `json:"client_version" msgpack:"client_version"`
	Capabilities    []string `json:"capabilities,omitempty" msgpack:"capabilities,omitempty"`
}

// HelloAck is the daemon handshake response.
type HelloAck struct {
	Accepted              bool    `json:"accepted" msgpack:"accepted"`
	DaemonProtocolVersion Version `json:"daemon_protocol_version" msgpack:"daemon_protocol_version"`
	DaemonVersion         string  `json:"daemon_version" msgpack:"daemon_version"`
	ErrorCode             ErrorCode `json:"error_code,omitempty" msgpack:"error_code,omitempty"`
	RetryAfterRestart     bool      `json:"retry_after_restart,omitempty" msgpack:"retry_after_restart,omitempty"`
	Reason                string  `json:"reason,omitempty" msgpack:"reason,omitempty"`
}

// NegotiateHello evaluates protocol compatibility at attach/reconnect handshake.
func NegotiateHello(hello Hello, daemonVersion string) HelloAck {
	ack := HelloAck{
		Accepted:              false,
		DaemonProtocolVersion: CurrentVersion,
		DaemonVersion:         daemonVersion,
	}

	switch {
	case hello.ProtocolVersion == 0:
		ack.ErrorCode = ErrorCodeInvalidRequest
		ack.Reason = "missing or invalid protocol version"
	case hello.ProtocolVersion < MinSupportedVersion:
		ack.ErrorCode = ErrorCodeUpgradeRequired
		ack.Reason = "client protocol version is too old"
	case hello.ProtocolVersion > MaxSupportedVersion:
		ack.ErrorCode = ErrorCodeIncompatible
		ack.RetryAfterRestart = true
		ack.Reason = "client protocol version is newer than daemon"
	default:
		ack.Accepted = true
		ack.Reason = "compatible protocol"
	}

	return ack
}
