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
