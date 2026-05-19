package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandDecisionList       = "decision.list"
	CommandDecisionGet        = "decision.get"
	CommandDecisionCreate     = "decision.create"
	CommandDecisionUpdate     = "decision.update"
	CommandDecisionDelete     = "decision.delete"
	CommandDecisionLinkList   = "decision.link.list"
	CommandDecisionLinkAdd    = "decision.link.add"
	CommandDecisionLinkRemove = "decision.link.remove"
)

type DecisionStatus string

const (
	DecisionStatusProposed   DecisionStatus = "proposed"
	DecisionStatusAccepted   DecisionStatus = "accepted"
	DecisionStatusRejected   DecisionStatus = "rejected"
	DecisionStatusDeprecated DecisionStatus = "deprecated"
	DecisionStatusSuperseded DecisionStatus = "superseded"
)

func (s DecisionStatus) Valid() bool {
	switch s {
	case DecisionStatusProposed,
		DecisionStatusAccepted,
		DecisionStatusRejected,
		DecisionStatusDeprecated,
		DecisionStatusSuperseded:
		return true
	}
	return false
}

type DecisionTargetKind string

const (
	DecisionTargetIssue       DecisionTargetKind = "issue"
	DecisionTargetRequirement DecisionTargetKind = "requirement"
)

func (k DecisionTargetKind) Valid() bool {
	switch k {
	case DecisionTargetIssue, DecisionTargetRequirement:
		return true
	}
	return false
}

type DecisionRelation string

const (
	DecisionRelationRelates      DecisionRelation = "relates"
	DecisionRelationImplements   DecisionRelation = "implements"
	DecisionRelationSupersedes   DecisionRelation = "supersedes"
	DecisionRelationSupersededBy DecisionRelation = "superseded-by"
)

func (r DecisionRelation) Valid() bool {
	switch r {
	case DecisionRelationRelates,
		DecisionRelationImplements,
		DecisionRelationSupersedes,
		DecisionRelationSupersededBy:
		return true
	}
	return false
}

type Decision struct {
	ID           string         `json:"id" msgpack:"id"`
	Title        string         `json:"title" msgpack:"title"`
	Context      string         `json:"context,omitempty" msgpack:"context,omitempty"`
	Decision     string         `json:"decision,omitempty" msgpack:"decision,omitempty"`
	Consequences string         `json:"consequences,omitempty" msgpack:"consequences,omitempty"`
	Status       DecisionStatus `json:"status" msgpack:"status"`
	CreatedAt    time.Time      `json:"created_at" msgpack:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at" msgpack:"updated_at"`
}

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
	Statuses      []DecisionStatus     `json:"statuses,omitempty" msgpack:"statuses,omitempty"`
	IssueID       naming.IssueID       `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	RequirementID naming.RequirementID `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
	Query         string               `json:"query,omitempty" msgpack:"query,omitempty"`
}

type DecisionListResponseBody struct {
	Decisions []Decision     `json:"decisions" msgpack:"decisions"`
	Links     []DecisionLink `json:"links,omitempty" msgpack:"links,omitempty"`
}

type DecisionGetRequestBody struct {
	ID          string `json:"id" msgpack:"id"`
	IncludeLinks bool  `json:"include_links,omitempty" msgpack:"include_links,omitempty"`
}

type DecisionGetResponseBody struct {
	Decision Decision       `json:"decision" msgpack:"decision"`
	Links    []DecisionLink `json:"links,omitempty" msgpack:"links,omitempty"`
}

type DecisionCreateRequestBody struct {
	ID           string         `json:"id" msgpack:"id"`
	Title        string         `json:"title" msgpack:"title"`
	Context      string         `json:"context,omitempty" msgpack:"context,omitempty"`
	Decision     string         `json:"decision,omitempty" msgpack:"decision,omitempty"`
	Consequences string         `json:"consequences,omitempty" msgpack:"consequences,omitempty"`
	Status       DecisionStatus `json:"status,omitempty" msgpack:"status,omitempty"`
}

type DecisionCreateResponseBody struct {
	Decision Decision `json:"decision" msgpack:"decision"`
}

type DecisionUpdateRequestBody struct {
	ID           string          `json:"id" msgpack:"id"`
	Title        *string         `json:"title,omitempty" msgpack:"title,omitempty"`
	Context      *string         `json:"context,omitempty" msgpack:"context,omitempty"`
	Decision     *string         `json:"decision,omitempty" msgpack:"decision,omitempty"`
	Consequences *string         `json:"consequences,omitempty" msgpack:"consequences,omitempty"`
	Status       *DecisionStatus `json:"status,omitempty" msgpack:"status,omitempty"`
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
