package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/beads"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type localDaemonTransport struct {
	projectID string
	cfg       *config.Config
	beads     *beads.Client
	tmux      *tmux.Client
	worktree  *git.WorktreeManager
	logger    *slog.Logger
}

func newLocalDaemonTransport(cfg *config.Config, repoDir, projectID string, logger *slog.Logger) (daemonclient.TransportClient, error) {
	beadsRunner := &beads.ExecRunner{}
	beadsClient := beads.NewClient(beadsRunner, logger)

	tmuxRunner := &tmux.ExecRunner{}
	tmuxClient := tmux.NewClient(tmuxRunner, logger)

	gitRunner := git.NewExecRunner(repoDir)
	worktreeManager := git.NewWorktreeManager(gitRunner, repoDir, logger)

	return &localDaemonTransport{
		projectID: projectID,
		cfg:       cfg,
		beads:     beadsClient,
		tmux:      tmuxClient,
		worktree:  worktreeManager,
		logger:    logger,
	}, nil
}

func (t *localDaemonTransport) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	_ = ctx
	_ = hello
	return protocol.HelloAck{Accepted: true}, nil
}

func (t *localDaemonTransport) Subscribe(ctx context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, error) {
	_ = ctx
	_ = projectID
	_ = fromRevision
	return nil, fmt.Errorf("subscribe not supported by local CLI transport")
}

func (t *localDaemonTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	switch req.Command {
	case commandSessionStart:
		return t.handleSessionStart(ctx, req)
	case commandSessionAttach:
		return t.handleSessionAttach(ctx, req)
	case commandSessionStop:
		return t.handleSessionStop(ctx, req)
	case commandSessionStatus:
		return t.handleSessionStatus(ctx, req)
	default:
		return protocol.ResponseEnvelope{
			ProtocolVersion: req.ProtocolVersion,
			RequestID:       req.RequestID,
			Kind:            protocol.EnvelopeKindResponse,
			Meta:            req.Meta,
			CompletedAt:     time.Now().UTC(),
			Error: &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeUnsupportedCommand,
				Message:   "unsupported command",
				Retryable: false,
			},
		}, nil
	}
}

func (t *localDaemonTransport) handleSessionStart(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, resp := decodeSessionRequest(req)
	if resp.Error != nil {
		return resp, nil
	}

	exists, err := t.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return transportFailure(req, err), nil
	}
	if exists {
		return protocolError(req, protocol.ErrorCodeConflict, fmt.Sprintf("session already exists: %s (use 'az attach %s' to connect)", cmd.SessionID, cmd.SessionID)), nil
	}

	tasks, err := t.beads.Search(ctx, cmd.SessionID)
	if err != nil {
		return transportFailure(req, err), nil
	}
	if len(tasks) == 0 {
		return protocolError(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("bead not found: %s", cmd.SessionID)), nil
	}
	task := tasks[0]

	baseBranch := cmd.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	worktree, err := t.worktree.Create(ctx, cmd.SessionID, baseBranch)
	if err != nil {
		return transportFailure(req, err), nil
	}

	if err := t.tmux.NewSession(ctx, cmd.SessionID, worktree.Path); err != nil {
		return transportFailure(req, err), nil
	}

	if err := t.tmux.SendKeys(ctx, cmd.SessionID, "claude"); err != nil {
		return transportFailure(req, err), nil
	}

	if err := t.beads.Update(ctx, cmd.SessionID, domain.StatusInProgress); err != nil {
		t.logger.Warn("failed to update bead status", "error", err)
	}

	output := strings.Join([]string{
		fmt.Sprintf("Starting session for: %s - %s", task.ID, task.Title),
		fmt.Sprintf("Creating worktree from branch: %s", baseBranch),
		fmt.Sprintf("Worktree created: %s", worktree.Path),
		fmt.Sprintf("Creating tmux session: %s", cmd.SessionID),
		"",
		"✓ Session started successfully",
		fmt.Sprintf("  To attach: az attach %s", cmd.SessionID),
		fmt.Sprintf("  Or run:    tmux attach-session -t %s", cmd.SessionID),
		"",
	}, "\n")

	return commandOK(req, commandOutputBody{Output: output}), nil
}

