package protocol

import (
	"fmt"
	"slices"
	"strings"
)

// Supported protocol window for this daemon/client build.
const (
	// Payload schemas are version-pinned and are not translated across protocol
	// revisions. Advertising older revisions as compatible lets a stale client
	// attach successfully and then fail while decoding a current payload.
	MinSupportedVersion Version = CurrentVersion
	MaxSupportedVersion Version = CurrentVersion
)

// Hello is the initial client handshake request.
type Hello struct {
	ProtocolVersion Version  `json:"protocol_version" msgpack:"protocol_version"`
	ClientName      string   `json:"client_name" msgpack:"client_name"`
	ClientVersion   string   `json:"client_version" msgpack:"client_version"`
	Capabilities    []string `json:"capabilities,omitempty" msgpack:"capabilities,omitempty"`
	// RequiredCommands declares command-level compatibility that cannot be
	// inferred from the envelope protocol version alone. Older daemons ignore
	// this additive field and omit NegotiatedCommands, which lets a newer
	// client safely identify a same-protocol predecessor.
	RequiredCommands []string `json:"required_commands,omitempty" msgpack:"required_commands,omitempty"`
}

// HelloAck is the daemon handshake response.
type HelloAck struct {
	Accepted              bool      `json:"accepted" msgpack:"accepted"`
	DaemonProtocolVersion Version   `json:"daemon_protocol_version" msgpack:"daemon_protocol_version"`
	DaemonVersion         string    `json:"daemon_version" msgpack:"daemon_version"`
	ErrorCode             ErrorCode `json:"error_code,omitempty" msgpack:"error_code,omitempty"`
	RetryAfterRestart     bool      `json:"retry_after_restart,omitempty" msgpack:"retry_after_restart,omitempty"`
	Reason                string    `json:"reason,omitempty" msgpack:"reason,omitempty"`
	NegotiatedCommands    []string  `json:"negotiated_commands,omitempty" msgpack:"negotiated_commands,omitempty"`
}

var requiredCLICommands = [...]string{CommandDecisionAcknowledge}

// RequiredCLICommands returns the command compatibility floor for this az build.
// Keep this list deliberately bounded to commands whose absence must trigger
// managed daemon replacement instead of a later unsupported_command failure.
func RequiredCLICommands() []string {
	return append([]string(nil), requiredCLICommands[:]...)
}

// NegotiateHello evaluates protocol compatibility at attach/reconnect handshake.
func NegotiateHello(hello Hello, daemonVersion string) HelloAck {
	return NegotiateHelloWithCommands(hello, daemonVersion, requiredCLICommands[:])
}

// NegotiateHelloWithCommands evaluates protocol and explicitly requested
// command support. supportedCommands must describe the daemon build, never a
// caller repository or project route.
func NegotiateHelloWithCommands(hello Hello, daemonVersion string, supportedCommands []string) HelloAck {
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

	if !ack.Accepted {
		return ack
	}
	for _, required := range hello.RequiredCommands {
		required = strings.TrimSpace(required)
		if required == "" {
			continue
		}
		if !slices.Contains(supportedCommands, required) {
			ack.Accepted = false
			ack.ErrorCode = ErrorCodeIncompatible
			ack.RetryAfterRestart = true
			ack.Reason = fmt.Sprintf("daemon does not support required command %q", required)
			ack.NegotiatedCommands = nil
			return ack
		}
		ack.NegotiatedCommands = append(ack.NegotiatedCommands, required)
	}
	return ack
}
