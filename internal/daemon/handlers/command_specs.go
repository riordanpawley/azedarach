package handlers

import (
	"encoding/json"
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
	CommandDispatchGit
	CommandDispatchWorktree
	CommandDispatchDevServer
)

type syncBootstrapPolicy int

const (
	syncBootstrapPolicyNever syncBootstrapPolicy = iota
	syncBootstrapPolicyAlways
	syncBootstrapPolicyOperationKind
)

// CommandSpec describes daemon policy metadata and dispatch ownership.
type CommandSpec struct {
	Command             string
	DispatchTarget      CommandDispatchTarget
	RequiresProjectID   bool
	syncBootstrapPolicy syncBootstrapPolicy
}

const (
	commandTaskList             = "task.list"
	commandTaskCreate           = "task.create"
	commandTaskUpdateStatus     = "task.update_status"
	commandTaskUpdateDetails    = "task.update_details"
	commandTaskAppendNotes      = "task.append_notes"
	commandTaskDelete           = "task.delete"
	commandTaskArchive          = "task.archive"
	commandTaskDependencyAdd    = "task.dependency.add"
	commandTaskDependencyRemove = "task.dependency.remove"
	commandTaskSnapshotExport   = "task.snapshot.export"
	commandSessionStatus        = "session.status"
	commandSessionRecover       = "session.recover"
)

