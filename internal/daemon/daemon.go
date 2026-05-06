package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/buildinfo"
	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/pr"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

const (
	defaultRuntimeReconcileQueueWorkers = 2
	defaultGitStatusRefreshQueueWorkers = 4
	defaultRuntimeReconcileBudget       = 8
	defaultWorktreeGitProbeBudget       = 16
	defaultGitStatusRefreshBudget       = 32

	defaultRuntimeReconcileUnchangedBackoff = 2 * time.Minute
	defaultRuntimeReconcileFailureBackoff   = 1 * time.Minute
	maxRuntimeReconcileUnchangedBackoff     = 10 * time.Minute
	maxRuntimeReconcileFailureBackoff       = 15 * time.Minute

	defaultWorktreeGitProbeUnchangedBackoff = 2 * time.Minute
	defaultWorktreeGitProbeFailureBackoff   = 1 * time.Minute
	maxWorktreeGitProbeUnchangedBackoff     = 10 * time.Minute
	maxWorktreeGitProbeFailureBackoff       = 15 * time.Minute

	defaultGitStatusRefreshCadence          = 5 * time.Second
	defaultGitStatusRefreshUnchangedBackoff = 15 * time.Second
	defaultGitStatusRefreshFailureBackoff   = 30 * time.Second
	maxGitStatusRefreshUnchangedBackoff     = 2 * time.Minute
	maxGitStatusRefreshFailureBackoff       = 5 * time.Minute
)

// Config configures daemon runtime wiring.
type Config struct {
	RepoDir                  string
	SocketPath               string
	LockPath                 string
	BaseBranch               string
	CLITool                  string
	SessionShell             string
	SessionInitCommands      []string
	WorktreeInitCommands     []string
	Logger                   *slog.Logger
	IdleTimeout              time.Duration
	RuntimeReconcileInterval time.Duration
	RuntimeReconcileTimeout  time.Duration
}

