package daemon

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"

	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type issueSpecService struct {
	daemon *Daemon
}

func (s issueSpecService) issueClient(ctx context.Context) (*issues.Client, error) {
	if s.daemon == nil {
		return nil, errors.New("issue store unavailable")
	}
	client := s.daemon.issueClientForProject(daemonProjectIDFromContext(ctx))
	if client == nil {
		return nil, errors.New("issue store unavailable")
	}
	return client, nil
}

func (s issueSpecService) ListRequirements(ctx context.Context, req protocol.SpecRequirementListRequestBody) (protocol.SpecRequirementListResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecRequirementListResponseBody{}, err
	}
	filter := issues.RequirementFilter{
		IssueID:  req.IssueID.String(),
		LocalIDs: requirementIDsToStrings(req.IDs),
	}
	if req.Status != "" {
		filter.Statuses = []issues.RequirementStatus{issues.RequirementStatus(req.Status)}
	}
	rows, err := client.ListRequirements(ctx, filter)
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
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecRequirementGetResponseBody{}, err
	}
	row, err := client.GetRequirement(ctx, req.ID.String())
	if err != nil {
		return protocol.SpecRequirementGetResponseBody{}, err
	}
	return protocol.SpecRequirementGetResponseBody{Requirement: mapRequirementToProtocol(row)}, nil
}

func (s issueSpecService) CreateRequirement(ctx context.Context, req protocol.SpecRequirementCreateRequestBody) (protocol.SpecRequirementCreateResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecRequirementCreateResponseBody{}, err
	}
	params := issues.CreateRequirementParams{
		LocalID:     req.ID.String(),
		Title:       req.Title,
		Description: req.Description,
		Status:      issues.RequirementStatusOpen,
	}
	if req.IssueID != "" {
		issueID := req.IssueID.String()
		params.IssueID = &issueID
	}
	row, err := client.CreateRequirement(ctx, params)
	if err != nil {
		return protocol.SpecRequirementCreateResponseBody{}, err
	}
	return protocol.SpecRequirementCreateResponseBody{Requirement: mapRequirementToProtocol(row)}, nil
}

func (s issueSpecService) UpdateRequirement(ctx context.Context, req protocol.SpecRequirementUpdateRequestBody) (protocol.SpecRequirementUpdateResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecRequirementUpdateResponseBody{}, err
	}
	params := issues.UpdateRequirementParams{
		Title:       req.Title,
		Description: req.Description,
	}
	if req.Status != nil {
		status := issues.RequirementStatus(*req.Status)
		params.Status = &status
	}
	row, err := client.UpdateRequirement(ctx, req.ID.String(), params)
	if err != nil {
		return protocol.SpecRequirementUpdateResponseBody{}, err
	}
	return protocol.SpecRequirementUpdateResponseBody{Requirement: mapRequirementToProtocol(row)}, nil
}

func (s issueSpecService) DeleteRequirement(ctx context.Context, req protocol.SpecRequirementDeleteRequestBody) (protocol.SpecRequirementDeleteResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecRequirementDeleteResponseBody{}, err
	}
	if err := client.DeleteRequirement(ctx, req.ID.String()); err != nil {
		return protocol.SpecRequirementDeleteResponseBody{}, err
	}
	return protocol.SpecRequirementDeleteResponseBody{
		ID:      req.ID,
		Deleted: true,
	}, nil
}

func (s issueSpecService) ListLinks(ctx context.Context, req protocol.SpecLinkListRequestBody) (protocol.SpecLinkListResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecLinkListResponseBody{}, err
	}
	links, err := client.ListSpecLinks(ctx, issues.SpecLinkFilter{
		IssueID:       req.IssueID.String(),
		RequirementID: req.ReqID.String(),
		LinkIDs:       specLinkIDsToStrings(req.IDs),
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
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecLinkAddResponseBody{}, err
	}
	params := issues.AddSpecLinkParams{
		IssueID:       req.IssueID.String(),
		RequirementID: req.ReqID.String(),
		Role:          issues.LinkRole(req.Role),
	}
	if req.Note != "" {
		params.Note = &req.Note
	}
	link, err := client.AddSpecLink(ctx, params)
	if err != nil {
		return protocol.SpecLinkAddResponseBody{}, err
	}
	return protocol.SpecLinkAddResponseBody{Link: mapLinkToProtocol(link)}, nil
}

