package errors

import (
	stderrors "errors"
	"fmt"
)

type Kind string

const (
	KindAuth             Kind = "auth"
	KindOffline          Kind = "offline"
	KindConflict         Kind = "conflict"
	KindTimeout          Kind = "timeout"
	KindLockContention   Kind = "lock_contention"
	KindUnexpectedOutput Kind = "unexpected_output"
)

type Error struct {
	Kind    Kind
	Op      string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}

	if e.Message != "" && e.Err != nil {
		return fmt.Sprintf("%s: %s: %s: %v", e.Kind, e.Op, e.Message, e.Err)
	}
	if e.Message != "" {
		return fmt.Sprintf("%s: %s: %s", e.Kind, e.Op, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Op, e.Err)
	}

	return fmt.Sprintf("%s: %s", e.Kind, e.Op)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func New(kind Kind, op string, message string, err error) error {
	return &Error{
		Kind:    kind,
		Op:      op,
		Message: message,
		Err:     err,
	}
}

func NewAuth(op string, message string, err error) error {
	return New(KindAuth, op, message, err)
}

func NewOffline(op string, message string, err error) error {
	return New(KindOffline, op, message, err)
}

func NewConflict(op string, message string, err error) error {
	return New(KindConflict, op, message, err)
}

func NewTimeout(op string, message string, err error) error {
	return New(KindTimeout, op, message, err)
}

func NewLockContention(op string, message string, err error) error {
	return New(KindLockContention, op, message, err)
}

func NewUnexpectedOutput(op string, message string, err error) error {
	return New(KindUnexpectedOutput, op, message, err)
}

func IsKind(err error, kind Kind) bool {
	var adapterErr *Error
	if !stderrors.As(err, &adapterErr) {
		return false
	}
	return adapterErr.Kind == kind
}

func IsAuth(err error) bool {
	return IsKind(err, KindAuth)
}

func IsOffline(err error) bool {
	return IsKind(err, KindOffline)
}

func IsConflict(err error) bool {
	return IsKind(err, KindConflict)
}

func IsTimeout(err error) bool {
	return IsKind(err, KindTimeout)
}

func IsLockContention(err error) bool {
	return IsKind(err, KindLockContention)
}

func IsUnexpectedOutput(err error) bool {
	return IsKind(err, KindUnexpectedOutput)
}
