package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	maxDaemonHookLogEvents = 400
)

func (d *Daemon) handleHookLogAppend(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd protocol.HookLogAppendCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	projectID := d.projectID(req.Meta)
	evt := cmd.Event
	evt.ProjectID = projectID
	evt.Hook = strings.TrimSpace(evt.Hook)
	evt.Worktree = strings.TrimSpace(evt.Worktree)
	evt.Source = strings.TrimSpace(evt.Source)
	evt.Level = strings.TrimSpace(evt.Level)
	evt.Message = strings.TrimSpace(evt.Message)
	if evt.Message == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: event.message"), nil
	}
	if evt.CreatedAt.IsZero() {
		evt.CreatedAt = time.Now().UTC()
	} else {
		evt.CreatedAt = evt.CreatedAt.UTC()
	}

	d.appendHookLogEvent(projectID, evt)

	body, err := json.Marshal(evt)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal hook log event: %v", err)), nil
	}
	resp := d.successResponse(req)
	resp.Body = body

	rev := d.nextRevision(projectID)
	resp.Revision = rev
	d.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       projectID,
		Meta:            protocol.Metadata{ProjectID: projectID},
		Revision:        rev,
		Event:           protocol.EventHookLogAppended,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Body:            body,
	})
	return resp, nil
}

func (d *Daemon) handleHookLogList(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd protocol.HookLogListCommandBody
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
		}
	}
	projectID := d.projectID(req.Meta)
	events := d.listHookLogEvents(projectID, cmd.Limit)

	body, err := json.Marshal(events)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal hook log list: %v", err)), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	return resp, nil
}

func (d *Daemon) appendHookLogEvent(projectID string, evt protocol.HookLogEvent) {
	projectID = protocol.NormalizeProjectID(projectID)
	d.hookLogMu.Lock()
	defer d.hookLogMu.Unlock()
	if d.hookLogByProject == nil {
		d.hookLogByProject = map[string][]protocol.HookLogEvent{}
	}
	events := append(d.hookLogByProject[projectID], evt)
	if len(events) > maxDaemonHookLogEvents {
		events = append([]protocol.HookLogEvent(nil), events[len(events)-maxDaemonHookLogEvents:]...)
	}
	d.hookLogByProject[projectID] = events
}

func (d *Daemon) listHookLogEvents(projectID string, limit int) []protocol.HookLogEvent {
	projectID = protocol.NormalizeProjectID(projectID)
	d.hookLogMu.Lock()
	defer d.hookLogMu.Unlock()
	events := append([]protocol.HookLogEvent(nil), d.hookLogByProject[projectID]...)
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events
}
