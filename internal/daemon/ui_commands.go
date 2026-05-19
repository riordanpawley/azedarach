package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func (d *Daemon) handleUIOpenTaskWorkspace(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd protocol.UICommandRequestBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if strings.TrimSpace(cmd.ProjectID.String()) != "" {
		projectID = d.canonicalProjectID(cmd.ProjectID.String())
	}
	issueID := naming.IssueID(strings.TrimSpace(cmd.IssueID.String()))
	if issueID.IsZero() {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: issue_id"), nil
	}
	command := strings.TrimSpace(cmd.Command)
	if command == "" {
		command = protocol.UICommandOpenTaskWorkspace
	}
	if command != protocol.UICommandOpenTaskWorkspace {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "unsupported ui command"), nil
	}
	createdAt := cmd.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	requestID := strings.TrimSpace(cmd.RequestID)
	if requestID == "" {
		requestID = req.RequestID.String()
	}
	body := protocol.UICommandResponseBody{
		ProjectID: naming.ProjectID(projectID),
		IssueID:   issueID,
		Command:   command,
		RequestID: requestID,
		CreatedAt: createdAt,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal ui command body: %v", err)), nil
	}
	rev := d.nextRevision(projectID)
	if d.hub != nil {
		d.hub.Publish(protocol.EventEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			ProjectID:       naming.ProjectID(projectID),
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID), CorrelationID: req.Meta.CorrelationID},
			Revision:        rev,
			Event:           protocol.EventUICommandRequested,
			Kind:            protocol.EnvelopeKindEvent,
			EmittedAt:       createdAt,
			Body:            payload,
		})
	}
	resp := d.successResponse(req)
	resp.Revision = rev
	resp.Body = payload
	return resp, nil
}

func (d *Daemon) handleUIStateGet(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd protocol.UIStateGetRequestBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if strings.TrimSpace(cmd.ProjectID.String()) != "" {
		projectID = d.canonicalProjectID(cmd.ProjectID.String())
	}
	key := strings.TrimSpace(cmd.Key)
	if key == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: key"), nil
	}
	state, found, err := d.sessionRuntimeStateStore(projectID).GetUIState(ctx, projectID, key)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("get ui state: %v", err)), nil
	}
	respBody := protocol.UIStateResponseBody{
		ProjectID: naming.ProjectID(projectID),
		Key:       key,
		Found:     found,
	}
	if found {
		respBody.Value = state.Value
		respBody.UpdatedAt = state.UpdatedAt
	}
	payload, err := json.Marshal(respBody)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal ui state response: %v", err)), nil
	}
	resp := d.successResponse(req)
	resp.Body = payload
	return resp, nil
}

func (d *Daemon) handleUIStateSet(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd protocol.UIStateSetRequestBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if strings.TrimSpace(cmd.ProjectID.String()) != "" {
		projectID = d.canonicalProjectID(cmd.ProjectID.String())
	}
	key := strings.TrimSpace(cmd.Key)
	if key == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: key"), nil
	}
	updatedAt := time.Now().UTC()
	if err := d.sessionRuntimeStateStore(projectID).UpsertUIState(ctx, daemonstate.UIState{
		ProjectID: projectID,
		Key:       key,
		Value:     cmd.Value,
		UpdatedAt: updatedAt,
	}); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("set ui state: %v", err)), nil
	}
	respBody := protocol.UIStateResponseBody{
		ProjectID: naming.ProjectID(projectID),
		Key:       key,
		Value:     cmd.Value,
		Found:     true,
		UpdatedAt: updatedAt,
	}
	payload, err := json.Marshal(respBody)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal ui state response: %v", err)), nil
	}
	resp := d.successResponse(req)
	resp.Body = payload
	return resp, nil
}
