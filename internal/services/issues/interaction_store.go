package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type InteractionResolution struct {
	Request          domain.InteractionRequest
	ExpectedRevision int64
}

// CreateInteraction persists a new durable decision request.
func (c *Client) CreateInteraction(ctx context.Context, request domain.InteractionRequest) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate interaction: %w", err)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal interaction: %w", err)
	}
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO interaction_requests
		(id, issue_id, decision_key, state, revision, request_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, request.ID, request.IssueID, request.DecisionKey,
		request.State, request.Revision, raw, request.CreatedAt.UTC().Format(time.RFC3339Nano), request.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			if _, found, lookupErr := c.GetInteraction(ctx, request.ID); lookupErr == nil && found {
				return fmt.Errorf("interaction %s already exists", request.ID)
			}
			return fmt.Errorf("%w: issue %s decision key %s", domain.ErrDuplicateUnresolvedDecision, request.IssueID, request.DecisionKey)
		}
		return fmt.Errorf("create interaction %s: %w", request.ID, err)
	}
	return c.RefreshInteractionProjection(ctx)
}

// UpdateInteraction applies an optimistic-revision replacement.
func (c *Client) UpdateInteraction(ctx context.Context, request domain.InteractionRequest, expectedRevision int64) error {
	if expectedRevision < 1 || request.Revision != expectedRevision+1 {
		return fmt.Errorf("%w: expected replacement revision %d", domain.ErrStaleInteractionRevision, expectedRevision+1)
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate interaction: %w", err)
	}
	current, found, err := c.GetInteraction(ctx, request.ID)
	if err != nil {
		return err
	}
	if !found || current.Revision != expectedRevision {
		return fmt.Errorf("%w: interaction %s expected revision %d", domain.ErrStaleInteractionRevision, request.ID, expectedRevision)
	}
	if !interactionDefinitionEqual(current, request) {
		return fmt.Errorf("interaction request definition and creation audit are immutable")
	}
	transitionCandidate := current
	transitionCandidate.Proposal = request.Proposal
	transitionCandidate.FinalAnswer = request.FinalAnswer
	transitionCandidate.Disposition = request.Disposition
	transitionCandidate.StaleAt = request.StaleAt
	transitionCandidate.Reminders = request.Reminders
	transitionCandidate.SessionID = request.SessionID
	transitionCandidate.Recovery = request.Recovery
	if _, err := transitionCandidate.Transition(request.State, expectedRevision, request.UpdatedAt); err != nil {
		return fmt.Errorf("validate interaction transition: %w", err)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal interaction: %w", err)
	}
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE interaction_requests SET state=?, revision=?, request_json=?, updated_at=? WHERE id=? AND revision=?`, request.State, request.Revision, raw, request.UpdatedAt.UTC().Format(time.RFC3339Nano), request.ID, expectedRevision)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			return fmt.Errorf("%w: issue %s decision key %s", domain.ErrDuplicateUnresolvedDecision, request.IssueID, request.DecisionKey)
		}
		return fmt.Errorf("update interaction %s: %w", request.ID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: interaction %s expected revision %d", domain.ErrStaleInteractionRevision, request.ID, expectedRevision)
	}
	return c.RefreshInteractionProjection(ctx)
}

// UpdateInteractionMetadata persists an orthogonal lifecycle audit mutation
// without changing the request's decision state.
func (c *Client) UpdateInteractionMetadata(ctx context.Context, request domain.InteractionRequest, expectedRevision int64) error {
	if expectedRevision < 1 || request.Revision != expectedRevision+1 {
		return fmt.Errorf("%w: expected replacement revision %d", domain.ErrStaleInteractionRevision, expectedRevision+1)
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate interaction metadata: %w", err)
	}
	current, found, err := c.GetInteraction(ctx, request.ID)
	if err != nil {
		return err
	}
	if !found || current.Revision != expectedRevision {
		return fmt.Errorf("%w: interaction %s expected revision %d", domain.ErrStaleInteractionRevision, request.ID, expectedRevision)
	}
	expected := current
	expected.SessionID = request.SessionID
	expected.StaleAt = request.StaleAt
	expected.Reminders = request.Reminders
	expected.Recovery = request.Recovery
	expected.Revision = request.Revision
	expected.UpdatedAt = request.UpdatedAt
	if !current.Unresolved() || !reflect.DeepEqual(expected, request) {
		return fmt.Errorf("interaction decision content is immutable during metadata update")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal interaction metadata: %w", err)
	}
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE interaction_requests SET revision=?,request_json=?,updated_at=? WHERE id=? AND revision=?`, request.Revision, raw, request.UpdatedAt.UTC().Format(time.RFC3339Nano), request.ID, expectedRevision)
	if err != nil {
		return fmt.Errorf("update interaction metadata %s: %w", request.ID, err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: interaction %s expected revision %d", domain.ErrStaleInteractionRevision, request.ID, expectedRevision)
	}
	return c.RefreshInteractionProjection(ctx)
}

