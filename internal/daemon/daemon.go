package daemon

import (
	"context"
	"encoding/json"
	"errors"
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
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/pr"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

const daemonVersion = "dev"

// Config configures daemon runtime wiring.
type Config struct {
	RepoDir              string
	SocketPath           string
	LockPath             string
	BaseBranch           string
	CLITool              string
	SessionShell         string
	SessionInitCommands  []string
	WorktreeInitCommands []string
	Logger               *slog.Logger
	IdleTimeout          time.Duration
}

// Daemon is the daemon runtime root.
type Daemon struct {
	cfg    Config
	lock   daemonLockManager
	hub    *publish.Hub
	serve  daemonServer
	router *daemonhandlers.Dispatcher
	apply  *daemonhandlers.ApplyHandler

	issues             *issues.Client
	tmux               *tmux.Client
	git                *git.Client
	worktree           *git.WorktreeManager
	gitHandler         *daemonhandlers.GitHandler
	worktreeHandler    *daemonhandlers.WorktreeHandler
	session            *daemonhandlers.SessionHandler
	sessionStore       *daemonstate.Store
	sessionLongRunning SessionLongRunningExecutor
	operationRuntime   *operationRuntime
	sessionStopMu      sync.Mutex
	sessionStopPending map[string]int

	revMu    sync.Mutex
	revision map[string]uint64

	shutdownMu       sync.Mutex
	shuttingDown     bool
	inFlightCommands sync.WaitGroup

	syncBootstrapState syncBootstrapState
	syncBootstrapFn    func(context.Context) error
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
	if normalizedRepoDir, err := appconfig.ResolveProjectRoot(cfg.RepoDir); err == nil {
		cfg.RepoDir = normalizedRepoDir
	}
	if cfg.BaseBranch == "" {
		cfg.BaseBranch = "main"
	}
	if cfg.CLITool == "" {
		cfg.CLITool = "claude"
	}
	if strings.TrimSpace(cfg.SessionShell) == "" {
		cfg.SessionShell = appconfig.DefaultSessionShell()
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = appconfig.GlobalDaemonSocketPath()
	}
	if cfg.LockPath == "" {
		cfg.LockPath = appconfig.GlobalDaemonLockPath()
	}

	tmuxRunner := &tmux.ExecRunner{}
	gitRunner := git.NewExecRunner(cfg.RepoDir)
	gitClient := git.NewClient(gitRunner, cfg.Logger)
	prWorkflow := pr.NewPRWorkflow(&pr.ExecRunner{}, cfg.Logger)
	devServerManager := devserver.NewManager(devserver.NewPortAllocator(3000), cfg.Logger)
	sessionStore := daemonstate.NewStore()
	issuesClient := issues.NewClient(cfg.RepoDir, cfg.Logger)
	sessionHandler := daemonhandlers.NewSessionHandler(sessionStore)
	prHandler := daemonhandlers.NewPRHandler(prWorkflow, gitClient)
	devServerHandler := daemonhandlers.NewDevServerHandler(devServerManager)
	specHandler := daemonhandlers.NewSpecHandler(issueSpecService{client: issuesClient})

	d := &Daemon{
		cfg:                cfg,
		lock:               lifecycle.NewLockManager(cfg.LockPath),
		hub:                publish.NewHub(512, 64, cfg.Logger),
		issues:             issuesClient,
		tmux:               tmux.NewClient(tmuxRunner, cfg.Logger),
		git:                gitClient,
		worktree:           git.NewWorktreeManager(gitRunner, cfg.RepoDir, cfg.Logger),
		session:            sessionHandler,
		sessionStore:       sessionStore,
		sessionStopPending: map[string]int{},
		revision:           map[string]uint64{},
	}
	d.syncBootstrapFn = d.defaultSyncBootstrap
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir:      cfg.RepoDir,
		logger:       cfg.Logger,
		hub:          d.hub,
		nextRevision: d.nextRevision,
		sessionStart: d.handleSessionStartDirect,
		sessionStop:  d.handleSessionStopDirect,
	})
	commandExecutor := operationCommandExecutor{runtime: runtime}
	sessionExecutor := sessionOperationExecutor{runtime: runtime}
	gitHandler := daemonhandlers.NewGitHandler(gitClient, daemonhandlers.WithGitLongRunningExecutor(commandExecutor))
	worktreeHandler := daemonhandlers.NewWorktreeHandler(
		worktreeServiceAdapter{manager: d.worktree},
		daemonhandlers.WithWorktreeLongRunningExecutor(commandExecutor),
	)
	runtime.gitHandler = gitHandler
	runtime.worktreeHandler = worktreeHandler
	d.gitHandler = gitHandler
	d.worktreeHandler = worktreeHandler
	d.operationRuntime = runtime
	d.sessionLongRunning = sessionExecutor
	d.router = daemonhandlers.NewDispatcher(
		sessionHandler,
		gitHandler,
		worktreeHandler,
		devServerHandler,
		prHandler,
		specHandler,
		runtime,
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
	startedAt := time.Now()
	lease, err := d.lock.Acquire()
	if err != nil {
		return err
	}
	d.cfg.Logger.Info("daemon startup phase", "phase", "lock_acquire", "duration_ms", time.Since(startedAt).Milliseconds())
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	shutdownDone := make(chan struct{})
	shutdownStop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			d.drainInFlightCommands()
			cancelServe()
			close(shutdownDone)
		case <-shutdownStop:
		}
	}()
	defer func() {
		close(shutdownStop)
		select {
		case <-shutdownDone:
		default:
			cancelServe()
		}
		if d.operationRuntime != nil {
			if closeErr := d.operationRuntime.Close(); closeErr != nil {
				d.cfg.Logger.Warn("failed to close operation runtime", "error", closeErr)
			}
		}
		if d.issues != nil {
			if closeErr := d.issues.CloseDB(); closeErr != nil {
				d.cfg.Logger.Warn("failed to close issue store", "error", closeErr)
			}
		}
		_ = lease.Release()
		_ = d.lock.Release()
	}()
	bootstrapStartedAt := time.Now()
	if err := d.bootstrapSyncOrchestrator(ctx); err != nil {
		d.cfg.Logger.Error("daemon startup phase failed", "phase", "sync_bootstrap", "duration_ms", time.Since(bootstrapStartedAt).Milliseconds(), "error", err)
		return err
	}
	d.cfg.Logger.Info("daemon startup phase", "phase", "sync_bootstrap", "duration_ms", time.Since(bootstrapStartedAt).Milliseconds())
	d.cfg.Logger.Info("daemon startup phase", "phase", "startup_ready", "duration_ms", time.Since(startedAt).Milliseconds())
	err = d.serve.Serve(serveCtx)
	if ctx.Err() != nil {
		<-shutdownDone
	}
	return err
}

