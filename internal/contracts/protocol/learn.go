package protocol

import (
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandLearnAdd                = "learn.add"
	CommandLearnRecall             = "learn.recall"
	CommandLearnShow               = "learn.show"
	CommandLearnReview             = "learn.review"
	CommandLearnPromote            = "learn.promote"
	CommandLearnRetire             = "learn.retire"
	CommandLearnRelate             = "learn.relate"
	CommandLearnStale              = "learn.stale"
	CommandLearnDemote             = "learn.demote"
	CommandLearnSupersede          = "learn.supersede"
	CommandLearnDoctor             = "learn.doctor"
	CommandLearnGC                 = "learn.gc"
	CommandLearnSuggest            = "learn.suggest"
	CommandLearnConsolidate        = "learn.consolidate"
	CommandLearnSuggestionReject   = "learn.suggestion.reject"
	CommandLearnActivate           = "learn.activate"
	CommandLearnFeedback           = "learn.feedback"
	CommandLearnCapture            = "learn.capture"
	CommandLearnContextualActivate = "learn.contextual_activate"
	CommandLearnActivationConfirm  = "learn.activation.confirm"
	CommandLearnHealth             = "learn.health"
)

type LearnHealthRequestBody struct {
	ProjectID string `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
}
type LearnHealthResponseBody struct {
	Health domain.LearningPortfolioHealth `json:"health" msgpack:"health"`
}

type LearningStatus string

const (
	LearningStatusCandidate LearningStatus = "candidate"
	LearningStatusAccepted  LearningStatus = "accepted"
	LearningStatusRejected  LearningStatus = "rejected"
	LearningStatusPromoted  LearningStatus = "promoted"
	LearningStatusStale     LearningStatus = "stale"
)

type LearningPromotionTarget string

const (
	LearningPromotionTargetRulesync LearningPromotionTarget = "rulesync"
	LearningPromotionTargetAgents   LearningPromotionTarget = "agents"
	LearningPromotionTargetSkill    LearningPromotionTarget = "skill"
	LearningPromotionTargetSpec     LearningPromotionTarget = "spec"
	LearningPromotionTargetDecision LearningPromotionTarget = "decision"
)

type LearningRelationType string

const (
	LearningRelationSupersedes LearningRelationType = "supersedes"
	LearningRelationConflicts  LearningRelationType = "conflicts"
)

type LearningTargetState string

const (
	LearningTargetStateActive  LearningTargetState = "active"
	LearningTargetStateRetired LearningTargetState = "retired"
	LearningTargetStateDrifted LearningTargetState = "drifted"
	LearningTargetStateMissing LearningTargetState = "missing"
)

type Learning struct {
	ID              string                  `json:"id" msgpack:"id"`
	ProjectID       string                  `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	IssueID         naming.IssueID          `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	ReqID           naming.RequirementID    `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
	SessionID       naming.SessionID        `json:"session_id,omitempty" msgpack:"session_id,omitempty"`
	Summary         string                  `json:"summary" msgpack:"summary"`
	Evidence        string                  `json:"evidence,omitempty" msgpack:"evidence,omitempty"`
	EvidencePrivate bool                    `json:"evidence_private,omitempty" msgpack:"evidence_private,omitempty"`
	Status          LearningStatus          `json:"status" msgpack:"status"`
	ReviewNote      string                  `json:"review_note,omitempty" msgpack:"review_note,omitempty"`
	ReviewedAt      string                  `json:"reviewed_at,omitempty" msgpack:"reviewed_at,omitempty"`
	Tags            []string                `json:"tags,omitempty" msgpack:"tags,omitempty"`
	Files           []string                `json:"files,omitempty" msgpack:"files,omitempty"`
	Target          LearningPromotionTarget `json:"target,omitempty" msgpack:"target,omitempty"`
	TargetID        string                  `json:"target_id,omitempty" msgpack:"target_id,omitempty"`
	TargetNote      string                  `json:"target_note,omitempty" msgpack:"target_note,omitempty"`
	PromotedAt      string                  `json:"promoted_at,omitempty" msgpack:"promoted_at,omitempty"`
	TargetState     LearningTargetState     `json:"target_state,omitempty" msgpack:"target_state,omitempty"`
	TargetHash      string                  `json:"target_hash,omitempty" msgpack:"target_hash,omitempty"`
	TargetMetadata  map[string]string       `json:"target_metadata,omitempty" msgpack:"target_metadata,omitempty"`
	ExpiresAt       string                  `json:"expires_at,omitempty" msgpack:"expires_at,omitempty"`
	StaleAt         string                  `json:"stale_at,omitempty" msgpack:"stale_at,omitempty"`
	LastRecalledAt  string                  `json:"last_recalled_at,omitempty" msgpack:"last_recalled_at,omitempty"`
	RecallCount     int                     `json:"recall_count,omitempty" msgpack:"recall_count,omitempty"`
	SupersededAt    string                  `json:"superseded_at,omitempty" msgpack:"superseded_at,omitempty"`
	TargetRetiredAt string                  `json:"target_retired_at,omitempty" msgpack:"target_retired_at,omitempty"`
	Relations       []LearningRelation      `json:"relations,omitempty" msgpack:"relations,omitempty"`
	TargetDriftedAt string                  `json:"target_drifted_at,omitempty" msgpack:"target_drifted_at,omitempty"`
	CreatedAt       string                  `json:"created_at" msgpack:"created_at"`
	UpdatedAt       string                  `json:"updated_at" msgpack:"updated_at"`
	RecallScore     int                     `json:"recall_score,omitempty" msgpack:"recall_score,omitempty"`
	RecallReason    string                  `json:"recall_reason,omitempty" msgpack:"recall_reason,omitempty"`
}

