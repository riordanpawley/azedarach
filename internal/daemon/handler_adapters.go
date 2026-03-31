package daemon

import (
	"context"
	"errors"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
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
	reqFilter := issues.RequirementFilter{IssueID: req.IssueID}
	if req.ReqID != "" {
		reqFilter.LocalIDs = []string{req.ReqID}
	}
	requirements, err := s.client.ListRequirements(ctx, reqFilter)
	if err != nil {
		return protocol.SpecReadResponseBody{}, err
	}
	linkFilter := issues.SpecLinkFilter{IssueID: req.IssueID, RequirementID: req.ReqID}
	links, err := s.client.ListSpecLinks(ctx, linkFilter)
	if err != nil {
		return protocol.SpecReadResponseBody{}, err
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
		links, err := s.client.ListSpecLinks(ctx, issues.SpecLinkFilter{RequirementID: req.LocalID})
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