func interactionDefinitionEqual(current, replacement domain.InteractionRequest) bool {
	current.State, replacement.State = "", ""
	current.Revision, replacement.Revision = 0, 0
	current.UpdatedAt, replacement.UpdatedAt = time.Time{}, time.Time{}
	current.Proposal, replacement.Proposal = nil, nil
	current.FinalAnswer, replacement.FinalAnswer = nil, nil
	current.SessionID, replacement.SessionID = "", ""
	current.StaleAt, replacement.StaleAt = nil, nil
	current.Reminders, replacement.Reminders = nil, nil
	current.Disposition, replacement.Disposition = nil, nil
	current.Recovery, replacement.Recovery = nil, nil
	return reflect.DeepEqual(current, replacement)
}

// RefreshInteractionProjection reloads the complete durable projection before evaluation.
func (c *Client) RefreshInteractionProjection(ctx context.Context) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT request_json FROM interaction_requests ORDER BY created_at, id`)
	if err != nil {
		return fmt.Errorf("refresh interaction projection: %w", err)
	}
	defer rows.Close()
	next := make(map[string]domain.InteractionRequest)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		r, err := decodeInteractionRequest(raw)
		if err != nil {
			return fmt.Errorf("decode interaction projection: %w", err)
		}
		next[r.ID] = r
	}
	if err := rows.Err(); err != nil {
		return err
	}
	c.interactionMu.Lock()
	c.interactionCache = next
	c.interactionMu.Unlock()
	return nil
}

func (c *Client) GetInteraction(ctx context.Context, id string) (domain.InteractionRequest, bool, error) {
	if err := c.RefreshInteractionProjection(ctx); err != nil {
		return domain.InteractionRequest{}, false, err
	}
	c.interactionMu.RLock()
	defer c.interactionMu.RUnlock()
	r, ok := c.interactionCache[strings.TrimSpace(id)]
	return r, ok, nil
}

func (c *Client) InteractionByDecisionKey(ctx context.Context, issueID, decisionKey string) (domain.InteractionRequest, bool, error) {
	requests, err := c.InteractionsForIssue(ctx, issueID)
	if err != nil {
		return domain.InteractionRequest{}, false, err
	}
	for _, r := range requests {
		if r.DecisionKey == decisionKey && r.Unresolved() {
			return r, true, nil
		}
	}
	return domain.InteractionRequest{}, false, nil
}

func (c *Client) InteractionsForIssue(ctx context.Context, issueID string) ([]domain.InteractionRequest, error) {
	if err := c.RefreshInteractionProjection(ctx); err != nil {
		return nil, err
	}
	c.interactionMu.RLock()
	defer c.interactionMu.RUnlock()
	out := make([]domain.InteractionRequest, 0)
	for _, r := range c.interactionCache {
		if r.IssueID == issueID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (c *Client) ListInteractions(ctx context.Context) ([]domain.InteractionRequest, error) {
	if err := c.RefreshInteractionProjection(ctx); err != nil {
		return nil, err
	}
	c.interactionMu.RLock()
	defer c.interactionMu.RUnlock()
	out := make([]domain.InteractionRequest, 0, len(c.interactionCache))
	for _, r := range c.interactionCache {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ResolveInteraction atomically applies explicitly supplied effects and resolves the request.
func (c *Client) ResolveInteraction(ctx context.Context, in InteractionResolution) (domain.InteractionRequest, error) {
	if in.ExpectedRevision < 1 || in.Request.Revision != in.ExpectedRevision+1 {
		return domain.InteractionRequest{}, fmt.Errorf("%w: expected replacement revision %d", domain.ErrStaleInteractionRevision, in.ExpectedRevision+1)
	}
	if err := in.Request.Validate(); err != nil {
		return domain.InteractionRequest{}, fmt.Errorf("validate interaction: %w", err)
	}
	if in.Request.ResolutionTrace != nil {
		return domain.InteractionRequest{}, fmt.Errorf("interaction resolution trace is store-owned")
	}
	plan, err := domain.PlanInteractionResolution(in.Request)
	if err != nil {
		return domain.InteractionRequest{}, fmt.Errorf("plan interaction resolution: %w", err)
	}
	err = c.withMutationLock(ctx, func(ctx context.Context) error {
		db, err := c.dbHandle()
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var raw []byte
		var revision int64
		if err := tx.QueryRowContext(ctx, `SELECT request_json, revision FROM interaction_requests WHERE id=?`, in.Request.ID).Scan(&raw, &revision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}
		if revision != in.ExpectedRevision {
			return fmt.Errorf("%w: interaction %s expected revision %d", domain.ErrStaleInteractionRevision, in.Request.ID, in.ExpectedRevision)
		}
		current, err := decodeInteractionRequest(raw)
		if err != nil {
			return err
		}
		current.FinalAnswer = in.Request.FinalAnswer
		current.Proposal = in.Request.Proposal
		if _, err := current.Transition(domain.InteractionResolved, in.ExpectedRevision, in.Request.UpdatedAt); err != nil {
			return err
		}
		if !interactionDefinitionEqual(current, in.Request) {
			return fmt.Errorf("interaction request definition and creation audit are immutable")
		}
		ch := in.Request.FinalAnswer.Answer.ApprovedIssueFieldEffects
		if interactionIssueChangesApproved(ch) {
			res, err := tx.ExecContext(ctx, `UPDATE issues SET title=CASE WHEN ? THEN ? ELSE title END,description=CASE WHEN ? THEN ? ELSE description END,design=CASE WHEN ? THEN ? ELSE design END,acceptance=CASE WHEN ? THEN ? ELSE acceptance END,priority=CASE WHEN ? THEN ? ELSE priority END,updated_at=? WHERE id=?`, ch.Title != nil, valueString(ch.Title), ch.Description != nil, valueString(ch.Description), ch.Design != nil, valueString(ch.Design), ch.Acceptance != nil, valueString(ch.Acceptance), ch.Priority != nil, valueInt(ch.Priority), in.Request.UpdatedAt.UTC().Format(time.RFC3339Nano), in.Request.IssueID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return domain.ErrNotFound
			}
			if err := c.appendIssueObservationEvent(ctx, tx, in.Request.IssueID, domain.IssueEventIssueDetailsChanged, map[string]any{"source": "interaction.resolve", "interaction_id": in.Request.ID}); err != nil {
				return err
			}
		}
		requirementIDs := make([]string, 0, len(plan.RequirementEffects))
		for _, effect := range plan.RequirementEffects {
			requirementID, err := c.applyInteractionRequirementEffect(WithSpecAuditActorSource(ctx, "interaction.resolve"), tx, in.Request, effect)
			if err != nil {
				return err
			}
			requirementIDs = append(requirementIDs, requirementID)
		}
		decisionID := ""
		if plan.Decision != nil {
			decision, err := c.resolveInteractionDecision(WithSpecAuditActorSource(ctx, "interaction.resolve"), tx, in.Request.UpdatedAt, *plan.Decision)
			if err != nil {
				return err
			}
			if err := c.ensureInteractionDecisionLink(WithSpecAuditActorSource(ctx, "interaction.resolve"), tx, decision, DecisionTargetIssue, in.Request.IssueID, in.Request.UpdatedAt); err != nil {
				return err
			}
			for _, requirementID := range requirementIDs {
				if err := c.ensureInteractionDecisionLink(WithSpecAuditActorSource(ctx, "interaction.resolve"), tx, decision, DecisionTargetRequirement, requirementID, in.Request.UpdatedAt); err != nil {
					return err
				}
			}
			decisionID = decision.LocalID
		}
		if decisionID != "" || len(requirementIDs) > 0 {
			in.Request.ResolutionTrace = &domain.InteractionResolutionTrace{DecisionID: decisionID, RequirementIDs: append([]string(nil), requirementIDs...)}
		}
		nextRaw, err := json.Marshal(in.Request)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE interaction_requests SET state=?,revision=?,request_json=?,updated_at=? WHERE id=? AND revision=?`, in.Request.State, in.Request.Revision, nextRaw, in.Request.UpdatedAt.UTC().Format(time.RFC3339Nano), in.Request.ID, in.ExpectedRevision)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: interaction %s", domain.ErrStaleInteractionRevision, in.Request.ID)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		c.interactionMu.Lock()
		if c.interactionCache == nil {
			c.interactionCache = make(map[string]domain.InteractionRequest)
		}
		c.interactionCache[in.Request.ID] = in.Request
		c.interactionMu.Unlock()
		return nil
	})
	if err != nil {
		return domain.InteractionRequest{}, err
	}
	return in.Request, nil
}

