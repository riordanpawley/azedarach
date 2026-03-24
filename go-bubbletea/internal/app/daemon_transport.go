package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/beads"
)

// localDaemonTransport adapts the legacy beads client to the shared daemon client boundary.
//
// The TUI model only talks to daemonclient.Client; this transport keeps the current
// local execution behavior intact until the daemon process owns these commands.
type localDaemonTransport struct {
	beads *beads.Client
}

func newLocalDaemonTransport(beadsClient *beads.Client) *localDaemonTransport {
	return &localDaemonTransport{beads: beadsClient}
}

func (t *localDaemonTransport) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	_ = ctx
	return protocol.NegotiateHello(hello, "local-app"), nil
}

func (t *localDaemonTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	resp := protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
	}

	switch req.Command {
	case daemonclient.CommandTaskList:
		tasks, err := t.beads.List(ctx)
		if err != nil {
			return resp, err
		}
		body, err := json.Marshal(tasks)
		if err != nil {
			return resp, fmt.Errorf("marshal task list response: %w", err)
		}
		resp.OK = true
		resp.Body = body
		return resp, nil

	case daemonclient.CommandTaskCreate:
		var cmd daemonclient.TaskCreateParams
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return resp, fmt.Errorf("decode task create request: %w", err)
		}
		taskID, err := t.beads.Create(ctx, beads.CreateTaskParams{
			Title:       cmd.Title,
			Description: cmd.Description,
			Type:        cmd.Type,
			Priority:    cmd.Priority,
			ParentID:    cmd.ParentID,
		})
		if err != nil {
			return resp, err
		}
		body, err := json.Marshal(daemonclient.TaskIDResponse{TaskID: taskID})
		if err != nil {
			return resp, fmt.Errorf("marshal task create response: %w", err)
		}
		resp.OK = true
		resp.Body = body
		return resp, nil

	case daemonclient.CommandTaskUpdateStatus:
		var cmd daemonclient.TaskStatusRequest
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return resp, fmt.Errorf("decode task status request: %w", err)
		}
		if err := t.beads.Update(ctx, cmd.TaskID, cmd.Status); err != nil {
			return resp, err
		}
		resp.OK = true
		return resp, nil

	case daemonclient.CommandTaskUpdate:
		var cmd struct {
			TaskID string `json:"task_id"`
			daemonclient.TaskUpdateParams
		}
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return resp, fmt.Errorf("decode task update request: %w", err)
		}
		if err := t.beads.UpdateDetails(ctx, cmd.TaskID, beads.UpdateTaskParams{
			Title:       cmd.Title,
			Description: cmd.Description,
			Type:        cmd.Type,
			Priority:    cmd.Priority,
		}); err != nil {
			return resp, err
		}
		resp.OK = true
		return resp, nil

	case daemonclient.CommandTaskDelete:
		var cmd daemonclient.TaskIDRequest
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return resp, fmt.Errorf("decode task delete request: %w", err)
		}
		if err := t.beads.Delete(ctx, cmd.TaskID); err != nil {
			return resp, err
		}
		resp.OK = true
		return resp, nil

	case daemonclient.CommandTaskArchive:
		var cmd daemonclient.TaskIDRequest
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return resp, fmt.Errorf("decode task archive request: %w", err)
		}
		if err := t.beads.Archive(ctx, cmd.TaskID); err != nil {
			return resp, err
		}
		resp.OK = true
		return resp, nil

	default:
		return resp, fmt.Errorf("unsupported daemon command: %s", req.Command)
	}
}

func (t *localDaemonTransport) Subscribe(ctx context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, error) {
	_ = ctx
	_ = projectID
	_ = fromRevision
	return make(chan protocol.EventEnvelope), nil
}