// Daemon is the daemon runtime root.
type Daemon struct {
	cfg    Config
	lock   daemonLockManager
	hub    *publish.Hub
	serve  daemonServer
	router *daemonhandlers.Dispatcher
	apply  *daemonhandlers.ApplyHandler

	issues                        *issues.Client
	issueClientsMu                sync.Mutex
	issueClientsByProject         map[string]*issues.Client
	issueClientsByRoot            map[string]*issues.Client
	projectConfigMu               sync.Mutex
	baseBranchByProject           map[string]string
	baseBranchByRoot              map[string]string
	cliToolByProject              map[string]string
	cliToolByRoot                 map[string]string
	sessionShellByProject         map[string]string
	sessionShellByRoot            map[string]string
	sessionInitCommandsByProject  map[string][]string
	sessionInitCommandsByRoot     map[string][]string
	worktreeInitCommandsByProject map[string][]string
	worktreeInitCommandsByRoot    map[string][]string
	worktreeManagersMu            sync.Mutex
	worktreeManagersByProject     map[string]*git.WorktreeManager
	worktreeManagersByRoot        map[string]*git.WorktreeManager
	runtimeStoresMu               sync.Mutex
	runtimeStoresByProject        map[string]*daemonstate.RuntimeStateStore
	runtimeStoresByRoot           map[string]*daemonstate.RuntimeStateStore
	hookLogMu                     sync.Mutex
	hookLogByProject              map[string][]protocol.HookLogEvent
	tmux                          *tmux.Client
	git                           *git.Client
	gitStatusAdapter              *gitServiceAdapter
	gitHandler                    *daemonhandlers.GitHandler
	worktreeHandler               *daemonhandlers.WorktreeHandler
	worktreeAdapter               *worktreeServiceAdapter
	session                       *daemonhandlers.SessionHandler
	sessionStore                  *daemonstate.Store
	runtimeProjectionWriter       runtimeProjectionWriter
	sessionLongRunning            SessionLongRunningExecutor
	runtimeReconciler             runtimeReconciler
	runtimeReconcileQueue         *reconcileQueue[protocol.RuntimeReconcileResponseBody]
	gitStatusRefreshQueue         *reconcileQueue[*git.GitStatus]
	runtimeReconcileThrottle      *reconcileThrottle
	worktreeGitProbeThrottle      *reconcileThrottle
	queueMu                       sync.Mutex
	operationRuntime              *operationRuntime
	sessionStopMu                 sync.Mutex
	sessionStopPending            map[string]int
	sessionStateRefreshMu         sync.Mutex
	sessionStateRefreshing        map[string]bool
	sessionStateLastRefresh       map[string]time.Time
	worktreeStateRefreshMu        sync.Mutex
	worktreeStateRefreshing       map[string]bool
	worktreeStateLastRefresh      map[string]time.Time

	revMu    sync.Mutex
	revision map[string]uint64

	shutdownMu       sync.Mutex
	shuttingDown     bool
	shutdownReqCh    chan struct{}
	shutdownReqOnce  sync.Once
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
	runtimeStateStore := daemonstate.NewRuntimeStateStore(cfg.RepoDir, cfg.Logger)
	runtimeReconcileQueue := newReconcileQueue[protocol.RuntimeReconcileResponseBody](reconcileQueueConfig{
		Name:    "runtime_reconcile",
		Workers: defaultRuntimeReconcileQueueWorkers,
		Logger:  cfg.Logger,
	})
	gitStatusRefreshQueue := newReconcileQueue[*git.GitStatus](reconcileQueueConfig{
		Name:    "git_status_refresh",
		Workers: defaultGitStatusRefreshQueueWorkers,
		Logger:  cfg.Logger,
	})
	gitService := &gitServiceAdapter{
		client:             gitClient,
		runtimeStateStore:  runtimeStateStore,
		statusRefreshQueue: gitStatusRefreshQueue,
		logger:             cfg.Logger,
		baseBranch:         cfg.BaseBranch,
	}
	prWorkflow := pr.NewPRWorkflow(&pr.ExecRunner{}, cfg.Logger)
	devServerManager := devserver.NewManager(devserver.NewPortAllocator(3000), cfg.Logger)
	sessionStore := daemonstate.NewStore()
	issuesClient := issues.NewClient(cfg.RepoDir, cfg.Logger)
	sessionHandler := daemonhandlers.NewSessionHandler(sessionStore)
	prHandler := daemonhandlers.NewPRHandler(prWorkflow, gitService)
	devServerHandler := daemonhandlers.NewDevServerHandler(devServerManager)
	specService := issueSpecService{daemon: nil}

	d := &Daemon{
		cfg:                           cfg,
		lock:                          lifecycle.NewLockManager(cfg.LockPath),
		hub:                           publish.NewHub(512, 64, cfg.Logger),
		issues:                        issuesClient,
		issueClientsByProject:         map[string]*issues.Client{},
		issueClientsByRoot:            map[string]*issues.Client{},
		baseBranchByProject:           map[string]string{},
		baseBranchByRoot:              map[string]string{},
		cliToolByProject:              map[string]string{},
		cliToolByRoot:                 map[string]string{},
		sessionShellByProject:         map[string]string{},
		sessionShellByRoot:            map[string]string{},
		sessionInitCommandsByProject:  map[string][]string{},
		sessionInitCommandsByRoot:     map[string][]string{},
		worktreeInitCommandsByProject: map[string][]string{},
		worktreeInitCommandsByRoot:    map[string][]string{},
		worktreeManagersByProject:     map[string]*git.WorktreeManager{},
		worktreeManagersByRoot:        map[string]*git.WorktreeManager{},
		runtimeStoresByProject:        map[string]*daemonstate.RuntimeStateStore{},
		runtimeStoresByRoot:           map[string]*daemonstate.RuntimeStateStore{},
		hookLogByProject:              map[string][]protocol.HookLogEvent{},
		tmux:                          tmux.NewClient(tmuxRunner, cfg.Logger),
		git:                           gitClient,
		gitStatusAdapter:              gitService,
		session:                       sessionHandler,
		sessionStore:                  sessionStore,
		runtimeReconcileQueue:         runtimeReconcileQueue,
		gitStatusRefreshQueue:         gitStatusRefreshQueue,
		sessionStopPending:            map[string]int{},
		sessionStateRefreshing:        map[string]bool{},
		sessionStateLastRefresh:       map[string]time.Time{},
		worktreeStateRefreshing:       map[string]bool{},
		worktreeStateLastRefresh:      map[string]time.Time{},
		revision:                      map[string]uint64{},
		shutdownReqCh:                 make(chan struct{}),
	}
	canonicalProjectID := protocol.DefaultProjectID
	if hashProjectID, err := appconfig.ProjectIDForRoot(strings.TrimSpace(cfg.RepoDir)); err == nil {
		canonicalProjectID = protocol.NormalizeProjectID(hashProjectID)
	} else if repoName := protocol.NormalizeProjectID(filepath.Base(strings.TrimSpace(cfg.RepoDir))); repoName != "" {
		canonicalProjectID = repoName
	}
	d.issueClientsByRoot[strings.TrimSpace(cfg.RepoDir)] = issuesClient
	d.issueClientsByProject[canonicalProjectID] = issuesClient
	baseWorktreeManager := git.NewWorktreeManager(gitRunner, cfg.RepoDir, cfg.Logger)
	d.worktreeManagersByRoot[strings.TrimSpace(cfg.RepoDir)] = baseWorktreeManager
	d.worktreeManagersByProject[canonicalProjectID] = baseWorktreeManager
	d.runtimeStoresByRoot[strings.TrimSpace(cfg.RepoDir)] = runtimeStateStore
	d.runtimeStoresByProject[canonicalProjectID] = runtimeStateStore
	specService.daemon = d
	specHandler := daemonhandlers.NewSpecHandler(specService)
	d.syncBootstrapFn = d.defaultSyncBootstrap
	d.runtimeProjectionWriter = newRuntimeProjectionWriter(d)
	gitService.runtimeProjectionWriter = d.runtimeProjectionStateWriter()
	gitService.runtimeStateStoreForProject = func(projectID string) *daemonstate.RuntimeStateStore {
		return d.worktreeRuntimeStateStore(projectID)
	}
	gitService.baseBranchForProject = d.baseBranchForProject
	gitService.onStatusUpdate = func(ctx context.Context, projectID, issueID, worktree string, status *git.GitStatus) {
		d.runtimeProjectionStateWriter().PublishGitStatusProjectionEvent(ctx, projectID, issueID, worktree, status)
	}
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir:                cfg.RepoDir,
		logger:                 cfg.Logger,
		hub:                    d.hub,
		nextRevision:           d.nextRevision,
		sessionStart:           d.handleSessionStartDirect,
		sessionStop:            d.handleSessionStopDirect,
		sessionResolveConflict: d.handleSessionResolveConflictDirect,
	})
	commandExecutor := operationCommandExecutor{runtime: runtime}
	sessionExecutor := sessionOperationExecutor{runtime: runtime}
	gitHandler := daemonhandlers.NewGitHandler(gitService, daemonhandlers.WithGitLongRunningExecutor(commandExecutor))
	worktreeAdapter := &worktreeServiceAdapter{
		managerForProject: func(projectID string) *git.WorktreeManager { return d.worktreeManagerForProject(projectID) },
		runtimeStateStore: d.worktreeRuntimeStateStore(),
		runtimeStateStoreForProject: func(projectID string) *daemonstate.RuntimeStateStore {
			return d.worktreeRuntimeStateStore(projectID)
		},
		runtimeProjectionWriter:            d.runtimeProjectionStateWriter(),
		ensureRuntimeFreshForMutation:      d.ensureFreshRuntimeForMutation,
		ensureRuntimeFreshForIssueMutation: d.ensureFreshRuntimeForIssueMutation,
		logger:                             cfg.Logger,
		onProjectionUpdate: func(ctx context.Context, projectID, issueID, path string) {
			d.runtimeProjectionStateWriter().PublishWorktreeProjectionEvent(ctx, projectID, issueID, path)
		},
		onWorktreeObserved: func(_ context.Context, projectID, _ string, path string) {
			gitService.refreshGitStatusAsync(projectID, path)
		},
	}
	d.worktreeAdapter = worktreeAdapter
	worktreeHandler := daemonhandlers.NewWorktreeHandler(
		worktreeAdapter,
		daemonhandlers.WithWorktreeLongRunningExecutor(commandExecutor),
	)
	runtime.gitHandler = gitHandler
	runtime.worktreeHandler = worktreeHandler
	d.gitHandler = gitHandler
	d.worktreeHandler = worktreeHandler
	d.operationRuntime = runtime
	d.sessionLongRunning = sessionExecutor
	d.runtimeReconciler = newRuntimeReconcileService(d)
	d.router = daemonhandlers.NewDispatcher(
		sessionHandler,
		gitHandler,
		worktreeHandler,
		devServerHandler,
		prHandler,
		specHandler,
		runtime,
	)
	d.apply = daemonhandlers.NewApplyHandler(d, applyRevisionAdapter{daemon: d})

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
	d.prepareRunShutdownState()
	if err := d.validateCommandPolicyConfiguration(); err != nil {
		return err
	}
	lease, err := d.lock.Acquire()
	if err != nil {
		return err
	}
	d.cfg.Logger.Info("daemon startup phase", "phase", "lock_acquire", "duration_ms", time.Since(startedAt).Milliseconds())
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	shutdownDone := make(chan struct{})
	shutdownStop := make(chan struct{})
	shutdownReqCh := d.shutdownRequestChannel()
	go func() {
		defer func() {
			if r := recover(); r != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Error("daemon shutdown watcher panicked", "panic", r, "stack", string(debug.Stack()))
			}
		}()
		select {
		case <-ctx.Done():
			d.requestShutdown()
			d.drainInFlightCommands()
			cancelServe()
			close(shutdownDone)
		case <-shutdownReqCh:
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
		d.closeIssueClients()
		if d.runtimeReconcileQueue != nil {
			if closeErr := d.runtimeReconcileQueue.Close(); closeErr != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Warn("failed to close runtime reconcile queue", "error", closeErr)
			}
		}
		if d.gitStatusRefreshQueue != nil {
			if closeErr := d.gitStatusRefreshQueue.Close(); closeErr != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Warn("failed to close git status refresh queue", "error", closeErr)
			}
		}
		d.closeRuntimeStateStores()
		_ = lease.Release()
		_ = d.lock.Release()
	}()
	bootstrapStartedAt := time.Now()
	if err := d.bootstrapSyncOrchestrator(ctx); err != nil {
		d.cfg.Logger.Error("daemon startup phase failed", "phase", "sync_bootstrap", "duration_ms", time.Since(bootstrapStartedAt).Milliseconds(), "error", err)
		return err
	}
	d.cfg.Logger.Info("daemon startup phase", "phase", "sync_bootstrap", "duration_ms", time.Since(bootstrapStartedAt).Milliseconds())
	reconcileStartedAt := time.Now()
	if result, err := d.runStartupRuntimeReconcile(ctx); err != nil {
		d.cfg.Logger.Warn("daemon startup reconcile failed",
			"phase", "runtime_reconcile",
			"project_id", result.ProjectID,
			"duration_ms", time.Since(reconcileStartedAt).Milliseconds(),
			"error", err,
		)
	} else {
		d.cfg.Logger.Info("daemon startup phase", "phase", "runtime_reconcile", "project_id", result.ProjectID, "duration_ms", time.Since(reconcileStartedAt).Milliseconds())
	}
	d.startRuntimeReconcileWorker(serveCtx)
	d.startLinearSyncWorker(serveCtx)
	d.cfg.Logger.Info("daemon startup phase", "phase", "startup_ready", "duration_ms", time.Since(startedAt).Milliseconds())
	err = d.serve.Serve(serveCtx)
	if ctx.Err() != nil {
		<-shutdownDone
	}
	return err
}

