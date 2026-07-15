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
	daemonnotices "github.com/riordanpawley/azedarach/internal/daemon/notices"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/aiaccount"
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

	defaultRuntimeProjectionCoalesceWindow = 25 * time.Millisecond
)

// Config configures daemon runtime wiring.
type Config struct {
	RepoDir       string
	SocketPath    string
	LockPath      string
	ScopedRuntime bool
	// ManagedGenerationBinDir is retained for source compatibility only.
	// Agent launches intentionally ignore it and inherit the project PATH.
	ManagedGenerationBinDir    string
	BaseBranch                 string
	GitWorkflowMode            string
	CLITool                    string
	DangerouslySkipPermissions bool
	CodexAppServer             bool
	SessionShell               string
	SessionSyncInitCommands    []string
	SessionAsyncInitCommands   []string
	WorktreeInitCommands       []string
	WorktreeAsyncInitCommands  []string
	IssueResources             appconfig.IssueResourcesConfig
	IssueAutoArchive           appconfig.IssueAutoArchiveConfig
	ScheduledScripts           appconfig.ScheduledScriptsConfig
	Orchestration              appconfig.OrchestrationConfig
	Logger                     *slog.Logger
	IdleTimeout                time.Duration
	RuntimeReconcileInterval   time.Duration
	RuntimeReconcileTimeout    time.Duration
	scheduledScriptRunner      scheduledScriptCommandRunner
}

