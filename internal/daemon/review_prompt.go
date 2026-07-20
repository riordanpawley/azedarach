package daemon

import (
	"fmt"
	"sort"
	"strings"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

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