func (d *Daemon) prepareRunShutdownState() {
	d.shutdownMu.Lock()
	defer d.shutdownMu.Unlock()
	d.shuttingDown = false
	d.shutdownReqCh = make(chan struct{})
	d.shutdownReqOnce = sync.Once{}
}

func (d *Daemon) validateCommandPolicyConfiguration() error {
	if err := daemonhandlers.ValidateCommandSpecs(); err != nil {
		return fmt.Errorf("daemon command-spec registry validation failed: %w", err)
	}
	if d.router != nil {
		if err := daemonhandlers.ValidateDispatcherWiring(d.router); err != nil {
			return fmt.Errorf("daemon command-spec wiring validation failed: %w", err)
		}
	}
	return nil
}

func (d *Daemon) handshake(_ context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	return protocol.NegotiateHello(hello, buildinfo.VersionString()), nil
}

func (d *Daemon) subscribe(_ context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, func(), error) {
	projectID = d.canonicalProjectID(projectID)
	ch, cancel := d.hub.Subscribe(projectID, fromRevision)
	return ch, cancel, nil
}

func (d *Daemon) command(ctx context.Context, req protocol.RequestEnvelope) (resp protocol.ResponseEnvelope, err error) {
	startedAt := time.Now()
	projectID := d.projectID(req.Meta)
	req.Meta.ProjectID = naming.ProjectID(projectID)
	ctx = withDaemonProjectIDContext(ctx, projectID)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info(
			"daemon command received",
			"command", req.Command,
			"request_id", req.RequestID,
			"project_id", projectID,
		)
	}
	defer func() {
		if d.cfg.Logger == nil {
			return
		}
		attrs := []any{
			"command", req.Command,
			"request_id", req.RequestID,
			"project_id", projectID,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		}
		switch {
		case err != nil:
			d.cfg.Logger.Error("daemon command transport failed", append(attrs, "error", err)...)
		case resp.Error != nil:
			d.cfg.Logger.Warn(
				"daemon command failed",
				append(attrs, "error_code", resp.Error.Code, "error", resp.Error.Message)...,
			)
		default:
			d.cfg.Logger.Info("daemon command completed", attrs...)
		}
	}()

	if resp, handled := d.guardSyncDependentCommand(req); handled {
		return resp, nil
	}
	if err := d.beginCommand(); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, err.Error()), nil
	}
	defer d.endCommand()

	if daemonhandlers.DaemonRoutesThroughDispatcher(req.Command) {
		if d.router == nil {
			return d.errorResponse(req, protocol.ErrorCodeUnsupportedCommand, "unsupported command"), nil
		}
		return d.router.Handle(ctx, req), nil
	}
	switch req.Command {
	case protocol.CommandDaemonShutdown:
		return d.handleDaemonShutdown(req), nil
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
	case protocol.CommandHookLogAppend:
		return d.handleHookLogAppend(ctx, req)
	case protocol.CommandHookLogList:
		return d.handleHookLogList(ctx, req)
	case protocol.CommandUIOpenTaskWorkspace:
		return d.handleUIOpenTaskWorkspace(ctx, req)
	case "task.list":
		return d.handleTaskList(ctx, req)
	case "task.get":
		return d.handleTaskGet(ctx, req)
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
	case commandSyncRun:
		return d.handleSyncRun(ctx, req)
	case commandSyncConflicts:
		return d.handleSyncConflicts(ctx, req)
	case protocol.CommandTaskBulkApply:
		return d.apply.Handle(ctx, req), nil
	case "session.start":
		return d.handleSessionStart(ctx, req)
	case "session.attach":
		return d.handleSessionAttach(ctx, req)
	case "session.pause":
		return d.handleSessionPause(ctx, req)
	case "session.resume":
		return d.handleSessionResume(ctx, req)
	case "session.stop":
		return d.handleSessionStop(ctx, req)
	case protocol.CommandSessionResolveConflict:
		return d.handleSessionResolveConflict(ctx, req)
	case "session.status":
		return d.handleSessionStatus(ctx, req)
	case "session.recover":
		return d.handleSessionRecover(ctx, req)
	case protocol.CommandRuntimeReconcile:
		return d.handleRuntimeReconcile(ctx, req)
	case protocol.CommandRuntimeReconcileIssue:
		return d.handleRuntimeReconcileIssue(ctx, req)
	default:
		return d.errorResponse(req, protocol.ErrorCodeUnsupportedCommand, "unsupported command"), nil
	}
}