// Daemon is the daemon runtime root.
type Daemon struct {
	cfg    Config
	lock   daemonLockManager
	hub    *publish.Hub
	serve  daemonServer
	router *daemonhandlers.Dispatcher
	apply  *daemonhandlers.ApplyHandler

	issues                               *issues.Client
	userStore                            *userstore.Store
	userStoreRefreshMu                   sync.Mutex
	userStoreRefreshPending              map[string]bool
	userStoreRefreshDirty                map[string]bool
	userStoreRefreshWG                   sync.WaitGroup
	userStoreRefreshStopping             bool
	userStoreRefreshCtx                  context.Context
	userStoreRefreshCancel               context.CancelFunc
	issueClientsMu                       sync.Mutex
	issueClientsByProject                map[string]*issues.Client
	issueClientsByRoot                   map[string]*issues.Client
	decisionPropagationMu                sync.Mutex
	projectIssueStoreHealthMu            sync.Mutex
	projectIssueStoreHealthByProject     map[string]projectIssueStoreHealthState
	projectConfigMu                      sync.Mutex
	baseBranchByProject                  map[string]string
	baseBranchByRoot                     map[string]string
	workflowModeByProject                map[string]string
	workflowModeByRoot                   map[string]string
	cliToolByProject                     map[string]string
	cliToolByRoot                        map[string]string
	sessionShellByProject                map[string]string
	sessionShellByRoot                   map[string]string
	codexAppServerByProject              map[string]bool
	codexAppServerByRoot                 map[string]bool
	sessionSyncInitCommandsByProject     map[string][]string
	sessionSyncInitCommandsByRoot        map[string][]string
	sessionAsyncInitCommandsByProject    map[string][]string
	sessionAsyncInitCommandsByRoot       map[string][]string
	worktreeInitCommandsByProject        map[string][]string
	worktreeInitCommandsByRoot           map[string][]string
	worktreeAsyncInitCommandsByProject   map[string][]string
	worktreeAsyncInitCommandsByRoot      map[string][]string
	issueResourcesByProject              map[string]appconfig.IssueResourcesConfig
	issueResourcesByRoot                 map[string]appconfig.IssueResourcesConfig
	issueAutoArchiveByProject            map[string]appconfig.IssueAutoArchiveConfig
	issueAutoArchiveByRoot               map[string]appconfig.IssueAutoArchiveConfig
	scheduledScriptsByProject            map[string]appconfig.ScheduledScriptsConfig
	scheduledScriptsByRoot               map[string]appconfig.ScheduledScriptsConfig
	orchestrationByProject               map[string]appconfig.OrchestrationConfig
	orchestrationByRoot                  map[string]appconfig.OrchestrationConfig
	worktreeManagersMu                   sync.Mutex
	worktreeManagersByProject            map[string]*git.WorktreeManager
	worktreeManagersByRoot               map[string]*git.WorktreeManager
	runtimeStoresMu                      sync.Mutex
	runtimeStoresByProject               map[string]*daemonstate.RuntimeStateStore
	runtimeStoresByRoot                  map[string]*daemonstate.RuntimeStateStore
	hookLogMu                            sync.Mutex
	hookLogByProject                     map[string][]protocol.HookLogEvent
	uiStateMu                            sync.RWMutex
	uiState                              map[string]string
	tmux                                 *tmux.Client
	agentInput                           *agentInputDeliveryService
	git                                  *git.Client
	gitStatusAdapter                     *gitServiceAdapter
	gitHandler                           *daemonhandlers.GitHandler
	worktreeHandler                      *daemonhandlers.WorktreeHandler
	worktreeAdapter                      *worktreeServiceAdapter
	session                              *daemonhandlers.SessionHandler
	sessionStore                         *daemonstate.Store
	runtimeProjectionWriter              runtimeProjectionWriter
	sessionLongRunning                   SessionLongRunningExecutor
	sessionResumeWait                    func(context.Context, time.Duration) error
	sessionShellRun                      func(context.Context, string, string, string, []string) ([]byte, error)
	runtimeReconciler                    runtimeReconciler
	runtimeReconcileQueue                *reconcileQueue[protocol.RuntimeReconcileResponseBody]
	gitStatusRefreshQueue                *reconcileQueue[*git.GitStatus]
	runtimeReconcileThrottle             *reconcileThrottle
	worktreeGitProbeThrottle             *reconcileThrottle
	queueMu                              sync.Mutex
	operationRuntime                     *operationRuntime
	noticeService                        *daemonnotices.Service
	runtimeProjectionCoalescer           *runtimeProjectionEventCoalescer
	scheduledScripts                     *scheduledScriptManager
	issueAutoArchive                     *issueAutoArchiveWorker
	issueAutoArchiveLastRun              map[string]time.Time
	sessionStopMu                        sync.Mutex
	sessionStopPending                   map[string]int
	orchestratorStopGracePeriod          time.Duration
	orchestratorStopPollInterval         time.Duration
	orchestratorStopAfterIntentPersisted func()
	sessionStateRefreshMu                sync.Mutex
	sessionStateRefreshing               map[string]bool
	sessionStateLastRefresh              map[string]time.Time
	worktreeStateRefreshMu               sync.Mutex
	worktreeStateRefreshing              map[string]bool
	worktreeStateLastRefresh             map[string]time.Time
	taskListRuntimeRefreshMu             sync.Mutex
	taskListRuntimeLastRefresh           map[string]time.Time
	taskListRuntimeRefreshes             map[string]*taskListRuntimeRefresh
	taskListSnapshotCacheMu              sync.Mutex
	taskListSnapshotCache                map[string]taskListSnapshotCacheEntry
	taskListSnapshotLoadMu               sync.Mutex
	taskListSnapshotLoads                map[string]*taskListSnapshotLoad
	taskGraphReadinessMu                 sync.Mutex
	taskGraphReadinessLoads              map[string]*taskGraphReadinessLoad
	orchestrationMu                      sync.Mutex
	reviewLeaseReleasedBeforeClose       func(context.Context, string, string) error
	watchClientsMu                       sync.Mutex
	watchClients                         map[string]watchClientObservation
	terminalFailureProbeMu               sync.Mutex
	terminalFailureProbes                map[string]terminalFailureProbeState
	reviewReadyRecoveryMu                sync.Mutex
	reviewReadyRecoveryCursor            map[string]int64
	reviewReadyRecoveryBeforeLoad        func()
	decisionTransferBeforeRevalidation   func(string, decisionMDTransferTarget)
	deferredCleanupOperationManager      deferredCleanupOperationManager

	revMu    sync.Mutex
	revision map[string]uint64

	shutdownMu       sync.Mutex
	shuttingDown     bool
	shutdownReqCh    chan struct{}
	shutdownReqOnce  sync.Once
	inFlightCommands sync.WaitGroup

	syncBootstrapState              syncBootstrapState
	syncBootstrapFn                 func(context.Context) error
	reconcileInteractionStalenessFn func(context.Context, string) error
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
	runtimeRepoDir := cfg.RepoDir
	cfg.ScopedRuntime = cfg.ScopedRuntime || appconfig.UseScopedDaemonRuntimeFor(cfg.RepoDir)
	if cfg.ScopedRuntime {
		if worktreeRoot, err := appconfig.ResolveWorktreeRoot(cfg.RepoDir); err == nil && strings.TrimSpace(worktreeRoot) != "" {
			runtimeRepoDir = worktreeRoot
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
	runtimeStateStore := daemonstate.NewRuntimeStateStore(runtimeRepoDir, cfg.Logger)
	if cfg.ScopedRuntime {
		runtimeStateStore = daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(runtimeRepoDir, ".azedarach", "azedarach.db"), cfg.Logger)
	}
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
		workflowMode:       cfg.GitWorkflowMode,
	}
	devServerManager := devserver.NewManager(devserver.NewPortAllocator(3000), cfg.Logger)
	sessionStore := daemonstate.NewStore()
	issuesClient := issues.NewClient(cfg.RepoDir, cfg.Logger)
	sessionHandler := daemonhandlers.NewSessionHandler(sessionStore)
	devServerHandler := daemonhandlers.NewDevServerHandler(devServerManager)
	specService := issueSpecService{daemon: nil}

	d := &Daemon{
		cfg:                                cfg,
		lock:                               lifecycle.NewLockManager(cfg.LockPath),
		hub:                                publish.NewHub(512, 64, cfg.Logger),
		issues:                             issuesClient,
		issueClientsByProject:              map[string]*issues.Client{},
		issueClientsByRoot:                 map[string]*issues.Client{},
		projectIssueStoreHealthByProject:   map[string]projectIssueStoreHealthState{},
		baseBranchByProject:                map[string]string{},
		baseBranchByRoot:                   map[string]string{},
		workflowModeByProject:              map[string]string{},
		workflowModeByRoot:                 map[string]string{},
		cliToolByProject:                   map[string]string{},
		cliToolByRoot:                      map[string]string{},
		sessionShellByProject:              map[string]string{},
		sessionShellByRoot:                 map[string]string{},
		codexAppServerByProject:            map[string]bool{},
		codexAppServerByRoot:               map[string]bool{},
		sessionSyncInitCommandsByProject:   map[string][]string{},
		sessionSyncInitCommandsByRoot:      map[string][]string{},
		sessionAsyncInitCommandsByProject:  map[string][]string{},
		sessionAsyncInitCommandsByRoot:     map[string][]string{},
		worktreeInitCommandsByProject:      map[string][]string{},
		worktreeInitCommandsByRoot:         map[string][]string{},
		worktreeAsyncInitCommandsByProject: map[string][]string{},
		worktreeAsyncInitCommandsByRoot:    map[string][]string{},
		issueResourcesByProject:            map[string]appconfig.IssueResourcesConfig{},
		issueResourcesByRoot:               map[string]appconfig.IssueResourcesConfig{},
		issueAutoArchiveByProject:          map[string]appconfig.IssueAutoArchiveConfig{},
		issueAutoArchiveByRoot:             map[string]appconfig.IssueAutoArchiveConfig{},
		scheduledScriptsByProject:          map[string]appconfig.ScheduledScriptsConfig{},
		scheduledScriptsByRoot:             map[string]appconfig.ScheduledScriptsConfig{},
		worktreeManagersByProject:          map[string]*git.WorktreeManager{},
		worktreeManagersByRoot:             map[string]*git.WorktreeManager{},
		runtimeStoresByProject:             map[string]*daemonstate.RuntimeStateStore{},
		runtimeStoresByRoot:                map[string]*daemonstate.RuntimeStateStore{},
		hookLogByProject:                   map[string][]protocol.HookLogEvent{},
		uiState:                            map[string]string{},
		tmux:                               tmux.NewClient(tmuxRunner, cfg.Logger),
		git:                                gitClient,
		gitStatusAdapter:                   gitService,
		session:                            sessionHandler,
		sessionStore:                       sessionStore,
		runtimeReconcileQueue:              runtimeReconcileQueue,
		gitStatusRefreshQueue:              gitStatusRefreshQueue,
		sessionStopPending:                 map[string]int{},
		sessionStateRefreshing:             map[string]bool{},
		sessionStateLastRefresh:            map[string]time.Time{},
		worktreeStateRefreshing:            map[string]bool{},
		worktreeStateLastRefresh:           map[string]time.Time{},
		taskListRuntimeLastRefresh:         map[string]time.Time{},
		taskListRuntimeRefreshes:           map[string]*taskListRuntimeRefresh{},
		taskListSnapshotCache:              map[string]taskListSnapshotCacheEntry{},
		issueAutoArchiveLastRun:            map[string]time.Time{},
		taskGraphReadinessLoads:            map[string]*taskGraphReadinessLoad{},
		revision:                           map[string]uint64{},
		userStoreRefreshPending:            map[string]bool{},
		userStoreRefreshDirty:              map[string]bool{},
		shutdownReqCh:                      make(chan struct{}),
	}
	d.agentInput = newAgentInputDeliveryService(d.tmux, d.sessionRuntimeStateStoreIfConfigured)
	if !cfg.ScopedRuntime && strings.TrimSpace(os.Getenv("AZEDARACH_DISABLE_USER_DB")) != "1" {
		if store, err := userstore.Open(userstore.DefaultPath()); err != nil {
			cfg.Logger.Warn("initialize user cross-project projection", "error", err)
		} else {
			d.userStore = store
		}
	}
	canonicalProjectID := protocol.DefaultProjectID
	if hashProjectID, err := appconfig.ProjectIDForRoot(strings.TrimSpace(cfg.RepoDir)); err == nil {
		canonicalProjectID = protocol.NormalizeProjectID(hashProjectID)
	} else if repoName := protocol.NormalizeProjectID(filepath.Base(strings.TrimSpace(cfg.RepoDir))); repoName != "" {
		canonicalProjectID = repoName
	}
	d.issueClientsByRoot[daemonStoreRootKey(cfg.RepoDir)] = issuesClient
	d.issueClientsByProject[canonicalProjectID] = issuesClient
	baseWorktreeManager := git.NewWorktreeManager(gitRunner, cfg.RepoDir, cfg.Logger)
	d.worktreeManagersByRoot[strings.TrimSpace(cfg.RepoDir)] = baseWorktreeManager
	d.worktreeManagersByProject[canonicalProjectID] = baseWorktreeManager
	d.runtimeStoresByRoot[daemonStoreRootKey(runtimeRepoDir)] = runtimeStateStore
	d.runtimeStoresByProject[canonicalProjectID] = runtimeStateStore
	specService.daemon = d
	specHandler := daemonhandlers.NewSpecHandler(specService)
	decisionHandler := daemonhandlers.NewDecisionHandler(issueDecisionService{daemon: d})
	interactionHandler := daemonhandlers.NewInteractionHandler(issueInteractionService{daemon: d})
	learnHandler := daemonhandlers.NewLearnHandler(issueLearnService{daemon: d})
	var accountService daemonhandlers.AIAccountService
	if service, err := aiaccount.New(aiaccount.Config{}); err != nil {
		cfg.Logger.Error("initialize AI account service", "error", err)
	} else {
		accountService = service
	}
	accountHandler := daemonhandlers.NewAIAccountHandler(accountService)
	d.syncBootstrapFn = d.defaultSyncBootstrap
	d.runtimeProjectionWriter = newRuntimeProjectionWriter(d)
	d.runtimeProjectionCoalescer = newRuntimeProjectionEventCoalescer(d, defaultRuntimeProjectionCoalesceWindow)
	d.scheduledScripts = newScheduledScriptManager(d, cfg.Logger, cfg.scheduledScriptRunner)
	d.issueAutoArchive = newIssueAutoArchiveWorker(d, cfg.Logger)
	prHandler := daemonhandlers.NewProjectPRHandler(gitService, func(_ context.Context, projectID string) (daemonhandlers.PRProjectResources, error) {
		repoDir := d.resolveRepoDirForProjectExact(projectID)
		if repoDir == "" {
			return daemonhandlers.PRProjectResources{}, fmt.Errorf("unknown project %q for PR command; refusing repository fallback", projectID)
		}
		issueRefs := d.issueClientForProject(projectID)
		if issueRefs == nil {
			return daemonhandlers.PRProjectResources{}, fmt.Errorf("project %q has no issue store for PR command; refusing project fallback", projectID)
		}
		return daemonhandlers.PRProjectResources{
			Workflow:  pr.NewPRWorkflow(pr.NewExecRunner(repoDir), cfg.Logger),
			IssueRefs: issueRefs,
		}, nil
	})
	gitService.runtimeProjectionWriter = d.runtimeProjectionStateWriter()
	gitService.runtimeStateStoreForProject = func(projectID string) *daemonstate.RuntimeStateStore {
		return d.worktreeRuntimeStateStore(projectID)
	}
	gitService.baseBranchForProject = d.baseBranchForProject
	gitService.workflowModeForProject = d.workflowModeForProject
	gitService.baseBranchForWorktree = d.runtimeDiffBaseBranchForWorktree
	gitService.heavySessionStartActive = func(ctx context.Context, projectID string) bool {
		active, err := d.hasActiveHeavySessionStart(ctx, projectID)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("git status heavy session-start check failed open",
					"project_id", d.canonicalProjectID(projectID),
					"error", err,
				)
			}
			return false
		}
		return active
	}
	gitService.onStatusUpdate = func(ctx context.Context, projectID, issueID, worktree string, status *git.GitStatus) {
		d.runtimeProjectionStateWriter().PublishGitStatusProjectionEvent(ctx, projectID, issueID, worktree, status)
	}
	noticeService := daemonnotices.NewService(daemonnotices.ServiceConfig{
		Repository:   daemonnotices.New(cfg.RepoDir, cfg.Logger),
		Hub:          d.hub,
		NextRevision: d.nextRevision,
		Logger:       cfg.Logger,
	})
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir:                 cfg.RepoDir,
		logger:                  cfg.Logger,
		hub:                     d.hub,
		nextRevision:            d.nextRevision,
		sessionStart:            d.handleSessionStartDirect,
		sessionStop:             d.handleSessionStopDirect,
		sessionResolveConflict:  d.handleSessionResolveConflictDirect,
		taskBulkCleanup:         d.handleTaskBulkCleanup,
		globalProjectionRebuild: d.handleGlobalProjectionRebuild,
		onMutationSuccess:       d.enqueueUserProjectionRefresh,
		onTerminal:              d.reconcileOrchestrationStartOperation,
		recoverInterrupted:      d.recoverInterruptedOperation,
		noticeService:           noticeService,
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
		runtimeIssueTasks: func(ctx context.Context, projectID string, issueIDs []string) map[string]domain.Task {
			issueClient := d.issueClientForProject(projectID)
			if issueClient == nil {
				return nil
			}
			tasks, err := issueClient.GetRuntimeWorktreeIssueContext(ctx, projectID, issueIDs)
			if err != nil {
				if cfg.Logger != nil {
					cfg.Logger.Debug("worktree projection issue snapshot failed", "project_id", projectID, "error", err)
				}
				return nil
			}
			taskByIssue := make(map[string]domain.Task, len(tasks))
			for _, task := range tasks {
				taskByIssue[strings.TrimSpace(task.ID.String())] = task
			}
			return taskByIssue
		},
		runWorktreeSyncInit:    d.runWorktreeSyncInitCommands,
		startWorktreeAsyncInit: d.startWorktreeAsyncInitCommands,
		logger:                 cfg.Logger,
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
	d.noticeService = noticeService
	d.sessionLongRunning = sessionExecutor
	d.runtimeReconciler = newRuntimeReconcileService(d)
	d.router = daemonhandlers.NewDispatcher(
		sessionHandler,
		gitHandler,
		worktreeHandler,
		devServerHandler,
		prHandler,
		specHandler,
		decisionHandler,
		interactionHandler,
		learnHandler,
		accountHandler,
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
	d.startSessionLaunchArtifactCleanup(serveCtx)
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
		if d.noticeService != nil {
			if closeErr := d.noticeService.Close(); closeErr != nil {
				d.cfg.Logger.Warn("failed to close notice service", "error", closeErr)
			}
		}
		if d.runtimeProjectionCoalescer != nil {
			d.runtimeProjectionCoalescer.Close()
		}
		if d.scheduledScripts != nil {
			d.scheduledScripts.Close()
		}
		if d.issueAutoArchive != nil {
			d.issueAutoArchive.Close()
		}
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
		d.stopUserProjectionWorkers()
		d.closeIssueClients()
		if d.userStore != nil {
			if closeErr := d.userStore.Close(); closeErr != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Warn("failed to close user database", "error", closeErr)
			}
		}
		d.closeRuntimeStateStores()
		_ = lease.Release()
		_ = d.lock.Release()
	}()

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- d.serve.Serve(serveCtx)
	}()
	d.cfg.Logger.Info("daemon startup phase", "phase", "ipc_serve_start", "duration_ms", time.Since(startedAt).Milliseconds())
	waitForShutdown := func() {
		if ctx.Err() != nil {
			<-shutdownDone
		}
	}
	checkServeErr := func(phase string) error {
		select {
		case err := <-serveErrCh:
			if ctx.Err() != nil {
				waitForShutdown()
				return nil
			}
			if err != nil {
				return fmt.Errorf("daemon server exited during %s: %w", phase, err)
			}
			return fmt.Errorf("daemon server exited during %s", phase)
		default:
			return nil
		}
	}

	bootstrapStartedAt := time.Now()
	if err := d.bootstrapSyncOrchestrator(ctx); err != nil {
		d.cfg.Logger.Warn("daemon startup optional phase failed",
			"phase", "sync_bootstrap",
			"duration_ms", time.Since(bootstrapStartedAt).Milliseconds(),
			"error", err,
		)
	} else {
		d.cfg.Logger.Info("daemon startup phase", "phase", "sync_bootstrap", "duration_ms", time.Since(bootstrapStartedAt).Milliseconds())
	}
	if ctx.Err() != nil {
		waitForShutdown()
		return nil
	}
	if err := checkServeErr("sync bootstrap"); err != nil {
		return err
	}
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
	if ctx.Err() != nil {
		waitForShutdown()
		return nil
	}
	d.reconcileAllDecisionPropagationOutboxes(ctx)
	d.startRuntimeReconcileWorker(serveCtx)
	d.startDecisionPropagationReconcileWorker(serveCtx)
	d.startLinearSyncWorker(serveCtx)
	d.startScheduledScriptWorker(serveCtx)
	d.startIssueAutoArchiveWorker(serveCtx)
	d.startGlobalProjectionRepairWorker(serveCtx)
	d.cfg.Logger.Info("daemon startup phase", "phase", "startup_ready", "duration_ms", time.Since(startedAt).Milliseconds())
	err = <-serveErrCh
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
	ctx, endCommandSpan := latencytrace.StartSpan(ctx, "daemon", "command", "command", req.Command, "request_id", req.RequestID, "project_id", projectID)
	d.recordWatchClientRequest(projectID, req, startedAt.UTC())
	defer func() {
		switch {
		case err != nil:
			endCommandSpan(err)
		case resp.Error != nil:
			endCommandSpan(fmt.Errorf("daemon response error: %s", resp.Error.Code))
		default:
			endCommandSpan(nil)
		}
	}()
	if req.ProtocolVersion != protocol.CurrentVersion {
		code := protocol.ErrorCodeUpgradeRequired
		if req.ProtocolVersion > protocol.CurrentVersion {
			code = protocol.ErrorCodeIncompatible
		}
		message := fmt.Sprintf(
			"client protocol %d does not match daemon protocol %d; the az command may be a stale long-lived client after an installed control-link switch: re-enter the shell through the stable installed az control link and restart any session that retained the older client identity",
			req.ProtocolVersion,
			protocol.CurrentVersion,
		)
		return d.errorResponse(req, code, message), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Log(
			ctx,
			daemonCommandSuccessLogLevel(req.Command),
			"daemon command received",
			append([]any{
				"command", req.Command,
				"request_id", req.RequestID,
				"project_id", projectID,
			}, daemonClientAuditAttrs(req.Meta)...)...,
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
		attrs = append(attrs, daemonClientAuditAttrs(req.Meta)...)
		switch {
		case err != nil:
			d.cfg.Logger.Error("daemon command transport failed", append(attrs, "error", err)...)
		case resp.Error != nil:
			logLevel := slog.LevelWarn
			if isCachedProjectIssueStoreHealthErrorMessage(resp.Error.Message) {
				logLevel = slog.LevelDebug
			}
			d.cfg.Logger.Log(
				ctx,
				logLevel,
				"daemon command failed",
				append(attrs, "error_code", resp.Error.Code, "error", resp.Error.Message)...,
			)
		default:
			d.cfg.Logger.Log(ctx, daemonCommandSuccessLogLevel(req.Command), "daemon command completed", attrs...)
		}
	}()

	beginStartedAt := time.Now()
	if err := d.beginCommand(); err != nil {
		latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "command.begin", beginStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "error", err)
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, err.Error()), nil
	}
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "command.begin", beginStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID)
	defer d.endCommand()
	if commandMutatesProjectProjection(req.Command) {
		defer func() {
			if err == nil && resp.OK {
				d.enqueueUserProjectionRefresh(projectID)
			}
		}()
	}

	if daemonhandlers.DaemonRoutesThroughDispatcher(req.Command) {
		if d.router == nil {
			return d.errorResponse(req, protocol.ErrorCodeUnsupportedCommand, "unsupported command"), nil
		}
		dispatchStartedAt := time.Now()
		defer func() {
			latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "command.dispatcher_handle", dispatchStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID)
		}()
		return d.router.Handle(ctx, req), nil
	}
	switch req.Command {
	case protocol.CommandDaemonShutdown:
		return d.handleDaemonShutdown(req), nil
	case protocol.CommandDaemonWatchClients:
		return d.handleDaemonWatchClients(req), nil
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
	case protocol.CommandRuntimeSignalIngest:
		return d.handleRuntimeSignalIngest(ctx, req)
	case protocol.CommandValidationAcquire, protocol.CommandValidationHeartbeat, protocol.CommandValidationNested, protocol.CommandValidationFinish, protocol.CommandValidationStatus:
		return d.handleValidationCommand(ctx, req)
	case protocol.CommandUIOpenTaskWorkspace, protocol.CommandUIOpenTaskDrillDown:
		return d.handleUIIssueCommand(ctx, req)
	case protocol.CommandUIStateGet:
		return d.handleUIStateGet(ctx, req)
	case protocol.CommandUIStateSet:
		return d.handleUIStateSet(ctx, req)
	case protocol.CommandBoardViewList:
		return d.handleBoardViewList(ctx, req)
	case protocol.CommandBoardViewGet:
		return d.handleBoardViewGet(ctx, req)
	case protocol.CommandBoardViewSave:
		return d.handleBoardViewSave(ctx, req)
	case protocol.CommandBoardViewDelete:
		return d.handleBoardViewDelete(ctx, req)
	case protocol.CommandBoardViewSelect:
		return d.handleBoardViewSelect(ctx, req)
	case protocol.CommandProjectCleanup:
		return d.handleProjectCleanup(ctx, req)
	case protocol.CommandNoticeList:
		return d.handleNoticeList(ctx, req), nil
	case protocol.CommandNoticeGet:
		return d.handleNoticeGet(ctx, req), nil
	case protocol.CommandNoticeUpdate:
		return d.handleNoticeUpdate(ctx, req), nil
	case protocol.CommandNoticeAction:
		return d.handleNoticeAction(ctx, req), nil
	case protocol.CommandBoardFetch:
		return d.handleBoardFetch(ctx, req)
	case protocol.CommandScheduledScriptsStatus:
		return d.handleScheduledScriptsStatus(ctx, req)
	case protocol.CommandGlobalSnapshot:
		return d.handleGlobalSnapshot(ctx, req)
	case protocol.CommandGlobalProjectionRebuild:
		return d.handleGlobalProjectionRebuild(ctx, req)
	case "task.list":
		return d.handleTaskList(ctx, req)
	case "task.get":
		return d.handleTaskGet(ctx, req)
	case "task.get_many":
		return d.handleTaskGetMany(ctx, req)
	case "task.events":
		return d.handleTaskEvents(ctx, req)
	case "task.event.append":
		return d.handleTaskEventAppend(ctx, req)
	case "task.create":
		return d.handleTaskCreate(ctx, req)
	case "task.close":
		return d.handleTaskClose(ctx, req)
	case protocol.CommandTaskBulkCleanup:
		return d.handleTaskBulkCleanup(ctx, req)
	case "task.close_preflight":
		return d.handleTaskClosePreflight(ctx, req)
	case "task.delete_preflight":
		return d.handleTaskDeletePreflight(ctx, req)
	case "task.graph_readiness":
		return d.handleTaskGraphReadiness(ctx, req)
	case protocol.CommandOrchestrationSnapshot:
		return d.handleOrchestrationSnapshot(ctx, req)
	case protocol.CommandOrchestrationIntent:
		return d.handleOrchestrationIntent(ctx, req)
	case protocol.CommandOrchestratorSessionStart, protocol.CommandOrchestratorSessionAttach, protocol.CommandOrchestratorSessionStop, protocol.CommandOrchestratorSessionStatus:
		return d.handleOrchestratorSession(ctx, req)
	case "task.complete_check":
		return d.handleTaskCompleteCheck(ctx, req)
	case "task.integration_readiness":
		return d.handleTaskIntegrationReadiness(ctx, req)
	case "task.context_risk":
		return d.handleTaskContextRisk(ctx, req)
	case "task.merge_base_target":
		return d.handleTaskMergeBaseTarget(ctx, req)
	case "task.follow_on_merge_candidates":
		return d.handleTaskFollowOnMergeCandidates(ctx, req)
	case "task.ownership.claim":
		return d.handleTaskOwnershipClaim(ctx, req)
	case "task.ownership.release":
		return d.handleTaskOwnershipRelease(ctx, req)
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
	case "task.unarchive":
		return d.handleTaskUnarchive(ctx, req)
	case "task.dependency.add":
		return d.handleTaskDependencyAdd(ctx, req)
	case "task.dependency.remove":
		return d.handleTaskDependencyRemove(ctx, req)
	case protocol.CommandTaskSQLiteWAL:
		return d.handleTaskSQLiteWAL(ctx, req)
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
	case daemonhandlers.CommandSessionMessage:
		return d.handleSessionMessage(ctx, req)
	case protocol.CommandSessionResolveConflict:
		return d.handleSessionResolveConflict(ctx, req)
	case protocol.CommandSessionRestartAll:
		return d.handleSessionRestartAll(ctx, req)
	case protocol.CommandSessionCapture:
		return d.handleSessionCapture(ctx, req)
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

func daemonCommandSuccessLogLevel(command string) slog.Level {
	switch command {
	case protocol.CommandMailWatch:
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

func (d *Daemon) handleDaemonShutdown(req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.DaemonShutdownCommandBody
	if len(req.Body) > 0 {
		_ = json.Unmarshal(req.Body, &body)
	}
	reason := strings.TrimSpace(body.Reason)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon shutdown requested", "reason", reason, "request_id", req.RequestID, "project_id", req.Meta.ProjectID)
	}
	if reason == "replace" {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("ignoring legacy daemon replace shutdown request", "reason", reason, "request_id", req.RequestID, "project_id", req.Meta.ProjectID)
		}
		return d.successResponse(req)
	}
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
	return d.applySessionLifecycleTransitionWithActivity(ctx, req, projectID, sessionID, issueID, command, "", "")
}

type sessionIntentSelector struct {
	Role      daemonstate.SessionRole
	ScopeKind daemonstate.SessionScopeKind
	ScopeID   string
}

func (d *Daemon) applySessionLifecycleTransitionWithActivity(
	ctx context.Context,
	req protocol.RequestEnvelope,
	projectID string,
	sessionID string,
	issueID string,
	command string,
	activity string,
	activitySource string,
) error {
	return d.applyTypedSessionLifecycleTransition(ctx, req, projectID, sessionID, issueID, command, activity, activitySource, sessionIntentSelector{Role: daemonstate.SessionRoleWorker, ScopeKind: daemonstate.SessionScopeIssue, ScopeID: issueID})
}

func (d *Daemon) applyTypedSessionLifecycleTransition(ctx context.Context, req protocol.RequestEnvelope, projectID, sessionID, issueID, command, activity, activitySource string, selector sessionIntentSelector) error {
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
	runtimeObservedFound := false
	if runtimeStore := d.sessionRuntimeStateStore(projectID); runtimeStore != nil && strings.TrimSpace(sessionID) != "" {
		if observed, found, loadErr := runtimeStore.GetSessionIntent(ctx, projectID, selector.Role, selector.ScopeKind, selector.ScopeID); loadErr == nil && found {
			runtimeObservedFound = strings.TrimSpace(string(observed.ObservedState)) != ""
			// Durable projection identity is authoritative. The transient lifecycle
			// store carries only physical session/issue strings and must not erase a
			// typed advisor or orchestrator product during a state transition.
			session.IssueID = observed.IssueID
			session.Role = observed.Role
			session.ScopeKind = observed.ScopeKind
			session.ScopeID = observed.ScopeID
			if strings.TrimSpace(string(observed.ObservedState)) != "" {
				session.ObservedState = observed.ObservedState
			}
			if strings.TrimSpace(session.Activity) == "" && strings.TrimSpace(observed.Activity) != "" {
				session.Activity = strings.TrimSpace(observed.Activity)
				session.ActivitySource = strings.TrimSpace(observed.ActivitySource)
			}
			if observed.StartedAt != nil && !observed.StartedAt.IsZero() {
				started := observed.StartedAt.UTC()
				session.StartedAt = &started
			}
			if observed.UpdatedAt.After(session.UpdatedAt) {
				session.UpdatedAt = observed.UpdatedAt
			}
		} else if loadErr != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load runtime session projection for transition failed", "project_id", projectID, "session_id", sessionID, "error", loadErr)
		}
	}
	if !runtimeObservedFound {
		session.ObservedState = ""
	}
	if state == daemonstate.SessionStateStopped {
		session.Activity = ""
		session.ActivitySource = ""
	} else if normalizedActivity := normalizeSessionActivity(activity); normalizedActivity != "" {
		session.Activity = normalizedActivity
		session.ActivitySource = normalizeSessionActivitySource(activitySource, "session")
	} else if isAgentScopedSessionID(sessionID) {
		switch state {
		case daemonstate.SessionStateRunning:
			session.Activity = "busy"
			session.ActivitySource = "hooks"
		case daemonstate.SessionStatePaused:
			session.Activity = "idle"
			session.ActivitySource = "hooks"
		}
	} else if session.Activity != "" {
		session.Activity = normalizeSessionActivity(session.Activity)
		session.ActivitySource = normalizeSessionActivitySource(session.ActivitySource, "session")
	}
	writer := d.runtimeProjectionStateWriter()
	if err := writer.PersistSessionProjection(ctx, projectID, session); err != nil {
		return err
	}
	if runtimeStore := d.sessionRuntimeStateStore(projectID); runtimeStore != nil {
		persisted, found, err := runtimeStore.GetSessionIntent(ctx, projectID, selector.Role, selector.ScopeKind, selector.ScopeID)
		if err != nil {
			return fmt.Errorf("reload persisted session intent for publication: %w", err)
		}
		if found {
			session = persisted
		}
	}
	writer.PublishSessionProjectionEvent(ctx, projectID, req.Meta, session)
	return nil
}

