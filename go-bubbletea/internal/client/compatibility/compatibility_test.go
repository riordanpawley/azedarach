package compatibility

import (
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestClassifyHandshake(t *testing.T) {
	if got := ClassifyHandshake(protocol.HelloAck{Accepted: true}); got != nil {
		t.Fatalf("accepted handshake should return nil diagnostic")
	}

	diag := ClassifyHandshake(protocol.HelloAck{
		Accepted:          false,
		ErrorCode:         protocol.ErrorCodeIncompatible,
		RetryAfterRestart: true,
		Reason:            "daemon older than client",
	})
	if diag == nil || !errors.Is(diag.Err, ErrIncompatible) || !diag.Retryable {
		t.Fatalf("unexpected incompatible diag: %+v", diag)
	}

	diag = ClassifyHandshake(protocol.HelloAck{
		Accepted:  false,
		ErrorCode: protocol.ErrorCodeUpgradeRequired,
		Reason:    "client too old",
	})
	if diag == nil || !errors.Is(diag.Err, ErrUpgradeRequired) || diag.Retryable {
		t.Fatalf("unexpected upgrade diag: %+v", diag)
	}
}

func TestClassifyConnectError(t *testing.T) {
	diag := ClassifyConnectError(errors.New("dial tcp refused"))
	if diag == nil || !diag.Retryable || !errors.Is(diag.Err, ErrOffline) {
		t.Fatalf("unexpected connect diag: %+v", diag)
	}
}
