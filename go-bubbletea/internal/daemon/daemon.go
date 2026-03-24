package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/services/beads"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

const daemonVersion = "dev"

// Config configures daemon runtime wiring.
type Config struct {
	RepoDir     string
	SocketPath  string
	LockPath    string
	BaseBranch  string
	CLITool     string
	Logger      *slog.Logger
	IdleTimeout time.Duration
}

// Daemon is the daemon runtime root.
type Daemon struct {
	cfg   Config
	lock  *lifecycle.LockManager
	hub   *publish.Hub
	serve *transport.Server

	beads    *beads.Client
	tmux     *tmux.Client
	worktree *git.WorktreeManager

	revMu    sync.Mutex
	revision map[string]uint64
}

// New constructs a runnable daemon runtime.
func New(cfg Config) *Daemon {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.RepoDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			cfg.RepoDir = cwd
		} else {
			cfg.RepoDir = "."
		}
	}
	if cfg.BaseBranch == "" {
		cfg.BaseBranch = "main"
	}
	if cfg.CLITool == "" {
		cfg.CLITool = "claude"
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = filepath.Join(cfg.RepoDir, ".beads", "azd.sock")
	}
	if cfg.LockPath == "" {
		cfg.LockPath = filepath.Join(cfg.RepoDir, ".beads", "daemon.lock")
	}

	beadsRunner := &beads.ExecRunner{}
	tmuxRunner := &tmux.ExecRunner{}
	gitRunner := git.NewExecRunner(cfg.RepoDir)

	d := &Daemon{
		cfg:      cfg,
		lock:     lifecycle.NewLockManager(cfg.LockPath),
		hub:      publish.NewHub(512, 64, cfg.Logger),
		beads:    beads.NewClient(beadsRunner, cfg.Logger),
		tmux:     tmux.NewClient(tmuxRunner, cfg.Logger),
		worktree: git.NewWorktreeManager(gitRunner, cfg.RepoDir, cfg.Logger),
		revision: map[string]uint64{},
	}

	d.serve = transport.NewServer(cfg.SocketPath, transport.Handlers{
		Handshake: d.handshake,
		Command:   d.command,
		Subscribe: d.subscribe,
	})
	return d
}

// Run acquires singleton lock and serves daemon IPC until context cancellation.
func (d *Daemon) Run(ctx context.Context) error {
	lease, err := d.lock.Acquire()
	if err != nil {
		return err
	}
	defer func() {
		_ = lease.Release()
		_ = d.lock.Release()
	}()
	return d.serve.Serve(ctx)
}

func (d *Daemon) handshake(_ context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	return protocol.NegotiateHello(hello, daemonVersion), nil
}

func (d *Daemon) subscribe(_ context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, func(), error) {
	ch, cancel := d.hub.Subscribe(projectID, fromRevision)
	return ch, cancel, nil
}

func (d *Daemon) command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	switch req.Command {
	case "task.list":
		return d.handleTaskList(ctx, req)
	case "task.create":
		return d.handleTaskCreate(ctx, req)
	case "task.update_status":
		return d.handleTaskUpdateStatus(ctx, req)
	case "task.update_details":
		return d.handleTaskUpdateDetails(ctx, req)
	case "task.delete":
		return d.handleTaskDelete(ctx, req)
	case "task.archive":
		return d.handleTaskArchive(ctx, req)
	case "session.start":
		return d.handleSessionStart(ctx, req)
	case "session.attach":
		return d.handleSessionAttach(ctx, req)
	case "session.stop":
		return d.handleSessionStop(ctx, req)
	case "session.status":
		return d.handleSessionStatus(ctx, req)
	default:
		return d.errorResponse(req, protocol.ErrorCodeUnsupportedCommand, "unsupported command"), nil
	}
}

func (d *Daemon) successResponse(req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
		OK:              true,
	}
}

func (d *Daemon) errorResponse(req protocol.RequestEnvelope, code protocol.ErrorCode, message string) protocol.ResponseEnvelope {
	resp := d.successResponse(req)
	resp.OK = false
	resp.Error = &protocol.ErrorEnvelope{
		Code:      code,
		Message:   message,
		Retryable: code.Retryable(),
	}
	return resp
}

