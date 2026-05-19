package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandDecisionList       = "decision.list"
	CommandDecisionGet        = "decision.get"
	CommandDecisionRecord     = "decision.record"
	CommandDecisionUpdate     = "decision.update"
	CommandDecisionDelete     = "decision.delete"
	CommandDecisionLinkList   = "decision.link.list"
	CommandDecisionLinkAdd    = "decision.link.add"
	CommandDecisionLinkRemove = "decision.link.remove"
	CommandDecisionSyncMD     = "decision.sync_md"
)

// DecisionTargetKind enumerates the things a decision link can point at.
type DecisionTargetKind string

const (
	DecisionTargetIssue       DecisionTargetKind = "issue"
	DecisionTargetRequirement DecisionTargetKind = "requirement"
	DecisionTargetDecision    DecisionTargetKind = "decision"
)

func (k DecisionTargetKind) Valid() bool {
	switch k {
	case DecisionTargetIssue, DecisionTargetRequirement, DecisionTargetDecision:
		return true
	}
	return false
}

// DecisionRelation is the role a link plays. applies-to is the default. The
// presence of a revises link is the single source of truth for "this decision
// replaced an older one" — there is no status column on the decision itself.
type DecisionRelation string

const (
	DecisionRelationAppliesTo DecisionRelation = "applies-to"
	DecisionRelationRevises   DecisionRelation = "revises"
	DecisionRelationInforms   DecisionRelation = "informs"
)

func (r DecisionRelation) Valid() bool {
	switch r {
	case DecisionRelationAppliesTo, DecisionRelationRevises, DecisionRelationInforms:
		return true
	}
	return false
}

// Decision is the recorded fact of a choice plus its rationale.
type Decision struct {
	ID           string    `json:"id" msgpack:"id"`
	Title        string    `json:"title" msgpack:"title"`
	Rationale    string    `json:"rationale" msgpack:"rationale"`
	Context      string    `json:"context,omitempty" msgpack:"context,omitempty"`
	Consequences string    `json:"consequences,omitempty" msgpack:"consequences,omitempty"`
	CreatedAt    time.Time `json:"created_at" msgpack:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" msgpack:"updated_at"`
}

// DecisionLink connects a decision to an issue, requirement, or another decision.
type DecisionLink struct {
	ID         string             `json:"id" msgpack:"id"`
	DecisionID string             `json:"decision_id" msgpack:"decision_id"`
	TargetKind DecisionTargetKind `json:"target_kind" msgpack:"target_kind"`
	TargetID   string             `json:"target_id" msgpack:"target_id"`
	Relation   DecisionRelation   `json:"relation" msgpack:"relation"`
	Note       string             `json:"note,omitempty" msgpack:"note,omitempty"`
}

type DecisionListRequestBody struct {
	IDs           []string             `json:"ids,omitempty" msgpack:"ids,omitempty"`
	IssueID       naming.IssueID       `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	RequirementID naming.RequirementID `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
	Query         string               `json:"query,omitempty" msgpack:"query,omitempty"`
}

type DecisionListResponseBody struct {
	Decisions []Decision     `json:"decisions" msgpack:"decisions"`
	Links     []DecisionLink `json:"links,omitempty" msgpack:"links,omitempty"`
}

type DecisionGetRequestBody struct {
	ID           string `json:"id" msgpack:"id"`
	IncludeLinks bool   `json:"include_links,omitempty" msgpack:"include_links,omitempty"`
}

type DecisionGetResponseBody struct {
	Decision Decision       `json:"decision" msgpack:"decision"`
	Links    []DecisionLink `json:"links,omitempty" msgpack:"links,omitempty"`
}

// DecisionRecordRequestBody creates a new decision. ID is allocated by the
// store (dec-N) so the caller doesn't have to pick a slug; title and rationale
// are required.
type DecisionRecordRequestBody struct {
	Title        string `json:"title" msgpack:"title"`
	Rationale    string `json:"rationale" msgpack:"rationale"`
	Context      string `json:"context,omitempty" msgpack:"context,omitempty"`
	Consequences string `json:"consequences,omitempty" msgpack:"consequences,omitempty"`
}

