package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func (d *Daemon) handleUIIssueCommand(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
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
		command = defaultUICommandForTransport(req.Command)
	}
	if !supportedUIIssueCommand(command) || command != defaultUICommandForTransport(req.Command) {
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

func defaultUICommandForTransport(command string) string {
	switch command {
	case protocol.CommandUIOpenTaskDrillDown:
		return protocol.UICommandOpenTaskDrillDown
	default:
		return protocol.UICommandOpenTaskWorkspace
	}
}

func supportedUIIssueCommand(command string) bool {
	switch command {
	case protocol.UICommandOpenTaskWorkspace, protocol.UICommandOpenTaskDrillDown:
		return true
	default:
		return false
	}
}

func (d *Daemon) handleUIStateGet(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
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
	value, found := d.getUIStateValue(projectID, key)
	respBody := protocol.UIStateResponseBody{
		ProjectID: naming.ProjectID(projectID),
		Key:       key,
		Found:     found,
	}
	if found {
		respBody.Value = value
		respBody.UpdatedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(respBody)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal ui state response: %v", err)), nil
	}
	resp := d.successResponse(req)
	resp.Body = payload
	return resp, nil
}

func (d *Daemon) handleUIStateSet(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
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
	d.uiStateMu.Lock()
	if d.uiState == nil {
		d.uiState = map[string]string{}
	}
	d.uiState[uiStateMapKey(projectID, key)] = cmd.Value
	d.uiStateMu.Unlock()
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

func (d *Daemon) getUIStateValue(projectID, key string) (string, bool) {
	projectID = d.canonicalProjectID(projectID)
	key = strings.TrimSpace(key)
	d.uiStateMu.RLock()
	defer d.uiStateMu.RUnlock()
	value, found := d.uiState[uiStateMapKey(projectID, key)]
	if found {
		return value, true
	}
	value, found = d.uiState[key]
	return value, found
}

func (d *Daemon) setUIStateValue(projectID, key, value string) {
	projectID = d.canonicalProjectID(projectID)
	key = strings.TrimSpace(key)
	d.uiStateMu.Lock()
	if d.uiState == nil {
		d.uiState = map[string]string{}
	}
	d.uiState[uiStateMapKey(projectID, key)] = value
	d.uiStateMu.Unlock()
}

func uiStateMapKey(projectID, key string) string {
	return strings.TrimSpace(projectID) + "\x00" + strings.TrimSpace(key)
}