func (d *Daemon) projectID(meta protocol.Metadata) string {
	if meta.ProjectID != "" {
		return meta.ProjectID
	}
	return "default"
}

func (d *Daemon) nextRevision(projectID string) uint64 {
	d.revMu.Lock()
	defer d.revMu.Unlock()
	d.revision[projectID]++
	return d.revision[projectID]
}

func (d *Daemon) currentRevision(projectID string) uint64 {
	d.revMu.Lock()
	defer d.revMu.Unlock()
	return d.revision[projectID]
}

func (d *Daemon) publishTaskEvent(req protocol.RequestEnvelope, eventName string, rev uint64) {
	projectID := d.projectID(req.Meta)
	d.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		ProjectID:       projectID,
		Meta:            req.Meta,
		Revision:        rev,
		Event:           eventName,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
	})
}

func (d *Daemon) handleTaskList(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	resp := d.successResponse(req)
	tasks, err := d.beads.List(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(tasks)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp.Body = body
	resp.Revision = d.currentRevision(d.projectID(req.Meta))
	return resp, nil
}

func (d *Daemon) handleTaskCreate(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	resp := d.successResponse(req)
	var cmd struct {
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Type        domain.TaskType `json:"type"`
		Priority    domain.Priority `json:"priority"`
		ParentID    *string         `json:"parent_id,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	taskID, err := d.beads.Create(ctx, beads.CreateTaskParams{
		Title:       cmd.Title,
		Description: cmd.Description,
		Type:        cmd.Type,
		Priority:    cmd.Priority,
		ParentID:    cmd.ParentID,
	})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, _ := json.Marshal(struct {
		TaskID string `json:"task_id"`
	}{TaskID: taskID})
	resp.Body = body
	resp.Revision = d.nextRevision(d.projectID(req.Meta))
	d.publishTaskEvent(req, "task.created", resp.Revision)
	return resp, nil
}

func (d *Daemon) handleTaskUpdateStatus(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd struct {
		TaskID string        `json:"task_id"`
		Status domain.Status `json:"status"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if err := d.beads.Update(ctx, cmd.TaskID, cmd.Status); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(d.projectID(req.Meta))
	d.publishTaskEvent(req, "task.updated", resp.Revision)
	return resp, nil
}

func (d *Daemon) handleTaskUpdateDetails(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd struct {
		TaskID      string          `json:"task_id"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Type        domain.TaskType `json:"type"`
		Priority    domain.Priority `json:"priority"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if err := d.beads.UpdateDetails(ctx, cmd.TaskID, beads.UpdateTaskParams{
		Title:       cmd.Title,
		Description: cmd.Description,
		Type:        cmd.Type,
		Priority:    cmd.Priority,
	}); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(d.projectID(req.Meta))
	d.publishTaskEvent(req, "task.updated", resp.Revision)
	return resp, nil
}

func (d *Daemon) handleTaskDelete(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if err := d.beads.Delete(ctx, cmd.TaskID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(d.projectID(req.Meta))
	d.publishTaskEvent(req, "task.deleted", resp.Revision)
	return resp, nil
}

func (d *Daemon) handleTaskArchive(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if err := d.beads.Archive(ctx, cmd.TaskID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(d.projectID(req.Meta))
	d.publishTaskEvent(req, "task.archived", resp.Revision)
	return resp, nil
}

type sessionCommandBody struct {
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	BaseBranch string `json:"base_branch,omitempty"`
}

func (d *Daemon) decodeSessionRequest(req protocol.RequestEnvelope) (sessionCommandBody, protocol.ResponseEnvelope, bool) {
	var cmd sessionCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return sessionCommandBody{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), false
	}
	if cmd.ProjectID == "" {
		cmd.ProjectID = req.Meta.ProjectID
	}
	if cmd.ProjectID == "" {
		cmd.ProjectID = "default"
	}
	if cmd.SessionID == "" {
		return sessionCommandBody{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: project_id/session_id"), false
	}
	return cmd, protocol.ResponseEnvelope{}, true
}

func (d *Daemon) handleSessionStart(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	exists, err := d.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if exists {
		return d.errorResponse(req, protocol.ErrorCodeConflict, fmt.Sprintf("session already exists: %s (use 'az attach %s' to connect)", cmd.SessionID, cmd.SessionID)), nil
	}
	tasks, err := d.beads.Search(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if len(tasks) == 0 {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("bead not found: %s", cmd.SessionID)), nil
	}
	baseBranch := cmd.BaseBranch
	if baseBranch == "" {
		baseBranch = d.cfg.BaseBranch
	}
	worktree, err := d.worktree.Create(ctx, cmd.SessionID, baseBranch)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if err := d.tmux.NewSession(ctx, cmd.SessionID, worktree.Path); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if err := d.tmux.SendKeys(ctx, cmd.SessionID, d.cfg.CLITool); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	_ = d.beads.Update(ctx, cmd.SessionID, domain.StatusInProgress)

	output := strings.Join([]string{
		fmt.Sprintf("Starting session for: %s - %s", tasks[0].ID, tasks[0].Title),
		fmt.Sprintf("Creating worktree from branch: %s", baseBranch),
		fmt.Sprintf("Worktree created: %s", worktree.Path),
		fmt.Sprintf("Creating tmux session: %s", cmd.SessionID),
		"",
		"✓ Session started successfully",
		fmt.Sprintf("  To attach: az attach %s", cmd.SessionID),
		fmt.Sprintf("  Or run:    tmux attach-session -t %s", cmd.SessionID),
		"",
	}, "\n")
	return d.commandOutput(req, output), nil
}

func (d *Daemon) handleSessionAttach(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	exists, err := d.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if !exists {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found: %s (use 'az start %s' to create)", cmd.SessionID, cmd.SessionID)), nil
	}
	output := strings.Join([]string{
		fmt.Sprintf("Attaching to session: %s", cmd.SessionID),
		"(Press Ctrl+B then D to detach)",
		"",
	}, "\n")
	return d.commandOutput(req, output), nil
}

func (d *Daemon) handleSessionStop(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	exists, err := d.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if !exists {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found: %s", cmd.SessionID)), nil
	}
	if err := d.tmux.KillSession(ctx, cmd.SessionID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	output := strings.Join([]string{
		fmt.Sprintf("Killing session: %s", cmd.SessionID),
		fmt.Sprintf("✓ Session killed: %s", cmd.SessionID),
		"  Note: Worktree is preserved. Use 'git worktree remove' to clean up.",
		"",
	}, "\n")
	return d.commandOutput(req, output), nil
}

func (d *Daemon) handleSessionStatus(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	tmuxSessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	tasks, err := d.beads.List(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	taskMap := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		taskMap[task.ID] = task
	}
	if cmd.SessionID != "" {
		found := false
		for _, name := range tmuxSessions {
			if name == cmd.SessionID {
				found = true
				break
			}
		}
		if !found {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("no active session found for bead: %s", cmd.SessionID)), nil
		}
		tmuxSessions = []string{cmd.SessionID}
	}
	if len(tmuxSessions) == 0 {
		return d.commandOutput(req, "No active sessions\n"), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Active Sessions (%d):\n\n", len(tmuxSessions))
	b.WriteString("BEAD ID\tSTATUS\tTITLE\n")
	b.WriteString("-------\t------\t-----\n")
	for _, name := range tmuxSessions {
		task, ok := taskMap[name]
		status := "unknown"
		title := "(not in beads)"
		if ok {
			status = string(task.Status)
			title = task.Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", name, status, title)
	}
	b.WriteString("\nUse 'az attach <bead-id>' to attach to a session\n")
	return d.commandOutput(req, b.String()), nil
}

func (d *Daemon) commandOutput(req protocol.RequestEnvelope, output string) protocol.ResponseEnvelope {
	resp := d.successResponse(req)
	payload, _ := json.Marshal(struct {
		Output string `json:"output"`
	}{Output: output})
	resp.Body = payload
	return resp
}
