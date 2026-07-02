package handlers

import (
	"fmt"
	"sort"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

// CommandDispatchTarget identifies the handler module for a command.
type CommandDispatchTarget int

const (
	CommandDispatchNone CommandDispatchTarget = iota
	CommandDispatchSession
	CommandDispatchOperation
	CommandDispatchPR
	CommandDispatchSpec
	CommandDispatchDecision
	CommandDispatchLearn
	CommandDispatchGit
	CommandDispatchWorktree
	CommandDispatchDevServer
)

// CommandSpec describes daemon policy metadata and dispatch ownership.
type CommandSpec struct {
	Command           string
	DispatchTarget    CommandDispatchTarget
	RequiresProjectID bool
}

const (
	commandTaskList              = "task.list"
	commandTaskGet               = "task.get"
	commandTaskGetMany           = "task.get_many"
	commandTaskCreate            = "task.create"
	commandTaskClose             = "task.close"
	commandTaskClosePreflight    = "task.close_preflight"
	commandTaskDeletePreflight   = "task.delete_preflight"
	commandTaskGraphReadiness    = "task.graph_readiness"
	commandTaskCompleteCheck     = "task.complete_check"
	commandTaskIntegrationReady  = "task.integration_readiness"
	commandTaskMergeBaseTarget   = "task.merge_base_target"
	commandTaskFollowOnMerge     = "task.follow_on_merge_candidates"
	commandTaskUpdateStatus      = "task.update_status"
	commandTaskUpdateDetails     = "task.update_details"
	commandTaskAppendNotes       = "task.append_notes"
	commandTaskDelete            = "task.delete"
	commandTaskArchive           = "task.archive"
	commandTaskDependencyAdd     = "task.dependency.add"
	commandTaskDependencyRemove  = "task.dependency.remove"
	commandTaskSnapshotExport    = "task.snapshot.export"
	commandSyncRun               = "sync.run"
	commandSyncConflicts         = "sync.conflicts"
	commandSessionStatus         = "session.status"
	commandSessionRecover        = "session.recover"
	commandRuntimeReconcile      = "runtime.reconcile"
	commandRuntimeReconcileIssue = "runtime.reconcile_issue"
)

var commandSpecRegistry = map[string]CommandSpec{
	CommandSessionStart:                    {Command: CommandSessionStart, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionAttach:                   {Command: CommandSessionAttach, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionPause:                    {Command: CommandSessionPause, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionResume:                   {Command: CommandSessionResume, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionStop:                     {Command: CommandSessionStop, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionMessage:                  {Command: CommandSessionMessage, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionResolveConflict:          {Command: CommandSessionResolveConflict, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionRestartAll:               {Command: CommandSessionRestartAll, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	commandSessionStatus:                   {Command: commandSessionStatus, RequiresProjectID: true},
	commandSessionRecover:                  {Command: commandSessionRecover, RequiresProjectID: true},
	commandRuntimeReconcile:                {Command: commandRuntimeReconcile, RequiresProjectID: true},
	commandRuntimeReconcileIssue:           {Command: commandRuntimeReconcileIssue, RequiresProjectID: true},
	protocol.CommandOperationSubmit:        {Command: protocol.CommandOperationSubmit, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	protocol.CommandOperationGet:           {Command: protocol.CommandOperationGet, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	protocol.CommandOperationList:          {Command: protocol.CommandOperationList, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	protocol.CommandOperationCancel:        {Command: protocol.CommandOperationCancel, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	CommandPRCreate:                        {Command: CommandPRCreate, DispatchTarget: CommandDispatchPR, RequiresProjectID: true},
	CommandGitBranchBehind:                 {Command: CommandGitBranchBehind, DispatchTarget: CommandDispatchPR, RequiresProjectID: true},
	protocol.CommandSpecRequirementList:    {Command: protocol.CommandSpecRequirementList, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementGet:     {Command: protocol.CommandSpecRequirementGet, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementCreate:  {Command: protocol.CommandSpecRequirementCreate, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementUpdate:  {Command: protocol.CommandSpecRequirementUpdate, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementDelete:  {Command: protocol.CommandSpecRequirementDelete, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLinkList:           {Command: protocol.CommandSpecLinkList, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLinkAdd:            {Command: protocol.CommandSpecLinkAdd, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLinkRemove:         {Command: protocol.CommandSpecLinkRemove, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRead:               {Command: protocol.CommandSpecRead, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecPack:               {Command: protocol.CommandSpecPack, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLint:               {Command: protocol.CommandSpecLint, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecParity:             {Command: protocol.CommandSpecParity, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecExport:             {Command: protocol.CommandSpecExport, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecSync:               {Command: protocol.CommandSpecSync, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecSyncMD:             {Command: protocol.CommandSpecSyncMD, DispatchTarget: CommandDispatchSpec},
	protocol.CommandDecisionList:           {Command: protocol.CommandDecisionList, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionGet:            {Command: protocol.CommandDecisionGet, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionRecord:         {Command: protocol.CommandDecisionRecord, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionUpdate:         {Command: protocol.CommandDecisionUpdate, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionDelete:         {Command: protocol.CommandDecisionDelete, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionLinkList:       {Command: protocol.CommandDecisionLinkList, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionLinkAdd:        {Command: protocol.CommandDecisionLinkAdd, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionLinkRemove:     {Command: protocol.CommandDecisionLinkRemove, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionSyncMD:         {Command: protocol.CommandDecisionSyncMD, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionImportMD:       {Command: protocol.CommandDecisionImportMD, DispatchTarget: CommandDispatchDecision},
	protocol.CommandLearnAdd:               {Command: protocol.CommandLearnAdd, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnRecall:            {Command: protocol.CommandLearnRecall, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnShow:              {Command: protocol.CommandLearnShow, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnReview:            {Command: protocol.CommandLearnReview, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnPromote:           {Command: protocol.CommandLearnPromote, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnRetire:            {Command: protocol.CommandLearnRetire, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnRelate:            {Command: protocol.CommandLearnRelate, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	CommandGitFetch:                        {Command: CommandGitFetch, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitMerge:                        {Command: CommandGitMerge, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitCheckout:                     {Command: CommandGitCheckout, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitAbortMerge:                   {Command: CommandGitAbortMerge, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitDiffStat:                     {Command: CommandGitDiffStat, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitStatus:                       {Command: CommandGitStatus, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitRuntimeSignals:               {Command: CommandGitRuntimeSignals, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitMergePreflight:               {Command: CommandGitMergePreflight, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitWorktreeForBranch:            {Command: CommandGitWorktreeForBranch, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitDiscardChanges:               {Command: CommandGitDiscardChanges, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitCheckpoint:                   {Command: CommandGitCheckpoint, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandWorktreeList:                    {Command: CommandWorktreeList, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandWorktreeCreate:                  {Command: CommandWorktreeCreate, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandWorktreeRemove:                  {Command: CommandWorktreeRemove, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandWorktreeCleanupOrphaned:         {Command: CommandWorktreeCleanupOrphaned, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandDevServerStart:                  {Command: CommandDevServerStart, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	CommandDevServerStop:                   {Command: CommandDevServerStop, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	CommandDevServerStatus:                 {Command: CommandDevServerStatus, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	CommandDevServerList:                   {Command: CommandDevServerList, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	protocol.CommandIssueFanout:            {Command: protocol.CommandIssueFanout, RequiresProjectID: true},
	protocol.CommandIssueFanoutDrift:       {Command: protocol.CommandIssueFanoutDrift, RequiresProjectID: true},
	protocol.CommandMailSend:               {Command: protocol.CommandMailSend, RequiresProjectID: true},
	protocol.CommandMailList:               {Command: protocol.CommandMailList, RequiresProjectID: true},
	protocol.CommandMailWatch:              {Command: protocol.CommandMailWatch, RequiresProjectID: true},
	protocol.CommandHookLogAppend:          {Command: protocol.CommandHookLogAppend, RequiresProjectID: true},
	protocol.CommandHookLogList:            {Command: protocol.CommandHookLogList, RequiresProjectID: true},
	protocol.CommandRuntimeSignalIngest:    {Command: protocol.CommandRuntimeSignalIngest, RequiresProjectID: true},
	protocol.CommandUIOpenTaskWorkspace:    {Command: protocol.CommandUIOpenTaskWorkspace, RequiresProjectID: true},
	protocol.CommandUIOpenTaskDrillDown:    {Command: protocol.CommandUIOpenTaskDrillDown, RequiresProjectID: true},
	protocol.CommandUIStateGet:             {Command: protocol.CommandUIStateGet, RequiresProjectID: true},
	protocol.CommandUIStateSet:             {Command: protocol.CommandUIStateSet, RequiresProjectID: true},
	protocol.CommandProjectCleanup:         {Command: protocol.CommandProjectCleanup, RequiresProjectID: true},
	protocol.CommandScheduledScriptsStatus: {Command: protocol.CommandScheduledScriptsStatus, RequiresProjectID: true},
	commandTaskList:                        {Command: commandTaskList, RequiresProjectID: true},
	commandTaskGet:                         {Command: commandTaskGet, RequiresProjectID: true},
	commandTaskGetMany:                     {Command: commandTaskGetMany, RequiresProjectID: true},
	commandTaskCreate:                      {Command: commandTaskCreate, RequiresProjectID: true},
	commandTaskClose:                       {Command: commandTaskClose, RequiresProjectID: true},
	commandTaskClosePreflight:              {Command: commandTaskClosePreflight, RequiresProjectID: true},
	commandTaskDeletePreflight:             {Command: commandTaskDeletePreflight, RequiresProjectID: true},
	commandTaskGraphReadiness:              {Command: commandTaskGraphReadiness, RequiresProjectID: true},
	commandTaskCompleteCheck:               {Command: commandTaskCompleteCheck, RequiresProjectID: true},
	commandTaskIntegrationReady:            {Command: commandTaskIntegrationReady, RequiresProjectID: true},
	commandTaskMergeBaseTarget:             {Command: commandTaskMergeBaseTarget, RequiresProjectID: true},
	commandTaskFollowOnMerge:               {Command: commandTaskFollowOnMerge, RequiresProjectID: true},
	commandTaskUpdateStatus:                {Command: commandTaskUpdateStatus, RequiresProjectID: true},
	commandTaskUpdateDetails:               {Command: commandTaskUpdateDetails, RequiresProjectID: true},
	commandTaskAppendNotes:                 {Command: commandTaskAppendNotes, RequiresProjectID: true},
	commandTaskDelete:                      {Command: commandTaskDelete, RequiresProjectID: true},
	commandTaskArchive:                     {Command: commandTaskArchive, RequiresProjectID: true},
	commandTaskDependencyAdd:               {Command: commandTaskDependencyAdd, RequiresProjectID: true},
	commandTaskDependencyRemove:            {Command: commandTaskDependencyRemove, RequiresProjectID: true},
	commandTaskSnapshotExport:              {Command: commandTaskSnapshotExport, RequiresProjectID: true},
	commandSyncRun:                         {Command: commandSyncRun, RequiresProjectID: true},
	commandSyncConflicts:                   {Command: commandSyncConflicts, RequiresProjectID: true},
	protocol.CommandTaskBulkApply:          {Command: protocol.CommandTaskBulkApply, RequiresProjectID: true},
}

// LookupCommandSpec returns the typed command specification for a command.
func LookupCommandSpec(command string) (CommandSpec, bool) {
	spec, ok := commandSpecRegistry[strings.TrimSpace(command)]
	return spec, ok
}

// CommandRequiresProjectID reports whether a command requires metadata.project_id.
func CommandRequiresProjectID(command string) bool {
	spec, ok := LookupCommandSpec(command)
	return ok && spec.RequiresProjectID
}

// DispatcherTarget returns the handler module responsible for a command.
func DispatcherTarget(command string) (CommandDispatchTarget, bool) {
	spec, ok := LookupCommandSpec(command)
	if !ok || spec.DispatchTarget == CommandDispatchNone {
		return CommandDispatchNone, false
	}
	return spec.DispatchTarget, true
}

// DaemonRoutesThroughDispatcher reports whether daemon command() should delegate to dispatcher.
func DaemonRoutesThroughDispatcher(command string) bool {
	target, ok := DispatcherTarget(command)
	if !ok {
		return false
	}
	switch target {
	case CommandDispatchOperation, CommandDispatchPR, CommandDispatchSpec, CommandDispatchDecision, CommandDispatchLearn, CommandDispatchGit, CommandDispatchWorktree, CommandDispatchDevServer:
		return true
	default:
		return false
	}
}

// RegisteredCommands returns all commands in the registry sorted lexicographically.
func RegisteredCommands() []string {
	commands := make([]string, 0, len(commandSpecRegistry))
	for command := range commandSpecRegistry {
		commands = append(commands, command)
	}
	sort.Strings(commands)
	return commands
}

// ValidateCommandSpecs verifies command registry invariants.
func ValidateCommandSpecs() error {
	for key, spec := range commandSpecRegistry {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			return fmt.Errorf("command spec registry contains empty command key")
		}
		if trimmedKey != key {
			return fmt.Errorf("command spec key %q contains surrounding whitespace", key)
		}
		if strings.TrimSpace(spec.Command) == "" {
			return fmt.Errorf("command spec %q has empty Command field", key)
		}
		if spec.Command != key {
			return fmt.Errorf("command spec %q declares mismatched Command value %q", key, spec.Command)
		}
		switch spec.DispatchTarget {
		case CommandDispatchNone, CommandDispatchSession, CommandDispatchOperation, CommandDispatchPR, CommandDispatchSpec, CommandDispatchDecision, CommandDispatchLearn, CommandDispatchGit, CommandDispatchWorktree, CommandDispatchDevServer:
		default:
			return fmt.Errorf("command spec %q declares unknown dispatch target %d", key, spec.DispatchTarget)
		}
	}
	return nil
}

// ValidateDispatcherWiring verifies that registered dispatch targets have handlers.
func ValidateDispatcherWiring(d *Dispatcher) error {
	if d == nil {
		return fmt.Errorf("dispatcher is nil")
	}
	for command, spec := range commandSpecRegistry {
		switch spec.DispatchTarget {
		case CommandDispatchNone:
		case CommandDispatchSession:
			if d.session == nil {
				return fmt.Errorf("command %q dispatches to session handler but dispatcher session handler is nil", command)
			}
		case CommandDispatchOperation:
			if d.operation == nil {
				return fmt.Errorf("command %q dispatches to operation handler but dispatcher operation handler is nil", command)
			}
		case CommandDispatchPR:
			if d.pr == nil {
				return fmt.Errorf("command %q dispatches to PR handler but dispatcher PR handler is nil", command)
			}
		case CommandDispatchSpec:
			if d.spec == nil {
				return fmt.Errorf("command %q dispatches to spec handler but dispatcher spec handler is nil", command)
			}
		case CommandDispatchDecision:
			if d.decision == nil {
				return fmt.Errorf("command %q dispatches to decision handler but dispatcher decision handler is nil", command)
			}
		case CommandDispatchLearn:
			if d.learn == nil {
				return fmt.Errorf("command %q dispatches to learn handler but dispatcher learn handler is nil", command)
			}
		case CommandDispatchGit:
			if d.git == nil {
				return fmt.Errorf("command %q dispatches to git handler but dispatcher git handler is nil", command)
			}
		case CommandDispatchWorktree:
			if d.worktree == nil {
				return fmt.Errorf("command %q dispatches to worktree handler but dispatcher worktree handler is nil", command)
			}
		case CommandDispatchDevServer:
			if d.devserver == nil {
				return fmt.Errorf("command %q dispatches to devserver handler but dispatcher devserver handler is nil", command)
			}
		default:
			return fmt.Errorf("command %q has unknown dispatch target %d", command, spec.DispatchTarget)
		}
	}
	return nil
}
