package gitpr

import (
	"context"
	"errors"
	"fmt"

	adapterrors "github.com/riordanpawley/azedarach/internal/adapters/errors"
	"github.com/riordanpawley/azedarach/internal/adapters/gh"
	"github.com/riordanpawley/azedarach/internal/adapters/git"
	"github.com/riordanpawley/azedarach/internal/core/ops"
)

var ErrPullRequestNotFound = errors.New("pull request not found")

type RemediationCode string

const (
	RemediationOffline    RemediationCode = "offline"
	RemediationAuth       RemediationCode = "auth"
	RemediationDivergence RemediationCode = "divergence"
	RemediationConflict   RemediationCode = "conflict"
)

type RemediationMessage struct {
	Code     RemediationCode
	Message  string
	NextStep string
}

type OperationState struct {
	ID     string
	State  ops.State
	Reason string
}

type ConflictFallback struct {
	ManualRequired bool
	Conflicts      []string
	AbortAttempted bool
	AbortSucceeded bool
	AbortCommand   string
	ResolveCommand string
}

type ConflictHandlingResult struct {
	Detected bool
	Fallback *ConflictFallback
}

type PreflightIssue struct {
	Code    string
	Message string
}

type CleanupBlockedError struct {
	Issues []PreflightIssue
}

func (e *CleanupBlockedError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "cleanup blocked by preflight checks"
	}
	return fmt.Sprintf("cleanup blocked by preflight checks: %s", e.Issues[0].Code)
}

type GitAdapter interface {
	ProvisionWorktree(ctx context.Context, repoPath string, worktreePath string, branch string, baseBranch string) error
	FetchBranch(ctx context.Context, repoPath string, branch string) error
	CheckDivergence(ctx context.Context, repoPath string, branch string) (bool, error)
	Merge(ctx context.Context, repoPath string, branch string) (git.MergeResult, error)
	AbortMerge(ctx context.Context, repoPath string) error
	OpenDiff(ctx context.Context, repoPath string, baseBranch string, headBranch string) (string, error)
	Status(ctx context.Context, repoPath string) (git.Status, error)
	IsSafeToDeleteWorktree(ctx context.Context, repoPath string, worktreePath string) (bool, error)
	RemoveWorktree(ctx context.Context, repoPath string, worktreePath string, branch string) error
	DeleteLocalBranch(ctx context.Context, repoPath string, branch string) error
}

type GHAdapter interface {
	CreatePullRequest(ctx context.Context, req gh.CreatePullRequestRequest) (gh.PullRequest, error)
	GetPullRequestByBranch(ctx context.Context, branch string) (gh.PullRequest, error)
}

type OpsAdapter interface {
	Queue(req ops.Request) (ops.Operation, bool, error)
	StartNext() (ops.Operation, bool)
	Succeed(operationID string) (ops.Operation, error)
	Fail(operationID string, reason string) (ops.Operation, error)
}

type Workflow struct {
	git GitAdapter
	gh  GHAdapter
	ops OpsAdapter
}

func New(gitAdapter GitAdapter, ghAdapter GHAdapter, opsAdapter OpsAdapter) *Workflow {
	return &Workflow{
		git: gitAdapter,
		gh:  ghAdapter,
		ops: opsAdapter,
	}
}

type ProvisionWorktreeInput struct {
	IssueKey     string
	RepoPath     string
	WorktreePath string
	Branch       string
	BaseBranch   string
}

type ProvisionWorktreeResult struct {
	Operation OperationState
}

func (w *Workflow) ProvisionWorktree(ctx context.Context, in ProvisionWorktreeInput) (ProvisionWorktreeResult, error) {
	op, err := w.startOperation(in.IssueKey)
	if err != nil {
		return ProvisionWorktreeResult{}, err
	}

	err = w.git.ProvisionWorktree(ctx, in.RepoPath, in.WorktreePath, in.Branch, in.BaseBranch)
	if err != nil {
		state, failErr := w.failOperation(op.ID, err.Error())
		if failErr != nil {
			return ProvisionWorktreeResult{}, failErr
		}
		return ProvisionWorktreeResult{Operation: state}, err
	}

	state, err := w.succeedOperation(op.ID)
	if err != nil {
		return ProvisionWorktreeResult{}, err
	}
	return ProvisionWorktreeResult{Operation: state}, nil
}

type UpdateFromBaseInput struct {
	IssueKey        string
	WorktreePath    string
	BaseBranch      string
	AbortOnConflict bool
}

type UpdateFromBaseResult struct {
	Operation   OperationState
	Conflict    ConflictHandlingResult
	Remediation *RemediationMessage
}