func (d *Daemon) sessionLifecycleTransitionNeeded(projectID, sessionID, issueID string, state daemonstate.SessionState) bool {
	if d.sessionStore == nil {
		return true
	}
	session, err := d.sessionStore.Session(projectID, sessionID)
	if err != nil {
		return true
	}
	if daemonstate.NormalizeSessionState(session.State) != daemonstate.NormalizeSessionState(state) {
		return true
	}
	issueID = strings.TrimSpace(issueID)
	return issueID != "" && strings.TrimSpace(session.IssueID) != issueID
}

func (d *Daemon) sessionLifecycleOrAgentActivityTransitionNeeded(projectID, sessionID, issueID string, state daemonstate.SessionState) bool {
	if d.sessionLifecycleTransitionNeeded(projectID, sessionID, issueID, state) {
		return true
	}
	if !isAgentScopedSessionID(sessionID) || d.sessionStore == nil {
		return false
	}
	wantActivity, wantSource, ok := agentScopedSessionActivityForLifecycleState(state)
	if !ok {
		return false
	}
	session, err := d.sessionStore.Session(projectID, sessionID)
	if err != nil {
		return true
	}
	return normalizeSessionActivity(session.Activity) != wantActivity ||
		strings.ToLower(strings.TrimSpace(session.ActivitySource)) != wantSource
}

