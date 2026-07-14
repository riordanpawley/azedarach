package daemonclient

import (
	"context"
	"time"
)

const (
	CommandDecisionList       = "decision.list"
	CommandDecisionGet        = "decision.get"
	CommandDecisionRecord     = "decision.record"
	CommandDecisionLinkList   = "decision.link.list"
	CommandDecisionLinkAdd    = "decision.link.add"
	CommandDecisionLinkRemove = "decision.link.remove"
)

// DecisionTargetKind identifies what a decision link points at.
type DecisionTargetKind string

const (
	DecisionTargetIssue       DecisionTargetKind = "issue"
	DecisionTargetRequirement DecisionTargetKind = "requirement"
	DecisionTargetDecision    DecisionTargetKind = "decision"
)

// DecisionRelation describes the link's role.
type DecisionRelation string

const (
	DecisionRelationAppliesTo DecisionRelation = "applies-to"
	DecisionRelationRevises   DecisionRelation = "revises"
	DecisionRelationInforms   DecisionRelation = "informs"
	DecisionRelationGoverns   DecisionRelation = "governs"
)

// Decision is the daemonclient-side projection of a decision record.
type Decision struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Rationale    string    `json:"rationale,omitempty"`
	Context      string    `json:"context,omitempty"`
	Consequences string    `json:"consequences,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DecisionLink is the daemonclient-side projection of a decision_links row.
type DecisionLink struct {
	ID         string             `json:"id"`
	DecisionID string             `json:"decision_id"`
	TargetKind DecisionTargetKind `json:"target_kind"`
	TargetID   string             `json:"target_id"`
	Relation   DecisionRelation   `json:"relation"`
	Note       string             `json:"note,omitempty"`
}

type DecisionLinkListRequest struct {
	DecisionID       string             `json:"decision_id,omitempty"`
	TargetKind       DecisionTargetKind `json:"target_kind,omitempty"`
	TargetID         string             `json:"target_id,omitempty"`
	IncludeDecisions bool               `json:"include_decisions,omitempty"`
}

type DecisionLinkListResult struct {
	Links     []DecisionLink `json:"links"`
	Decisions []Decision     `json:"decisions,omitempty"`
}

func (c *Client) ListDecisionLinks(ctx context.Context, req DecisionLinkListRequest) (DecisionLinkListResult, error) {
	var out DecisionLinkListResult
	if err := c.commandJSON(ctx, CommandDecisionLinkList, req, &out); err != nil {
		return DecisionLinkListResult{}, err
	}
	return out, nil
}