type LearningSuggestion struct {
	ID                  string `json:"id" msgpack:"id"`
	ProjectID           string `json:"project_id" msgpack:"project_id"`
	Kind                string `json:"kind" msgpack:"kind"`
	LeftLearningID      string `json:"left_learning_id" msgpack:"left_learning_id"`
	RightLearningID     string `json:"right_learning_id" msgpack:"right_learning_id"`
	Score               int    `json:"score" msgpack:"score"`
	Reason              string `json:"reason" msgpack:"reason"`
	Status              string `json:"status" msgpack:"status"`
	ReviewNote          string `json:"review_note,omitempty" msgpack:"review_note,omitempty"`
	CanonicalLearningID string `json:"canonical_learning_id,omitempty" msgpack:"canonical_learning_id,omitempty"`
	CreatedAt           string `json:"created_at" msgpack:"created_at"`
	UpdatedAt           string `json:"updated_at" msgpack:"updated_at"`
}

type LearnSuggestRequestBody struct {
	ProjectID string `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	Status    string `json:"status,omitempty" msgpack:"status,omitempty"`
	Refresh   bool   `json:"refresh,omitempty" msgpack:"refresh,omitempty"`
	Limit     int    `json:"limit,omitempty" msgpack:"limit,omitempty"`
}
type LearnSuggestResponseBody struct {
	Suggestions []LearningSuggestion `json:"suggestions" msgpack:"suggestions"`
}
type LearnConsolidateRequestBody struct {
	SuggestionID        string `json:"suggestion_id" msgpack:"suggestion_id"`
	CanonicalLearningID string `json:"canonical_learning_id" msgpack:"canonical_learning_id"`
	Summary             string `json:"summary,omitempty" msgpack:"summary,omitempty"`
	Note                string `json:"note" msgpack:"note"`
}
type LearnConsolidateResponseBody struct {
	Suggestion        LearningSuggestion `json:"suggestion" msgpack:"suggestion"`
	Learning          Learning           `json:"learning" msgpack:"learning"`
	SourceLearningIDs []string           `json:"source_learning_ids" msgpack:"source_learning_ids"`
}
type LearnSuggestionRejectRequestBody struct {
	SuggestionID string `json:"suggestion_id" msgpack:"suggestion_id"`
	Note         string `json:"note" msgpack:"note"`
}
type LearnSuggestionRejectResponseBody struct {
	Suggestion LearningSuggestion `json:"suggestion" msgpack:"suggestion"`
}

type LearningRelation struct {
	ID               string               `json:"id" msgpack:"id"`
	Type             LearningRelationType `json:"type" msgpack:"type"`
	SourceLearningID string               `json:"source_learning_id" msgpack:"source_learning_id"`
	TargetLearningID string               `json:"target_learning_id" msgpack:"target_learning_id"`
	Note             string               `json:"note" msgpack:"note"`
	ScopeIssueID     naming.IssueID       `json:"scope_issue_id,omitempty" msgpack:"scope_issue_id,omitempty"`
	ScopeReqID       naming.RequirementID `json:"scope_req_id,omitempty" msgpack:"scope_req_id,omitempty"`
	ScopeSessionID   naming.SessionID     `json:"scope_session_id,omitempty" msgpack:"scope_session_id,omitempty"`
	ScopeTags        []string             `json:"scope_tags,omitempty" msgpack:"scope_tags,omitempty"`
	ScopeFiles       []string             `json:"scope_files,omitempty" msgpack:"scope_files,omitempty"`
	CreatedAt        string               `json:"created_at" msgpack:"created_at"`
	UpdatedAt        string               `json:"updated_at" msgpack:"updated_at"`
}

type LearnAddRequestBody struct {
	ProjectID string               `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	IssueID   naming.IssueID       `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	ReqID     naming.RequirementID `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
	SessionID naming.SessionID     `json:"session_id,omitempty" msgpack:"session_id,omitempty"`
	Summary   string               `json:"summary,omitempty" msgpack:"summary,omitempty"`
	Evidence  string               `json:"evidence" msgpack:"evidence"`
	Private   bool                 `json:"private,omitempty" msgpack:"private,omitempty"`
	Tags      []string             `json:"tags,omitempty" msgpack:"tags,omitempty"`
	Files     []string             `json:"files,omitempty" msgpack:"files,omitempty"`
}