func (d *Daemon) handleDaemonShutdown(req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	d.requestShutdown()
	return d.successResponse(req)
}

func (d *Daemon) shutdownRequestChannel() chan struct{} {
	d.shutdownMu.Lock()
	defer d.shutdownMu.Unlock()
	if d.shutdownReqCh == nil {
		d.shutdownReqCh = make(chan struct{})
	}
	return d.shutdownReqCh
}

func (d *Daemon) requestShutdown() {
	ch := d.shutdownRequestChannel()
	d.shutdownReqOnce.Do(func() {
		close(ch)
	})
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
	if d.sessionStore == nil {
		return errors.New("session store unavailable")
	}

	state, ok := lifecycleCommandState(command)
	if !ok {
		return fmt.Errorf("unsupported session command: %s", command)
	}

	_, err := d.sessionStore.UpsertSession(projectID, sessionID, issueID, state)
	if err != nil && state == daemonstate.SessionStateStarting && errors.Is(err, daemonstate.ErrInvalidTransition) {
		// Start uses tmux as source-of-truth for conflict detection; when tmux has no
		// session but stale desired state exists, allow resetting desired->starting.
		_, err = d.sessionStore.ForceUpsertSession(projectID, sessionID, issueID, state)
	}
	if err != nil {
		return err
	}

	session, err := d.sessionStore.Session(projectID, sessionID)
	if err != nil {
		return err
	}
	if runtimeStore := d.sessionRuntimeStateStore(projectID); runtimeStore != nil && strings.TrimSpace(issueID) != "" {
		if observed, found, loadErr := runtimeStore.GetSessionStateByIssueID(ctx, projectID, issueID); loadErr == nil && found {
			if strings.TrimSpace(string(observed.ObservedState)) != "" {
				session.ObservedState = observed.ObservedState
			}
			if observed.StartedAt != nil && !observed.StartedAt.IsZero() {
				started := observed.StartedAt.UTC()
				session.StartedAt = &started
			}
			if observed.UpdatedAt.After(session.UpdatedAt) {
				session.UpdatedAt = observed.UpdatedAt
			}
		} else if loadErr != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load runtime session projection for transition failed", "project_id", projectID, "issue_id", issueID, "error", loadErr)
		}
	}
	d.runtimeProjectionStateWriter().PersistSessionProjectionAndPublish(ctx, projectID, req.Meta, session)
	return nil
}

