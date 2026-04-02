package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"

	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type issueSpecService struct {
	client *issues.Client
}

func (s issueSpecService) ListRequirements(ctx context.Context, req protocol.SpecRequirementListRequestBody) (protocol.SpecRequirementListResponseBody, error) {
	filter := issues.RequirementFilter{
		IssueID:  req.IssueID,
		LocalIDs: req.IDs,
	}
	if req.Status != "" {
		filter.Statuses = []issues.RequirementStatus{issues.RequirementStatus(req.Status)}
	}
	rows, err := s.client.ListRequirements(ctx, filter)
	if err != nil {
		return protocol.SpecRequirementListResponseBody{}, err
	}
	out := make([]protocol.SpecRequirement, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapRequirementToProtocol(row))
	}
	return protocol.SpecRequirementListResponseBody{Requirements: out}, nil
}

func (s issueSpecService) GetRequirement(ctx context.Context, req protocol.SpecRequirementGetRequestBody) (protocol.SpecRequirementGetResponseBody, error) {
	row, err := s.client.GetRequirement(ctx, req.ID)
	if err != nil {
		return protocol.SpecRequirementGetResponseBody{}, err
	}
	return protocol.SpecRequirementGetResponseBody{Requirement: mapRequirementToProtocol(row)}, nil
}

func (s issueSpecService) CreateRequirement(ctx context.Context, req protocol.SpecRequirementCreateRequestBody) (protocol.SpecRequirementCreateResponseBody, error) {
	params := issues.CreateRequirementParams{
		LocalID:     req.ID,
		Title:       req.Title,
		Description: req.Description,
		Status:      issues.RequirementStatusOpen,
	}
	if req.IssueID != "" {
		params.IssueID = &req.IssueID
	}
	row, err := s.client.CreateRequirement(ctx, params)
	if err != nil {
		return protocol.SpecRequirementCreateResponseBody{}, err
	}
	return protocol.SpecRequirementCreateResponseBody{Requirement: mapRequirementToProtocol(row)}, nil
}

func (s issueSpecService) UpdateRequirement(ctx context.Context, req protocol.SpecRequirementUpdateRequestBody) (protocol.SpecRequirementUpdateResponseBody, error) {
	params := issues.UpdateRequirementParams{
		Title:       req.Title,
		Description: req.Description,
	}
	if req.Status != nil {
		status := issues.RequirementStatus(*req.Status)
		params.Status = &status
	}
	row, err := s.client.UpdateRequirement(ctx, req.ID, params)
	if err != nil {
		return protocol.SpecRequirementUpdateResponseBody{}, err
	}
	return protocol.SpecRequirementUpdateResponseBody{Requirement: mapRequirementToProtocol(row)}, nil
}

func (s issueSpecService) DeleteRequirement(ctx context.Context, req protocol.SpecRequirementDeleteRequestBody) (protocol.SpecRequirementDeleteResponseBody, error) {
	if err := s.client.DeleteRequirement(ctx, req.ID); err != nil {
		return protocol.SpecRequirementDeleteResponseBody{}, err
	}
	return protocol.SpecRequirementDeleteResponseBody{
		ID:      req.ID,
		Deleted: true,
	}, nil
}

func (s issueSpecService) ListLinks(ctx context.Context, req protocol.SpecLinkListRequestBody) (protocol.SpecLinkListResponseBody, error) {
	links, err := s.client.ListSpecLinks(ctx, issues.SpecLinkFilter{
		IssueID:       req.IssueID,
		RequirementID: req.ReqID,
		LinkIDs:       req.IDs,
	})
	if err != nil {
		return protocol.SpecLinkListResponseBody{}, err
	}
	out := make([]protocol.SpecLink, 0, len(links))
	for _, link := range links {
		out = append(out, mapLinkToProtocol(link))
	}
	return protocol.SpecLinkListResponseBody{Links: out}, nil
}

func (s issueSpecService) AddLink(ctx context.Context, req protocol.SpecLinkAddRequestBody) (protocol.SpecLinkAddResponseBody, error) {
	params := issues.AddSpecLinkParams{
		IssueID:       req.IssueID,
		RequirementID: req.ReqID,
		Role:          issues.LinkRole(req.Role),
	}
	if req.Note != "" {
		params.Note = &req.Note
	}
	link, err := s.client.AddSpecLink(ctx, params)
	if err != nil {
		return protocol.SpecLinkAddResponseBody{}, err
	}
	return protocol.SpecLinkAddResponseBody{Link: mapLinkToProtocol(link)}, nil
}