type DecisionRecordResponseBody struct {
	Decision Decision `json:"decision" msgpack:"decision"`
}

type DecisionUpdateRequestBody struct {
	ID           string  `json:"id" msgpack:"id"`
	Title        *string `json:"title,omitempty" msgpack:"title,omitempty"`
	Rationale    *string `json:"rationale,omitempty" msgpack:"rationale,omitempty"`
	Context      *string `json:"context,omitempty" msgpack:"context,omitempty"`
	Consequences *string `json:"consequences,omitempty" msgpack:"consequences,omitempty"`
}

type DecisionUpdateResponseBody struct {
	Decision Decision `json:"decision" msgpack:"decision"`
}

type DecisionDeleteRequestBody struct {
	ID      string `json:"id" msgpack:"id"`
	Confirm bool   `json:"confirm" msgpack:"confirm"`
}

type DecisionDeleteResponseBody struct {
	ID      string `json:"id" msgpack:"id"`
	Deleted bool   `json:"deleted" msgpack:"deleted"`
}

type DecisionLinkListRequestBody struct {
	DecisionID       string             `json:"decision_id,omitempty" msgpack:"decision_id,omitempty"`
	TargetKind       DecisionTargetKind `json:"target_kind,omitempty" msgpack:"target_kind,omitempty"`
	TargetID         string             `json:"target_id,omitempty" msgpack:"target_id,omitempty"`
	IncludeDecisions bool               `json:"include_decisions,omitempty" msgpack:"include_decisions,omitempty"`
}

type DecisionLinkListResponseBody struct {
	Links     []DecisionLink `json:"links" msgpack:"links"`
	Decisions []Decision     `json:"decisions,omitempty" msgpack:"decisions,omitempty"`
}

type DecisionLinkAddRequestBody struct {
	DecisionID string             `json:"decision_id" msgpack:"decision_id"`
	TargetKind DecisionTargetKind `json:"target_kind" msgpack:"target_kind"`
	TargetID   string             `json:"target_id" msgpack:"target_id"`
	Relation   DecisionRelation   `json:"relation,omitempty" msgpack:"relation,omitempty"`
	Note       string             `json:"note,omitempty" msgpack:"note,omitempty"`
}

type DecisionLinkAddResponseBody struct {
	Link DecisionLink `json:"link" msgpack:"link"`
}

type DecisionLinkRemoveRequestBody struct {
	DecisionID string             `json:"decision_id" msgpack:"decision_id"`
	TargetKind DecisionTargetKind `json:"target_kind" msgpack:"target_kind"`
	TargetID   string             `json:"target_id" msgpack:"target_id"`
}

type DecisionLinkRemoveResponseBody struct {
	DecisionID string             `json:"decision_id" msgpack:"decision_id"`
	TargetKind DecisionTargetKind `json:"target_kind" msgpack:"target_kind"`
	TargetID   string             `json:"target_id" msgpack:"target_id"`
	Removed    bool               `json:"removed" msgpack:"removed"`
}

// DecisionSyncMDRequestBody asks the daemon to write decision records to
// markdown files under docs/decisions/. When Check is true the daemon
// computes what would change without writing anything.
type DecisionSyncMDRequestBody struct {
	Check bool `json:"check,omitempty" msgpack:"check,omitempty"`
}

// DecisionSyncMDResponseBody reports the outcome of the sync. Files is the
// list of paths that were written (or would be written, in --check mode).
type DecisionSyncMDResponseBody struct {
	Check   bool     `json:"check" msgpack:"check"`
	Changed bool     `json:"changed" msgpack:"changed"`
	Files   []string `json:"files,omitempty" msgpack:"files,omitempty"`
}