func lifecycleCommandState(command string) (daemonstate.SessionState, bool) {
	switch command {
	case daemonhandlers.CommandSessionStart:
		return daemonstate.SessionStateStarting, true
	case daemonhandlers.CommandSessionAttach:
		return daemonstate.SessionStateAttached, true
	case daemonhandlers.CommandSessionPause:
		return daemonstate.SessionStatePaused, true
	case daemonhandlers.CommandSessionResume:
		return daemonstate.SessionStateAttached, true
	case daemonhandlers.CommandSessionStop:
		return daemonstate.SessionStateStopped, true
	default:
		return "", false
	}
}

func (d *Daemon) sessionRuntimeStateStore(projectID ...string) *daemonstate.RuntimeStateStore {
	if d == nil {
		return nil
	}
	if len(projectID) > 0 {
		return d.runtimeStateStoreForProject(projectID[0])
	}
	return d.runtimeStateStoreForProject(protocol.DefaultProjectID)
}

func (d *Daemon) worktreeRuntimeStateStore(projectID ...string) *daemonstate.RuntimeStateStore {
	if d == nil {
		return nil
	}
	if len(projectID) > 0 {
		return d.runtimeStateStoreForProject(projectID[0])
	}
	return d.runtimeStateStoreForProject(protocol.DefaultProjectID)
}

func (d *Daemon) worktreeRuntimeStateStoreIfConfigured(projectID string) *daemonstate.RuntimeStateStore {
	if d == nil {
		return nil
	}
	d.runtimeStoresMu.Lock()
	hasConfiguredStores := len(d.runtimeStoresByProject) > 0 || len(d.runtimeStoresByRoot) > 0
	d.runtimeStoresMu.Unlock()
	if !hasConfiguredStores {
		return nil
	}
	return d.worktreeRuntimeStateStore(projectID)
}

func (d *Daemon) sessionRuntimeStateStoreIfConfigured(projectID string) *daemonstate.RuntimeStateStore {
	if d == nil {
		return nil
	}
	d.runtimeStoresMu.Lock()
	hasConfiguredStores := len(d.runtimeStoresByProject) > 0 || len(d.runtimeStoresByRoot) > 0
	d.runtimeStoresMu.Unlock()
	if !hasConfiguredStores {
		return nil
	}
	return d.sessionRuntimeStateStore(projectID)
}

func (d *Daemon) refreshSessionInvariantCache(ctx context.Context, projectID string) error {
	if d == nil || d.sessionStore == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		d.sessionStore.ReplaceProjectSessions(projectID, nil)
		return nil
	}
	sessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return err
	}
	d.sessionStore.ReplaceProjectSessions(projectID, sessions)
	return nil
}

func (d *Daemon) refreshSessionInvariantCacheIfConfigured(ctx context.Context, projectID string) error {
	projectID = d.canonicalProjectID(projectID)
	if d.sessionRuntimeStateStoreIfConfigured(projectID) == nil {
		return nil
	}
	return d.refreshSessionInvariantCache(ctx, projectID)
}