func (s issueSpecService) RemoveLink(ctx context.Context, req protocol.SpecLinkRemoveRequestBody) (protocol.SpecLinkRemoveResponseBody, error) {
	if err := s.client.RemoveSpecLink(ctx, req.IssueID, req.ReqID); err != nil {
		return protocol.SpecLinkRemoveResponseBody{}, err
	}
	return protocol.SpecLinkRemoveResponseBody{
		IssueID: req.IssueID,
		ReqID:   req.ReqID,
		Removed: true,
	}, nil
}

func (s issueSpecService) Read(ctx context.Context, req protocol.SpecReadRequestBody) (protocol.SpecReadResponseBody, error) {
	resolvedReqID := req.ReqID
	requirements := make([]issues.Requirement, 0, 1)
	if req.ReqID != "" {
		requirement, err := s.client.GetRequirement(ctx, req.ReqID)
		if err != nil {
			return protocol.SpecReadResponseBody{}, err
		}
		resolvedReqID = requirement.LocalID
		requirements = append(requirements, requirement)
	}

	if req.ReqID == "" {
		reqFilter := issues.RequirementFilter{IssueID: req.IssueID}
		rows, err := s.client.ListRequirements(ctx, reqFilter)
		if err != nil {
			return protocol.SpecReadResponseBody{}, err
		}
		requirements = rows
	}
	linkFilter := issues.SpecLinkFilter{IssueID: req.IssueID, RequirementID: resolvedReqID}
	links, err := s.client.ListSpecLinks(ctx, linkFilter)
	if err != nil {
		return protocol.SpecReadResponseBody{}, err
	}

	// For issue-scoped reads without explicit requirement selector, include requirements
	// referenced by links even when requirement.issue_id is empty.
	if req.ReqID == "" && req.IssueID != "" {
		present := make(map[string]struct{}, len(requirements))
		for _, row := range requirements {
			present[row.LocalID] = struct{}{}
		}
		for _, link := range links {
			if _, ok := present[link.RequirementID]; ok {
				continue
			}
			row, err := s.client.GetRequirement(ctx, link.RequirementID)
			if err != nil {
				return protocol.SpecReadResponseBody{}, err
			}
			requirements = append(requirements, row)
			present[row.LocalID] = struct{}{}
		}
	}

	outReqs := make([]protocol.SpecRequirement, 0, len(requirements))
	for _, row := range requirements {
		outReqs = append(outReqs, mapRequirementToProtocol(row))
	}
	outLinks := make([]protocol.SpecLink, 0, len(links))
	for _, link := range links {
		outLinks = append(outLinks, mapLinkToProtocol(link))
	}
	return protocol.SpecReadResponseBody{
		Requirements: outReqs,
		Links:        outLinks,
	}, nil
}

func (s issueSpecService) Lint(ctx context.Context, _ protocol.SpecLintRequestBody) (protocol.SpecLintResponseBody, error) {
	requirements, err := s.client.ListRequirements(ctx, issues.RequirementFilter{})
	if err != nil {
		return protocol.SpecLintResponseBody{}, err
	}
	diagnostics := make([]protocol.SpecDiagnostic, 0)
	for _, req := range requirements {
		links, err := s.client.ListSpecLinksByRequirementLocalID(ctx, req.LocalID)
		if err != nil {
			return protocol.SpecLintResponseBody{}, err
		}
		if len(links) == 0 {
			diagnostics = append(diagnostics, protocol.SpecDiagnostic{
				Code:     "unlinked_requirement",
				Message:  "requirement has no linked issue",
				Severity: "warning",
				ReqID:    req.LocalID,
			})
		}
	}
	return protocol.SpecLintResponseBody{
		OK:          len(diagnostics) == 0,
		Diagnostics: diagnostics,
	}, nil
}

func (s issueSpecService) Parity(ctx context.Context, req protocol.SpecParityRequestBody) (protocol.SpecParityResponseBody, error) {
	lint, err := s.Lint(ctx, protocol.SpecLintRequestBody{})
	if err != nil {
		return protocol.SpecParityResponseBody{}, err
	}
	findings := make([]protocol.SpecParityFinding, 0, len(lint.Diagnostics))
	for _, d := range lint.Diagnostics {
		findings = append(findings, protocol.SpecParityFinding{
			ReqID:    d.ReqID,
			Severity: d.Severity,
			Message:  d.Message,
		})
	}
	ok := len(findings) == 0
	if req.FailOnOut && !ok {
		return protocol.SpecParityResponseBody{OK: false, Findings: findings}, domain.ErrConflict
	}
	return protocol.SpecParityResponseBody{OK: ok, Findings: findings}, nil
}

