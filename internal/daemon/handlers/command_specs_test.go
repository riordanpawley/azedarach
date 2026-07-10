package handlers

import (
	"slices"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestCommandSpecRegistryProjectIDPolicy(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{command: CommandGitFetch, want: true},
		{command: protocol.CommandOperationQueue, want: true},
		{command: CommandSessionStart, want: true},
		{command: CommandSessionMessage, want: true},
		{command: protocol.CommandSessionCapture, want: true},
		{command: protocol.CommandRuntimeReconcile, want: true},
		{command: protocol.CommandRuntimeReconcileIssue, want: true},
		{command: protocol.CommandIssueFanout, want: true},
		{command: protocol.CommandHookLogList, want: true},
		{command: protocol.CommandBoardFetch, want: true},
		{command: protocol.CommandBoardViewList, want: true},
		{command: protocol.CommandSpecRead, want: false},
		{command: protocol.CommandLearnSupersede, want: true},
		{command: protocol.CommandLearnDoctor, want: true},
		{command: "unknown.command", want: false},
	}

	for _, tt := range tests {
		if got := CommandRequiresProjectID(tt.command); got != tt.want {
			t.Fatalf("CommandRequiresProjectID(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

func TestCommandSpecRegistryDispatcherTargets(t *testing.T) {
	tests := []struct {
		command string
		want    CommandDispatchTarget
		ok      bool
	}{
		{command: CommandSessionStart, want: CommandDispatchSession, ok: true},
		{command: CommandSessionMessage, want: CommandDispatchSession, ok: true},
		{command: protocol.CommandOperationQueue, want: CommandDispatchOperation, ok: true},
		{command: CommandPRCreate, want: CommandDispatchPR, ok: true},
		{command: CommandPRGet, want: CommandDispatchPR, ok: true},
		{command: CommandPRList, want: CommandDispatchPR, ok: true},
		{command: CommandPRChecks, want: CommandDispatchPR, ok: true},
		{command: CommandPROpen, want: CommandDispatchPR, ok: true},
		{command: CommandPRMerge, want: CommandDispatchPR, ok: true},
		{command: CommandGitBranchBehind, want: CommandDispatchPR, ok: true},
		{command: protocol.CommandSpecRead, want: CommandDispatchSpec, ok: true},
		{command: protocol.CommandLearnRetire, want: CommandDispatchLearn, ok: true},
		{command: protocol.CommandLearnSupersede, want: CommandDispatchLearn, ok: true},
		{command: protocol.CommandLearnGC, want: CommandDispatchLearn, ok: true},
		{command: CommandGitCheckpoint, want: CommandDispatchGit, ok: true},
		{command: CommandWorktreeRemove, want: CommandDispatchWorktree, ok: true},
		{command: CommandDevServerList, want: CommandDispatchDevServer, ok: true},
		{command: protocol.CommandRuntimeReconcile, want: CommandDispatchNone, ok: false},
		{command: protocol.CommandBoardFetch, want: CommandDispatchNone, ok: false},
		{command: "task.list", want: CommandDispatchNone, ok: false},
		{command: "unknown.command", want: CommandDispatchNone, ok: false},
	}

	for _, tt := range tests {
		got, ok := DispatcherTarget(tt.command)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("DispatcherTarget(%q) = (%v, %v), want (%v, %v)", tt.command, got, ok, tt.want, tt.ok)
		}
	}
}

func TestCommandSpecRegistryDaemonDispatchOwnership(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{command: CommandGitFetch, want: true},
		{command: protocol.CommandOperationQueue, want: true},
		{command: protocol.CommandSpecRead, want: true},
		{command: protocol.CommandLearnRetire, want: true},
		{command: protocol.CommandLearnSupersede, want: true},
		{command: protocol.CommandLearnDoctor, want: true},
		{command: CommandSessionStart, want: false},
		{command: protocol.CommandBoardFetch, want: false},
		{command: "task.list", want: false},
		{command: "unknown.command", want: false},
	}

	for _, tt := range tests {
		if got := DaemonRoutesThroughDispatcher(tt.command); got != tt.want {
			t.Fatalf("DaemonRoutesThroughDispatcher(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

func TestCommandSpecRegistryCoversKnownDaemonCommands(t *testing.T) {
	knownCommands := []string{
		CommandSessionStart,
		CommandSessionAttach,
		CommandSessionPause,
		CommandSessionResume,
		CommandSessionStop,
		CommandSessionMessage,
		CommandSessionResolveConflict,
		CommandSessionRestartAll,
		CommandSessionCapture,
		commandSessionStatus,
		commandSessionRecover,
		commandRuntimeReconcile,
		commandRuntimeReconcileIssue,
		protocol.CommandOperationSubmit,
		protocol.CommandOperationGet,
		protocol.CommandOperationList,
		protocol.CommandOperationQueue,
		protocol.CommandOperationCancel,
		CommandPRCreate,
		CommandPRGet,
		CommandPRList,
		CommandPRChecks,
		CommandPROpen,
		CommandPRMerge,
		CommandGitBranchBehind,
		protocol.CommandSpecRequirementList,
		protocol.CommandSpecRequirementGet,
		protocol.CommandSpecRequirementCreate,
		protocol.CommandSpecRequirementUpdate,
		protocol.CommandSpecRequirementDelete,
		protocol.CommandSpecLinkList,
		protocol.CommandSpecLinkAdd,
		protocol.CommandSpecLinkRemove,
		protocol.CommandSpecRead,
		protocol.CommandSpecPack,
		protocol.CommandSpecLint,
		protocol.CommandSpecParity,
		protocol.CommandSpecExport,
		protocol.CommandSpecSync,
		protocol.CommandSpecSyncMD,
		protocol.CommandDecisionList,
		protocol.CommandDecisionGet,
		protocol.CommandDecisionRecord,
		protocol.CommandDecisionUpdate,
		protocol.CommandDecisionDelete,
		protocol.CommandDecisionLinkList,
		protocol.CommandDecisionLinkAdd,
		protocol.CommandDecisionLinkRemove,
		protocol.CommandDecisionSyncMD,
		protocol.CommandDecisionImportMD,
		protocol.CommandInteractionCreate,
		protocol.CommandInteractionList,
		protocol.CommandInteractionGet,
		protocol.CommandInteractionDiscuss,
		protocol.CommandInteractionPropose,
		protocol.CommandInteractionAnswer,
		protocol.CommandInteractionResolve,
		protocol.CommandInteractionWithdraw,
		protocol.CommandInteractionSupersede,
		protocol.CommandInteractionRecover,
		protocol.CommandLearnAdd,
		protocol.CommandLearnRecall,
		protocol.CommandLearnShow,
		protocol.CommandLearnReview,
		protocol.CommandLearnPromote,
		protocol.CommandLearnRetire,
		protocol.CommandLearnRelate,
		protocol.CommandLearnStale,
		protocol.CommandLearnDemote,
		protocol.CommandLearnSupersede,
		protocol.CommandLearnDoctor,
		protocol.CommandLearnGC,
		CommandGitFetch,
		CommandGitPullBase,
		CommandGitPush,
		CommandGitMerge,
		CommandGitCheckout,
		CommandGitAbortMerge,
		CommandGitDiffStat,
		CommandGitStatus,
		CommandGitRuntimeSignals,
		CommandGitMergePreflight,
		CommandGitWorktreeForBranch,
		CommandGitDiscardChanges,
		CommandGitCheckpoint,
		CommandWorktreeList,
		CommandWorktreeCreate,
		CommandWorktreeRemove,
		CommandWorktreeCleanupOrphaned,
		CommandDevServerStart,
		CommandDevServerStop,
		CommandDevServerStatus,
		CommandDevServerList,
		protocol.CommandIssueFanout,
		protocol.CommandIssueFanoutDrift,
		protocol.CommandMailSend,
		protocol.CommandMailList,
		protocol.CommandMailWatch,
		protocol.CommandHookLogAppend,
		protocol.CommandHookLogList,
		protocol.CommandRuntimeSignalIngest,
		protocol.CommandUIOpenTaskWorkspace,
		protocol.CommandUIOpenTaskDrillDown,
		protocol.CommandUIStateGet,
		protocol.CommandUIStateSet,
		protocol.CommandBoardViewList,
		protocol.CommandBoardViewGet,
		protocol.CommandBoardViewSave,
		protocol.CommandBoardViewDelete,
		protocol.CommandBoardViewSelect,
		protocol.CommandProjectCleanup,
		protocol.CommandNoticeList,
		protocol.CommandNoticeGet,
		protocol.CommandNoticeUpdate,
		protocol.CommandNoticeAction,
		commandBoardFetch,
		protocol.CommandScheduledScriptsStatus,
		commandTaskList,
		commandTaskGet,
		commandTaskGetMany,
		commandTaskCreate,
		commandTaskClose,
		commandTaskBulkCleanup,
		commandTaskClosePreflight,
		commandTaskDeletePreflight,
		commandTaskGraphReadiness,
		commandTaskCompleteCheck,
		commandTaskIntegrationReady,
		commandTaskContextRisk,
		commandTaskMergeBaseTarget,
		commandTaskFollowOnMerge,
		commandTaskClaimOwnership,
		commandTaskReleaseOwnership,
		commandTaskUpdateStatus,
		commandTaskUpdateDetails,
		commandTaskAppendNotes,
		commandTaskDelete,
		commandTaskArchive,
		commandTaskUnarchive,
		commandTaskDependencyAdd,
		commandTaskDependencyRemove,
		commandTaskSQLiteWAL,
		commandTaskSnapshotExport,
		commandSyncRun,
		commandSyncConflicts,
		protocol.CommandTaskBulkApply,
		protocol.CommandAIAccountBackup,
		protocol.CommandAIAccountList,
		protocol.CommandAIAccountStatus,
		protocol.CommandAIAccountActivate,
		protocol.CommandAIAccountDelete,
	}

	registered := RegisteredCommands()
	if len(registered) != len(knownCommands) {
		t.Fatalf("registered command count = %d, want %d", len(registered), len(knownCommands))
	}

	for _, command := range knownCommands {
		if _, ok := LookupCommandSpec(command); !ok {
			t.Fatalf("expected registry entry for %q", command)
		}
	}
	for _, command := range registered {
		if !slices.Contains(knownCommands, command) {
			t.Fatalf("registry contains unexpected command %q", command)
		}
	}
}

func TestCommandSpecRegistryValidation(t *testing.T) {
	if err := ValidateCommandSpecs(); err != nil {
		t.Fatalf("ValidateCommandSpecs() error = %v", err)
	}
}

func TestDispatcherWiringValidation(t *testing.T) {
	if err := ValidateDispatcherWiring(nil); err == nil {
		t.Fatal("expected nil dispatcher validation error")
	}

	incomplete := &Dispatcher{}
	if err := ValidateDispatcherWiring(incomplete); err == nil {
		t.Fatal("expected missing handler validation error")
	}

	complete := &Dispatcher{
		session:     &SessionHandler{},
		git:         &GitHandler{},
		pr:          &PRHandler{},
		spec:        &SpecHandler{},
		decision:    &DecisionHandler{},
		learn:       &LearnHandler{},
		operation:   &routeOperationHandler{},
		worktree:    &WorktreeHandler{},
		devserver:   &DevServerHandler{},
		aiAccount:   &AIAccountHandler{},
		interaction: &InteractionHandler{},
	}
	if err := ValidateDispatcherWiring(complete); err != nil {
		t.Fatalf("expected complete dispatcher wiring validation to pass: %v", err)
	}
}
