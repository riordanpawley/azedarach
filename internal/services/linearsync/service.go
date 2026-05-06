package linearsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/linearapi"
)

const ProviderLinear = "linear"

const (
	defaultMaxPushesPerRun = 25
	defaultPushBatchSize   = linearapi.MaxIssueUpdateBatchSize
	defaultRetryAttempts   = 3
	defaultRetryDelay      = time.Second
	minRemainingRequests   = 5
)

type LinearClient interface {
	ListIssues(ctx context.Context, opts linearapi.ListIssuesOptions) ([]linearapi.Issue, error)
	ViewerID(ctx context.Context) (string, error)
	UpdateIssues(ctx context.Context, requests []linearapi.IssueUpdateRequest) ([]linearapi.Issue, error)
	Metrics() linearapi.Metrics
}

type IssueStore interface {
	List(ctx context.Context) ([]domain.Task, error)
	UpsertSyncedTask(ctx context.Context, task domain.Task) (bool, error)
	UpsertExternalRef(ctx context.Context, ref issues.ExternalRef) error
	ListExternalRefs(ctx context.Context, provider string) ([]issues.ExternalRef, error)
	GetExternalSyncState(ctx context.Context, provider, projectID string) (issues.ExternalSyncState, bool, error)
	UpsertExternalSyncState(ctx context.Context, state issues.ExternalSyncState) error
	RecordSyncConflict(ctx context.Context, conflict issues.SyncConflict) error
	ListSyncConflicts(ctx context.Context, provider, projectID string, includeResolved bool) ([]issues.SyncConflict, error)
}

type Service struct {
	Store     IssueStore
	Linear    LinearClient
	Config    config.IssueTrackerConfig
	ProjectID string
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
}

type Summary struct {
	Provider              string `json:"provider"`
	Enabled               bool   `json:"enabled"`
	Skipped               bool   `json:"skipped"`
	Reason                string `json:"reason,omitempty"`
	Imported              int    `json:"imported"`
	UpdatedLocal          int    `json:"updated_local"`
	PushedRemote          int    `json:"pushed_remote"`
	PushBatches           int    `json:"push_batches"`
	Conflicts             int    `json:"conflicts"`
	RemoteIssues          int    `json:"remote_issues"`
	LocalIssues           int    `json:"local_issues"`
	SkippedPushOutOfScope int    `json:"skipped_push_out_of_scope"`
	OutOfScopeRefs        int    `json:"out_of_scope_refs"`
	SkippedUnchanged      int    `json:"skipped_unchanged"`
	PendingPushes         int    `json:"pending_pushes"`
	PushBudgetExhausted   bool   `json:"push_budget_exhausted"`
	RetriedRequests       int    `json:"retried_requests"`
	APIRequests           int    `json:"api_requests"`
	RateLimitLimit        int    `json:"rate_limit_limit,omitempty"`
	RateLimitRemaining    int    `json:"rate_limit_remaining,omitempty"`
	RateLimitReset        string `json:"rate_limit_reset,omitempty"`
	Incremental           bool   `json:"incremental"`
	Cursor                string `json:"cursor,omitempty"`
	RemoteScopeIssues     int    `json:"remote_scope_issues,omitempty"`
}

