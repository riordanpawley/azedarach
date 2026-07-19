package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type taskCloseHistoricalPublicationBinding struct {
	BindingID         string
	ReviewEventID     int64
	ValidationEventID int64
}

func (d *Daemon) recoverHistoricalTaskClosePublication(ctx context.Context, projectID, issueID, targetWorktree string, integration taskCloseIntegrationResult) (taskCloseHistoricalPublicationBinding, error) {
	projectID = protocol.NormalizeProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	targetWorktree = filepath.Clean(strings.TrimSpace(targetWorktree))
	if projectID == "" || issueID == "" || targetWorktree == "." {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication recovery requires exact project, issue, and target worktree identity")
	}
	if d.git == nil {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication recovery requires live Git authority")
	}
	var binding taskCloseHistoricalPublicationBinding
	err := d.git.WithIntegrationTransactionLock(ctx, targetWorktree, func(lockCtx context.Context) error {
		var recoverErr error
		binding, recoverErr = d.recoverHistoricalTaskClosePublicationLocked(lockCtx, projectID, issueID, targetWorktree, integration)
		return recoverErr
	})
	return binding, err
}

func (d *Daemon) recoverHistoricalTaskClosePublicationLocked(ctx context.Context, projectID, issueID, targetWorktree string, integration taskCloseIntegrationResult) (taskCloseHistoricalPublicationBinding, error) {
	if !integration.ReceiptRecovered || !integration.Integrated || !integration.ConfiguredBaseTarget || !strings.EqualFold(strings.TrimSpace(integration.TargetID), "base") {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication recovery requires an exact integrated configured-base receipt")
	}
	wantBranch := strings.TrimSpace(d.baseBranchForProject(projectID))
	if wantBranch == "" || strings.TrimSpace(integration.TargetBranch) != wantBranch {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication target branch changed: recorded=%s current=%s", strings.TrimSpace(integration.TargetBranch), wantBranch)
	}
	exactRepo := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
	if exactRepo == "" || filepath.Clean(exactRepo) != targetWorktree {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication target worktree identity changed: recorded=%s current=%s", targetWorktree, exactRepo)
	}
	receipt := taskCloseIntegrationReceipt{
		ProjectID: projectID, SourceBranch: integration.SourceBranch, TargetBranch: integration.TargetBranch,
		Integrated: true, ConfiguredBaseTarget: true, TargetID: integration.TargetID,
		BaseOID: integration.BaseOID, SourceOID: integration.SourceOID, TargetOID: integration.TargetOID,
	}
	if err := verifyTaskCloseIntegrationReceipt(ctx, d.git, targetWorktree, receipt, projectID, integration.SourceBranch, integration.TargetBranch); err != nil {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication live containment failed: %w", err)
	}
	baseContained, err := d.git.CommitContainedInRef(ctx, targetWorktree, integration.BaseOID, integration.TargetBranch)
	if err != nil {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("verify historical publication base containment: %w", err)
	}
	if !baseContained {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication base %s is not contained by typed target %s", integration.BaseOID, integration.TargetBranch)
	}

	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication recovery issue store unavailable")
	}
	events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{
		Types: []domain.IssueObservationEventType{
			domain.IssueEventReviewCompleted,
			domain.IssueEventHistoricalReviewAccepted,
			domain.IssueEventHistoricalReviewReturned,
			domain.IssueEventHistoricalValidationCompleted,
			domain.IssueEventValidationFailed,
		},
		NewestFirst: true,
	})
	if err != nil {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("read historical review and validation evidence: %w", err)
	}
	var review, validation domain.IssueObservationEvent
	for _, event := range events {
		if event.Type == domain.IssueEventReviewCompleted {
			if _, trusted := domain.TrustedReviewOutcome(event); trusted {
				return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication recovery is unavailable after daemon-owned review publication authority exists")
			}
			continue
		}
		candidate := observationPayloadString(event.Payload, "candidate_revision")
		if candidate != strings.TrimSpace(integration.TargetOID) {
			continue
		}
		switch event.Type {
		case domain.IssueEventHistoricalReviewAccepted, domain.IssueEventHistoricalReviewReturned:
			if review.ID == 0 {
				review = event
			}
		case domain.IssueEventHistoricalValidationCompleted, domain.IssueEventValidationFailed:
			if validation.ID == 0 {
				validation = event
			}
		}
	}
	if review.ID == 0 {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("exact synthetic merge %s is missing current accepted historical review evidence", integration.TargetOID)
	}
	if err := domain.ValidateHistoricalPublicationReviewEvidence(review, integration.BaseOID, integration.TargetOID); err != nil {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("exact synthetic merge %s has invalid historical review evidence: %w", integration.TargetOID, err)
	}
	if validation.ID == 0 {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("exact synthetic merge %s is missing current clean historical validation evidence", integration.TargetOID)
	}
	if err := domain.ValidateHistoricalPublicationValidationEvidence(validation, integration.BaseOID, integration.TargetOID); err != nil {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("exact synthetic merge %s has invalid historical validation evidence: %w", integration.TargetOID, err)
	}
	if validation.ID >= review.ID {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication evidence order is invalid: validation=%d review=%d", validation.ID, review.ID)
	}

	digest := sha256.Sum256([]byte(strings.Join([]string{
		projectID, issueID, strings.TrimSpace(integration.SourceBranch), strings.TrimSpace(integration.TargetBranch), "base",
		strings.TrimSpace(integration.BaseOID), strings.TrimSpace(integration.SourceOID), strings.TrimSpace(integration.TargetOID),
		strconv.FormatInt(review.ID, 10), strconv.FormatInt(validation.ID, 10),
	}, "\x00")))
	binding := taskCloseHistoricalPublicationBinding{
		BindingID: "historical-" + fmt.Sprintf("%x", digest[:16]), ReviewEventID: review.ID, ValidationEventID: validation.ID,
	}
	_, err = issueClient.BindTaskIntegrationHistoricalRecovery(ctx, issueID, issues.TaskIntegrationHistoricalBinding{
		ProjectID: projectID, SourceBranch: integration.SourceBranch, TargetBranch: integration.TargetBranch, TargetID: "base",
		BaseOID: integration.BaseOID, SourceOID: integration.SourceOID, TargetOID: integration.TargetOID,
		BindingID: binding.BindingID, ReviewEventID: binding.ReviewEventID, ValidationEventID: binding.ValidationEventID,
		WorktreePath: targetWorktree,
	})
	if err != nil {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("persist historical publication recovery binding: %w", err)
	}
	return binding, nil
}