func (s issueSpecService) RemoveLink(ctx context.Context, req protocol.SpecLinkRemoveRequestBody) (protocol.SpecLinkRemoveResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecLinkRemoveResponseBody{}, err
	}
	if err := client.RemoveSpecLink(ctx, req.IssueID.String(), req.ReqID.String()); err != nil {
		return protocol.SpecLinkRemoveResponseBody{}, err
	}
	return protocol.SpecLinkRemoveResponseBody{
		IssueID: req.IssueID,
		ReqID:   req.ReqID,
		Removed: true,
	}, nil
}

func (s issueSpecService) Read(ctx context.Context, req protocol.SpecReadRequestBody) (protocol.SpecReadResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecReadResponseBody{}, err
	}
	resolvedReqID := req.ReqID
	requirements := make([]issues.Requirement, 0, 1)
	if req.ReqID != "" {
		requirement, err := client.GetRequirement(ctx, req.ReqID.String())
		if err != nil {
			return protocol.SpecReadResponseBody{}, err
		}
		resolvedReqID = naming.RequirementID(requirement.LocalID)
		requirements = append(requirements, requirement)
	}

	if req.ReqID == "" {
		reqFilter := issues.RequirementFilter{IssueID: req.IssueID.String()}
		rows, err := client.ListRequirements(ctx, reqFilter)
		if err != nil {
			return protocol.SpecReadResponseBody{}, err
		}
		requirements = rows
	}
	linkFilter := issues.SpecLinkFilter{IssueID: req.IssueID.String(), RequirementID: resolvedReqID.String()}
	links, err := client.ListSpecLinks(ctx, linkFilter)
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
			row, err := client.GetRequirement(ctx, link.RequirementID)
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
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecLintResponseBody{}, err
	}
	requirements, err := client.ListRequirements(ctx, issues.RequirementFilter{})
	if err != nil {
		return protocol.SpecLintResponseBody{}, err
	}
	diagnostics := make([]protocol.SpecDiagnostic, 0)
	for _, req := range requirements {
		links, err := client.ListSpecLinksByRequirementLocalID(ctx, req.LocalID)
		if err != nil {
			return protocol.SpecLintResponseBody{}, err
		}
		if len(links) == 0 {
			diagnostics = append(diagnostics, protocol.SpecDiagnostic{
				Code:     "unlinked_requirement",
				Message:  "requirement has no linked issue",
				Severity: "warning",
				ReqID:    naming.RequirementID(req.LocalID),
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
		ID:          naming.RequirementID(req.LocalID),
		Title:       req.Title,
		Description: req.Description,
		Status:      protocol.SpecRequirementStatus(req.Status),
	}
	if req.IssueID != nil {
		out.IssueID = naming.IssueID(*req.IssueID)
	}
	return out
}

func mapLinkToProtocol(link issues.SpecLink) protocol.SpecLink {
	out := protocol.SpecLink{
		ID:      naming.SpecLinkID(link.ID),
		IssueID: naming.IssueID(link.IssueID),
		ReqID:   naming.RequirementID(link.RequirementID),
		Role:    protocol.SpecLinkRole(link.Role),
	}
	if link.Note != nil {
		out.Note = *link.Note
	}
	return out
}

func requirementIDsToStrings(ids []naming.RequirementID) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		out = append(out, id.String())
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func specLinkIDsToStrings(ids []naming.SpecLinkID) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		out = append(out, id.String())
	}
	if len(out) == 0 {
		return nil
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
	manager                       *git.WorktreeManager
	managerForProject             func(string) *git.WorktreeManager
	runtimeStateStore             *daemonstate.RuntimeStateStore
	runtimeStateStoreForProject   func(string) *daemonstate.RuntimeStateStore
	runtimeProjectionWriter       runtimeProjectionWriter
	ensureRuntimeFreshForMutation func(context.Context, string, string) error
	logger                        *slog.Logger
	pollInterval                  time.Duration
	onProjectionUpdate            func(ctx context.Context, projectID, issueID, path string)
	onWorktreeObserved            func(ctx context.Context, projectID, issueID, path string)

	mu      sync.Mutex
	pollers map[string]context.CancelFunc
}

func (a *worktreeServiceAdapter) runtimeStore(projectID string) *daemonstate.RuntimeStateStore {
	if a == nil {
		return nil
	}
	if a.runtimeStateStoreForProject != nil {
		if store := a.runtimeStateStoreForProject(projectID); store != nil {
			return store
		}
	}
	return a.runtimeStateStore
}

func (a *worktreeServiceAdapter) managerFor(projectID string) *git.WorktreeManager {
	if a == nil {
		return nil
	}
	if a.managerForProject != nil {
		if manager := a.managerForProject(projectID); manager != nil {
			return manager
		}
	}
	return a.manager
}

func (a *worktreeServiceAdapter) List(ctx context.Context, projectID string) ([]git.Worktree, error) {
	projectID = normalizedProjectID(projectID)
	manager := a.managerFor(projectID)
	if manager == nil {
		return nil, errors.New("worktree manager unavailable")
	}
	runtimeStore := a.runtimeStore(projectID)
	if runtimeStore == nil {
		if a.logger != nil {
			a.logger.Debug("worktree projection store unavailable for projection-only read",
				"project_id", projectID,
			)
		}
		return []git.Worktree{}, nil
	}

	cached, cacheErr := runtimeStore.ListWorktreeStates(ctx, projectID)
	if cacheErr != nil {
		if a.logger != nil {
			a.logger.Debug("worktree list cache read failed", "project_id", projectID, "error", cacheErr)
		}
		return nil, cacheErr
	}
	worktrees := mapProjectionWorktrees(cached)
	if len(worktrees) > 0 {
		a.observeWorktrees(ctx, projectID, worktrees)
	}
	a.ensureBackgroundPoller(projectID)
	if a.logger != nil {
		a.logger.Debug("using projection-backed worktree runtime state",
			"project_id", projectID,
			"cached_worktrees", len(worktrees),
		)
	}
	return worktrees, nil
}

func (a *worktreeServiceAdapter) Create(ctx context.Context, projectID string, issueID string, baseBranch string) (*git.Worktree, error) {
	manager := a.managerFor(projectID)
	if manager == nil {
		return nil, errors.New("worktree manager unavailable")
	}
	if a.ensureRuntimeFreshForMutation != nil {
		if err := a.ensureRuntimeFreshForMutation(ctx, normalizedProjectID(projectID), daemonhandlers.CommandWorktreeCreate); err != nil {
			return nil, err
		}
	}
	worktree, err := manager.Create(ctx, issueID, baseBranch)
	if err != nil {
		return nil, err
	}
	if a.runtimeStore(projectID) != nil && worktree != nil {
		if a.runtimeProjectionWriter != nil {
			a.runtimeProjectionWriter.PersistWorktreeProjectionAndPublish(ctx, normalizedProjectID(projectID), worktree.IssueID, worktree.Path, worktree.Branch)
		}
		a.observeWorktrees(ctx, normalizedProjectID(projectID), []git.Worktree{*worktree})
	}
	return worktree, nil
}

func (a *worktreeServiceAdapter) Delete(ctx context.Context, projectID string, issueID string, force bool) error {
	manager := a.managerFor(projectID)
	if manager == nil {
		return errors.New("worktree manager unavailable")
	}
	if a.ensureRuntimeFreshForMutation != nil {
		if err := a.ensureRuntimeFreshForMutation(ctx, normalizedProjectID(projectID), daemonhandlers.CommandWorktreeRemove); err != nil {
			return err
		}
	}
	worktree, err := manager.Get(ctx, issueID)
	if err != nil {
		return err
	}
	if err := manager.DeleteWithOptions(ctx, issueID, force); err != nil {
		return err
	}
	if worktree != nil {
		_ = lifecycle.TerminateLockOwner(appconfig.ScopedDaemonLockPath(worktree.Path))
	}
	if a.runtimeProjectionWriter != nil {
		a.runtimeProjectionWriter.DeleteWorktreeProjectionAndPublish(ctx, normalizedProjectID(projectID), issueID)
	}
	return nil
}

func (a *worktreeServiceAdapter) CleanupOrphaned(ctx context.Context, projectID string) (*daemonhandlers.CleanupOrphanedResult, error) {
	manager := a.managerFor(projectID)
	if manager == nil {
		return nil, errors.New("worktree manager unavailable")
	}
	if a.ensureRuntimeFreshForMutation != nil {
		if err := a.ensureRuntimeFreshForMutation(ctx, normalizedProjectID(projectID), daemonhandlers.CommandWorktreeCleanupOrphaned); err != nil {
			return nil, err
		}
	}
	worktrees, err := manager.List(ctx)
	if err != nil {
		return nil, err
	}

	result := &daemonhandlers.CleanupOrphanedResult{
		ProjectID: projectID,
	}
	for _, wt := range worktrees {
		if err := manager.Delete(ctx, wt.IssueID); err != nil {
			result.Skipped = append(result.Skipped, wt)
			continue
		}
		_ = lifecycle.TerminateLockOwner(appconfig.ScopedDaemonLockPath(wt.Path))
		result.Removed = append(result.Removed, wt)
		if a.runtimeProjectionWriter != nil {
			a.runtimeProjectionWriter.DeleteWorktreeProjectionAndPublish(ctx, normalizedProjectID(projectID), wt.IssueID)
		}
	}

	return result, nil
}

func (a *worktreeServiceAdapter) ensureBackgroundPoller(projectID string) {
	if a.runtimeStore(projectID) == nil {
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
		defer func() {
			if r := recover(); r != nil && a.logger != nil {
				a.logger.Error("worktree background poller panicked", "project_id", projectID, "panic", r, "stack", string(debug.Stack()))
			}
		}()
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
	manager := a.managerFor(projectID)
	if manager == nil {
		return
	}
	var previous []daemonstate.WorktreeState
	if runtimeStore := a.runtimeStore(projectID); runtimeStore != nil {
		if cached, err := runtimeStore.ListWorktreeStates(ctx, projectID); err == nil {
			previous = cached
		}
	}
	worktrees, err := manager.List(ctx)
	if err != nil {
		if a.logger != nil {
			a.logger.Debug("worktree projection poll failed", "project_id", projectID, "error", err)
		}
		return
	}
	a.writeWorktreeProjectionSnapshot(ctx, projectID, worktrees)
	a.observeWorktrees(ctx, projectID, worktrees)
	if a.onProjectionUpdate != nil {
		publishWorktreeProjectionDelta(ctx, a.onProjectionUpdate, projectID, previous, worktrees)
	}
}

func (a *worktreeServiceAdapter) observeWorktrees(ctx context.Context, projectID string, worktrees []git.Worktree) {
	if a.onWorktreeObserved == nil || len(worktrees) == 0 {
		return
	}
	projectID = normalizedProjectID(projectID)
	for _, wt := range worktrees {
		issueID := strings.TrimSpace(wt.IssueID)
		path := strings.TrimSpace(wt.Path)
		if issueID == "" || path == "" {
			continue
		}
		if runtimeStore := a.runtimeStore(projectID); runtimeStore != nil {
			row, found, err := runtimeStore.GetWorktreeStateByIssueID(ctx, projectID, issueID)
			if err != nil {
				if a.logger != nil {
					a.logger.Debug("worktree observation lookup failed", "project_id", projectID, "issue_id", issueID, "error", err)
				}
				continue
			}
			if found && len(row.GitStatusRaw) > 0 {
				continue
			}
		}
		a.onWorktreeObserved(ctx, projectID, issueID, path)
	}
}

func (a *worktreeServiceAdapter) writeWorktreeProjectionSnapshot(ctx context.Context, projectID string, worktrees []git.Worktree) {
	runtimeStore := a.runtimeStore(projectID)
	if runtimeStore == nil {
		return
	}
	rows := make([]daemonstate.WorktreeState, 0, len(worktrees))
	now := time.Now().UTC()
	for _, wt := range worktrees {
		if strings.TrimSpace(wt.IssueID) == "" {
			continue
		}
		rows = append(rows, daemonstate.WorktreeState{
			ProjectID: normalizedProjectID(projectID),
			IssueID:   wt.IssueID,
			Path:      wt.Path,
			Branch:    wt.Branch,
			UpdatedAt: now,
		})
	}
	if a.runtimeProjectionWriter != nil {
		if err := a.runtimeProjectionWriter.ReplaceWorktreeProjectionSnapshot(ctx, normalizedProjectID(projectID), rows); err != nil && a.logger != nil {
			a.logger.Warn("replace worktree projection snapshot failed", "project_id", projectID, "error", err)
		}
		return
	}
	if err := runtimeStore.ReplaceWorktreeStates(ctx, normalizedProjectID(projectID), rows); err != nil && a.logger != nil {
		a.logger.Warn("replace worktree projection snapshot failed", "project_id", projectID, "error", err)
	}
}

func mapProjectionWorktrees(projections []daemonstate.WorktreeState) []git.Worktree {
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

func publishWorktreeProjectionDelta(ctx context.Context, publish func(ctx context.Context, projectID, issueID, path string), projectID string, previous []daemonstate.WorktreeState, next []git.Worktree) {
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
	return protocol.NormalizeProjectID(projectID)
}
