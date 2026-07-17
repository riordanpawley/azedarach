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
	CommandDispatchAIAccount
	CommandDispatchInteraction
)

// CommandSpec describes daemon policy metadata and dispatch ownership.
type CommandSpec struct {
	Command           string
	DispatchTarget    CommandDispatchTarget
	RequiresProjectID bool
}

const (
	commandBoardFetch            = protocol.CommandBoardFetch
	commandTaskList              = "task.list"
	commandTaskGet               = "task.get"
	commandTaskGetMany           = "task.get_many"
	commandTaskCreate            = "task.create"
	commandTaskClose             = "task.close"
	commandTaskBulkCleanup       = protocol.CommandTaskBulkCleanup
	commandTaskClosePreflight    = "task.close_preflight"
	commandTaskDeletePreflight   = "task.delete_preflight"
	commandTaskGraphReadiness    = "task.graph_readiness"
	commandOrchestrationSnapshot = protocol.CommandOrchestrationSnapshot
	commandOrchestrationIntent   = protocol.CommandOrchestrationIntent
	commandTaskCompleteCheck     = "task.complete_check"
	commandTaskIntegrationReady  = "task.integration_readiness"
	commandTaskContextRisk       = "task.context_risk"
	commandTaskMergeBaseTarget   = "task.merge_base_target"
	commandTaskFollowOnMerge     = "task.follow_on_merge_candidates"
	commandTaskClaimOwnership    = "task.ownership.claim"
	commandTaskReleaseOwnership  = "task.ownership.release"
	commandTaskUpdateStatus      = "task.update_status"
	commandTaskUpdateDetails     = "task.update_details"
	commandTaskAppendNotes       = "task.append_notes"
	commandTaskDelete            = "task.delete"
	commandTaskArchive           = "task.archive"
	commandTaskUnarchive         = "task.unarchive"
	commandTaskDependencyAdd     = "task.dependency.add"
	commandTaskDependencyRemove  = "task.dependency.remove"
	commandTaskSQLiteWAL         = protocol.CommandTaskSQLiteWAL
	commandTaskSnapshotExport    = "task.snapshot.export"
	commandSyncRun               = "sync.run"
	commandSyncConflicts         = "sync.conflicts"
	commandSessionStatus         = "session.status"
	commandSessionRecover        = "session.recover"
	commandRuntimeReconcile      = "runtime.reconcile"
	commandRuntimeReconcileIssue = "runtime.reconcile_issue"
	commandProjectionDeltaList   = protocol.CommandProjectionDeltaList
	commandProjectionDeltaWatch  = protocol.CommandProjectionDeltaWatch
	commandProjectionSnapshot    = protocol.CommandProjectionSnapshot
)

