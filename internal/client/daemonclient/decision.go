package daemonclient

import (
	"context"
	"time"
)

const (
	CommandDecisionList       = "decision.list"
	CommandDecisionGet        = "decision.get"
	CommandDecisionLinkList   = "decision.link.list"
)

// DecisionStatus mirrors the protocol-level decision status.
type DecisionStatus string

// DecisionTargetKind identifies what a decision link points at.
type DecisionTargetKind string

const (
	DecisionTargetIssue       DecisionTargetKind = "issue"
	DecisionTargetRequirement DecisionTargetKind = "requirement"
)

// DecisionRelation describes the link's role.
type DecisionRelation string

// Decision is the daemonclient-side projection of a decision record.
type Decision struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Context      string         `json:"context,omitempty"`
	Decision     string         `json:"decision,omitempty"`
	Consequences string         `json:"consequences,omitempty"`
	Status       DecisionStatus `json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
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

// DecisionLinkListRequest filters the decision link query.
type DecisionLinkListRequest struct {
	DecisionID       string             `json:"decision_id,omitempty"`
	TargetKind       DecisionTargetKind `json:"target_kind,omitempty"`
	TargetID         string             `json:"target_id,omitempty"`
	IncludeDecisions bool               `json:"include_decisions,omitempty"`
}

// DecisionLinkListResult returns links and (optionally) the decisions they reference.
type DecisionLinkListResult struct {
	Links     []DecisionLink `json:"links"`
	Decisions []Decision     `json:"decisions,omitempty"`
}

// ListDecisionLinks fetches links and, when IncludeDecisions is set, summary records
// for the decisions they reference.
func (c *Client) ListDecisionLinks(ctx context.Context, req DecisionLinkListRequest) (DecisionLinkListResult, error) {
	var out DecisionLinkListResult
	if err := c.commandJSON(ctx, CommandDecisionLinkList, req, &out); err != nil {
		return DecisionLinkListResult{}, err
	}
	return out, nil
}