func (s issueSpecService) SyncMD(context.Context, protocol.SpecSyncMDRequestBody) (protocol.SpecSyncMDResponseBody, error) {
	return protocol.SpecSyncMDResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func mapRequirementToProtocol(req issues.Requirement) protocol.SpecRequirement {
	out := protocol.SpecRequirement{
		ID:          req.LocalID,
		Title:       req.Title,
		Description: req.Description,
		Status:      protocol.SpecRequirementStatus(req.Status),
	}
	if req.IssueID != nil {
		out.IssueID = *req.IssueID
	}
	return out
}

func mapLinkToProtocol(link issues.SpecLink) protocol.SpecLink {
	out := protocol.SpecLink{
		ID:      link.ID,
		IssueID: link.IssueID,
		ReqID:   link.RequirementID,
		Role:    protocol.SpecLinkRole(link.Role),
	}
	if link.Note != nil {
		out.Note = *link.Note
	}
	return out
}

func mapSpecStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrConflict) {
		return err
	}
	return err
}

type worktreeServiceAdapter struct {
	manager            *git.WorktreeManager
	projectionStore    *daemonstate.ProjectionStore
	logger             *slog.Logger
	pollInterval       time.Duration
	onProjectionUpdate func(ctx context.Context, projectID, issueID, path string)

	mu      sync.Mutex
	pollers map[string]context.CancelFunc
}

func (a *worktreeServiceAdapter) List(ctx context.Context, projectID string) ([]git.Worktree, error) {
	projectID = normalizedProjectID(projectID)
	if a.projectionStore == nil {
		return a.manager.List(ctx)
	}

	cached, err := a.projectionStore.ListWorktrees(ctx, projectID)
	if err == nil && len(cached) > 0 {
		a.ensureBackgroundPoller(projectID)
		return mapProjectionWorktrees(cached), nil
	}

	worktrees, listErr := a.manager.List(ctx)
	if listErr != nil {
		if err == nil {
			return mapProjectionWorktrees(cached), nil
		}
		return nil, listErr
	}
	a.writeWorktreeProjectionSnapshot(ctx, projectID, worktrees)
	a.ensureBackgroundPoller(projectID)
	return worktrees, nil
}

func (a *worktreeServiceAdapter) Create(ctx context.Context, projectID string, issueID string, baseBranch string) (*git.Worktree, error) {
	worktree, err := a.manager.Create(ctx, issueID, baseBranch)
	if err != nil {
		return nil, err
	}
	if a.projectionStore != nil && worktree != nil {
		if upsertErr := a.projectionStore.UpsertWorktree(ctx, daemonstate.WorktreeProjection{
			ProjectID: normalizedProjectID(projectID),
			IssueID:   worktree.IssueID,
			Path:      worktree.Path,
			Branch:    worktree.Branch,
			UpdatedAt: time.Now().UTC(),
		}); upsertErr != nil && a.logger != nil {
			a.logger.Warn("failed to upsert worktree projection on create", "project_id", projectID, "issue_id", issueID, "error", upsertErr)
		}
		if a.onProjectionUpdate != nil {
			a.onProjectionUpdate(ctx, normalizedProjectID(projectID), worktree.IssueID, worktree.Path)
		}
	}
	return worktree, nil
}

func (a *worktreeServiceAdapter) Delete(ctx context.Context, projectID string, issueID string, force bool) error {
	worktree, err := a.manager.Get(ctx, issueID)
	if err != nil {
		return err
	}
	if err := a.manager.DeleteWithOptions(ctx, issueID, force); err != nil {
		return err
	}
	if worktree != nil {
		_ = lifecycle.TerminateLockOwner(appconfig.ScopedDaemonLockPath(worktree.Path))
	}
	if a.projectionStore != nil {
		if deleteErr := a.projectionStore.DeleteWorktree(ctx, normalizedProjectID(projectID), issueID); deleteErr != nil && a.logger != nil {
			a.logger.Warn("failed to delete worktree projection", "project_id", projectID, "issue_id", issueID, "error", deleteErr)
		}
		if a.onProjectionUpdate != nil {
			a.onProjectionUpdate(ctx, normalizedProjectID(projectID), issueID, "")
		}
	}
	return nil
}