var commandSpecRegistry = map[string]CommandSpec{
	commandProjectionDeltaList:              {Command: commandProjectionDeltaList, RequiresProjectID: true},
	commandProjectionDeltaWatch:             {Command: commandProjectionDeltaWatch, RequiresProjectID: true},
	commandProjectionSnapshot:               {Command: commandProjectionSnapshot, RequiresProjectID: true},
	commandOrchestrationSnapshot:            {Command: commandOrchestrationSnapshot, RequiresProjectID: true},
	commandOrchestrationIntent:              {Command: commandOrchestrationIntent, RequiresProjectID: true},
	commandTaskBulkCleanup:                  {Command: commandTaskBulkCleanup, RequiresProjectID: true},
	CommandSessionStart:                     {Command: CommandSessionStart, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionAttach:                    {Command: CommandSessionAttach, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionPause:                     {Command: CommandSessionPause, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionResume:                    {Command: CommandSessionResume, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionStop:                      {Command: CommandSessionStop, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionMessage:                   {Command: CommandSessionMessage, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionResolveConflict:           {Command: CommandSessionResolveConflict, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionRestartAll:                {Command: CommandSessionRestartAll, DispatchTarget: CommandDispatchSession, RequiresProjectID: true},
	CommandSessionCapture:                   {Command: CommandSessionCapture, RequiresProjectID: true},
	commandSessionStatus:                    {Command: commandSessionStatus, RequiresProjectID: true},
	commandSessionRecover:                   {Command: commandSessionRecover, RequiresProjectID: true},
	commandRuntimeReconcile:                 {Command: commandRuntimeReconcile, RequiresProjectID: true},
	commandRuntimeReconcileIssue:            {Command: commandRuntimeReconcileIssue, RequiresProjectID: true},
	protocol.CommandOperationSubmit:         {Command: protocol.CommandOperationSubmit, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	protocol.CommandOperationGet:            {Command: protocol.CommandOperationGet, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	protocol.CommandOperationList:           {Command: protocol.CommandOperationList, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	protocol.CommandOperationQueue:          {Command: protocol.CommandOperationQueue, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	protocol.CommandOperationCancel:         {Command: protocol.CommandOperationCancel, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	CommandPRCreate:                         {Command: CommandPRCreate, DispatchTarget: CommandDispatchPR, RequiresProjectID: true},
	CommandPRGet:                            {Command: CommandPRGet, DispatchTarget: CommandDispatchPR, RequiresProjectID: true},
	CommandPRList:                           {Command: CommandPRList, DispatchTarget: CommandDispatchPR, RequiresProjectID: true},
	CommandPRChecks:                         {Command: CommandPRChecks, DispatchTarget: CommandDispatchPR, RequiresProjectID: true},
	CommandPROpen:                           {Command: CommandPROpen, DispatchTarget: CommandDispatchPR, RequiresProjectID: true},
	CommandPRMerge:                          {Command: CommandPRMerge, DispatchTarget: CommandDispatchPR, RequiresProjectID: true},
	CommandGitBranchBehind:                  {Command: CommandGitBranchBehind, DispatchTarget: CommandDispatchPR, RequiresProjectID: true},
	protocol.CommandSpecRequirementList:     {Command: protocol.CommandSpecRequirementList, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementGet:      {Command: protocol.CommandSpecRequirementGet, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementCreate:   {Command: protocol.CommandSpecRequirementCreate, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementUpdate:   {Command: protocol.CommandSpecRequirementUpdate, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementDelete:   {Command: protocol.CommandSpecRequirementDelete, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLinkList:            {Command: protocol.CommandSpecLinkList, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLinkAdd:             {Command: protocol.CommandSpecLinkAdd, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLinkRemove:          {Command: protocol.CommandSpecLinkRemove, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRead:                {Command: protocol.CommandSpecRead, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecPack:                {Command: protocol.CommandSpecPack, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLint:                {Command: protocol.CommandSpecLint, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecParity:              {Command: protocol.CommandSpecParity, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecExport:              {Command: protocol.CommandSpecExport, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecSync:                {Command: protocol.CommandSpecSync, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecSyncMD:              {Command: protocol.CommandSpecSyncMD, DispatchTarget: CommandDispatchSpec},
	protocol.CommandDecisionList:            {Command: protocol.CommandDecisionList, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionGet:             {Command: protocol.CommandDecisionGet, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionRecord:          {Command: protocol.CommandDecisionRecord, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionUpdate:          {Command: protocol.CommandDecisionUpdate, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionDelete:          {Command: protocol.CommandDecisionDelete, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionLinkList:        {Command: protocol.CommandDecisionLinkList, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionLinkAdd:         {Command: protocol.CommandDecisionLinkAdd, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionLinkRemove:      {Command: protocol.CommandDecisionLinkRemove, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionSyncMD:          {Command: protocol.CommandDecisionSyncMD, DispatchTarget: CommandDispatchDecision},
	protocol.CommandDecisionImportMD:        {Command: protocol.CommandDecisionImportMD, DispatchTarget: CommandDispatchDecision},
	protocol.CommandInteractionCreate:       {Command: protocol.CommandInteractionCreate, DispatchTarget: CommandDispatchInteraction, RequiresProjectID: true},
	protocol.CommandInteractionList:         {Command: protocol.CommandInteractionList, DispatchTarget: CommandDispatchInteraction, RequiresProjectID: true},
	protocol.CommandInteractionGet:          {Command: protocol.CommandInteractionGet, DispatchTarget: CommandDispatchInteraction, RequiresProjectID: true},
	protocol.CommandInteractionDiscuss:      {Command: protocol.CommandInteractionDiscuss, DispatchTarget: CommandDispatchInteraction, RequiresProjectID: true},
	protocol.CommandInteractionPropose:      {Command: protocol.CommandInteractionPropose, DispatchTarget: CommandDispatchInteraction, RequiresProjectID: true},
	protocol.CommandInteractionAnswer:       {Command: protocol.CommandInteractionAnswer, DispatchTarget: CommandDispatchInteraction, RequiresProjectID: true},
	protocol.CommandInteractionResolve:      {Command: protocol.CommandInteractionResolve, DispatchTarget: CommandDispatchInteraction, RequiresProjectID: true},
	protocol.CommandInteractionWithdraw:     {Command: protocol.CommandInteractionWithdraw, DispatchTarget: CommandDispatchInteraction, RequiresProjectID: true},
	protocol.CommandInteractionSupersede:    {Command: protocol.CommandInteractionSupersede, DispatchTarget: CommandDispatchInteraction, RequiresProjectID: true},
	protocol.CommandInteractionRecover:      {Command: protocol.CommandInteractionRecover, DispatchTarget: CommandDispatchInteraction, RequiresProjectID: true},
	protocol.CommandLearnAdd:                {Command: protocol.CommandLearnAdd, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnCapture:            {Command: protocol.CommandLearnCapture, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnRecall:             {Command: protocol.CommandLearnRecall, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnShow:               {Command: protocol.CommandLearnShow, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnReview:             {Command: protocol.CommandLearnReview, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnPromote:            {Command: protocol.CommandLearnPromote, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnRetire:             {Command: protocol.CommandLearnRetire, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnRelate:             {Command: protocol.CommandLearnRelate, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnStale:              {Command: protocol.CommandLearnStale, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnDemote:             {Command: protocol.CommandLearnDemote, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnSupersede:          {Command: protocol.CommandLearnSupersede, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnDoctor:             {Command: protocol.CommandLearnDoctor, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnGC:                 {Command: protocol.CommandLearnGC, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnActivate:           {Command: protocol.CommandLearnActivate, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnFeedback:           {Command: protocol.CommandLearnFeedback, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnContextualActivate: {Command: protocol.CommandLearnContextualActivate, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnActivationConfirm:  {Command: protocol.CommandLearnActivationConfirm, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnActivationAbandon:  {Command: protocol.CommandLearnActivationAbandon, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnHealth:             {Command: protocol.CommandLearnHealth, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	CommandGitFetch:                         {Command: CommandGitFetch, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitPullBase:                      {Command: CommandGitPullBase, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitPush:                          {Command: CommandGitPush, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitMerge:                         {Command: CommandGitMerge, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitCheckout:                      {Command: CommandGitCheckout, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitAbortMerge:                    {Command: CommandGitAbortMerge, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitDiffStat:                      {Command: CommandGitDiffStat, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitStatus:                        {Command: CommandGitStatus, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitRuntimeSignals:                {Command: CommandGitRuntimeSignals, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitMergePreflight:                {Command: CommandGitMergePreflight, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitWorktreeForBranch:             {Command: CommandGitWorktreeForBranch, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitDiscardChanges:                {Command: CommandGitDiscardChanges, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitCheckpoint:                    {Command: CommandGitCheckpoint, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandWorktreeList:                     {Command: CommandWorktreeList, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandWorktreeCreate:                   {Command: CommandWorktreeCreate, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandWorktreeRemove:                   {Command: CommandWorktreeRemove, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandWorktreeCleanupOrphaned:          {Command: CommandWorktreeCleanupOrphaned, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandDevServerStart:                   {Command: CommandDevServerStart, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	CommandDevServerStop:                    {Command: CommandDevServerStop, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	CommandDevServerStatus:                  {Command: CommandDevServerStatus, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	CommandDevServerList:                    {Command: CommandDevServerList, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	protocol.CommandAIAccountBackup:         {Command: protocol.CommandAIAccountBackup, DispatchTarget: CommandDispatchAIAccount},
	protocol.CommandAIAccountList:           {Command: protocol.CommandAIAccountList, DispatchTarget: CommandDispatchAIAccount},
	protocol.CommandAIAccountStatus:         {Command: protocol.CommandAIAccountStatus, DispatchTarget: CommandDispatchAIAccount},
	protocol.CommandAIAccountActivate:       {Command: protocol.CommandAIAccountActivate, DispatchTarget: CommandDispatchAIAccount},
	protocol.CommandAIAccountDelete:         {Command: protocol.CommandAIAccountDelete, DispatchTarget: CommandDispatchAIAccount},
	protocol.CommandIssueFanout:             {Command: protocol.CommandIssueFanout, RequiresProjectID: true},
	protocol.CommandIssueFanoutDrift:        {Command: protocol.CommandIssueFanoutDrift, RequiresProjectID: true},
	protocol.CommandMailSend:                {Command: protocol.CommandMailSend, RequiresProjectID: true},
	protocol.CommandMailList:                {Command: protocol.CommandMailList, RequiresProjectID: true},
	protocol.CommandMailWatch:               {Command: protocol.CommandMailWatch, RequiresProjectID: true},
	protocol.CommandHookLogAppend:           {Command: protocol.CommandHookLogAppend, RequiresProjectID: true},
	protocol.CommandHookLogList:             {Command: protocol.CommandHookLogList, RequiresProjectID: true},
	protocol.CommandRuntimeSignalIngest:     {Command: protocol.CommandRuntimeSignalIngest, RequiresProjectID: true},
	protocol.CommandValidationAcquire:       {Command: protocol.CommandValidationAcquire, RequiresProjectID: true},
	protocol.CommandValidationHeartbeat:     {Command: protocol.CommandValidationHeartbeat, RequiresProjectID: true},
	protocol.CommandValidationNested:        {Command: protocol.CommandValidationNested, RequiresProjectID: true},
	protocol.CommandValidationFinish:        {Command: protocol.CommandValidationFinish, RequiresProjectID: true},
	protocol.CommandValidationStatus:        {Command: protocol.CommandValidationStatus, RequiresProjectID: true},
	protocol.CommandUIOpenTaskWorkspace:     {Command: protocol.CommandUIOpenTaskWorkspace, RequiresProjectID: true},
	protocol.CommandUIOpenTaskDrillDown:     {Command: protocol.CommandUIOpenTaskDrillDown, RequiresProjectID: true},
	protocol.CommandUIStateGet:              {Command: protocol.CommandUIStateGet, RequiresProjectID: true},
	protocol.CommandUIStateSet:              {Command: protocol.CommandUIStateSet, RequiresProjectID: true},
	protocol.CommandBoardViewList:           {Command: protocol.CommandBoardViewList, RequiresProjectID: true},
	protocol.CommandBoardViewGet:            {Command: protocol.CommandBoardViewGet, RequiresProjectID: true},
	protocol.CommandBoardViewSave:           {Command: protocol.CommandBoardViewSave, RequiresProjectID: true},
	protocol.CommandBoardViewDelete:         {Command: protocol.CommandBoardViewDelete, RequiresProjectID: true},
	protocol.CommandBoardViewSelect:         {Command: protocol.CommandBoardViewSelect, RequiresProjectID: true},
	protocol.CommandProjectCleanup:          {Command: protocol.CommandProjectCleanup, RequiresProjectID: true},
	protocol.CommandNoticeList:              {Command: protocol.CommandNoticeList, RequiresProjectID: true},
	protocol.CommandNoticeGet:               {Command: protocol.CommandNoticeGet, RequiresProjectID: true},
	protocol.CommandNoticeUpdate:            {Command: protocol.CommandNoticeUpdate, RequiresProjectID: true},
	protocol.CommandNoticeAction:            {Command: protocol.CommandNoticeAction, RequiresProjectID: true},
	commandBoardFetch:                       {Command: commandBoardFetch, RequiresProjectID: true},
	protocol.CommandScheduledScriptsStatus:  {Command: protocol.CommandScheduledScriptsStatus, RequiresProjectID: true},
	commandTaskList:                         {Command: commandTaskList, RequiresProjectID: true},
	commandTaskGet:                          {Command: commandTaskGet, RequiresProjectID: true},
	commandTaskGetMany:                      {Command: commandTaskGetMany, RequiresProjectID: true},
	commandTaskCreate:                       {Command: commandTaskCreate, RequiresProjectID: true},
	commandTaskClose:                        {Command: commandTaskClose, RequiresProjectID: true},
	commandTaskClosePreflight:               {Command: commandTaskClosePreflight, RequiresProjectID: true},
	commandTaskDeletePreflight:              {Command: commandTaskDeletePreflight, RequiresProjectID: true},
	commandTaskGraphReadiness:               {Command: commandTaskGraphReadiness, RequiresProjectID: true},
	commandTaskCompleteCheck:                {Command: commandTaskCompleteCheck, RequiresProjectID: true},
	commandTaskIntegrationReady:             {Command: commandTaskIntegrationReady, RequiresProjectID: true},
	commandTaskContextRisk:                  {Command: commandTaskContextRisk, RequiresProjectID: true},
	commandTaskMergeBaseTarget:              {Command: commandTaskMergeBaseTarget, RequiresProjectID: true},
	commandTaskFollowOnMerge:                {Command: commandTaskFollowOnMerge, RequiresProjectID: true},
	commandTaskClaimOwnership:               {Command: commandTaskClaimOwnership, RequiresProjectID: true},
	commandTaskReleaseOwnership:             {Command: commandTaskReleaseOwnership, RequiresProjectID: true},
	commandTaskUpdateStatus:                 {Command: commandTaskUpdateStatus, RequiresProjectID: true},
	commandTaskUpdateDetails:                {Command: commandTaskUpdateDetails, RequiresProjectID: true},
	commandTaskAppendNotes:                  {Command: commandTaskAppendNotes, RequiresProjectID: true},
	commandTaskDelete:                       {Command: commandTaskDelete, RequiresProjectID: true},
	commandTaskArchive:                      {Command: commandTaskArchive, RequiresProjectID: true},
	commandTaskUnarchive:                    {Command: commandTaskUnarchive, RequiresProjectID: true},
	commandTaskDependencyAdd:                {Command: commandTaskDependencyAdd, RequiresProjectID: true},
	commandTaskDependencyRemove:             {Command: commandTaskDependencyRemove, RequiresProjectID: true},
	commandTaskSQLiteWAL:                    {Command: commandTaskSQLiteWAL, RequiresProjectID: true},
	commandTaskSnapshotExport:               {Command: commandTaskSnapshotExport, RequiresProjectID: true},
	commandSyncRun:                          {Command: commandSyncRun, RequiresProjectID: true},
	commandSyncConflicts:                    {Command: commandSyncConflicts, RequiresProjectID: true},
	protocol.CommandTaskBulkApply:           {Command: protocol.CommandTaskBulkApply, RequiresProjectID: true},
	protocol.CommandLearnSuggest:            {Command: protocol.CommandLearnSuggest, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnConsolidate:        {Command: protocol.CommandLearnConsolidate, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
	protocol.CommandLearnSuggestionReject:   {Command: protocol.CommandLearnSuggestionReject, DispatchTarget: CommandDispatchLearn, RequiresProjectID: true},
}

func init() {
	for _, command := range []string{
		protocol.CommandPublicationEvidenceRecord,
		protocol.CommandPublicationEvidenceStatus,
		protocol.CommandPublicationEvidenceEvaluate,
	} {
		commandSpecRegistry[command] = CommandSpec{Command: command, RequiresProjectID: true}
	}
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
	case CommandDispatchOperation, CommandDispatchPR, CommandDispatchSpec, CommandDispatchDecision, CommandDispatchLearn, CommandDispatchGit, CommandDispatchWorktree, CommandDispatchDevServer, CommandDispatchAIAccount, CommandDispatchInteraction:
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
		case CommandDispatchNone, CommandDispatchSession, CommandDispatchOperation, CommandDispatchPR, CommandDispatchSpec, CommandDispatchDecision, CommandDispatchLearn, CommandDispatchGit, CommandDispatchWorktree, CommandDispatchDevServer, CommandDispatchAIAccount, CommandDispatchInteraction:
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
		case CommandDispatchAIAccount:
			if d.aiAccount == nil {
				return fmt.Errorf("command %q dispatches to AI account handler but dispatcher AI account handler is nil", command)
			}
		case CommandDispatchInteraction:
			if d.interaction == nil {
				return fmt.Errorf("command %q dispatches to interaction handler but dispatcher interaction handler is nil", command)
			}
		default:
			return fmt.Errorf("command %q has unknown dispatch target %d", command, spec.DispatchTarget)
		}
	}
	return nil
}