func (c *Client) applyInteractionRequirementEffect(ctx context.Context, tx *sql.Tx, request domain.InteractionRequest, effect domain.InteractionRequirementEffect) (string, error) {
	requirement, err := c.lookupRequirementBySelector(ctx, tx, effect.RequirementID, false)
	if err != nil {
		return "", fmt.Errorf("lookup approved requirement %s: %w", effect.RequirementID, err)
	}
	if _, err := c.lookupLinkByIssueAndRequirement(ctx, tx, request.IssueID, requirement.rowID, false); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", fmt.Errorf("approved requirement %s is not linked to issue %s", requirement.LocalID, request.IssueID)
		}
		return "", err
	}
	after, err := applyRequirementUpdate(requirement.Requirement, UpdateRequirementParams{Title: effect.Title, Description: effect.Description})
	if err != nil {
		return "", fmt.Errorf("apply approved requirement %s update: %w", requirement.LocalID, err)
	}
	after.UpdatedAt = request.UpdatedAt
	if _, err := tx.ExecContext(ctx, `UPDATE spec_requirements SET title=?,description=?,updated_at=? WHERE id=?`, after.Title, nullableString(after.Description), formatTimestamp(after.UpdatedAt), requirement.rowID); err != nil {
		return "", fmt.Errorf("update approved requirement %s: %w", requirement.LocalID, err)
	}
	if err := c.insertSpecAuditRow(ctx, tx, specAuditEntityRequirement, requirement.LocalID, specAuditOpUpdate, requirement.Requirement, after); err != nil {
		return "", fmt.Errorf("audit approved requirement %s: %w", requirement.LocalID, err)
	}
	return requirement.LocalID, nil
}