func (d *Daemon) handshake(_ context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	return protocol.NegotiateHello(hello, daemonVersion), nil
}

func (d *Daemon) subscribe(_ context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, func(), error) {
	ch, cancel := d.hub.Subscribe(projectID, fromRevision)
	return ch, cancel, nil
}

func (d *Daemon) command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if resp, handled := d.guardSyncDependentCommand(req); handled {
		return resp, nil
	}
	if commandRequiresExplicitProjectID(req.Command) && strings.TrimSpace(req.Meta.ProjectID) == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required metadata: project_id"), nil
	}
	if err := d.beginCommand(); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, err.Error()), nil
	}
	defer d.endCommand()

	if strings.HasPrefix(req.Command, "git.") || strings.HasPrefix(req.Command, "pr.") || strings.HasPrefix(req.Command, "worktree.") || strings.HasPrefix(req.Command, "devserver.") || strings.HasPrefix(req.Command, "operation.") || strings.HasPrefix(req.Command, "spec.") {
		if d.router == nil {
			return d.errorResponse(req, protocol.ErrorCodeUnsupportedCommand, "unsupported command"), nil
		}
		return d.router.Handle(ctx, req), nil
	}
	switch req.Command {
	case protocol.CommandIssueFanout:
		return d.handleIssueFanout(ctx, req)
	case protocol.CommandIssueFanoutDrift:
		return d.handleIssueFanoutDrift(ctx, req)
	case protocol.CommandMailSend:
		return d.handleMailSend(ctx, req)
	case protocol.CommandMailList:
		return d.handleMailList(ctx, req)
	case protocol.CommandMailWatch:
		return d.handleMailWatch(ctx, req)
	case "task.list":
		return d.handleTaskList(ctx, req)
	case "task.create":
		return d.handleTaskCreate(ctx, req)
	case "task.update_status":
		return d.handleTaskUpdateStatus(ctx, req)
	case "task.update_details":
		return d.handleTaskUpdateDetails(ctx, req)
	case "task.append_notes":
		return d.handleTaskAppendNotes(ctx, req)
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
	case "session.recover":
		return d.handleSessionRecover(ctx, req)
	default:
		return d.errorResponse(req, protocol.ErrorCodeUnsupportedCommand, "unsupported command"), nil
	}
}

