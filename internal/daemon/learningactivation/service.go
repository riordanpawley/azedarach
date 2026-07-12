package learningactivation

import (
	"context"
	"strings"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type Store interface {
	ListLearnings(context.Context, issues.LearningFilter) ([]issues.Learning, error)
	DeliveredLearningIDs(context.Context, string, string) (map[string]struct{}, error)
	ProposeLearningActivation(context.Context, issues.RecordLearningActivationParams) (issues.LearningActivationProposal, error)
	ConfirmLearningActivation(context.Context, string, string, int) (issues.LearningActivation, error)
	RecordLearningActivationOutcome(context.Context, issues.LearningActivationOutcome) (issues.LearningActivationOutcome, bool, error)
}

type Request struct {
	ProjectID, Purpose, Surface, SessionID, IssueID, RequirementID, Query string
	Tags, Files                                                           []string
	TokenBudget                                                           int
}

type Proposal struct {
	Activation  issues.LearningActivationProposal
	Learnings   []issues.Learning
	Explanation string
}

type Service struct{ store Store }

func New(store Store) *Service { return &Service{store: store} }

func (s *Service) Propose(ctx context.Context, req Request) (Proposal, error) {
	rows, err := s.store.ListLearnings(ctx, issues.LearningFilter{ProjectID: req.ProjectID, ContextIssueID: req.IssueID, ContextReqID: req.RequirementID, ContextTags: req.Tags, ContextFiles: req.Files, Query: req.Query, Statuses: []issues.LearningStatus{issues.LearningStatusAccepted, issues.LearningStatusPromoted}, ExcludePrivate: true, ActiveOnly: true, SkipRecallTracking: true})
	if err != nil {
		return Proposal{}, err
	}
	suppressed, err := s.store.DeliveredLearningIDs(ctx, req.ProjectID, req.SessionID)
	if err != nil {
		return Proposal{}, err
	}
	candidates := make([]domain.LearningActivationCandidate, 0, len(rows))
	byID := make(map[string]issues.Learning, len(rows))
	for _, row := range rows {
		candidates = append(candidates, domain.LearningActivationCandidate{ID: row.LocalID, Summary: row.Summary, Score: row.RecallScore, Reason: row.RecallReason})
		byID[row.LocalID] = row
	}
	selection := domain.SelectContextualLearnings(candidates, suppressed, req.TokenBudget)
	if len(selection.IDs) == 0 {
		return Proposal{Explanation: "no eligible guidance within budget or all guidance already delivered to session"}, nil
	}
	learnings := make([]issues.Learning, 0, len(selection.IDs))
	for _, id := range selection.IDs {
		learnings = append(learnings, byID[id])
	}
	explanation := strings.Join(selection.Explanations, "; ")
	fingerprint, err := domain.LearningActivationContextFingerprint(req.Purpose, req.SessionID, req.IssueID, req.RequirementID, req.Query, req.Tags, req.Files)
	if err != nil {
		return Proposal{}, err
	}
	a, err := s.store.ProposeLearningActivation(ctx, issues.RecordLearningActivationParams{ProjectID: req.ProjectID, Surface: req.Surface, ContextFingerprint: fingerprint, Purpose: req.Purpose, SessionID: req.SessionID, LearningIDs: selection.IDs, Explanation: explanation})
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{Activation: a, Learnings: learnings, Explanation: explanation}, nil
}

func (s *Service) Confirm(ctx context.Context, projectID, activationID string, tokenCost int) (issues.LearningActivation, error) {
	return s.store.ConfirmLearningActivation(ctx, projectID, activationID, tokenCost)
}
func (s *Service) Feedback(ctx context.Context, in issues.LearningActivationOutcome) (issues.LearningActivationOutcome, bool, error) {
	return s.store.RecordLearningActivationOutcome(ctx, in)
}