func (c *Client) resolveInteractionDecision(ctx context.Context, tx *sql.Tx, at time.Time, effect domain.InteractionDecisionEffect) (decisionRecord, error) {
	if effect.ExistingDecisionID != "" {
		decision, err := c.lookupDecisionByLocalID(ctx, tx, effect.ExistingDecisionID, false)
		if err != nil {
			return decisionRecord{}, fmt.Errorf("lookup approved decision %s: %w", effect.ExistingDecisionID, err)
		}
		return decision, nil
	}
	normalized, err := normalizeRecordDecisionParams(RecordDecisionParams{Title: effect.Title, Rationale: effect.Rationale, Context: effect.Context, Consequences: effect.Consequences})
	if err != nil {
		return decisionRecord{}, err
	}
	stamp := formatTimestamp(at)
	result, err := tx.ExecContext(ctx, `INSERT INTO decisions(local_id,title,rationale,context,consequences,created_at,updated_at,deleted_at) VALUES('',?,?,?,?,?,?,NULL)`, normalized.Title, normalized.Rationale, normalized.Context, normalized.Consequences, stamp, stamp)
	if err != nil {
		return decisionRecord{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return decisionRecord{}, err
	}
	localID := fmt.Sprintf("dec-%d", id)
	if _, err := tx.ExecContext(ctx, `UPDATE decisions SET local_id=? WHERE id=?`, localID, id); err != nil {
		return decisionRecord{}, err
	}
	decision := Decision{LocalID: localID, Title: normalized.Title, Rationale: normalized.Rationale, Context: normalized.Context, Consequences: normalized.Consequences, CreatedAt: at, UpdatedAt: at}
	if err := c.insertDecisionAuditRow(ctx, tx, decisionEntityKind, localID, decisionOpCreate, nil, decision); err != nil {
		return decisionRecord{}, err
	}
	return decisionRecord{rowID: fmt.Sprint(id), Decision: decision}, nil
}

func (c *Client) ensureInteractionDecisionLink(ctx context.Context, tx *sql.Tx, decision decisionRecord, kind DecisionTargetKind, targetID string, at time.Time) error {
	if _, err := c.lookupDecisionLink(ctx, tx, decision.rowID, kind, targetID, false); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	link := DecisionLink{ID: decisionLinkID(decision.LocalID, kind, targetID), DecisionID: decision.LocalID, TargetKind: kind, TargetID: targetID, Relation: DecisionRelationAppliesTo, CreatedAt: at, UpdatedAt: at}
	if _, err := tx.ExecContext(ctx, `INSERT INTO decision_links(decision_id,target_kind,target_id,relation,note,created_at,updated_at,deleted_at) VALUES(?,?,?,?,NULL,?,?,NULL)`, decision.rowID, string(kind), targetID, string(link.Relation), formatTimestamp(at), formatTimestamp(at)); err != nil {
		return fmt.Errorf("link decision %s to %s %s: %w", decision.LocalID, kind, targetID, err)
	}
	if err := c.insertDecisionAuditRow(ctx, tx, decisionLinkEntityKind, link.ID, decisionOpCreate, nil, link); err != nil {
		return err
	}
	return nil
}

func decodeInteractionRequest(raw []byte) (domain.InteractionRequest, error) {
	var request domain.InteractionRequest
	if err := json.Unmarshal(raw, &request); err == nil {
		return request, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return domain.InteractionRequest{}, err
	}
	proposalRaw, finalRaw := fields["proposal"], fields["final_answer"]
	delete(fields, "proposal")
	delete(fields, "final_answer")
	base, err := json.Marshal(fields)
	if err != nil {
		return domain.InteractionRequest{}, err
	}
	if err := json.Unmarshal(base, &request); err != nil {
		return domain.InteractionRequest{}, err
	}
	if request.Proposal, err = decodeLegacyInteractionAnswerAudit(proposalRaw, request.Significance); err != nil {
		return domain.InteractionRequest{}, fmt.Errorf("decode proposal audit: %w", err)
	}
	if request.FinalAnswer, err = decodeLegacyInteractionAnswerAudit(finalRaw, request.Significance); err != nil {
		return domain.InteractionRequest{}, fmt.Errorf("decode final answer audit: %w", err)
	}
	return request, nil
}

func decodeLegacyInteractionAnswerAudit(raw json.RawMessage, significance domain.InteractionSignificance) (*domain.InteractionAnswerAudit, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var current domain.InteractionAnswerAudit
	if err := json.Unmarshal(raw, &current); err == nil {
		return &current, nil
	}
	var legacy struct {
		Answer    string    `json:"answer"`
		Actor     string    `json:"actor"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, err
	}
	return &domain.InteractionAnswerAudit{
		Answer: domain.InteractionAnswerPayload{
			SelectedOption:             legacy.Answer,
			Rationale:                  "Migrated from the legacy unstructured answer audit.",
			SignificanceRecommendation: significance,
			Revision:                   1,
		},
		Actor: legacy.Actor, CreatedAt: legacy.CreatedAt,
	}, nil
}
func interactionIssueChangesApproved(ch domain.InteractionIssueFieldEffects) bool {
	return ch.Any()
}
func valueString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func valueInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// Interactions returns the complete durable request projection in stable order.
func (c *Client) Interactions(ctx context.Context) ([]domain.InteractionRequest, error) {
	if err := c.RefreshInteractionProjection(ctx); err != nil {
		return nil, err
	}
	c.interactionMu.RLock()
	defer c.interactionMu.RUnlock()
	out := make([]domain.InteractionRequest, 0, len(c.interactionCache))
	for _, request := range c.interactionCache {
		out = append(out, request)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (c *Client) IssueHasUnresolvedInteraction(ctx context.Context, issueID string) (bool, error) {
	requests, err := c.InteractionsForIssue(ctx, issueID)
	if err != nil {
		return false, err
	}
	return domain.IssueWaitingHuman(issueID, requests), nil
}

func (c *Client) UnresolvedInteractionIssueIDs(ctx context.Context) (map[string]struct{}, error) {
	if err := c.RefreshInteractionProjection(ctx); err != nil {
		return nil, err
	}
	c.interactionMu.RLock()
	defer c.interactionMu.RUnlock()
	out := make(map[string]struct{})
	for _, r := range c.interactionCache {
		if r.Unresolved() {
			out[r.IssueID] = struct{}{}
		}
	}
	return out, nil
}