func (a *worktreeServiceAdapter) CleanupOrphaned(ctx context.Context, projectID string) (*daemonhandlers.CleanupOrphanedResult, error) {
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
		if a.projectionStore != nil {
			if deleteErr := a.projectionStore.DeleteWorktree(ctx, normalizedProjectID(projectID), wt.IssueID); deleteErr != nil && a.logger != nil {
				a.logger.Warn("failed to delete worktree projection during cleanup", "project_id", projectID, "issue_id", wt.IssueID, "error", deleteErr)
			}
			if a.onProjectionUpdate != nil {
				a.onProjectionUpdate(ctx, normalizedProjectID(projectID), wt.IssueID, "")
			}
		}
	}

	return result, nil
}

func (a *worktreeServiceAdapter) ensureBackgroundPoller(projectID string) {
	if a.projectionStore == nil {
		return
	}
	interval := a.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	projectID = normalizedProjectID(projectID)

	a.mu.Lock()
	if a.pollers == nil {
		a.pollers = make(map[string]context.CancelFunc)
	}
	if _, exists := a.pollers[projectID]; exists {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.pollers[projectID] = cancel
	a.mu.Unlock()

	go func() {
		a.pollAndPersistWorktrees(ctx, projectID)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.pollAndPersistWorktrees(ctx, projectID)
			}
		}
	}()
}

func (a *worktreeServiceAdapter) pollAndPersistWorktrees(ctx context.Context, projectID string) {
	var previous []daemonstate.WorktreeProjection
	if a.projectionStore != nil {
		if cached, err := a.projectionStore.ListWorktrees(ctx, projectID); err == nil {
			previous = cached
		}
	}
	worktrees, err := a.manager.List(ctx)
	if err != nil {
		if a.logger != nil {
			a.logger.Debug("worktree projection poll failed", "project_id", projectID, "error", err)
		}
		return
	}
	a.writeWorktreeProjectionSnapshot(ctx, projectID, worktrees)
	if a.onProjectionUpdate != nil {
		publishWorktreeProjectionDelta(ctx, a.onProjectionUpdate, projectID, previous, worktrees)
	}
}

func (a *worktreeServiceAdapter) writeWorktreeProjectionSnapshot(ctx context.Context, projectID string, worktrees []git.Worktree) {
	if a.projectionStore == nil {
		return
	}
	rows := make([]daemonstate.WorktreeProjection, 0, len(worktrees))
	now := time.Now().UTC()
	for _, wt := range worktrees {
		if strings.TrimSpace(wt.IssueID) == "" {
			continue
		}
		rows = append(rows, daemonstate.WorktreeProjection{
			ProjectID: normalizedProjectID(projectID),
			IssueID:   wt.IssueID,
			Path:      wt.Path,
			Branch:    wt.Branch,
			UpdatedAt: now,
		})
	}
	if err := a.projectionStore.ReplaceWorktrees(ctx, normalizedProjectID(projectID), rows); err != nil && a.logger != nil {
		a.logger.Warn("replace worktree projection snapshot failed", "project_id", projectID, "error", err)
	}
}

func mapProjectionWorktrees(projections []daemonstate.WorktreeProjection) []git.Worktree {
	out := make([]git.Worktree, 0, len(projections))
	for _, projection := range projections {
		out = append(out, git.Worktree{
			Path:    projection.Path,
			Branch:  projection.Branch,
			IssueID: projection.IssueID,
		})
	}
	return out
}

func publishWorktreeProjectionDelta(ctx context.Context, publish func(ctx context.Context, projectID, issueID, path string), projectID string, previous []daemonstate.WorktreeProjection, next []git.Worktree) {
	prevByIssue := make(map[string]string, len(previous))
	for _, row := range previous {
		prevByIssue[row.IssueID] = row.Path + "|" + row.Branch
	}
	nextByIssue := make(map[string]git.Worktree, len(next))
	for _, row := range next {
		nextByIssue[row.IssueID] = row
	}
	for issueID, wt := range nextByIssue {
		nextKey := wt.Path + "|" + wt.Branch
		if prevByIssue[issueID] != nextKey {
			publish(ctx, projectID, issueID, wt.Path)
		}
		delete(prevByIssue, issueID)
	}
	for issueID := range prevByIssue {
		publish(ctx, projectID, issueID, "")
	}
}

func normalizedProjectID(projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "default"
	}
	return projectID
}