func agentScopedSessionActivityForLifecycleState(state daemonstate.SessionState) (string, string, bool) {
	switch daemonstate.NormalizeSessionState(state) {
	case daemonstate.SessionStateRunning:
		return "busy", "hooks", true
	case daemonstate.SessionStatePaused:
		return "idle", "hooks", true
	default:
		return "", "", false
	}
}

func lifecycleCommandState(command string) (daemonstate.SessionState, bool) {
	switch command {
	case daemonhandlers.CommandSessionStart:
		return daemonstate.SessionStateStarting, true
	case daemonhandlers.CommandSessionAttach:
		return daemonstate.SessionStateRunning, true
	case daemonhandlers.CommandSessionPause:
		return daemonstate.SessionStatePaused, true
	case daemonhandlers.CommandSessionResume:
		return daemonstate.SessionStateRunning, true
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

func (d *Daemon) recoverInterruptedOperation(ctx context.Context, record daemonops.Record) (interruptedOperationRecovery, bool) {
	if d == nil {
		return interruptedOperationRecovery{}, false
	}
	if record.Kind == taskDeferredWorktreeCleanupOperationKind {
		return d.recoverInterruptedDeferredWorktreeCleanup(ctx, record)
	}
	if record.Kind != daemonhandlers.CommandSessionStart {
		return interruptedOperationRecovery{}, false
	}
	projectID := protocol.NormalizeProjectID(record.ProjectID)
	if projectID == "" {
		projectID = protocol.DefaultProjectID
	}
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return interruptedOperationRecovery{}, false
	}
	canonicalID := naming.CanonicalSessionID(d.sessionNamingScope(projectID), record.IssueID)
	session, found, err := store.GetWorkerSessionStateByIssueID(ctx, projectID, record.IssueID, canonicalID)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("failed to inspect interrupted worker session.start projection",
				"operation_id", record.ID,
				"project_id", projectID,
				"issue_id", record.IssueID,
				"error", err,
			)
		}
		return interruptedOperationRecovery{}, false
	}
	if !found || strings.TrimSpace(session.IssueID) != strings.TrimSpace(record.IssueID) {
		return interruptedOperationRecovery{}, false
	}
	if !interruptedSessionStartCompleted(session) {
		return interruptedOperationRecovery{}, false
	}
	result, err := json.Marshal(map[string]string{
		"output":     "session start recovered after daemon restart",
		"session_id": session.ID,
		"issue_id":   session.IssueID,
	})
	if err != nil {
		result = nil
	}
	return interruptedOperationRecovery{
		State:         daemonops.StateDone,
		ResultPayload: result,
	}, true
}

