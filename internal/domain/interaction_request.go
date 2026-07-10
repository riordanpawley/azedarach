package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrDuplicateUnresolvedDecision = errors.New("duplicate unresolved interaction decision")
	ErrStaleInteractionRevision    = errors.New("stale interaction revision")
	ErrInvalidInteractionAnswer    = errors.New("invalid interaction answer")
)

type InteractionState string

const (
	InteractionOpen           InteractionState = "open"
	InteractionDiscussing     InteractionState = "discussing"
	InteractionAnswerProposed InteractionState = "answer_proposed"
	InteractionResolved       InteractionState = "resolved"
	InteractionWithdrawn      InteractionState = "withdrawn"
	InteractionSuperseded     InteractionState = "superseded"
)

type InteractionSignificance string

const (
	InteractionSignificanceRoutine  InteractionSignificance = "routine"
	InteractionSignificanceMaterial InteractionSignificance = "material"
	InteractionSignificanceCritical InteractionSignificance = "critical"
)

type InteractionOption struct {
	Key         string `json:"key" msgpack:"key"`
	Label       string `json:"label" msgpack:"label"`
	Description string `json:"description,omitempty" msgpack:"description,omitempty"`
}

type InteractionDecisionPacket struct {
	Summary        string   `json:"summary" msgpack:"summary"`
	Alternatives   []string `json:"alternatives,omitempty" msgpack:"alternatives,omitempty"`
	Recommendation string   `json:"recommendation,omitempty" msgpack:"recommendation,omitempty"`
}

// InteractionIssueFieldEffects is the complete set of issue mutations explicitly
// approved in an answer. Nil fields are not approved and must remain unchanged.
type InteractionIssueFieldEffects struct {
	Title       *string `json:"title,omitempty" msgpack:"title,omitempty"`
	Description *string `json:"description,omitempty" msgpack:"description,omitempty"`
	Design      *string `json:"design,omitempty" msgpack:"design,omitempty"`
	Acceptance  *string `json:"acceptance,omitempty" msgpack:"acceptance,omitempty"`
	Priority    *int    `json:"priority,omitempty" msgpack:"priority,omitempty"`
}

// InteractionAnswerPayload is shared by advisor proposals and human final
// answers. Revision identifies the request context the answer was authored
// against; mutations must additionally pass the store's optimistic revision gate.
type InteractionAnswerPayload struct {
	SelectedOption             string                       `json:"selected_option" msgpack:"selected_option"`
	Rationale                  string                       `json:"rationale" msgpack:"rationale"`
	Constraints                []string                     `json:"constraints,omitempty" msgpack:"constraints,omitempty"`
	ApprovedIssueFieldEffects  InteractionIssueFieldEffects `json:"approved_issue_field_effects,omitempty" msgpack:"approved_issue_field_effects,omitempty"`
	SignificanceRecommendation InteractionSignificance      `json:"significance_recommendation" msgpack:"significance_recommendation"`
	Revision                   int64                        `json:"revision" msgpack:"revision"`
}

type InteractionAnswerAudit struct {
	Answer    InteractionAnswerPayload `json:"answer" msgpack:"answer"`
	Actor     string                   `json:"actor" msgpack:"actor"`
	CreatedAt time.Time                `json:"created_at" msgpack:"created_at"`
}

type InteractionRequest struct {
	ID                 string                    `json:"id" msgpack:"id"`
	IssueID            string                    `json:"issue_id" msgpack:"issue_id"`
	DecisionKey        string                    `json:"decision_key" msgpack:"decision_key"`
	OrchestrationScope string                    `json:"orchestration_scope" msgpack:"orchestration_scope"`
	Question           string                    `json:"question" msgpack:"question"`
	Why                string                    `json:"why" msgpack:"why"`
	Options            []InteractionOption       `json:"options,omitempty" msgpack:"options,omitempty"`
	RequiredDecisions  []string                  `json:"required_decisions,omitempty" msgpack:"required_decisions,omitempty"`
	Context            string                    `json:"context,omitempty" msgpack:"context,omitempty"`
	Significance       InteractionSignificance   `json:"significance" msgpack:"significance"`
	Respondent         string                    `json:"respondent" msgpack:"respondent"`
	DecisionPacket     InteractionDecisionPacket `json:"decision_packet" msgpack:"decision_packet"`
	Proposal           *InteractionAnswerAudit   `json:"proposal,omitempty" msgpack:"proposal,omitempty"`
	FinalAnswer        *InteractionAnswerAudit   `json:"final_answer,omitempty" msgpack:"final_answer,omitempty"`
	SessionID          string                    `json:"session_id,omitempty" msgpack:"session_id,omitempty"`
	State              InteractionState          `json:"state" msgpack:"state"`
	Revision           int64                     `json:"revision" msgpack:"revision"`
	CreatedAt          time.Time                 `json:"created_at" msgpack:"created_at"`
	UpdatedAt          time.Time                 `json:"updated_at" msgpack:"updated_at"`
}

