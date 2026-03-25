package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
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
	cfg    Config
	lock   *lifecycle.LockManager
	hub    *publish.Hub
	serve  *transport.Server
	router *daemonhandlers.Dispatcher
	apply  *daemonhandlers.ApplyHandler

	issues   *issues.Client
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
		cfg.SocketPath = appconfig.GlobalDaemonSocketPath()
	}
	if cfg.LockPath == "" {
		cfg.LockPath = appconfig.GlobalDaemonLockPath()
	}

	tmuxRunner := &tmux.ExecRunner{}
	gitRunner := git.NewExecRunner(cfg.RepoDir)
	devServerManager := devserver.NewManager(devserver.NewPortAllocator(3000), cfg.Logger)

	d := &Daemon{
		cfg:      cfg,
		lock:     lifecycle.NewLockManager(cfg.LockPath),
		hub:      publish.NewHub(512, 64, cfg.Logger),
		issues:   issues.NewClient(cfg.RepoDir, cfg.Logger),
		tmux:     tmux.NewClient(tmuxRunner, cfg.Logger),
		worktree: git.NewWorktreeManager(gitRunner, cfg.RepoDir, cfg.Logger),
		revision: map[string]uint64{},
	}
	d.router = daemonhandlers.NewDispatcher(
		nil,
		daemonhandlers.NewWorktreeHandler(worktreeServiceAdapter{manager: d.worktree}),
		daemonhandlers.NewDevServerHandler(devServerManager),
	)
	d.apply = daemonhandlers.NewApplyHandler(d.issues, applyRevisionAdapter{daemon: d})

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
	if strings.HasPrefix(req.Command, "worktree.") || strings.HasPrefix(req.Command, "devserver.") {
		return d.router.Handle(ctx, req), nil
	}
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
	case "task.dependency.add":
		return d.handleTaskDependencyAdd(ctx, req)
	case "task.dependency.remove":
		return d.handleTaskDependencyRemove(ctx, req)
	case "task.snapshot.export":
		return d.handleTaskSnapshotExport(ctx, req)
	case protocol.CommandTaskBulkApply:
		return d.apply.Handle(ctx, req), nil
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

func (d *Daemon) commandOutput(req protocol.RequestEnvelope, output string) protocol.ResponseEnvelope {
	resp := d.successResponse(req)
	payload, _ := json.Marshal(struct {
		Output string `json:"output"`
	}{Output: output})
	resp.Body = payload
	return resp
}

type applyRevisionAdapter struct {
	daemon *Daemon
}

func (a applyRevisionAdapter) CurrentRevision(projectID string) uint64 {
	return a.daemon.currentRevision(projectID)
}

func (a applyRevisionAdapter) NextRevision(projectID string) uint64 {
	return a.daemon.nextRevision(projectID)
}

func (a applyRevisionAdapter) PublishTaskEvent(req protocol.RequestEnvelope, eventName string, rev uint64) {
	a.daemon.publishTaskEvent(req, eventName, rev)
}