func (s Service) Run(ctx context.Context) (Summary, error) {
	summary := Summary{Provider: ProviderLinear, Enabled: s.Config.Sync.Enabled}
	if strings.TrimSpace(strings.ToLower(s.Config.Backend)) != ProviderLinear || !s.Config.Sync.Enabled {
		summary.Skipped = true
		summary.Reason = "linear sync is not enabled"
		return summary, nil
	}
	if s.Store == nil {
		return summary, fmt.Errorf("issue store is required")
	}
	if s.Linear == nil {
		return summary, fmt.Errorf("linear client is required")
	}
	now := time.Now().UTC
	if s.Now != nil {
		now = func() time.Time { return s.Now().UTC() }
	}
	localTasks, err := s.Store.List(ctx)
	if err != nil {
		return summary, fmt.Errorf("list local issues: %w", err)
	}
	summary.LocalIssues = len(localTasks)
	localByID := map[string]domain.Task{}
	for _, task := range localTasks {
		localByID[task.ID.String()] = task
	}
	refs, err := s.Store.ListExternalRefs(ctx, ProviderLinear)
	if err != nil {
		return summary, fmt.Errorf("list external refs: %w", err)
	}
	refByIdentifier := map[string]issues.ExternalRef{}
	refByIssueID := map[string]issues.ExternalRef{}
	for _, ref := range refs {
		refByIdentifier[strings.ToUpper(ref.ExternalIdentifier)] = ref
		refByIssueID[ref.IssueID] = ref
	}
	listOpts, err := s.listOptions(ctx)
	if err != nil {
		return summary, err
	}
	stateID := s.syncStateID(listOpts)
	state, _, err := s.Store.GetExternalSyncState(ctx, ProviderLinear, stateID)
	if err != nil {
		return summary, fmt.Errorf("load linear sync state: %w", err)
	}
	if cursorTime := parseCursor(state.Cursor); !cursorTime.IsZero() {
		after := cursorTime.Add(-time.Second)
		listOpts.UpdatedAfter = &after
		summary.Incremental = true
		summary.Cursor = cursorTime.UTC().Format(time.RFC3339Nano)
	}
	remoteIssues, err := s.Linear.ListIssues(ctx, listOpts)
	if err != nil {
		_ = s.recordSyncStateError(ctx, stateID, state.Cursor, err, now())
		return summary, fmt.Errorf("list linear issues: %w", err)
	}
	summary.RemoteIssues = len(remoteIssues)
	maxRemoteUpdated := maxIssueUpdatedAt(remoteIssues)
	remoteExternalIDs := map[string]struct{}{}
	for _, remote := range remoteIssues {
		if strings.TrimSpace(remote.ID) != "" {
			remoteExternalIDs[strings.TrimSpace(remote.ID)] = struct{}{}
		}
		task, ok := taskFromLinear(remote)
		if !ok {
			continue
		}
		issueID := task.ID.String()
		remoteHash := issues.HashTaskForSync(task)
		ref, hasRef := refByIdentifier[strings.ToUpper(remote.Identifier)]
		local, hasLocal := localByID[issueID]
		if !hasLocal {
			if _, err := s.Store.UpsertSyncedTask(ctx, task); err != nil {
				return summary, fmt.Errorf("import linear issue %s: %w", remote.Identifier, err)
			}
			summary.Imported++
			if err := s.Store.UpsertExternalRef(ctx, externalRef(remote, issueID, remoteHash, now())); err != nil {
				return summary, err
			}
			refByIdentifier[strings.ToUpper(remote.Identifier)] = externalRef(remote, issueID, remoteHash, now())
			continue
		}
		if !hasRef {
			ref = externalRef(remote, issueID, remoteHash, now())
		}
		localHash := issues.HashTaskForSync(local)
		localChanged := ref.LastSyncHash != "" && localHash != ref.LastSyncHash
		remoteChanged := ref.LastSyncHash != "" && remoteHash != ref.LastSyncHash
		if localChanged && remoteChanged {
			if err := s.Store.RecordSyncConflict(ctx, issues.SyncConflict{
				Provider:        ProviderLinear,
				ProjectID:       strings.TrimSpace(s.ProjectID),
				IssueID:         issueID,
				Field:           "issue",
				LocalValue:      local.Title,
				RemoteValue:     task.Title,
				LocalUpdatedAt:  &local.UpdatedAt,
				RemoteUpdatedAt: &task.UpdatedAt,
				DetectedAt:      now(),
			}); err != nil {
				return summary, err
			}
			summary.Conflicts++
			continue
		}
		if remoteChanged || ref.LastSyncHash == "" {
			if _, err := s.Store.UpsertSyncedTask(ctx, task); err != nil {
				return summary, fmt.Errorf("update local issue %s: %w", issueID, err)
			}
			summary.UpdatedLocal++
			localByID[issueID] = task
			localHash = remoteHash
		}
		ref = externalRef(remote, issueID, remoteHash, now())
		if err := s.Store.UpsertExternalRef(ctx, ref); err != nil {
			return summary, err
		}
		refByIssueID[issueID] = ref
	}
	for _, local := range localTasks {
		ref, ok := refByIssueID[local.ID.String()]
		if !ok || ref.ExternalID == "" {
			continue
		}
		localHash := issues.HashTaskForSync(local)
		if ref.LastSyncHash == localHash {
			summary.SkippedUnchanged++
			continue
		}
		summary.PendingPushes++
	}
	if summary.PendingPushes > 0 && summary.Incremental {
		scopeOpts := listOpts
		scopeOpts.UpdatedAfter = nil
		scopeIssues, err := s.Linear.ListIssues(ctx, scopeOpts)
		if err != nil {
			_ = s.recordSyncStateError(ctx, stateID, state.Cursor, err, now())
			return summary, fmt.Errorf("list linear issue push scope: %w", err)
		}
		summary.RemoteScopeIssues = len(scopeIssues)
		maxRemoteUpdated = maxTime(maxRemoteUpdated, maxIssueUpdatedAt(scopeIssues))
		remoteExternalIDs = map[string]struct{}{}
		for _, remote := range scopeIssues {
			if strings.TrimSpace(remote.ID) != "" {
				remoteExternalIDs[strings.TrimSpace(remote.ID)] = struct{}{}
			}
		}
	}
	pendingBatch := []pendingPush{}
	for _, local := range localTasks {
		ref, ok := refByIssueID[local.ID.String()]
		if !ok || ref.ExternalID == "" {
			continue
		}
		localHash := issues.HashTaskForSync(local)
		if ref.LastSyncHash == localHash {
			continue
		}
		if _, inScope := remoteExternalIDs[strings.TrimSpace(ref.ExternalID)]; !inScope {
			summary.SkippedPushOutOfScope++
			summary.OutOfScopeRefs++
			continue
		}
		if len(pendingBatch)+summary.PushedRemote >= defaultMaxPushesPerRun {
			summary.PushBudgetExhausted = true
			continue
		}
		if s.rateLimitBudgetLow() {
			summary.PushBudgetExhausted = true
			continue
		}
		description := local.Description
		priority := int(local.Priority)
		pendingBatch = append(pendingBatch, pendingPush{
			IssueID:   local.ID.String(),
			LocalHash: localHash,
			Request: linearapi.IssueUpdateRequest{
				ID: ref.ExternalID,
				Input: linearapi.IssueInput{
					Title:       local.Title,
					Description: &description,
					Priority:    &priority,
				},
			},
		})
		if len(pendingBatch) >= defaultPushBatchSize {
			if err := s.flushPushBatch(ctx, pendingBatch, &summary, stateID, state.Cursor, now()); err != nil {
				return summary, err
			}
			pendingBatch = pendingBatch[:0]
		}
	}
	if len(pendingBatch) > 0 {
		if err := s.flushPushBatch(ctx, pendingBatch, &summary, stateID, state.Cursor, now()); err != nil {
			return summary, err
		}
	}
	if !maxRemoteUpdated.IsZero() {
		state.Cursor = maxRemoteUpdated.UTC().Format(time.RFC3339Nano)
	}
	state.Provider = ProviderLinear
	state.ProjectID = stateID
	state.LastSuccessAt = now()
	state.LastError = ""
	state.UpdatedAt = now()
	if err := s.Store.UpsertExternalSyncState(ctx, state); err != nil {
		return summary, fmt.Errorf("save linear sync state: %w", err)
	}
	s.applyLinearMetrics(&summary)
	return summary, nil
}

