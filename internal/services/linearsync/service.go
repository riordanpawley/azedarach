package linearsync

import (
	"context"
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

type LinearClient interface {
	ListIssues(ctx context.Context, teamKey, projectName string) ([]linearapi.Issue, error)
	UpdateIssue(ctx context.Context, id string, input linearapi.IssueInput) (linearapi.Issue, error)
}

type IssueStore interface {
	List(ctx context.Context) ([]domain.Task, error)
	UpsertSyncedTask(ctx context.Context, task domain.Task) (bool, error)
	UpsertExternalRef(ctx context.Context, ref issues.ExternalRef) error
	ListExternalRefs(ctx context.Context, provider string) ([]issues.ExternalRef, error)
	RecordSyncConflict(ctx context.Context, conflict issues.SyncConflict) error
	ListSyncConflicts(ctx context.Context, provider, projectID string, includeResolved bool) ([]issues.SyncConflict, error)
}

type Service struct {
	Store     IssueStore
	Linear    LinearClient
	Config    config.IssueTrackerConfig
	ProjectID string
	Now       func() time.Time
}

type Summary struct {
	Provider     string `json:"provider"`
	Enabled      bool   `json:"enabled"`
	Skipped      bool   `json:"skipped"`
	Reason       string `json:"reason,omitempty"`
	Imported     int    `json:"imported"`
	UpdatedLocal int    `json:"updated_local"`
	PushedRemote int    `json:"pushed_remote"`
	Conflicts    int    `json:"conflicts"`
	RemoteIssues int    `json:"remote_issues"`
	LocalIssues  int    `json:"local_issues"`
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
	remoteIssues, err := s.Linear.ListIssues(ctx, s.Config.Linear.Team, s.Config.Linear.Project)
	if err != nil {
		return summary, fmt.Errorf("list linear issues: %w", err)
	}
	summary.RemoteIssues = len(remoteIssues)
	for _, remote := range remoteIssues {
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
		ref = externalRef(remote, issueID, localHash, now())
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
			continue
		}
		description := local.Description
		priority := int(local.Priority)
		updated, err := s.Linear.UpdateIssue(ctx, ref.ExternalID, linearapi.IssueInput{
			Title:       local.Title,
			Description: &description,
			Priority:    &priority,
		})
		if err != nil {
			return summary, fmt.Errorf("push linear issue %s: %w", local.ID, err)
		}
		summary.PushedRemote++
		if err := s.Store.UpsertExternalRef(ctx, externalRef(updated, local.ID.String(), localHash, now())); err != nil {
			return summary, err
		}
	}
	return summary, nil
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