type LearnAddResponseBody struct {
	Learning Learning `json:"learning" msgpack:"learning"`
}

type LearningObservationProvenance struct {
	Source string `json:"source" msgpack:"source"`
	Actor  string `json:"actor,omitempty" msgpack:"actor,omitempty"`
	Ref    string `json:"ref,omitempty" msgpack:"ref,omitempty"`
}
type LearnCaptureRequestBody struct {
	ProjectID         string                        `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	IssueID           naming.IssueID                `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	ReqID             naming.RequirementID          `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
	SessionID         naming.SessionID              `json:"session_id,omitempty" msgpack:"session_id,omitempty"`
	ObservedBehavior  string                        `json:"observed_behavior" msgpack:"observed_behavior"`
	PreferredBehavior string                        `json:"preferred_behavior" msgpack:"preferred_behavior"`
	Outcome           string                        `json:"outcome,omitempty" msgpack:"outcome,omitempty"`
	Impact            string                        `json:"impact,omitempty" msgpack:"impact,omitempty"`
	Context           map[string]string             `json:"context,omitempty" msgpack:"context,omitempty"`
	Provenance        LearningObservationProvenance `json:"provenance" msgpack:"provenance"`
	Sensitivity       string                        `json:"sensitivity" msgpack:"sensitivity"`
	Tags              []string                      `json:"tags,omitempty" msgpack:"tags,omitempty"`
	Files             []string                      `json:"files,omitempty" msgpack:"files,omitempty"`
}
type LearningObservation struct {
	ID                   string                        `json:"id" msgpack:"id"`
	Learning             Learning                      `json:"learning" msgpack:"learning"`
	ObservedBehavior     string                        `json:"observed_behavior,omitempty" msgpack:"observed_behavior,omitempty"`
	PreferredBehavior    string                        `json:"preferred_behavior,omitempty" msgpack:"preferred_behavior,omitempty"`
	Outcome              string                        `json:"outcome,omitempty" msgpack:"outcome,omitempty"`
	Impact               string                        `json:"impact,omitempty" msgpack:"impact,omitempty"`
	Context              map[string]string             `json:"context,omitempty" msgpack:"context,omitempty"`
	Provenance           LearningObservationProvenance `json:"provenance" msgpack:"provenance"`
	Sensitivity          string                        `json:"sensitivity" msgpack:"sensitivity"`
	SafeFingerprint      string                        `json:"safe_fingerprint,omitempty" msgpack:"safe_fingerprint,omitempty"`
	DuplicateLearningIDs []string                      `json:"duplicate_learning_ids,omitempty" msgpack:"duplicate_learning_ids,omitempty"`
	CreatedAt            string                        `json:"created_at" msgpack:"created_at"`
}
type LearnCaptureResponseBody struct {
	Observation LearningObservation `json:"observation" msgpack:"observation"`
}

type LearnRecallRequestBody struct {
	ProjectID       string               `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	IssueID         naming.IssueID       `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	ReqID           naming.RequirementID `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
	ContextIssueID  naming.IssueID       `json:"context_issue_id,omitempty" msgpack:"context_issue_id,omitempty"`
	ContextReqID    naming.RequirementID `json:"context_req_id,omitempty" msgpack:"context_req_id,omitempty"`
	ContextTags     []string             `json:"context_tags,omitempty" msgpack:"context_tags,omitempty"`
	ContextFiles    []string             `json:"context_files,omitempty" msgpack:"context_files,omitempty"`
	Query           string               `json:"query,omitempty" msgpack:"query,omitempty"`
	Statuses        []LearningStatus     `json:"statuses,omitempty" msgpack:"statuses,omitempty"`
	Tags            []string             `json:"tags,omitempty" msgpack:"tags,omitempty"`
	Files           []string             `json:"files,omitempty" msgpack:"files,omitempty"`
	Limit           int                  `json:"limit,omitempty" msgpack:"limit,omitempty"`
	IncludeEvidence bool                 `json:"include_evidence,omitempty" msgpack:"include_evidence,omitempty"`
	IncludePrivate  bool                 `json:"include_private,omitempty" msgpack:"include_private,omitempty"`
}