func (w *Workflow) UpdateFromBase(ctx context.Context, in UpdateFromBaseInput) (UpdateFromBaseResult, error) {
	op, err := w.startOperation(in.IssueKey)
	if err != nil {
		return UpdateFromBaseResult{}, err
	}

	if err := w.git.FetchBranch(ctx, in.WorktreePath, in.BaseBranch); err != nil {
		state, failErr := w.failOperation(op.ID, err.Error())
		if failErr != nil {
			return UpdateFromBaseResult{}, failErr
		}
		return UpdateFromBaseResult{Operation: state, Remediation: remediationForError(err)}, err
	}

	diverged, err := w.git.CheckDivergence(ctx, in.WorktreePath, in.BaseBranch)
	if err != nil {
		state, failErr := w.failOperation(op.ID, err.Error())
		if failErr != nil {
			return UpdateFromBaseResult{}, failErr
		}
		return UpdateFromBaseResult{Operation: state, Remediation: remediationForError(err)}, err
	}
	if diverged {
		state, failErr := w.failOperation(op.ID, "branch divergence")
		if failErr != nil {
			return UpdateFromBaseResult{}, failErr
		}
		return UpdateFromBaseResult{
			Operation: state,
			Remediation: &RemediationMessage{
				Code:     RemediationDivergence,
				Message:  "Base and worktree have diverged.",
				NextStep: fmt.Sprintf("Run `git pull --rebase origin %s` in %s and retry.", in.BaseBranch, in.WorktreePath),
			},
		}, nil
	}

	mergeResult, err := w.git.Merge(ctx, in.WorktreePath, in.BaseBranch)
	if err != nil {
		if adapterrors.IsConflict(err) {
			return w.handleConflict(ctx, op.ID, in.AbortOnConflict, in.WorktreePath, in.BaseBranch, in.WorktreePath, nil)
		}
		state, failErr := w.failOperation(op.ID, err.Error())
		if failErr != nil {
			return UpdateFromBaseResult{}, failErr
		}
		return UpdateFromBaseResult{Operation: state, Remediation: remediationForError(err)}, err
	}

	if mergeResult.HasConflicts {
		return w.handleConflict(ctx, op.ID, in.AbortOnConflict, in.WorktreePath, in.BaseBranch, in.WorktreePath, mergeResult.Conflicts)
	}

	state, err := w.succeedOperation(op.ID)
	if err != nil {
		return UpdateFromBaseResult{}, err
	}

	return UpdateFromBaseResult{Operation: state}, nil
}

type MergeToBaseInput struct {
	IssueKey        string
	RepoPath        string
	BaseBranch      string
	HeadBranch      string
	AbortOnConflict bool
}

type MergeToBaseResult struct {
	Operation   OperationState
	Merged      bool
	Conflict    ConflictHandlingResult
	Remediation *RemediationMessage
}

func (w *Workflow) MergeToBase(ctx context.Context, in MergeToBaseInput) (MergeToBaseResult, error) {
	op, err := w.startOperation(in.IssueKey)
	if err != nil {
		return MergeToBaseResult{}, err
	}

	diverged, err := w.git.CheckDivergence(ctx, in.RepoPath, in.BaseBranch)
	if err != nil {
		state, failErr := w.failOperation(op.ID, err.Error())
		if failErr != nil {
			return MergeToBaseResult{}, failErr
		}
		return MergeToBaseResult{Operation: state, Remediation: remediationForError(err)}, err
	}
	if diverged {
		state, failErr := w.failOperation(op.ID, "branch divergence")
		if failErr != nil {
			return MergeToBaseResult{}, failErr
		}
		return MergeToBaseResult{
			Operation: state,
			Remediation: &RemediationMessage{
				Code:     RemediationDivergence,
				Message:  "Base branch diverged from remote.",
				NextStep: fmt.Sprintf("Update `%s` before merge, then retry.", in.BaseBranch),
			},
		}, nil
	}

	mergeResult, err := w.git.Merge(ctx, in.RepoPath, in.HeadBranch)
	if err != nil {
		if adapterrors.IsConflict(err) {
			updateResult, handleErr := w.handleConflict(ctx, op.ID, in.AbortOnConflict, in.RepoPath, in.BaseBranch, in.HeadBranch, nil)
			return MergeToBaseResult{
				Operation:   updateResult.Operation,
				Merged:      false,
				Conflict:    updateResult.Conflict,
				Remediation: updateResult.Remediation,
			}, handleErr
		}
		state, failErr := w.failOperation(op.ID, err.Error())
		if failErr != nil {
			return MergeToBaseResult{}, failErr
		}
		return MergeToBaseResult{Operation: state, Remediation: remediationForError(err)}, err
	}

	if mergeResult.HasConflicts {
		updateResult, handleErr := w.handleConflict(ctx, op.ID, in.AbortOnConflict, in.RepoPath, in.BaseBranch, in.HeadBranch, mergeResult.Conflicts)
		return MergeToBaseResult{
			Operation:   updateResult.Operation,
			Merged:      false,
			Conflict:    updateResult.Conflict,
			Remediation: updateResult.Remediation,
		}, handleErr
	}

	state, err := w.succeedOperation(op.ID)
	if err != nil {
		return MergeToBaseResult{}, err
	}

	return MergeToBaseResult{Operation: state, Merged: true}, nil
}