func (t *localDaemonTransport) handleSessionAttach(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, resp := decodeSessionRequest(req)
	if resp.Error != nil {
		return resp, nil
	}

	exists, err := t.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return transportFailure(req, err), nil
	}
	if !exists {
		return protocolError(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found: %s (use 'az start %s' to create)", cmd.SessionID, cmd.SessionID)), nil
	}

	if err := t.tmux.AttachSession(ctx, cmd.SessionID); err != nil {
		return transportFailure(req, err), nil
	}

	output := strings.Join([]string{
		fmt.Sprintf("Attaching to session: %s", cmd.SessionID),
		"(Press Ctrl+B then D to detach)",
		"",
	}, "\n")

	return commandOK(req, commandOutputBody{Output: output}), nil
}

func (t *localDaemonTransport) handleSessionStop(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, resp := decodeSessionRequest(req)
	if resp.Error != nil {
		return resp, nil
	}

	exists, err := t.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return transportFailure(req, err), nil
	}
	if !exists {
		return protocolError(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found: %s", cmd.SessionID)), nil
	}

	if err := t.tmux.KillSession(ctx, cmd.SessionID); err != nil {
		return transportFailure(req, err), nil
	}

	output := strings.Join([]string{
		fmt.Sprintf("Killing session: %s", cmd.SessionID),
		fmt.Sprintf("✓ Session killed: %s", cmd.SessionID),
		"  Note: Worktree is preserved. Use 'git worktree remove' to clean up.",
		"",
	}, "\n")

	return commandOK(req, commandOutputBody{Output: output}), nil
}

func (t *localDaemonTransport) handleSessionStatus(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, resp := decodeSessionRequest(req)
	if resp.Error != nil {
		return resp, nil
	}

	tmuxSessions, err := t.tmux.ListSessions(ctx)
	if err != nil {
		return transportFailure(req, err), nil
	}

	tasks, err := t.beads.List(ctx)
	if err != nil {
		return transportFailure(req, err), nil
	}

	taskMap := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		taskMap[task.ID] = task
	}

	if cmd.SessionID != "" {
		found := false
		for _, sessionName := range tmuxSessions {
			if sessionName == cmd.SessionID {
				found = true
				break
			}
		}
		if !found {
			return protocolError(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("no active session found for bead: %s", cmd.SessionID)), nil
		}
		tmuxSessions = []string{cmd.SessionID}
	}

	if len(tmuxSessions) == 0 {
		return commandOK(req, commandOutputBody{Output: "No active sessions\n"}), nil
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Active Sessions (%d):\n\n", len(tmuxSessions))

	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BEAD ID\tSTATUS\tTITLE")
	fmt.Fprintln(w, "-------\t------\t-----")

	for _, sessionName := range tmuxSessions {
		task, ok := taskMap[sessionName]
		status := "unknown"
		title := "(not in beads)"

		if ok {
			status = string(task.Status)
			title = task.Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
		}

		fmt.Fprintf(w, "%s\t%s\t%s\n", sessionName, status, title)
	}

	_ = w.Flush()
	buf.WriteString("\nUse 'az attach <bead-id>' to attach to a session\n")

	return commandOK(req, commandOutputBody{Output: buf.String()}), nil
}

type sessionCommandBody struct {
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	BaseBranch string `json:"base_branch,omitempty"`
}

func decodeSessionRequest(req protocol.RequestEnvelope) (sessionCommandBody, protocol.ResponseEnvelope) {
	var cmd sessionCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return sessionCommandBody{}, protocolError(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}

	if cmd.ProjectID == "" {
		cmd.ProjectID = req.Meta.ProjectID
	}
	if cmd.ProjectID == "" {
		cmd.ProjectID = "default"
	}

	if cmd.SessionID == "" {
		return sessionCommandBody{}, protocolError(req, protocol.ErrorCodeInvalidRequest, "missing required fields: project_id/session_id")
	}

	return cmd, protocol.ResponseEnvelope{}
}

func commandOK(req protocol.RequestEnvelope, body any) protocol.ResponseEnvelope {
	payload, err := json.Marshal(body)
	if err != nil {
		return protocolError(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal response body: %v", err))
	}

	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
		OK:              true,
		Body:            payload,
	}
}

func protocolError(req protocol.RequestEnvelope, code protocol.ErrorCode, message string) protocol.ResponseEnvelope {
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
		Error: &protocol.ErrorEnvelope{
			Code:      code,
			Message:   message,
			Retryable: false,
		},
	}
}

func transportFailure(req protocol.RequestEnvelope, err error) protocol.ResponseEnvelope {
	return protocolError(req, protocol.ErrorCodeInternal, err.Error())
}