type LearnRecallResponseBody struct {
	Learnings []Learning `json:"learnings" msgpack:"learnings"`
}

type LearnActivateRequestBody struct {
	ProjectID      string               `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	Surface        string               `json:"surface" msgpack:"surface"`
	ContextIssueID naming.IssueID       `json:"context_issue_id,omitempty" msgpack:"context_issue_id,omitempty"`
	ContextReqID   naming.RequirementID `json:"context_req_id,omitempty" msgpack:"context_req_id,omitempty"`
	ContextTags    []string             `json:"context_tags,omitempty" msgpack:"context_tags,omitempty"`
	ContextFiles   []string             `json:"context_files,omitempty" msgpack:"context_files,omitempty"`
	LearningIDs    []string             `json:"learning_ids" msgpack:"learning_ids"`
	TokenCost      int                  `json:"token_cost" msgpack:"token_cost"`
	Explanation    string               `json:"explanation,omitempty" msgpack:"explanation,omitempty"`
}
type LearningActivation struct {
	ActivationID       string   `json:"activation_id" msgpack:"activation_id"`
	Surface            string   `json:"surface" msgpack:"surface"`
	ContextFingerprint string   `json:"context_fingerprint" msgpack:"context_fingerprint"`
	LearningIDs        []string `json:"learning_ids" msgpack:"learning_ids"`
	TokenCost          int      `json:"token_cost" msgpack:"token_cost"`
	Explanation        string   `json:"explanation,omitempty" msgpack:"explanation,omitempty"`
	DeliveredAt        string   `json:"delivered_at" msgpack:"delivered_at"`
}
type LearningActivationProposal struct {
	ActivationID       string   `json:"activation_id" msgpack:"activation_id"`
	Surface            string   `json:"surface" msgpack:"surface"`
	ContextFingerprint string   `json:"context_fingerprint" msgpack:"context_fingerprint"`
	LearningIDs        []string `json:"learning_ids" msgpack:"learning_ids"`
	Explanation        string   `json:"explanation,omitempty" msgpack:"explanation,omitempty"`
}
type LearnActivateResponseBody struct {
	Activation LearningActivation `json:"activation" msgpack:"activation"`
}

type LearnContextualActivateRequestBody struct {
	ProjectID      string               `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	Purpose        string               `json:"purpose" msgpack:"purpose"`
	Surface        string               `json:"surface" msgpack:"surface"`
	SessionID      naming.SessionID     `json:"session_id" msgpack:"session_id"`
	ContextIssueID naming.IssueID       `json:"context_issue_id,omitempty" msgpack:"context_issue_id,omitempty"`
	ContextReqID   naming.RequirementID `json:"context_req_id,omitempty" msgpack:"context_req_id,omitempty"`
	ContextTags    []string             `json:"context_tags,omitempty" msgpack:"context_tags,omitempty"`
	ContextFiles   []string             `json:"context_files,omitempty" msgpack:"context_files,omitempty"`
	Query          string               `json:"query,omitempty" msgpack:"query,omitempty"`
	TokenBudget    int                  `json:"token_budget" msgpack:"token_budget"`
}

