package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

// ApplyDryRunOperation is one deterministic operation entry in a dry-run preview.
type ApplyDryRunOperation struct {
	Index   int             `json:"index"`
	Command string          `json:"command"`
	Body    json.RawMessage `json:"body,omitempty"`
}

// ApplyDryRunPreview captures the validated request shape used for deterministic dry-run output.
type ApplyDryRunPreview struct {
	SchemaVersion    uint16                 `json:"schema_version"`
	SnapshotRevision uint64                 `json:"snapshot_revision"`
	DryRun           bool                   `json:"dry_run"`
	Operations       []ApplyDryRunOperation `json:"operations"`
}

// ApplyRevisionGateResult captures the revision precondition outcome for an apply request.
type ApplyRevisionGateResult struct {
	Allowed          bool                    `json:"allowed"`
	CurrentRevision  uint64                  `json:"current_revision"`
	SnapshotRevision uint64                  `json:"snapshot_revision"`
	Error            *protocol.ErrorEnvelope `json:"error,omitempty"`
}

// BuildApplyDryRunPreview returns a deterministic preview of a validated apply request.
//
// The preview intentionally clones operation bodies so the returned structure is stable even if
// the caller later mutates the original request payload.
func BuildApplyDryRunPreview(req protocol.ApplyRequestBody) ApplyDryRunPreview {
	preview := ApplyDryRunPreview{
		SchemaVersion:    req.SchemaVersion,
		SnapshotRevision: req.SnapshotRevision,
		DryRun:           req.DryRun,
		Operations:       make([]ApplyDryRunOperation, 0, len(req.Operations)),
	}

	for i, op := range req.Operations {
		preview.Operations = append(preview.Operations, ApplyDryRunOperation{
			Index:   i,
			Command: op.Command,
			Body:    cloneApplyBody(op.Body),
		})
	}

	return preview
}

// EvaluateApplyRevisionGate validates the request against the current revision watermark.
func EvaluateApplyRevisionGate(req protocol.ApplyRequestBody, currentRevision uint64) ApplyRevisionGateResult {
	result := ApplyRevisionGateResult{
		Allowed:          req.SnapshotRevision == currentRevision,
		CurrentRevision:  currentRevision,
		SnapshotRevision: req.SnapshotRevision,
	}
	if result.Allowed {
		return result
	}

	result.Error = &protocol.ErrorEnvelope{
		Code:      protocol.ErrorCodeRevisionGap,
		Message:   fmt.Sprintf("snapshot revision %d does not match current revision %d", req.SnapshotRevision, currentRevision),
		Retryable: protocol.ErrorCodeRevisionGap.Retryable(),
	}
	return result
}

func cloneApplyBody(body json.RawMessage) json.RawMessage {
	if len(body) == 0 {
		return nil
	}
	cloned := make([]byte, len(body))
	copy(cloned, body)
	return json.RawMessage(cloned)
}