func (d *Daemon) runtimeProjectionStateWriter() runtimeProjectionWriter {
	if d == nil {
		return nil
	}
	if d.runtimeProjectionWriter == nil {
		d.runtimeProjectionWriter = newRuntimeProjectionWriter(d)
	}
	return d.runtimeProjectionWriter
}

func (d *Daemon) closeRuntimeStateStores() {
	if d == nil {
		return
	}
	d.runtimeStoresMu.Lock()
	defer d.runtimeStoresMu.Unlock()

	stores := make([]*daemonstate.RuntimeStateStore, 0, len(d.runtimeStoresByRoot))
	for _, store := range d.runtimeStoresByRoot {
		stores = append(stores, store)
	}

	seen := map[*daemonstate.RuntimeStateStore]struct{}{}
	for _, store := range stores {
		if store == nil {
			continue
		}
		if _, exists := seen[store]; exists {
			continue
		}
		seen[store] = struct{}{}
		if closeErr := store.Close(); closeErr != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("failed to close runtime state store", "error", closeErr)
		}
	}
}

func (d *Daemon) persistSessionState(projectID string, session daemonstate.Session) {
	if d.sessionRuntimeStateStore(projectID) == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.sessionRuntimeStateStore(projectID).UpsertSessionState(ctx, projectID, session); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn(
			"persist session runtime state failed",
			"project_id", projectID,
			"session_id", session.ID,
			"issue_id", session.IssueID,
			"state", session.State,
			"error", err,
		)
	}
}

func (d *Daemon) triggerSessionStateRefresh(projectID string, refreshFn func(context.Context, string) error) {
	if d.sessionRuntimeStateStore(projectID) == nil || refreshFn == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}

	const minRefreshInterval = 3 * time.Second
	now := time.Now()

	d.sessionStateRefreshMu.Lock()
	if d.sessionStateRefreshing == nil {
		d.sessionStateRefreshing = map[string]bool{}
	}
	if d.sessionStateLastRefresh == nil {
		d.sessionStateLastRefresh = map[string]time.Time{}
	}
	if d.sessionStateRefreshing[projectID] {
		d.sessionStateRefreshMu.Unlock()
		return
	}
	if last := d.sessionStateLastRefresh[projectID]; !last.IsZero() && now.Sub(last) < minRefreshInterval {
		d.sessionStateRefreshMu.Unlock()
		return
	}
	d.sessionStateRefreshing[projectID] = true
	d.sessionStateLastRefresh[projectID] = now
	d.sessionStateRefreshMu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Error("session runtime-state refresh goroutine panicked", "project_id", projectID, "panic", r, "stack", string(debug.Stack()))
			}
			d.sessionStateRefreshMu.Lock()
			d.sessionStateRefreshing[projectID] = false
			d.sessionStateRefreshMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := refreshFn(ctx, projectID); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("session runtime-state refresh failed", "project_id", projectID, "error", err)
		}
	}()
}

func (d *Daemon) triggerWorktreeStateRefresh(projectID string) {
	if d.worktreeAdapter == nil || d.worktreeRuntimeStateStore(projectID) == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = protocol.DefaultProjectID
	}

	const minRefreshInterval = 3 * time.Second
	now := time.Now()

	d.worktreeStateRefreshMu.Lock()
	if d.worktreeStateRefreshing == nil {
		d.worktreeStateRefreshing = map[string]bool{}
	}
	if d.worktreeStateLastRefresh == nil {
		d.worktreeStateLastRefresh = map[string]time.Time{}
	}
	if d.worktreeStateRefreshing[projectID] {
		d.worktreeStateRefreshMu.Unlock()
		return
	}
	if last := d.worktreeStateLastRefresh[projectID]; !last.IsZero() && now.Sub(last) < minRefreshInterval {
		d.worktreeStateRefreshMu.Unlock()
		return
	}
	d.worktreeStateRefreshing[projectID] = true
	d.worktreeStateLastRefresh[projectID] = now
	d.worktreeStateRefreshMu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Error("worktree runtime-state refresh goroutine panicked", "project_id", projectID, "panic", r, "stack", string(debug.Stack()))
			}
			d.worktreeStateRefreshMu.Lock()
			d.worktreeStateRefreshing[projectID] = false
			d.worktreeStateRefreshMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.worktreeAdapter.pollAndPersistWorktrees(ctx, projectID)
	}()
}

func (d *Daemon) refreshIssueWorktreeState(ctx context.Context, projectID, issueID string) {
	if d == nil || d.gitStatusAdapter == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = protocol.DefaultProjectID
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return
	}
	store := d.worktreeRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return
	}
	projection, found, err := store.GetWorktreeStateByIssueID(ctx, projectID, issueID)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("issue worktree refresh lookup failed", "project_id", projectID, "issue_id", issueID, "error", err)
		}
		return
	}
	if !found || strings.TrimSpace(projection.Path) == "" {
		return
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := d.gitStatusAdapter.refreshGitStatusManual(refreshCtx, projectID, projection.Path); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("issue worktree refresh failed", "project_id", projectID, "issue_id", issueID, "worktree", projection.Path, "error", err)
	}
}