type OpenDiffInput struct {
	RepoPath   string
	BaseBranch string
	HeadBranch string
}

type OpenDiffResult struct {
	URL string
}

func (w *Workflow) OpenDiff(ctx context.Context, in OpenDiffInput) (OpenDiffResult, error) {
	url, err := w.git.OpenDiff(ctx, in.RepoPath, in.BaseBranch, in.HeadBranch)
	if err != nil {
		return OpenDiffResult{}, err
	}
	return OpenDiffResult{URL: url}, nil
}

type CreateOrOpenPRInput struct {
	IssueKey   string
	Title      string
	Body       string
	HeadBranch string
	BaseBranch string
	Draft      bool
}

type CreateOrOpenPRResult struct {
	Operation   OperationState
	PullRequest gh.PullRequest
	Created     bool
	Remediation *RemediationMessage
}

func (w *Workflow) CreateOrOpenPR(ctx context.Context, in CreateOrOpenPRInput) (CreateOrOpenPRResult, error) {
	op, err := w.startOperation(in.IssueKey)
	if err != nil {
		return CreateOrOpenPRResult{}, err
	}

	existing, err := w.gh.GetPullRequestByBranch(ctx, in.HeadBranch)
	if err == nil {
		state, opErr := w.succeedOperation(op.ID)
		if opErr != nil {
			return CreateOrOpenPRResult{}, opErr
		}
		return CreateOrOpenPRResult{Operation: state, PullRequest: existing, Created: false}, nil
	}

	if !errors.Is(err, ErrPullRequestNotFound) {
		state, failErr := w.failOperation(op.ID, err.Error())
		if failErr != nil {
			return CreateOrOpenPRResult{}, failErr
		}
		if remediation := remediationForError(err); remediation != nil {
			return CreateOrOpenPRResult{Operation: state, Remediation: remediation}, nil
		}
		return CreateOrOpenPRResult{Operation: state}, err
	}

	pr, err := w.gh.CreatePullRequest(ctx, gh.CreatePullRequestRequest{
		Title:      in.Title,
		Body:       in.Body,
		HeadBranch: in.HeadBranch,
		BaseBranch: in.BaseBranch,
		Draft:      in.Draft,
	})
	if err != nil {
		state, failErr := w.failOperation(op.ID, err.Error())
		if failErr != nil {
			return CreateOrOpenPRResult{}, failErr
		}
		if remediation := remediationForError(err); remediation != nil {
			return CreateOrOpenPRResult{Operation: state, Remediation: remediation}, nil
		}
		return CreateOrOpenPRResult{Operation: state}, err
	}

	state, err := w.succeedOperation(op.ID)
	if err != nil {
		return CreateOrOpenPRResult{}, err
	}

	return CreateOrOpenPRResult{Operation: state, PullRequest: pr, Created: true}, nil
}

type CleanupInput struct {
	IssueKey     string
	RepoPath     string
	WorktreePath string
	Branch       string
	DeleteBranch bool
	Force        bool
}

type CleanupResult struct {
	Operation        OperationState
	PreflightBlocked bool
	PreflightIssues  []PreflightIssue
}

func (w *Workflow) Cleanup(ctx context.Context, in CleanupInput) (CleanupResult, error) {
	op, err := w.startOperation(in.IssueKey)
	if err != nil {
		return CleanupResult{}, err
	}

	issues, err := w.cleanupPreflight(ctx, in)
	if err != nil {
		state, failErr := w.failOperation(op.ID, err.Error())
		if failErr != nil {
			return CleanupResult{}, failErr
		}
		return CleanupResult{Operation: state}, err
	}

	if len(issues) > 0 {
		state, failErr := w.failOperation(op.ID, "cleanup preflight blocked")
		if failErr != nil {
			return CleanupResult{}, failErr
		}
		blocked := &CleanupBlockedError{Issues: issues}
		return CleanupResult{
			Operation:        state,
			PreflightBlocked: true,
			PreflightIssues:  issues,
		}, blocked
	}

	if err := w.git.RemoveWorktree(ctx, in.RepoPath, in.WorktreePath, in.Branch); err != nil {
		state, failErr := w.failOperation(op.ID, err.Error())
		if failErr != nil {
			return CleanupResult{}, failErr
		}
		return CleanupResult{Operation: state}, err
	}

	if in.DeleteBranch {
		if err := w.git.DeleteLocalBranch(ctx, in.RepoPath, in.Branch); err != nil {
			state, failErr := w.failOperation(op.ID, err.Error())
			if failErr != nil {
				return CleanupResult{}, failErr
			}
			return CleanupResult{Operation: state}, err
		}
	}

	state, err := w.succeedOperation(op.ID)
	if err != nil {
		return CleanupResult{}, err
	}

	return CleanupResult{Operation: state}, nil
}

