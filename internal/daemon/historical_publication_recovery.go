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
	BindingID string
}

type taskCloseHistoricalAuthorizationContextKey struct{}

func withTaskCloseHistoricalAuthorization(ctx context.Context, authorization domain.HistoricalPublicationAuthorization) context.Context {
	return context.WithValue(ctx, taskCloseHistoricalAuthorizationContextKey{}, authorization)
}

func taskCloseHistoricalAuthorizationFromContext(ctx context.Context) (domain.HistoricalPublicationAuthorization, bool) {
	authorization, ok := ctx.Value(taskCloseHistoricalAuthorizationContextKey{}).(domain.HistoricalPublicationAuthorization)
	return authorization, ok
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
	if strings.TrimSpace(integration.PublicationOperationID) != "" && strings.TrimSpace(integration.HistoricalBindingID) != "" {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication receipt contains mixed modern and historical authority")
	}
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
	authorization, authorized := taskCloseHistoricalAuthorizationFromContext(ctx)
	if strings.TrimSpace(integration.HistoricalBindingID) != "" {
		var err error
		authorization, err = loadTaskCloseHistoricalAuthorization(ctx, issueClient, issueID, integration)
		if err != nil {
			return taskCloseHistoricalPublicationBinding{}, err
		}
		authorized = true
	}
	if !authorized {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication recovery requires an explicit operator authorization; generic review records are not authority")
	}
	if err := authorization.Validate(); err != nil {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication authorization is invalid: %w", err)
	}
	originalReceiptID := integration.ReceiptEventID
	if integration.HistoricalOriginalReceiptEventID > 0 {
		originalReceiptID = integration.HistoricalOriginalReceiptEventID
	}
	if authorization.ReceiptEventID != originalReceiptID {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication authorization pins receipt %d, not exact original receipt %d", authorization.ReceiptEventID, originalReceiptID)
	}

	digest := sha256.Sum256([]byte(strings.Join([]string{
		projectID, issueID, strings.TrimSpace(integration.SourceBranch), strings.TrimSpace(integration.TargetBranch), "base",
		strings.TrimSpace(integration.BaseOID), strings.TrimSpace(integration.SourceOID), strings.TrimSpace(integration.TargetOID),
		strconv.FormatInt(authorization.ReviewEventID, 10), strconv.FormatInt(authorization.ValidationEventID, 10), strconv.FormatInt(authorization.ReceiptEventID, 10),
		strings.TrimSpace(authorization.ReviewerID), strings.TrimSpace(authorization.AuthoritativeEvidenceID), string(authorization.Class), string(authorization.Scope), string(authorization.Purpose), string(authorization.Execution), string(authorization.Override), strconv.FormatBool(authorization.EvidencePresent), strconv.FormatBool(authorization.AttestsMissingLegacySemantics),
	}, "\x00")))
	binding := taskCloseHistoricalPublicationBinding{
		BindingID: "historical-" + fmt.Sprintf("%x", digest[:16]),
	}
	if integration.HistoricalBindingID != "" && integration.HistoricalBindingID != binding.BindingID {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("historical publication binding changed: recorded=%s current=%s", integration.HistoricalBindingID, binding.BindingID)
	}
	_, err = issueClient.BindTaskIntegrationHistoricalRecovery(ctx, issueID, issues.TaskIntegrationHistoricalBinding{
		ProjectID: projectID, SourceBranch: integration.SourceBranch, TargetBranch: integration.TargetBranch, TargetID: "base",
		BaseOID: integration.BaseOID, SourceOID: integration.SourceOID, TargetOID: integration.TargetOID,
		BindingID: binding.BindingID, Authorization: authorization,
		WorktreePath: targetWorktree,
	})
	if err != nil {
		return taskCloseHistoricalPublicationBinding{}, fmt.Errorf("persist historical publication recovery binding: %w", err)
	}
	return binding, nil
}

func loadTaskCloseHistoricalAuthorization(ctx context.Context, issueClient *issues.Client, issueID string, integration taskCloseIntegrationResult) (domain.HistoricalPublicationAuthorization, error) {
	if integration.HistoricalAuthorizationEventID <= 0 || integration.HistoricalOriginalReceiptEventID <= 0 {
		return domain.HistoricalPublicationAuthorization{}, fmt.Errorf("historical correction is missing exact authorization or original receipt identity")
	}
	events, err := issueClient.GetProjectIssueObservationEventsByIDs(ctx, []int64{integration.HistoricalAuthorizationEventID})
	if err != nil {
		return domain.HistoricalPublicationAuthorization{}, fmt.Errorf("read historical authorization: %w", err)
	}
	for _, event := range events {
		if event.ID != integration.HistoricalAuthorizationEventID || string(event.IssueID) != strings.TrimSpace(issueID) || event.Type != domain.IssueEventTaskIntegrationHistoricalAuthorized {
			continue
		}
		if event.Source != "daemon-task-close" || event.SourceCommand != "historical-integration-authorize" || observationPayloadString(event.Payload, "binding_id") != integration.HistoricalBindingID {
			return domain.HistoricalPublicationAuthorization{}, fmt.Errorf("historical authorization %d has invalid daemon provenance or binding", event.ID)
		}
		authorization := domain.HistoricalPublicationAuthorization{
			ReviewEventID: observationPayloadInt64(event.Payload["review_event_id"]), ValidationEventID: observationPayloadInt64(event.Payload["validation_event_id"]), ReceiptEventID: observationPayloadInt64(event.Payload["original_receipt_event_id"]),
			ReviewerID: observationPayloadString(event.Payload, "reviewer_id"), AuthoritativeEvidenceID: observationPayloadString(event.Payload, "authoritative_evidence_id"),
			Class: domain.ValidationClass(observationPayloadString(event.Payload, "validation_class")), Scope: domain.ValidationScope(observationPayloadString(event.Payload, "validation_scope")), Purpose: domain.ValidationPurpose(observationPayloadString(event.Payload, "validation_purpose")),
			Execution: domain.ValidationExecution(observationPayloadString(event.Payload, "validation_execution")), Override: domain.ValidationOverride(observationPayloadString(event.Payload, "validation_override")), EvidencePresent: observationPayloadBool(event.Payload, "evidence_present"), AttestsMissingLegacySemantics: observationPayloadBool(event.Payload, "attests_missing_legacy_semantics"),
		}
		return authorization, nil
	}
	return domain.HistoricalPublicationAuthorization{}, fmt.Errorf("historical authorization event %d is absent", integration.HistoricalAuthorizationEventID)
}
