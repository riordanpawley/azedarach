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

type Learning struct {
	ID              string                  `json:"id" msgpack:"id"`
	ProjectID       string                  `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	IssueID         naming.IssueID          `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	ReqID           naming.RequirementID    `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
	SessionID       naming.SessionID        `json:"session_id,omitempty" msgpack:"session_id,omitempty"`
	Summary         string                  `json:"summary" msgpack:"summary"`
	Evidence        string                  `json:"evidence,omitempty" msgpack:"evidence,omitempty"`
	Status          LearningStatus          `json:"status" msgpack:"status"`
	ReviewNote      string                  `json:"review_note,omitempty" msgpack:"review_note,omitempty"`
	ReviewedAt      string                  `json:"reviewed_at,omitempty" msgpack:"reviewed_at,omitempty"`
	Tags            []string                `json:"tags,omitempty" msgpack:"tags,omitempty"`
	Files           []string                `json:"files,omitempty" msgpack:"files,omitempty"`
	Target          LearningPromotionTarget `json:"target,omitempty" msgpack:"target,omitempty"`
	TargetID        string                  `json:"target_id,omitempty" msgpack:"target_id,omitempty"`
	TargetNote      string                  `json:"target_note,omitempty" msgpack:"target_note,omitempty"`
	PromotedAt      string                  `json:"promoted_at,omitempty" msgpack:"promoted_at,omitempty"`
	ExpiresAt       string                  `json:"expires_at,omitempty" msgpack:"expires_at,omitempty"`
	StaleAt         string                  `json:"stale_at,omitempty" msgpack:"stale_at,omitempty"`
	LastRecalledAt  string                  `json:"last_recalled_at,omitempty" msgpack:"last_recalled_at,omitempty"`
	RecallCount     int                     `json:"recall_count,omitempty" msgpack:"recall_count,omitempty"`
	SupersededAt    string                  `json:"superseded_at,omitempty" msgpack:"superseded_at,omitempty"`
	TargetRetiredAt string                  `json:"target_retired_at,omitempty" msgpack:"target_retired_at,omitempty"`
	Relations       []LearningRelation      `json:"relations,omitempty" msgpack:"relations,omitempty"`
	CreatedAt       string                  `json:"created_at" msgpack:"created_at"`
	UpdatedAt       string                  `json:"updated_at" msgpack:"updated_at"`
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
	Query           string               `json:"query,omitempty" msgpack:"query,omitempty"`
	Statuses        []LearningStatus     `json:"statuses,omitempty" msgpack:"statuses,omitempty"`
	Tags            []string             `json:"tags,omitempty" msgpack:"tags,omitempty"`
	Files           []string             `json:"files,omitempty" msgpack:"files,omitempty"`
	Limit           int                  `json:"limit,omitempty" msgpack:"limit,omitempty"`
	IncludeEvidence bool                 `json:"include_evidence,omitempty" msgpack:"include_evidence,omitempty"`
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
	ID       string                  `json:"id" msgpack:"id"`
	Target   LearningPromotionTarget `json:"target" msgpack:"target"`
	TargetID string                  `json:"target_id" msgpack:"target_id"`
	Note     string                  `json:"note,omitempty" msgpack:"note,omitempty"`
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
