package daemon

import (
	"context"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"

	"github.com/riordanpawley/azedarach/internal/services/git"
)

type unavailableSpecService struct{}

func (unavailableSpecService) ListRequirements(context.Context, protocol.SpecRequirementListRequestBody) (protocol.SpecRequirementListResponseBody, error) {
	return protocol.SpecRequirementListResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func (unavailableSpecService) GetRequirement(context.Context, protocol.SpecRequirementGetRequestBody) (protocol.SpecRequirementGetResponseBody, error) {
	return protocol.SpecRequirementGetResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func (unavailableSpecService) CreateRequirement(context.Context, protocol.SpecRequirementCreateRequestBody) (protocol.SpecRequirementCreateResponseBody, error) {
	return protocol.SpecRequirementCreateResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func (unavailableSpecService) UpdateRequirement(context.Context, protocol.SpecRequirementUpdateRequestBody) (protocol.SpecRequirementUpdateResponseBody, error) {
	return protocol.SpecRequirementUpdateResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func (unavailableSpecService) DeleteRequirement(context.Context, protocol.SpecRequirementDeleteRequestBody) (protocol.SpecRequirementDeleteResponseBody, error) {
	return protocol.SpecRequirementDeleteResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func (unavailableSpecService) ListLinks(context.Context, protocol.SpecLinkListRequestBody) (protocol.SpecLinkListResponseBody, error) {
	return protocol.SpecLinkListResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func (unavailableSpecService) AddLink(context.Context, protocol.SpecLinkAddRequestBody) (protocol.SpecLinkAddResponseBody, error) {
	return protocol.SpecLinkAddResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func (unavailableSpecService) RemoveLink(context.Context, protocol.SpecLinkRemoveRequestBody) (protocol.SpecLinkRemoveResponseBody, error) {
	return protocol.SpecLinkRemoveResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func (unavailableSpecService) Read(context.Context, protocol.SpecReadRequestBody) (protocol.SpecReadResponseBody, error) {
	return protocol.SpecReadResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func (unavailableSpecService) Lint(context.Context, protocol.SpecLintRequestBody) (protocol.SpecLintResponseBody, error) {
	return protocol.SpecLintResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func (unavailableSpecService) Parity(context.Context, protocol.SpecParityRequestBody) (protocol.SpecParityResponseBody, error) {
	return protocol.SpecParityResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func (unavailableSpecService) SyncMD(context.Context, protocol.SpecSyncMDRequestBody) (protocol.SpecSyncMDResponseBody, error) {
	return protocol.SpecSyncMDResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

type worktreeServiceAdapter struct {
	manager *git.WorktreeManager
}

func (a worktreeServiceAdapter) List(ctx context.Context, _ string) ([]git.Worktree, error) {
	return a.manager.List(ctx)
}

func (a worktreeServiceAdapter) Create(ctx context.Context, _ string, issueID string, baseBranch string) (*git.Worktree, error) {
	return a.manager.Create(ctx, issueID, baseBranch)
}

func (a worktreeServiceAdapter) Delete(ctx context.Context, _ string, issueID string) error {
	worktree, err := a.manager.Get(ctx, issueID)
	if err != nil {
		return err
	}
	if err := a.manager.Delete(ctx, issueID); err != nil {
		return err
	}
	if worktree != nil {
		_ = lifecycle.TerminateLockOwner(appconfig.ScopedDaemonLockPath(worktree.Path))
	}
	return nil
}

func (a worktreeServiceAdapter) CleanupOrphaned(ctx context.Context, projectID string) (*daemonhandlers.CleanupOrphanedResult, error) {
	worktrees, err := a.manager.List(ctx)
	if err != nil {
		return nil, err
	}

	result := &daemonhandlers.CleanupOrphanedResult{
		ProjectID: projectID,
	}
	for _, wt := range worktrees {
		if err := a.manager.Delete(ctx, wt.IssueID); err != nil {
			result.Skipped = append(result.Skipped, wt)
			continue
		}
		_ = lifecycle.TerminateLockOwner(appconfig.ScopedDaemonLockPath(wt.Path))
		result.Removed = append(result.Removed, wt)
	}

	return result, nil
}