type LearnContextualActivateResponseBody struct {
	Proposal    *LearningActivationProposal `json:"proposal,omitempty" msgpack:"proposal,omitempty"`
	Learnings   []Learning                  `json:"learnings,omitempty" msgpack:"learnings,omitempty"`
	Explanation string                      `json:"explanation" msgpack:"explanation"`
}

type LearnActivationConfirmRequestBody struct {
	ActivationID string `json:"activation_id" msgpack:"activation_id"`
	TokenCost    int    `json:"token_cost" msgpack:"token_cost"`
}
type LearnActivationConfirmResponseBody struct {
	Activation LearningActivation `json:"activation" msgpack:"activation"`
}

type LearnFeedbackRequestBody struct {
	ActivationID   string `json:"activation_id" msgpack:"activation_id"`
	IdempotencyKey string `json:"idempotency_key" msgpack:"idempotency_key"`
	Outcome        string `json:"outcome" msgpack:"outcome"`
	Source         string `json:"source" msgpack:"source"`
	Explanation    string `json:"explanation,omitempty" msgpack:"explanation,omitempty"`
}
type LearningActivationFeedback struct {
	ActivationID    string `json:"activation_id" msgpack:"activation_id"`
	IdempotencyKey  string `json:"idempotency_key" msgpack:"idempotency_key"`
	Outcome         string `json:"outcome" msgpack:"outcome"`
	Source          string `json:"source" msgpack:"source"`
	Explanation     string `json:"explanation,omitempty" msgpack:"explanation,omitempty"`
	RecordedAt      string `json:"recorded_at" msgpack:"recorded_at"`
	ResolvedOutcome string `json:"resolved_outcome" msgpack:"resolved_outcome"`
	ResolvedSource  string `json:"resolved_source" msgpack:"resolved_source"`
}
type LearnFeedbackResponseBody struct {
	Feedback LearningActivationFeedback `json:"feedback" msgpack:"feedback"`
	Created  bool                       `json:"created" msgpack:"created"`
}

type LearnShowRequestBody struct {
	ID string `json:"id" msgpack:"id"`
}

type LearnShowResponseBody struct {
	Learning Learning `json:"learning" msgpack:"learning"`
}

type LearnReviewRequestBody struct {
	ID               string                `json:"id,omitempty" msgpack:"id,omitempty"`
	IDs              []string              `json:"ids,omitempty" msgpack:"ids,omitempty"`
	Status           LearningStatus        `json:"status,omitempty" msgpack:"status,omitempty"`
	Note             string                `json:"note,omitempty" msgpack:"note,omitempty"`
	Limit            int                   `json:"limit,omitempty" msgpack:"limit,omitempty"`
	QueueStatuses    []LearningStatus      `json:"queue_statuses,omitempty" msgpack:"queue_statuses,omitempty"`
	IssueID          naming.IssueID        `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	ReqID            naming.RequirementID  `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
	Tags             []string              `json:"tags,omitempty" msgpack:"tags,omitempty"`
	Files            []string              `json:"files,omitempty" msgpack:"files,omitempty"`
	TargetStates     []LearningTargetState `json:"target_states,omitempty" msgpack:"target_states,omitempty"`
	OlderThanSeconds int64                 `json:"older_than_seconds,omitempty" msgpack:"older_than_seconds,omitempty"`
	BulkStale        bool                  `json:"bulk_stale,omitempty" msgpack:"bulk_stale,omitempty"`
}

