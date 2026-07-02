package protocol

import "github.com/riordanpawley/azedarach/internal/naming"

const (
	CommandLearnAdd     = "learn.add"
	CommandLearnRecall  = "learn.recall"
	CommandLearnShow    = "learn.show"
	CommandLearnReview  = "learn.review"
	CommandLearnPromote = "learn.promote"
	CommandLearnRelate  = "learn.relate"
)

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

type LearnShowRequestBody struct {
	ID string `json:"id" msgpack:"id"`
}

type LearnShowResponseBody struct {
	Learning Learning `json:"learning" msgpack:"learning"`
}

type LearnReviewRequestBody struct {
	ID     string         `json:"id,omitempty" msgpack:"id,omitempty"`
	Status LearningStatus `json:"status,omitempty" msgpack:"status,omitempty"`
	Note   string         `json:"note,omitempty" msgpack:"note,omitempty"`
	Limit  int            `json:"limit,omitempty" msgpack:"limit,omitempty"`
}

type LearnReviewResponseBody struct {
	Learnings []Learning `json:"learnings" msgpack:"learnings"`
	Updated   *Learning  `json:"updated,omitempty" msgpack:"updated,omitempty"`
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