func (r InteractionRequest) Unresolved() bool {
	switch r.State {
	case InteractionOpen, InteractionDiscussing, InteractionAnswerProposed:
		return true
	default:
		return false
	}
}

func (r InteractionRequest) BlocksIssue() bool { return r.Unresolved() }

func (r InteractionRequest) Validate() error {
	required := map[string]string{
		"id": r.ID, "issue_id": r.IssueID, "decision_key": r.DecisionKey,
		"orchestration_scope": r.OrchestrationScope, "question": r.Question,
		"why": r.Why, "respondent": r.Respondent,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("interaction %s is required", field)
		}
	}
	if r.Revision < 1 {
		return fmt.Errorf("interaction revision must be positive")
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return fmt.Errorf("interaction timestamps are invalid")
	}
	if r.Significance != InteractionSignificanceRoutine && r.Significance != InteractionSignificanceMaterial && r.Significance != InteractionSignificanceCritical {
		return fmt.Errorf("invalid interaction significance: %s", r.Significance)
	}
	if len(r.Options) == 0 && len(r.RequiredDecisions) == 0 {
		return fmt.Errorf("interaction requires options or required decisions")
	}
	optionKeys := make(map[string]struct{}, len(r.Options))
	for _, option := range r.Options {
		key := strings.TrimSpace(option.Key)
		if key == "" || strings.TrimSpace(option.Label) == "" {
			return fmt.Errorf("interaction option key and label are required")
		}
		if _, exists := optionKeys[key]; exists {
			return fmt.Errorf("duplicate interaction option key: %s", key)
		}
		optionKeys[key] = struct{}{}
	}
	for _, decision := range r.RequiredDecisions {
		if strings.TrimSpace(decision) == "" {
			return fmt.Errorf("interaction required decision must not be empty")
		}
	}
	if strings.TrimSpace(r.DecisionPacket.Summary) == "" {
		return fmt.Errorf("interaction decision packet summary is required")
	}
	if !validInteractionState(r.State) {
		return fmt.Errorf("invalid interaction state: %s", r.State)
	}
	if r.Proposal != nil {
		if err := r.validateAnswerAudit("proposal", r.Proposal); err != nil {
			return err
		}
	}
	if r.State == InteractionAnswerProposed && r.Proposal == nil {
		return fmt.Errorf("interaction proposal audit is required")
	}
	if r.FinalAnswer != nil {
		if err := r.validateAnswerAudit("final answer", r.FinalAnswer); err != nil {
			return err
		}
	}
	if r.State == InteractionResolved && r.FinalAnswer == nil {
		return fmt.Errorf("interaction final answer audit is required")
	}
	if r.State != InteractionResolved && r.FinalAnswer != nil {
		return fmt.Errorf("interaction final answer requires resolved state")
	}
	return nil
}

func (r InteractionRequest) Transition(next InteractionState, expectedRevision int64, at time.Time) (InteractionRequest, error) {
	if expectedRevision != r.Revision {
		return InteractionRequest{}, fmt.Errorf("%w: expected %d, current %d", ErrStaleInteractionRevision, expectedRevision, r.Revision)
	}
	if at.IsZero() || at.Before(r.UpdatedAt) {
		return InteractionRequest{}, fmt.Errorf("interaction transition time is invalid")
	}
	if !interactionTransitionAllowed(r.State, next) {
		return InteractionRequest{}, fmt.Errorf("interaction transition %s -> %s is not allowed", r.State, next)
	}
	r.State, r.Revision, r.UpdatedAt = next, r.Revision+1, at
	if err := r.Validate(); err != nil {
		return InteractionRequest{}, err
	}
	return r, nil
}

func RejectDuplicateUnresolvedInteraction(existing []InteractionRequest, candidate InteractionRequest) error {
	if !candidate.Unresolved() {
		return nil
	}
	for _, request := range existing {
		if request.ID != candidate.ID && request.Unresolved() && request.IssueID == candidate.IssueID && request.DecisionKey == candidate.DecisionKey {
			return fmt.Errorf("%w: issue %s decision key %s", ErrDuplicateUnresolvedDecision, candidate.IssueID, candidate.DecisionKey)
		}
	}
	return nil
}

