package types

import "time"

// Toast represents a notification message
type Toast struct {
	Level   ToastLevel
	Message string
	Expires time.Time
}

// ToastLevel indicates the severity of a toast
type ToastLevel int

const (
	ToastInfo ToastLevel = iota
	ToastSuccess
	ToastWarning
	ToastError
)