func (d *Daemon) recoverInterruptedDeferredWorktreeCleanup(ctx context.Context, record daemonops.Record) (interruptedOperationRecovery, bool) {
	projectID := protocol.NormalizeProjectID(record.ProjectID)
	if projectID == "" {
		projectID = protocol.DefaultProjectID
	}
	taskID := strings.TrimSpace(record.IssueID)
	if taskID == "" {
		return interruptedOperationRecovery{}, false
	}
	fallbackPath, fallbackBranch := deferredCleanupFallbacksFromResourceKeys(record.ResourceKeys)
	issueClient := d.issueClientForProject(projectID)
	if issueClient != nil {
		task, err := issueClient.GetWithRuntime(ctx, projectID, taskID)
		if err == nil && !task.IssueClosed() {
			if fallbackPath != "" {
				d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, taskID, fallbackPath, fallbackBranch)
			} else {
				d.restoreDeferredCleanupWorktreeProjection(ctx, projectID, taskID)
			}
			payload, _ := json.Marshal(deferredTaskWorktreeCleanupResult{
				ProjectID: projectID,
				TaskID:    taskID,
				Skipped:   true,
				Reason:    "issue no longer closed",
			})
			return interruptedOperationRecovery{
				State:         daemonops.StateDone,
				ResultPayload: payload,
			}, true
		}
	}
	manager := d.worktreeManagerForProject(projectID)
	if manager == nil {
		return interruptedOperationRecovery{
			State:        daemonops.StateFailed,
			ErrorMessage: "worktree manager unavailable",
		}, true
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, taskDeferredWorktreeCleanupTimeout)
	defer cancel()
	removedWorktree, err := manager.DeleteWithOptions(cleanupCtx, taskID, git.WorktreeDeleteOptions{
		Force:          true,
		BranchCleanup:  git.WorktreeBranchCleanupRequired,
		FallbackBranch: fallbackBranch,
	})
	if err != nil && !errors.Is(err, git.ErrWorktreeNotFound) {
		return interruptedOperationRecovery{
			State:        daemonops.StateFailed,
			ErrorMessage: err.Error(),
		}, true
	}
	if cleanupErr := finalizeDeletedWorktree(cleanupCtx, projectID, taskID, manager, removedWorktree, d.runtimeProjectionStateWriter()); cleanupErr != nil {
		return interruptedOperationRecovery{
			State:        daemonops.StateFailed,
			ErrorMessage: cleanupErr.Error(),
		}, true
	}
	payload, _ := json.Marshal(deferredTaskWorktreeCleanupResult{
		ProjectID: projectID,
		TaskID:    taskID,
	})
	return interruptedOperationRecovery{
		State:         daemonops.StateDone,
		ResultPayload: payload,
	}, true
}