func commandRequiresExplicitProjectID(command string) bool {
	switch {
	case strings.HasPrefix(command, "git."),
		strings.HasPrefix(command, "pr."),
		strings.HasPrefix(command, "worktree."),
		strings.HasPrefix(command, "devserver."),
		strings.HasPrefix(command, "operation."),
		strings.HasPrefix(command, "task."),
		strings.HasPrefix(command, "session."):
		return true
	}

	switch command {
	case protocol.CommandIssueFanout,
		protocol.CommandIssueFanoutDrift,
		protocol.CommandMailSend,
		protocol.CommandMailList,
		protocol.CommandMailWatch:
		return true
	default:
		return false
	}
}

func (d *Daemon) beginCommand() error {
	d.shutdownMu.Lock()
	defer d.shutdownMu.Unlock()
	if d.shuttingDown {
		return errors.New("daemon shutting down")
	}
	d.inFlightCommands.Add(1)
	return nil
}

func (d *Daemon) endCommand() {
	d.inFlightCommands.Done()
}

func (d *Daemon) drainInFlightCommands() {
	d.shutdownMu.Lock()
	if d.shuttingDown {
		d.shutdownMu.Unlock()
		return
	}
	d.shuttingDown = true
	d.shutdownMu.Unlock()

	timeout := d.cfg.IdleTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if d.operationRuntime != nil {
			if err := d.operationRuntime.StopIntake(); err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Warn("daemon operation intake stop failed", "error", err)
			}
			if err := d.operationRuntime.CancelQueued(context.Background(), "daemon shutting down"); err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Warn("daemon queued operation cancellation failed", "error", err)
			}
		}

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.inFlightCommands.Wait()
		}()
		if d.operationRuntime != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				drainCtx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				if err := d.operationRuntime.Drain(drainCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) && d.cfg.Logger != nil {
					d.cfg.Logger.Warn("daemon operation drain failed", "error", err)
				}
			}()
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("daemon shutdown drain timed out", "timeout", timeout.String())
		}
	}
}

func (d *Daemon) applySessionLifecycleTransition(
	ctx context.Context,
	req protocol.RequestEnvelope,
	projectID string,
	sessionID string,
	issueID string,
	command string,
) error {
	if d.session == nil {
		return errors.New("session handler unavailable")
	}

	body, err := json.Marshal(struct {
		ProjectID string `json:"project_id"`
		SessionID string `json:"session_id"`
		IssueID   string `json:"issue_id"`
	}{
		ProjectID: projectID,
		SessionID: sessionID,
		IssueID:   issueID,
	})
	if err != nil {
		return err
	}

	sessionReq := req
	sessionReq.Command = command
	sessionReq.Body = body

	resp := d.session.Handle(ctx, sessionReq)
	if resp.OK {
		if d.sessionStore == nil {
			return errors.New("session store unavailable")
		}
		session, err := d.sessionStore.Session(projectID, sessionID)
		if err != nil {
			return err
		}
		d.publishSessionProjectionEvent(projectID, req.Meta, session)
		return nil
	}
	if resp.Error != nil {
		return errors.New(resp.Error.Message)
	}
	return errors.New("session lifecycle transition failed")
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
	if d.revision == nil {
		d.revision = map[string]uint64{}
	}
	d.revision[projectID]++
	return d.revision[projectID]
}

func (d *Daemon) currentRevision(projectID string) uint64 {
	d.revMu.Lock()
	defer d.revMu.Unlock()
	if d.revision == nil {
		return 0
	}
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

func (d *Daemon) publishSessionProjectionEvent(projectID string, meta protocol.Metadata, session daemonstate.Session) uint64 {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	rev := d.nextRevision(projectID)
	if d.hub == nil {
		return rev
	}
	body, err := json.Marshal(protocol.SessionProjectionEventBody{
		ProjectID: projectID,
		Revision:  rev,
		Session: protocol.SessionProjection{
			SessionID: session.ID,
			IssueID:   session.IssueID,
			State:     protocol.SessionLifecycleState(session.State),
			UpdatedAt: session.UpdatedAt,
		},
	})
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("marshal session projection event body failed", "project_id", projectID, "session_id", session.ID, "error", err)
		}
		return rev
	}
	if meta.ProjectID == "" {
		meta.ProjectID = projectID
	}
	d.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       projectID,
		Meta:            meta,
		Revision:        rev,
		Event:           protocol.EventSessionUpdated,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Body:            body,
	})
	return rev
}

func (d *Daemon) commandOutput(req protocol.RequestEnvelope, output string) protocol.ResponseEnvelope {
	resp := d.successResponse(req)
	payload, _ := json.Marshal(struct {
		Output string `json:"output"`
	}{Output: output})
	resp.Body = payload
	return resp
}

func (d *Daemon) SetSessionLongRunningExecutor(executor SessionLongRunningExecutor) {
	d.sessionLongRunning = executor
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