func IssueWaitingHuman(issueID string, requests []InteractionRequest) bool {
	for _, request := range requests {
		if request.IssueID == issueID && request.Unresolved() {
			return true
		}
	}
	return false
}

type ProjectInteractionPredicates struct {
	HasExecutableWork     bool
	HasActiveSessions     bool
	HasReviewRequests     bool
	HasUnresolvedRequests bool
}

func (p ProjectInteractionPredicates) Quiescent() bool {
	return !p.HasExecutableWork && !p.HasActiveSessions && !p.HasReviewRequests
}

func (p ProjectInteractionPredicates) Complete() bool {
	return p.Quiescent() && !p.HasUnresolvedRequests
}

func validInteractionState(state InteractionState) bool {
	switch state {
	case InteractionOpen, InteractionDiscussing, InteractionAnswerProposed, InteractionResolved, InteractionWithdrawn, InteractionSuperseded:
		return true
	default:
		return false
	}
}

func interactionTransitionAllowed(from, to InteractionState) bool {
	switch from {
	case InteractionOpen:
		return to == InteractionDiscussing || to == InteractionAnswerProposed || to == InteractionResolved || to == InteractionWithdrawn || to == InteractionSuperseded
	case InteractionDiscussing:
		return to == InteractionAnswerProposed || to == InteractionResolved || to == InteractionWithdrawn || to == InteractionSuperseded
	case InteractionAnswerProposed:
		return to == InteractionDiscussing || to == InteractionResolved || to == InteractionWithdrawn || to == InteractionSuperseded
	default:
		return false
	}
}

func (r InteractionRequest) validateAnswerAudit(name string, audit *InteractionAnswerAudit) error {
	if audit == nil || strings.TrimSpace(audit.Actor) == "" || audit.CreatedAt.IsZero() {
		return fmt.Errorf("interaction %s audit is required", name)
	}
	if audit.CreatedAt.Before(r.CreatedAt) || audit.CreatedAt.After(r.UpdatedAt) {
		return fmt.Errorf("interaction %s timestamp is outside the request audit window", name)
	}
	if err := r.validateAnswerPayload(name, audit.Answer); err != nil {
		return err
	}
	return nil
}

func (r InteractionRequest) validateAnswerPayload(name string, answer InteractionAnswerPayload) error {
	selected := strings.TrimSpace(answer.SelectedOption)
	if selected == "" || strings.TrimSpace(answer.Rationale) == "" {
		return fmt.Errorf("%w: interaction %s selected option and rationale are required", ErrInvalidInteractionAnswer, name)
	}
	if answer.Revision < 1 || answer.Revision >= r.Revision {
		return fmt.Errorf("%w: interaction %s revision %d is outside the request audit window", ErrInvalidInteractionAnswer, name, answer.Revision)
	}
	if answer.SignificanceRecommendation != InteractionSignificanceRoutine && answer.SignificanceRecommendation != InteractionSignificanceMaterial && answer.SignificanceRecommendation != InteractionSignificanceCritical {
		return fmt.Errorf("%w: interaction %s significance recommendation is invalid: %s", ErrInvalidInteractionAnswer, name, answer.SignificanceRecommendation)
	}
	if len(r.Options) > 0 {
		found := false
		for _, option := range r.Options {
			if option.Key == selected {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: interaction %s selected option is not offered: %s", ErrInvalidInteractionAnswer, name, selected)
		}
	}
	seenConstraints := make(map[string]struct{}, len(answer.Constraints))
	for _, constraint := range answer.Constraints {
		constraint = strings.TrimSpace(constraint)
		if constraint == "" {
			return fmt.Errorf("%w: interaction %s constraint must not be empty", ErrInvalidInteractionAnswer, name)
		}
		if _, exists := seenConstraints[constraint]; exists {
			return fmt.Errorf("%w: interaction %s constraint is duplicated: %s", ErrInvalidInteractionAnswer, name, constraint)
		}
		seenConstraints[constraint] = struct{}{}
	}
	effects := answer.ApprovedIssueFieldEffects
	if effects.Title != nil && strings.TrimSpace(*effects.Title) == "" {
		return fmt.Errorf("%w: interaction %s approved issue title must be non-empty", ErrInvalidInteractionAnswer, name)
	}
	if effects.Priority != nil && (*effects.Priority < 0 || *effects.Priority > 4) {
		return fmt.Errorf("%w: interaction %s approved issue priority is invalid", ErrInvalidInteractionAnswer, name)
	}
	return nil
}
