package probe

import (
	"encoding/json"
	"errors"
	"fmt"
)

const SchemaVersion = "1.0.0"

var (
	ErrInvalidSchemaVersion = errors.New("invalid schema version")
	ErrMissingMode          = errors.New("missing mode")
	ErrMissingFocus         = errors.New("missing focus")
	ErrNegativeSelection    = errors.New("selection index must be >= 0")
)

type Payload struct {
	SchemaVersion string           `json:"schemaVersion"`
	Mode          string           `json:"mode"`
	Focus         string           `json:"focus"`
	Selection     SelectionState   `json:"selection"`
	Overlay       OverlayState     `json:"overlay"`
	Operation     OperationSummary `json:"operation"`
}

type SelectionState struct {
	Column string `json:"column"`
	ItemID string `json:"itemId,omitempty"`
	Index  int    `json:"index"`
}

type OverlayState struct {
	Name    string `json:"name,omitempty"`
	Visible bool   `json:"visible"`
}

type OperationSummary struct {
	Kind      string `json:"kind,omitempty"`
	State     string `json:"state,omitempty"`
	Active    bool   `json:"active"`
	ErrorCode string `json:"errorCode,omitempty"`
}

func (p *Payload) Validate() error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: got %q want %q", ErrInvalidSchemaVersion, p.SchemaVersion, SchemaVersion)
	}

	if p.Mode == "" {
		return ErrMissingMode
	}

	if p.Focus == "" {
		return ErrMissingFocus
	}

	if p.Selection.Index < 0 {
		return fmt.Errorf("%w: %d", ErrNegativeSelection, p.Selection.Index)
	}

	return nil
}

func Marshal(payload Payload) ([]byte, error) {
	if payload.SchemaVersion == "" {
		payload.SchemaVersion = SchemaVersion
	}

	if err := payload.Validate(); err != nil {
		return nil, err
	}

	return json.Marshal(payload)
}

func Unmarshal(data []byte) (Payload, error) {
	var payload Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		return Payload{}, fmt.Errorf("unmarshal probe payload: %w", err)
	}

	if err := payload.Validate(); err != nil {
		return Payload{}, err
	}

	return payload, nil
}