func deferredCleanupFallbacksFromResourceKeys(resourceKeys []string) (path, branch string) {
	for _, key := range resourceKeys {
		if value, ok := strings.CutPrefix(key, "worktree:"); ok {
			path = strings.TrimSpace(value)
			continue
		}
		if value, ok := strings.CutPrefix(key, "branch:"); ok {
			branch = strings.TrimSpace(value)
		}
	}
	return path, branch
}

func interruptedSessionStartCompleted(session daemonstate.Session) bool {
	switch daemonstate.NormalizeSessionState(session.ObservedState) {
	case daemonstate.SessionStateRunning, daemonstate.SessionStatePaused:
		return true
	}
	switch daemonstate.NormalizeSessionState(session.State) {
	case daemonstate.SessionStateRunning, daemonstate.SessionStatePaused:
		return true
	default:
		return false
	}
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

func (d *Daemon) persistSessionState(projectID string, session daemonstate.Session) error {
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if (session.Role == "" || session.Role == daemonstate.SessionRoleWorker) && !isAgentScopedSessionID(session.ID) {
		canonicalID := naming.CanonicalSessionID(d.sessionNamingScope(projectID), session.IssueID)
		if existing, found, err := store.GetWorkerSessionStateByIssueID(ctx, projectID, session.IssueID, canonicalID); err != nil {
			return fmt.Errorf("load logical worker session before persist: %w", err)
		} else if found && !isAgentScopedSessionID(existing.ID) {
			session.ID = existing.ID
			session.IssueID = existing.IssueID
			session.Role = existing.Role
			session.ScopeKind = existing.ScopeKind
			session.ScopeID = existing.ScopeID
		}
	}
	if err := store.UpsertSessionState(ctx, projectID, session); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn(
				"persist session runtime state failed",
				"project_id", projectID,
				"session_id", session.ID,
				"issue_id", session.IssueID,
				"state", session.State,
				"error", err,
			)
		}
		return err
	}
	return nil
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
	if _, err := d.gitStatusAdapter.queueGitStatusRefresh(projectID, projection.Path, reconcilePriorityVisible, "issue-read"); err != nil && d.cfg.Logger != nil {
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
	}); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn(
				"persist worktree runtime state failed",
				"project_id", projectID,
				"issue_id", issueID,
				"path", path,
				"branch", branch,
				"error", err,
			)
		}
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
	if d.revision == nil {
		d.revision = map[string]uint64{}
	}
	d.revision[projectID]++
	rev := d.revision[projectID]
	d.revMu.Unlock()
	d.invalidateTaskListSnapshotCache(projectID)
	return rev
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
	var runtimeBody *protocol.RuntimeProjectionEventBody
	if strings.TrimSpace(session.IssueID) != "" {
		runtime := d.runtimeProjectionForEvent(ctx, projectID, session.IssueID, "", nil)
		if strings.TrimSpace(session.ID) != "" {
			sessionRuntime := buildRuntimeProjection(projectID, &session, nil)
			runtime.IssueID = sessionRuntime.IssueID
			runtime.Session = sessionRuntime.Session
		}
		applyRuntimeSessionCounts(&runtime, d.sessionProjectionCountsForIssue(ctx, projectID, session.IssueID))
		encodedRuntime := buildRuntimeProjectionEventBody(projectID, rev, runtime)
		runtimeBody = &encodedRuntime
	}
	body, err := json.Marshal(protocol.SessionProjectionEventBody{
		ProjectID: naming.ProjectID(projectID),
		Revision:  rev,
		Session: protocol.SessionProjection{
			SessionID: parseSessionIDOrZero(session.ID),
			IssueID:   parseIssueIDOrZero(session.IssueID),
			Role:      protocol.SessionRole(session.Role),
			ScopeKind: protocol.SessionScopeKind(session.ScopeKind),
			ScopeID:   strings.TrimSpace(session.ScopeID),
			State:     protocol.SessionLifecycleState(session.State),
			UpdatedAt: session.UpdatedAt,
		},
		Runtime: runtimeBody,
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
		if runtimeStore := d.sessionRuntimeStateStoreIfConfigured(projectID); runtimeStore != nil {
			if loaded, err := runtimeStore.ListSessionStates(ctx, projectID); err == nil {
				aggregated := sessionProjectionAggregateByIssueKey(loaded, d.sessionNamingScope(projectID))
				if merged, found := aggregated[sessionKey(issueID)]; found {
					copy := merged
					session = &copy
				}
				if display, found := sessionDisplayActivityByIssueKeyFromSessions(loaded, d.sessionNamingScope(projectID))[sessionKey(issueID)]; found && session != nil {
					session.Activity = display.Activity
					session.ActivitySource = display.Source
				}
			} else if err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Debug("load runtime session projection failed", "project_id", projectID, "issue_id", issueID, "error", err)
			}
		}
		if session == nil && d.sessionStore != nil {
			snapshot := d.sessionStore.ReadSnapshot(projectID)
			sessions := make([]daemonstate.Session, 0, len(snapshot.Sessions))
			for _, loaded := range snapshot.Sessions {
				sessions = append(sessions, loaded)
			}
			if loaded, ok := nonAgentSessionProjectionByIssue(sessions, d.sessionNamingScope(projectID), issueID); ok {
				session = &loaded
			}
			if display, found := sessionDisplayActivityByIssueKeyFromSessions(sessions, d.sessionNamingScope(projectID))[sessionKey(issueID)]; found && session != nil {
				session.Activity = display.Activity
				session.ActivitySource = display.Source
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
	if session != nil {
		applyRuntimeSessionCounts(&projection, d.sessionProjectionCountsForIssue(ctx, projectID, issueID))
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