type LearnReviewResponseBody struct {
	Learnings        []Learning `json:"learnings" msgpack:"learnings"`
	Updated          *Learning  `json:"updated,omitempty" msgpack:"updated,omitempty"`
	UpdatedLearnings []Learning `json:"updated_learnings,omitempty" msgpack:"updated_learnings,omitempty"`
}

type LearnStaleRequestBody struct {
	ID   string `json:"id" msgpack:"id"`
	Note string `json:"note" msgpack:"note"`
}

type LearnStaleResponseBody struct {
	Learning Learning `json:"learning" msgpack:"learning"`
}

type LearnDemoteRequestBody struct {
	ID   string `json:"id" msgpack:"id"`
	Note string `json:"note" msgpack:"note"`
}

type LearnDemoteResponseBody struct {
	Learning Learning `json:"learning" msgpack:"learning"`
}

type LearnPromoteRequestBody struct {
	ID                   string                  `json:"id" msgpack:"id"`
	Target               LearningPromotionTarget `json:"target" msgpack:"target"`
	TargetID             string                  `json:"target_id,omitempty" msgpack:"target_id,omitempty"`
	Note                 string                  `json:"note,omitempty" msgpack:"note,omitempty"`
	TargetHash           string                  `json:"target_hash,omitempty" msgpack:"target_hash,omitempty"`
	TargetMetadata       map[string]string       `json:"target_metadata,omitempty" msgpack:"target_metadata,omitempty"`
	CreateTarget         bool                    `json:"create_target,omitempty" msgpack:"create_target,omitempty"`
	TargetTitle          string                  `json:"target_title,omitempty" msgpack:"target_title,omitempty"`
	TargetDescription    string                  `json:"target_description,omitempty" msgpack:"target_description,omitempty"`
	TargetIssueID        naming.IssueID          `json:"target_issue_id,omitempty" msgpack:"target_issue_id,omitempty"`
	DecisionRationale    string                  `json:"decision_rationale,omitempty" msgpack:"decision_rationale,omitempty"`
	DecisionContext      string                  `json:"decision_context,omitempty" msgpack:"decision_context,omitempty"`
	DecisionConsequences string                  `json:"decision_consequences,omitempty" msgpack:"decision_consequences,omitempty"`
}

type LearnPromoteResponseBody struct {
	Learning Learning `json:"learning" msgpack:"learning"`
	Guidance string   `json:"guidance" msgpack:"guidance"`
}

type LearnRetireRequestBody struct {
	ID   string `json:"id" msgpack:"id"`
	Note string `json:"note" msgpack:"note"`
}

type LearnRetireResponseBody struct {
	Learning Learning `json:"learning" msgpack:"learning"`
	Guidance string   `json:"guidance" msgpack:"guidance"`
}

type LearnRelateRequestBody struct {
	Type             LearningRelationType `json:"type" msgpack:"type"`
	SourceLearningID string               `json:"source_learning_id" msgpack:"source_learning_id"`
	TargetLearningID string               `json:"target_learning_id" msgpack:"target_learning_id"`
	Note             string               `json:"note" msgpack:"note"`
	ScopeIssueID     naming.IssueID       `json:"scope_issue_id,omitempty" msgpack:"scope_issue_id,omitempty"`
	ScopeReqID       naming.RequirementID `json:"scope_req_id,omitempty" msgpack:"scope_req_id,omitempty"`
	ScopeSessionID   naming.SessionID     `json:"scope_session_id,omitempty" msgpack:"scope_session_id,omitempty"`
	ScopeTags        []string             `json:"scope_tags,omitempty" msgpack:"scope_tags,omitempty"`
	ScopeFiles       []string             `json:"scope_files,omitempty" msgpack:"scope_files,omitempty"`
}