func (w *Workflow) cleanupPreflight(ctx context.Context, in CleanupInput) ([]PreflightIssue, error) {
	issues := make([]PreflightIssue, 0, 3)

	safeToDelete, err := w.git.IsSafeToDeleteWorktree(ctx, in.RepoPath, in.WorktreePath)
	if err != nil {
		return nil, err
	}
	if !safeToDelete {
		issues = append(issues, PreflightIssue{
			Code:    "unsafe_path",
			Message: "Worktree path failed destructive-operation safety checks.",
		})
	}

	status, err := w.git.Status(ctx, in.WorktreePath)
	if err != nil {
		return nil, err
	}
	if !in.Force && (status.HasChanges || len(status.Staged) > 0 || len(status.Untracked) > 0) {
		issues = append(issues, PreflightIssue{
			Code:    "dirty_worktree",
			Message: "Worktree has uncommitted changes; cleanup requires --force.",
		})
	}

	diverged, err := w.git.CheckDivergence(ctx, in.RepoPath, in.Branch)
	if err != nil {
		return nil, err
	}
	if !in.Force && diverged {
		issues = append(issues, PreflightIssue{
			Code:    "diverged_branch",
			Message: "Branch divergence detected; resolve before cleanup or use --force.",
		})
	}

	return issues, nil
}

func (w *Workflow) handleConflict(
	ctx context.Context,
	operationID string,
	abortOnConflict bool,
	repoPath string,
	baseBranch string,
	headBranch string,
	conflicts []string,
) (UpdateFromBaseResult, error) {
	fallback := &ConflictFallback{
		ManualRequired: true,
		Conflicts:      conflicts,
		AbortAttempted: abortOnConflict,
		AbortCommand:   "git merge --abort",
		ResolveCommand: fmt.Sprintf("Resolve conflicts and run `git commit` in %s, then retry merge of %s into %s.", repoPath, baseBranch, headBranch),
	}

	if abortOnConflict {
		if err := w.git.AbortMerge(ctx, repoPath); err == nil {
			fallback.AbortSucceeded = true
		}
	}

	state, err := w.failOperation(operationID, "merge conflict")
	if err != nil {
		return UpdateFromBaseResult{}, err
	}

	return UpdateFromBaseResult{
		Operation: state,
		Conflict: ConflictHandlingResult{
			Detected: true,
			Fallback: fallback,
		},
		Remediation: &RemediationMessage{
			Code:     RemediationConflict,
			Message:  "Merge conflict requires manual resolution.",
			NextStep: fallback.ResolveCommand,
		},
	}, nil
}

func (w *Workflow) startOperation(issueKey string) (ops.Operation, error) {
	queued, _, err := w.ops.Queue(ops.Request{IssueKey: issueKey})
	if err != nil {
		return ops.Operation{}, err
	}
	op, ok := w.ops.StartNext()
	if !ok {
		return ops.Operation{}, fmt.Errorf("operation %s queued but did not start", queued.ID)
	}
	return op, nil
}

func (w *Workflow) succeedOperation(operationID string) (OperationState, error) {
	op, err := w.ops.Succeed(operationID)
	if err != nil {
		return OperationState{}, err
	}
	return OperationState{ID: op.ID, State: op.State, Reason: op.Reason}, nil
}

func (w *Workflow) failOperation(operationID string, reason string) (OperationState, error) {
	op, err := w.ops.Fail(operationID, reason)
	if err != nil {
		return OperationState{}, err
	}
	return OperationState{ID: op.ID, State: op.State, Reason: op.Reason}, nil
}

func remediationForError(err error) *RemediationMessage {
	switch {
	case adapterrors.IsOffline(err):
		return &RemediationMessage{
			Code:     RemediationOffline,
			Message:  "Network appears offline for this operation.",
			NextStep: "Reconnect network and retry.",
		}
	case adapterrors.IsAuth(err):
		return &RemediationMessage{
			Code:     RemediationAuth,
			Message:  "Authentication is required.",
			NextStep: "Refresh credentials (for example `gh auth login`) and retry.",
		}
	case adapterrors.IsConflict(err):
		return &RemediationMessage{
			Code:     RemediationConflict,
			Message:  "Operation hit a merge conflict.",
			NextStep: "Resolve merge conflicts and retry.",
		}
	default:
		return nil
	}
}