var commandSpecRegistry = map[string]CommandSpec{
	CommandSessionStart:               {Command: CommandSessionStart, DispatchTarget: CommandDispatchSession, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	CommandSessionAttach:              {Command: CommandSessionAttach, DispatchTarget: CommandDispatchSession, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	CommandSessionPause:               {Command: CommandSessionPause, DispatchTarget: CommandDispatchSession, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	CommandSessionResume:              {Command: CommandSessionResume, DispatchTarget: CommandDispatchSession, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	CommandSessionStop:                {Command: CommandSessionStop, DispatchTarget: CommandDispatchSession, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	commandSessionStatus:              {Command: commandSessionStatus, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	commandSessionRecover:             {Command: commandSessionRecover, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	protocol.CommandOperationSubmit:   {Command: protocol.CommandOperationSubmit, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyOperationKind},
	protocol.CommandOperationGet:      {Command: protocol.CommandOperationGet, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	protocol.CommandOperationList:     {Command: protocol.CommandOperationList, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	protocol.CommandOperationCancel:   {Command: protocol.CommandOperationCancel, DispatchTarget: CommandDispatchOperation, RequiresProjectID: true},
	CommandPRCreate:                   {Command: CommandPRCreate, DispatchTarget: CommandDispatchPR, RequiresProjectID: true},
	CommandGitBranchBehind:            {Command: CommandGitBranchBehind, DispatchTarget: CommandDispatchPR, RequiresProjectID: true},
	protocol.CommandSpecRequirementList:   {Command: protocol.CommandSpecRequirementList, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementGet:    {Command: protocol.CommandSpecRequirementGet, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementCreate: {Command: protocol.CommandSpecRequirementCreate, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementUpdate: {Command: protocol.CommandSpecRequirementUpdate, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRequirementDelete: {Command: protocol.CommandSpecRequirementDelete, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLinkList:          {Command: protocol.CommandSpecLinkList, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLinkAdd:           {Command: protocol.CommandSpecLinkAdd, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLinkRemove:        {Command: protocol.CommandSpecLinkRemove, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecRead:              {Command: protocol.CommandSpecRead, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecLint:              {Command: protocol.CommandSpecLint, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecParity:            {Command: protocol.CommandSpecParity, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecSync:              {Command: protocol.CommandSpecSync, DispatchTarget: CommandDispatchSpec},
	protocol.CommandSpecSyncMD:            {Command: protocol.CommandSpecSyncMD, DispatchTarget: CommandDispatchSpec},
	CommandGitFetch:                  {Command: CommandGitFetch, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitMerge:                  {Command: CommandGitMerge, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitCheckout:               {Command: CommandGitCheckout, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitAbortMerge:             {Command: CommandGitAbortMerge, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitDiffStat:               {Command: CommandGitDiffStat, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitStatus:                 {Command: CommandGitStatus, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitRuntimeSignals:         {Command: CommandGitRuntimeSignals, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitMergePreflight:         {Command: CommandGitMergePreflight, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitDiscardChanges:         {Command: CommandGitDiscardChanges, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandGitCheckpoint:             {Command: CommandGitCheckpoint, DispatchTarget: CommandDispatchGit, RequiresProjectID: true},
	CommandWorktreeList:              {Command: CommandWorktreeList, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandWorktreeCreate:            {Command: CommandWorktreeCreate, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandWorktreeRemove:            {Command: CommandWorktreeRemove, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandWorktreeCleanupOrphaned:   {Command: CommandWorktreeCleanupOrphaned, DispatchTarget: CommandDispatchWorktree, RequiresProjectID: true},
	CommandDevServerStart:            {Command: CommandDevServerStart, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	CommandDevServerStop:             {Command: CommandDevServerStop, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	CommandDevServerStatus:           {Command: CommandDevServerStatus, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	CommandDevServerList:             {Command: CommandDevServerList, DispatchTarget: CommandDispatchDevServer, RequiresProjectID: true},
	protocol.CommandIssueFanout:      {Command: protocol.CommandIssueFanout, RequiresProjectID: true},
	protocol.CommandIssueFanoutDrift: {Command: protocol.CommandIssueFanoutDrift, RequiresProjectID: true},
	protocol.CommandMailSend:         {Command: protocol.CommandMailSend, RequiresProjectID: true},
	protocol.CommandMailList:         {Command: protocol.CommandMailList, RequiresProjectID: true},
	protocol.CommandMailWatch:        {Command: protocol.CommandMailWatch, RequiresProjectID: true},
	commandTaskList:                  {Command: commandTaskList, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	commandTaskCreate:                {Command: commandTaskCreate, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	commandTaskUpdateStatus:          {Command: commandTaskUpdateStatus, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	commandTaskUpdateDetails:         {Command: commandTaskUpdateDetails, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	commandTaskAppendNotes:           {Command: commandTaskAppendNotes, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	commandTaskDelete:                {Command: commandTaskDelete, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	commandTaskArchive:               {Command: commandTaskArchive, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	commandTaskDependencyAdd:         {Command: commandTaskDependencyAdd, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	commandTaskDependencyRemove:      {Command: commandTaskDependencyRemove, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	commandTaskSnapshotExport:        {Command: commandTaskSnapshotExport, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
	protocol.CommandTaskBulkApply:    {Command: protocol.CommandTaskBulkApply, RequiresProjectID: true, syncBootstrapPolicy: syncBootstrapPolicyAlways},
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

// CommandRequiresSyncBootstrap reports whether sync bootstrap readiness is required.
func CommandRequiresSyncBootstrap(req protocol.RequestEnvelope) bool {
	spec, ok := LookupCommandSpec(req.Command)
	if !ok {
		return false
	}
	switch spec.syncBootstrapPolicy {
	case syncBootstrapPolicyAlways:
		return true
	case syncBootstrapPolicyOperationKind:
		return syncDependentOperationKind(req.Body)
	default:
		return false
	}
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
	case CommandDispatchOperation, CommandDispatchPR, CommandDispatchSpec, CommandDispatchGit, CommandDispatchWorktree, CommandDispatchDevServer:
		return true
	default:
		return false
	}
}

func syncDependentOperationKind(body []byte) bool {
	var req protocol.OperationSubmitRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	kind := strings.TrimSpace(req.Kind)
	return strings.HasPrefix(kind, "task.") || strings.HasPrefix(kind, "session.")
}