func (d *Daemon) persistWorktreeState(ctx context.Context, projectID, issueID, path, branch string) error {
	if d.worktreeRuntimeStateStore(projectID) == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := d.worktreeRuntimeStateStore(projectID).UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: strings.TrimSpace(projectID),
		IssueID:   strings.TrimSpace(issueID),
		Path:      strings.TrimSpace(path),
		Branch:    strings.TrimSpace(branch),
		UpdatedAt: time.Now().UTC(),
	}); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn(
			"persist worktree runtime state failed",
			"project_id", projectID,
			"issue_id", issueID,
			"path", path,
			"branch", branch,
			"error", err,
		)
		return err
	}
	return nil
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
	return d.canonicalProjectID(meta.ProjectID.String())
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

func (d *Daemon) publishTaskEvent(req protocol.RequestEnvelope, eventName string, rev uint64, bodies ...protocol.TaskEventBody) {
	projectID := d.projectID(req.Meta)
	var body []byte
	if len(bodies) > 0 {
		eventBody := bodies[0]
		if eventBody.ProjectID == "" {
			eventBody.ProjectID = naming.ProjectID(projectID)
		}
		if eventBody.UpdatedAt.IsZero() {
			eventBody.UpdatedAt = time.Now().UTC()
		}
		encoded, err := json.Marshal(eventBody)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("marshal task event body failed", "project_id", projectID, "event", eventName, "revision", rev, "error", err)
			}
		} else {
			body = encoded
		}
	}
	d.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		ProjectID:       naming.ProjectID(projectID),
		Meta:            req.Meta,
		Revision:        rev,
		Event:           eventName,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Body:            body,
	})
}

func (d *Daemon) publishSessionProjectionEvent(ctx context.Context, projectID string, meta protocol.Metadata, session daemonstate.Session) uint64 {
	projectID = d.canonicalProjectID(projectID)
	rev := d.nextRevision(projectID)
	d.publishSessionProjectionEventAtRevision(ctx, projectID, meta, session, rev)
	return rev
}

func (d *Daemon) publishWorktreeProjectionEvent(ctx context.Context, projectID, issueID, worktree string) uint64 {
	projectID = d.canonicalProjectID(projectID)
	rev := d.nextRevision(projectID)
	d.publishWorktreeProjectionEventAtRevision(ctx, projectID, issueID, worktree, rev)
	return rev
}

func (d *Daemon) publishGitStatusProjectionEvent(ctx context.Context, projectID, issueID, worktree string, status *git.GitStatus) uint64 {
	projectID = d.canonicalProjectID(projectID)
	rev := d.nextRevision(projectID)
	d.publishGitStatusProjectionEventAtRevision(ctx, projectID, issueID, worktree, status, rev)
	return rev
}

func (d *Daemon) publishSessionProjectionEventAtRevision(ctx context.Context, projectID string, meta protocol.Metadata, session daemonstate.Session, rev uint64) {
	projectID = d.canonicalProjectID(projectID)
	if d.hub == nil {
		return
	}
	runtime := d.runtimeProjectionForEvent(ctx, projectID, session.IssueID, "", nil)
	if strings.TrimSpace(session.ID) != "" {
		sessionRuntime := buildRuntimeProjection(projectID, &session, nil)
		runtime.IssueID = sessionRuntime.IssueID
		runtime.Session = sessionRuntime.Session
		runtime.Agent = sessionRuntime.Agent
	}
	runtimeBody := buildRuntimeProjectionEventBody(projectID, rev, runtime)
	body, err := json.Marshal(protocol.SessionProjectionEventBody{
		ProjectID: naming.ProjectID(projectID),
		Revision:  rev,
		Session: protocol.SessionProjection{
			SessionID: parseSessionIDOrZero(session.ID),
			IssueID:   parseIssueIDOrZero(session.IssueID),
			State:     protocol.SessionLifecycleState(session.State),
			UpdatedAt: session.UpdatedAt,
		},
		Runtime: &runtimeBody,
	})
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("marshal session projection event body failed", "project_id", projectID, "session_id", session.ID, "error", err)
		}
		return
	}
	if meta.ProjectID == "" {
		meta.ProjectID = naming.ProjectID(projectID)
	}
	d.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       naming.ProjectID(projectID),
		Meta:            meta,
		Revision:        rev,
		Event:           protocol.EventSessionUpdated,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Body:            body,
	})
}

func (d *Daemon) publishWorktreeProjectionEventAtRevision(ctx context.Context, projectID, issueID, worktree string, rev uint64) {
	projectID = d.canonicalProjectID(projectID)
	if d.hub == nil {
		return
	}
	runtime := d.runtimeProjectionForEvent(ctx, projectID, issueID, worktree, nil)
	runtimeBody := buildRuntimeProjectionEventBody(projectID, rev, runtime)
	body, err := json.Marshal(protocol.ProjectionUpdateEventBody{
		ProjectID: naming.ProjectID(projectID),
		IssueID:   parseIssueIDOrZero(issueID),
		Worktree:  strings.TrimSpace(worktree),
		UpdatedAt: time.Now().UTC(),
		Runtime:   &runtimeBody,
	})
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("marshal worktree projection event body failed", "project_id", projectID, "issue_id", issueID, "error", err)
		}
		return
	}
	d.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       naming.ProjectID(projectID),
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Revision:        rev,
		Event:           protocol.EventWorktreeProjectionUpdated,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Body:            body,
	})
}