type pendingPush struct {
	IssueID   string
	LocalHash string
	Request   linearapi.IssueUpdateRequest
}

func (s Service) flushPushBatch(ctx context.Context, batch []pendingPush, summary *Summary, stateID, cursor string, now time.Time) error {
	if len(batch) == 0 {
		return nil
	}
	requests := make([]linearapi.IssueUpdateRequest, 0, len(batch))
	for _, item := range batch {
		requests = append(requests, item.Request)
	}
	updated, retried, err := s.updateIssuesWithRetry(ctx, requests)
	summary.RetriedRequests += retried
	if err != nil {
		_ = s.recordSyncStateError(ctx, stateID, cursor, err, now)
		return fmt.Errorf("push %d linear issue(s): %w", len(batch), err)
	}
	if len(updated) != len(batch) {
		err := fmt.Errorf("linear batch update returned %d issue(s), want %d", len(updated), len(batch))
		_ = s.recordSyncStateError(ctx, stateID, cursor, err, now)
		return err
	}
	summary.PushBatches++
	for i, issue := range updated {
		item := batch[i]
		summary.PushedRemote++
		if err := s.Store.UpsertExternalRef(ctx, externalRef(issue, item.IssueID, item.LocalHash, now)); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) updateIssuesWithRetry(ctx context.Context, requests []linearapi.IssueUpdateRequest) ([]linearapi.Issue, int, error) {
	var lastErr error
	retries := 0
	for attempt := 1; attempt <= defaultRetryAttempts; attempt++ {
		updated, err := s.Linear.UpdateIssues(ctx, requests)
		if err == nil {
			return updated, retries, nil
		}
		lastErr = err
		if !linearapi.IsRetryable(err) || attempt == defaultRetryAttempts {
			break
		}
		if err := s.sleep(ctx, time.Duration(attempt)*defaultRetryDelay); err != nil {
			return nil, retries, err
		}
		retries++
	}
	return nil, retries, lastErr
}

func (s Service) sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if s.Sleep != nil {
		return s.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s Service) recordSyncStateError(ctx context.Context, stateID, cursor string, err error, now time.Time) error {
	return s.Store.UpsertExternalSyncState(ctx, issues.ExternalSyncState{
		Provider:  ProviderLinear,
		ProjectID: stateID,
		Cursor:    cursor,
		LastError: err.Error(),
		UpdatedAt: now,
	})
}

func (s Service) applyLinearMetrics(summary *Summary) {
	metrics := s.Linear.Metrics()
	summary.APIRequests = metrics.RequestCount
	summary.RateLimitLimit = metrics.RateLimit.Limit
	summary.RateLimitRemaining = metrics.RateLimit.Remaining
	if !metrics.RateLimit.Reset.IsZero() {
		summary.RateLimitReset = metrics.RateLimit.Reset.UTC().Format(time.RFC3339Nano)
	}
}

func (s Service) rateLimitBudgetLow() bool {
	remaining := s.Linear.Metrics().RateLimit.Remaining
	return remaining > 0 && remaining <= minRemainingRequests
}

func (s Service) syncStateID(opts linearapi.ListIssuesOptions) string {
	raw := strings.Join([]string{
		strings.TrimSpace(s.ProjectID),
		strings.TrimSpace(opts.TeamKey),
		strings.TrimSpace(opts.Project),
		strings.TrimSpace(opts.AssigneeID),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return strings.TrimSpace(s.ProjectID) + ":" + hex.EncodeToString(sum[:8])
}

func (s Service) listOptions(ctx context.Context) (linearapi.ListIssuesOptions, error) {
	opts := linearapi.ListIssuesOptions{
		TeamKey: strings.TrimSpace(s.Config.Linear.Team),
		Project: strings.TrimSpace(s.Config.Linear.Project),
	}
	var assignee string
	if s.Config.Linear.Filter != nil {
		assignee = strings.TrimSpace(s.Config.Linear.Filter.Assignee)
	}
	if assignee == "" {
		return opts, nil
	}
	if strings.EqualFold(assignee, "me") {
		viewerID, err := s.Linear.ViewerID(ctx)
		if err != nil {
			return opts, fmt.Errorf("resolve linear filter assignee me: %w", err)
		}
		opts.AssigneeID = viewerID
		return opts, nil
	}
	opts.AssigneeID = assignee
	return opts, nil
}

func parseCursor(raw string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw)); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func maxIssueUpdatedAt(items []linearapi.Issue) time.Time {
	var max time.Time
	for _, item := range items {
		max = maxTime(max, item.UpdatedAt)
	}
	return max
}

func maxTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func (s Service) Conflicts(ctx context.Context, includeResolved bool) ([]issues.SyncConflict, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("issue store is required")
	}
	return s.Store.ListSyncConflicts(ctx, ProviderLinear, strings.TrimSpace(s.ProjectID), includeResolved)
}

func taskFromLinear(issue linearapi.Issue) (domain.Task, bool) {
	identifier := strings.TrimSpace(issue.Identifier)
	if identifier == "" {
		return domain.Task{}, false
	}
	issueID, err := naming.ParseIssueID(identifier)
	if err != nil {
		return domain.Task{}, false
	}
	status := mapLinearStatus(issue.State)
	priority := mapLinearPriority(issue.Priority)
	taskType := domain.TypeTask
	for _, label := range issue.Labels {
		switch strings.ToLower(strings.TrimSpace(label)) {
		case "bug":
			taskType = domain.TypeBug
		case "feature":
			taskType = domain.TypeFeature
		case "chore":
			taskType = domain.TypeChore
		}
	}
	assignee := issue.Assignee.Email
	if assignee == "" {
		assignee = issue.Assignee.Name
	}
	return domain.Task{
		ID:          issueID,
		Title:       strings.TrimSpace(issue.Title),
		Description: strings.TrimSpace(issue.Description),
		Status:      status,
		Priority:    priority,
		Type:        taskType,
		Assignee:    strings.TrimSpace(assignee),
		Labels:      append([]string(nil), issue.Labels...),
		CreatedAt:   issue.CreatedAt,
		UpdatedAt:   issue.UpdatedAt,
	}, true
}

func mapLinearStatus(state linearapi.State) domain.Status {
	raw := strings.ToLower(strings.TrimSpace(state.Type + " " + state.Name))
	switch {
	case strings.Contains(raw, "completed"), strings.Contains(raw, "done"), strings.Contains(raw, "closed"):
		return domain.StatusDone
	case strings.Contains(raw, "started"), strings.Contains(raw, "progress"):
		return domain.StatusInProgress
	case strings.Contains(raw, "blocked"):
		return domain.StatusBlocked
	default:
		return domain.StatusOpen
	}
}

func mapLinearPriority(priority int) domain.Priority {
	switch priority {
	case 1:
		return domain.P0
	case 2:
		return domain.P1
	case 3:
		return domain.P2
	case 4:
		return domain.P3
	default:
		return domain.P4
	}
}

func externalRef(issue linearapi.Issue, issueID, hash string, syncedAt time.Time) issues.ExternalRef {
	return issues.ExternalRef{
		Provider:           ProviderLinear,
		IssueID:            strings.TrimSpace(issueID),
		ExternalID:         strings.TrimSpace(issue.ID),
		ExternalIdentifier: strings.TrimSpace(issue.Identifier),
		ExternalURL:        strings.TrimSpace(issue.URL),
		ExternalUpdatedAt:  issue.UpdatedAt,
		LastSyncedAt:       syncedAt.UTC(),
		LastSyncHash:       hash,
	}
}
