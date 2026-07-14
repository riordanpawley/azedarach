package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func (d *Daemon) handleProjectionDeltaRead(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var body protocol.ProjectionDeltaReadRequest
	if err := decodeOptionalJSON(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid projection delta request: %v", err)), nil
	}
	projectID := d.projectID(req.Meta)
	if strings.TrimSpace(body.ProjectID.String()) != "" {
		projectID = d.canonicalProjectID(body.ProjectID.String())
	}
	client := d.issueClientForProject(projectID)
	if client == nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, "issue store unavailable"), nil
	}
	authority := NewProjectionDeltaAuthority(client)
	var batch protocol.ProjectionDeltaBatch
	var err error
	if req.Command == protocol.CommandProjectionDeltaWatch {
		batch, err = authority.Watch(ctx, "default", body.AfterCursor, body.Limit)
	} else {
		batch, err = authority.List(ctx, "default", body.AfterCursor, body.Limit)
	}
	if err != nil {
		return d.projectionDeltaErrorResponse(req, err), nil
	}
	remapProjectionDeltaBatch(&batch, projectID)
	return d.marshalBoardResponse(req, batch)
}

func (d *Daemon) handleProjectionSnapshot(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var body protocol.ProjectionSnapshotRequest
	if err := decodeOptionalJSON(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid projection snapshot request: %v", err)), nil
	}
	projectID := d.projectID(req.Meta)
	if strings.TrimSpace(body.ProjectID.String()) != "" {
		projectID = d.canonicalProjectID(body.ProjectID.String())
	}
	client := d.issueClientForProject(projectID)
	if client == nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, "issue store unavailable"), nil
	}
	snapshot, err := NewProjectionDeltaAuthority(client).Snapshot(ctx, "default", body.Cursor)
	if err != nil {
		return d.projectionDeltaErrorResponse(req, err), nil
	}
	snapshot.ProjectID = naming.ProjectID(projectID)
	return d.marshalBoardResponse(req, snapshot)
}

func (d *Daemon) projectionDeltaErrorResponse(req protocol.RequestEnvelope, err error) protocol.ResponseEnvelope {
	envelope := ProjectionDeltaErrorEnvelope(err)
	resp := d.errorResponse(req, envelope.Code, envelope.Message)
	resp.Error.Retryable = envelope.Retryable
	return resp
}

func remapProjectionDeltaBatch(batch *protocol.ProjectionDeltaBatch, projectID string) {
	batch.ProjectID = naming.ProjectID(projectID)
	for index := range batch.Deltas {
		batch.Deltas[index].ProjectID = naming.ProjectID(projectID)
	}
}
