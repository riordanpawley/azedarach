package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type boundReviewPrompt struct {
	EpochEventID                                      int64
	Source, Digest, CompositionMode, CoverageContract string
}

func bindReviewActionPrompt(action orchestratorActionableState, prompt appconfig.ResolvedReviewPrompt) orchestratorActionableState {
	digest := sha256.Sum256([]byte(strings.TrimSpace(action.Revision) + "\x00" + prompt.Digest))
	action.Revision = fmt.Sprintf("%x", digest[:12])
	return action
}

func (d *Daemon) recordReviewPromptBindings(ctx context.Context, projectID, deliveryIntent string, prompt appconfig.ResolvedReviewPrompt, reviews []protocol.OrchestrationReview, delivered bool) error {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return fmt.Errorf("persist review prompt binding: issue store unavailable")
	}
	for _, review := range reviews {
		if !review.Actionable || review.ReviewEpochEventID <= 0 {
			continue
		}
		events, err := client.ListIssueObservationEvents(ctx, review.IssueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewPromptBound}, NewestFirst: true})
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				continue // synthetic snapshots used by lifecycle tests have no issue row
			}
			return fmt.Errorf("inspect review prompt binding for %s: %w", review.IssueID, err)
		}
		if len(events) > 0 && reviewPayloadInt64(events[0].Payload["review_epoch_event_id"]) == review.ReviewEpochEventID &&
			strings.TrimSpace(fmt.Sprint(events[0].Payload["delivery_intent_key"])) == deliveryIntent &&
			strings.EqualFold(strings.TrimSpace(fmt.Sprint(events[0].Payload["review_prompt_digest"])), prompt.Digest) &&
			strings.EqualFold(strings.TrimSpace(fmt.Sprint(events[0].Payload["delivered"])), fmt.Sprint(delivered)) {
			continue
		}
		_, err = client.AppendIssueObservationEvent(ctx, review.IssueID, issues.IssueObservationEventParams{
			Type: domain.IssueEventReviewPromptBound, Source: "daemon-orchestration", SourceCommand: "review-prompt-delivery",
			Payload: map[string]any{"review_epoch_event_id": review.ReviewEpochEventID, "delivery_intent_key": deliveryIntent, "delivered": delivered,
				"review_prompt_source": prompt.Source, "review_prompt_digest": prompt.Digest, "review_prompt_composition_mode": prompt.CompositionMode, "review_coverage_contract": prompt.CoverageContract},
		})
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				continue // synthetic snapshots used by lifecycle tests have no issue row
			}
			return fmt.Errorf("persist review prompt binding for %s: %w", review.IssueID, err)
		}
	}
	return nil
}

func (d *Daemon) boundReviewPromptForOutcome(ctx context.Context, projectID, issueID string, epoch int64, digest string) (boundReviewPrompt, error) {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return boundReviewPrompt{}, fmt.Errorf("review prompt binding unavailable")
	}
	events, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewPromptBound}, NewestFirst: true})
	if err != nil {
		return boundReviewPrompt{}, err
	}
	for _, event := range events {
		if reviewPayloadInt64(event.Payload["review_epoch_event_id"]) != epoch {
			continue
		}
		bound := boundReviewPrompt{EpochEventID: epoch, Source: strings.TrimSpace(fmt.Sprint(event.Payload["review_prompt_source"])), Digest: strings.TrimSpace(fmt.Sprint(event.Payload["review_prompt_digest"])), CompositionMode: strings.TrimSpace(fmt.Sprint(event.Payload["review_prompt_composition_mode"])), CoverageContract: strings.TrimSpace(fmt.Sprint(event.Payload["review_coverage_contract"]))}
		if !strings.EqualFold(strings.TrimSpace(fmt.Sprint(event.Payload["delivered"])), "true") {
			return boundReviewPrompt{}, fmt.Errorf("review prompt for epoch %d was not delivered", epoch)
		}
		if !strings.EqualFold(bound.Digest, strings.TrimSpace(digest)) {
			return boundReviewPrompt{}, fmt.Errorf("review prompt digest is stale for epoch %d", epoch)
		}
		return bound, nil
	}
	return boundReviewPrompt{}, fmt.Errorf("no delivered review prompt binding for epoch %d", epoch)
}

func (d *Daemon) hasReviewPromptBinding(ctx context.Context, projectID, issueID string, epoch int64) (bool, error) {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return false, fmt.Errorf("review prompt binding unavailable")
	}
	events, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewPromptBound}, NewestFirst: true})
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if reviewPayloadInt64(event.Payload["review_epoch_event_id"]) == epoch {
			return true, nil
		}
	}
	return false, nil
}

func (d *Daemon) resolvedReviewPrompt(projectID string) (appconfig.ResolvedReviewPrompt, error) {
	repoDir := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
	if repoDir == "" {
		repoDir = strings.TrimSpace(d.resolveRepoDirForProject(projectID))
	}
	if repoDir == "" {
		return appconfig.ResolvedReviewPrompt{}, fmt.Errorf("resolve review prompt: project root unavailable")
	}
	cfg, err := appconfig.LoadConfig(repoDir)
	if err != nil {
		return appconfig.ResolvedReviewPrompt{}, fmt.Errorf("load review prompt configuration: %w", err)
	}
	prompt, err := appconfig.ResolveReviewPrompt(repoDir, cfg.Orchestration)
	if err != nil {
		return appconfig.ResolvedReviewPrompt{}, fmt.Errorf("resolve review prompt: %w", err)
	}
	return prompt, nil
}

func composeReviewWakePrompt(base string, prompt appconfig.ResolvedReviewPrompt, reviewEpochs string) string {
	return fmt.Sprintf("%s\n\nAI code-review instructions (source=%s digest=%s composition_mode=%s review_epoch=%s coverage_contract=%s):\n%s",
		base, prompt.Source, prompt.Digest, prompt.CompositionMode, reviewEpochs, prompt.CoverageContract, prompt.Text)
}

func reviewEpochManifest(reviews []protocol.OrchestrationReview) string {
	values := make([]string, 0, len(reviews))
	for _, review := range reviews {
		if review.Actionable {
			values = append(values, fmt.Sprintf("%s:%d", review.IssueID, review.ReviewEpochEventID))
		}
	}
	sort.Strings(values)
	if len(values) == 0 {
		return "unavailable"
	}
	return strings.Join(values, ",")
}