type LearnRelateResponseBody struct {
	Relation LearningRelation `json:"relation" msgpack:"relation"`
}

type LearnSupersedeRequestBody struct {
	NewLearningID  string               `json:"new_learning_id" msgpack:"new_learning_id"`
	OldLearningID  string               `json:"old_learning_id" msgpack:"old_learning_id"`
	Note           string               `json:"note" msgpack:"note"`
	ScopeIssueID   naming.IssueID       `json:"scope_issue_id,omitempty" msgpack:"scope_issue_id,omitempty"`
	ScopeReqID     naming.RequirementID `json:"scope_req_id,omitempty" msgpack:"scope_req_id,omitempty"`
	ScopeSessionID naming.SessionID     `json:"scope_session_id,omitempty" msgpack:"scope_session_id,omitempty"`
	ScopeTags      []string             `json:"scope_tags,omitempty" msgpack:"scope_tags,omitempty"`
	ScopeFiles     []string             `json:"scope_files,omitempty" msgpack:"scope_files,omitempty"`
}

type LearnSupersedeResponseBody struct {
	Relation LearningRelation `json:"relation" msgpack:"relation"`
}

type LearnMaintenanceFinding struct {
	Type       string   `json:"type" msgpack:"type"`
	Severity   string   `json:"severity" msgpack:"severity"`
	LearningID string   `json:"learning_id" msgpack:"learning_id"`
	Message    string   `json:"message" msgpack:"message"`
	Action     string   `json:"action" msgpack:"action"`
	Learning   Learning `json:"learning" msgpack:"learning"`
}

type LearnDoctorRequestBody struct {
	ProjectID              string `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	CandidateOlderThanDays int    `json:"candidate_older_than_days,omitempty" msgpack:"candidate_older_than_days,omitempty"`
	InactiveOlderThanDays  int    `json:"inactive_older_than_days,omitempty" msgpack:"inactive_older_than_days,omitempty"`
	Limit                  int    `json:"limit,omitempty" msgpack:"limit,omitempty"`
}

type LearnDoctorResponseBody struct {
	Findings []LearnMaintenanceFinding `json:"findings" msgpack:"findings"`
}

type LearnGCRequestBody struct {
	ProjectID              string `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	CandidateOlderThanDays int    `json:"candidate_older_than_days,omitempty" msgpack:"candidate_older_than_days,omitempty"`
	InactiveOlderThanDays  int    `json:"inactive_older_than_days,omitempty" msgpack:"inactive_older_than_days,omitempty"`
	Limit                  int    `json:"limit,omitempty" msgpack:"limit,omitempty"`
	Confirm                bool   `json:"confirm,omitempty" msgpack:"confirm,omitempty"`
}

type LearnGCResponseBody struct {
	DryRun  bool                      `json:"dry_run" msgpack:"dry_run"`
	Deleted []LearnMaintenanceFinding `json:"deleted,omitempty" msgpack:"deleted,omitempty"`
	Skipped []LearnMaintenanceFinding `json:"skipped,omitempty" msgpack:"skipped,omitempty"`
}

func (s LearningStatus) Valid() bool {
	switch s {
	case LearningStatusCandidate, LearningStatusAccepted, LearningStatusRejected, LearningStatusPromoted, LearningStatusStale:
		return true
	default:
		return false
	}
}

func (t LearningPromotionTarget) Valid() bool {
	switch t {
	case LearningPromotionTargetRulesync, LearningPromotionTargetAgents, LearningPromotionTargetSkill, LearningPromotionTargetSpec, LearningPromotionTargetDecision:
		return true
	default:
		return false
	}
}

func (t LearningRelationType) Valid() bool {
	switch t {
	case LearningRelationSupersedes, LearningRelationConflicts:
		return true
	default:
		return false
	}
}

func (s LearningTargetState) Valid() bool {
	switch s {
	case LearningTargetStateActive, LearningTargetStateRetired, LearningTargetStateDrifted, LearningTargetStateMissing:
		return true
	default:
		return false
	}
}
