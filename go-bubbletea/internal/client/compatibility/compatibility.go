package compatibility

import (
	"errors"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

var (
	// ErrOffline indicates daemon reachability/connectivity failure.
	ErrOffline = errors.New("daemon offline")
	// ErrIncompatible indicates daemon/client protocol mismatch.
	ErrIncompatible = errors.New("daemon/client incompatible")
	// ErrUpgradeRequired indicates client upgrade is required.
	ErrUpgradeRequired = errors.New("client upgrade required")
)

// Diagnostic is a typed handshake/reconnect diagnostic.
type Diagnostic struct {
	Code       protocol.ErrorCode
	Retryable  bool
	Message    string
	Err        error
}

// ClassifyHandshake translates handshake ack into a typed diagnostic.
func ClassifyHandshake(ack protocol.HelloAck) *Diagnostic {
	if ack.Accepted {
		return nil
	}
	switch ack.ErrorCode {
	case protocol.ErrorCodeUpgradeRequired:
		return &Diagnostic{
			Code:      ack.ErrorCode,
			Retryable: false,
			Message:   ack.Reason,
			Err:       ErrUpgradeRequired,
		}
	case protocol.ErrorCodeIncompatible:
		return &Diagnostic{
			Code:      ack.ErrorCode,
			Retryable: ack.RetryAfterRestart,
			Message:   ack.Reason,
			Err:       ErrIncompatible,
		}
	default:
		return &Diagnostic{
			Code:      ack.ErrorCode,
			Retryable: false,
			Message:   ack.Reason,
			Err:       ErrIncompatible,
		}
	}
}

// ClassifyConnectError maps low-level connect failures.
func ClassifyConnectError(err error) *Diagnostic {
	if err == nil {
		return nil
	}
	return &Diagnostic{
		Code:      protocol.ErrorCodeUnavailable,
		Retryable: true,
		Message:   err.Error(),
		Err:       ErrOffline,
	}
}