func (d *Daemon) publishGitStatusProjectionEventAtRevision(ctx context.Context, projectID, issueID, worktree string, status *git.GitStatus, rev uint64) {
	projectID = d.canonicalProjectID(projectID)
	if d.hub == nil {
		return
	}
	runtime := d.runtimeProjectionForEvent(ctx, projectID, issueID, worktree, status)
	runtimeBody := buildRuntimeProjectionEventBody(projectID, rev, runtime)
	body, err := json.Marshal(protocol.ProjectionUpdateEventBody{
		ProjectID: naming.ProjectID(projectID),
		IssueID:   parseIssueIDOrZero(issueID),
		Worktree:  strings.TrimSpace(worktree),
		UpdatedAt: time.Now().UTC(),
		Runtime:   &runtimeBody,
	})
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("marshal git status projection event body failed", "project_id", projectID, "issue_id", issueID, "error", err)
		}
		return
	}
	d.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       naming.ProjectID(projectID),
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Revision:        rev,
		Event:           protocol.EventGitStatusUpdated,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Body:            body,
	})
}

func (d *Daemon) runtimeProjectionForEvent(ctx context.Context, projectID, issueID, worktree string, status *git.GitStatus) protocol.RuntimeProjection {
	projectID = normalizeRuntimeProjectionProjectID(projectID).String()
	issueID = strings.TrimSpace(issueID)
	worktree = strings.TrimSpace(worktree)

	var session *daemonstate.Session
	if issueID != "" {
		if runtimeStore := d.sessionRuntimeStateStore(projectID); runtimeStore != nil {
			if loaded, found, err := runtimeStore.GetSessionStateByIssueID(ctx, projectID, issueID); err == nil && found {
				copy := loaded
				session = &copy
			} else if err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Debug("load runtime session projection failed", "project_id", projectID, "issue_id", issueID, "error", err)
			}
		}
		if session == nil && d.sessionStore != nil {
			if loaded, ok := d.sessionStore.SessionByIssueID(projectID, issueID); ok {
				copy := loaded
				session = &copy
			}
		}
	}

	var projectionWorktree *daemonstate.WorktreeState
	if d.worktreeRuntimeStateStore(projectID) != nil {
		if issueID != "" {
			if loaded, found, err := d.worktreeRuntimeStateStore(projectID).GetWorktreeStateByIssueID(ctx, projectID, issueID); err == nil && found {
				copy := loaded
				projectionWorktree = &copy
			}
		}
		if projectionWorktree == nil && worktree != "" {
			if loaded, found, err := d.worktreeRuntimeStateStore(projectID).GetWorktreeStateByPath(ctx, projectID, worktree); err == nil && found {
				copy := loaded
				projectionWorktree = &copy
			}
		}
	}

	projection := buildRuntimeProjection(projectID, session, projectionWorktree)
	if projection.IssueID == "" {
		projection.IssueID = parseIssueIDOrZero(issueID)
	}
	if session != nil && projection.Session.Worktree == "" && projection.Worktree.Path != "" {
		projection.Session.Worktree = projection.Worktree.Path
	}
	fallbackStatus := status
	if fallbackStatus == nil && projectionWorktree != nil && len(projectionWorktree.GitStatusRaw) > 0 {
		var projectedStatus git.GitStatus
		if err := json.Unmarshal(projectionWorktree.GitStatusRaw, &projectedStatus); err == nil {
			fallbackStatus = &projectedStatus
		}
	}

	if status != nil {
		projection.Git.HasUncommittedChanges = status.HasChanges
		projection.Git.HasConflicts = status.HasConflicts
		projection.Git.ConflictFiles = append([]string(nil), status.Conflicted...)
		projection.Git.GitAdditions = status.GitAdditions
		projection.Git.GitDeletions = status.GitDeletions
		projection.Git.GitAheadCount = status.GitAheadCount
		projection.Git.GitBehindCount = status.GitBehindCount
		fallbackStatus = status
	}

	if projection.Git.GitAheadCount == 0 && fallbackStatus != nil {
		projection.Git.GitAheadCount = fallbackStatus.GitAheadCount
	}
	if projection.Git.GitBehindCount == 0 && fallbackStatus != nil {
		projection.Git.GitBehindCount = fallbackStatus.GitBehindCount
	}
	return projection
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

func (a applyRevisionAdapter) PublishTaskEvent(req protocol.RequestEnvelope, eventName string, rev uint64, bodies ...protocol.TaskEventBody) {
	a.daemon.publishTaskEvent(req, eventName, rev, bodies...)
}

func (a applyRevisionAdapter) TaskEventBody(ctx context.Context, projectID, taskID string) protocol.TaskEventBody {
	return a.daemon.taskEventBody(ctx, projectID, taskID)
}
