package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type OrchestrationRouteResult struct {
	Route              domain.OrchestrationCandidateRoute
	Task               domain.Task
	Interaction        *domain.InteractionRequest
	InteractionCreated bool
}

// RouteOrchestrationCandidate applies project-steward routing and execution
// handback in one durable transaction. Purpose-scoped orchestration and review
// leases are intentionally not modified.
func (c *Client) RouteOrchestrationCandidate(ctx context.Context, projectID, actorID string, route domain.OrchestrationCandidateRoute) (OrchestrationRouteResult, error) {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return OrchestrationRouteResult{}, errors.New("orchestration route actor id is required")
	}
	if err := route.Validate(); err != nil {
		return OrchestrationRouteResult{}, err
	}
	var interaction *domain.InteractionRequest
	var created bool
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			var err error
			interaction, created, err = c.routeOrchestrationCandidateLocked(ctx, actorID, &route)
			return err
		})
	})
	if err != nil {
		return OrchestrationRouteResult{}, err
	}
	if interaction != nil {
		if err := c.RefreshInteractionProjection(ctx); err != nil {
			return OrchestrationRouteResult{}, err
		}
	}
	task, err := c.GetWithRuntime(ctx, projectID, route.IssueID)
	if err != nil {
		return OrchestrationRouteResult{}, err
	}
	return OrchestrationRouteResult{Route: route, Task: task, Interaction: interaction, InteractionCreated: created}, nil
}

func (c *Client) routeOrchestrationCandidateLocked(ctx context.Context, actorID string, route *domain.OrchestrationCandidateRoute) (*domain.InteractionRequest, bool, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	task, state, err := orchestrationRouteTaskForUpdate(ctx, tx, route.IssueID)
	if err != nil {
		return nil, false, c.wrapError("orchestration-route", route.IssueID, err)
	}
	if task.Ownership != nil && task.Ownership.IsActive(time.Now().UTC()) && !task.Ownership.OwnedBy(actorID, time.Now().UTC()) {
		return nil, false, c.wrapError("orchestration-route", route.IssueID, fmt.Errorf("%w: execution lease owned by %s", domain.ErrConflict, task.Ownership.OwnerID))
	}

	now := time.Now().UTC()
	if err := releaseOrchestrationExecutionLease(ctx, c, tx, task, actorID, now, route.Kind); err != nil {
		return nil, false, err
	}
	var interaction *domain.InteractionRequest
	var interactionCreated bool
	switch route.Kind {
	case domain.OrchestrationRouteBacklog:
		assessment := domain.AssessIssueExecutability(task, nil, domain.IssueContractProposal{})
		guidance, ok := domain.PrematureRouteGuidance(assessment)
		if !ok {
			return nil, false, c.wrapError("orchestration-route", route.IssueID, fmt.Errorf("issue is not clearly premature: %s", strings.Join(assessment.Reasons, "; ")))
		}
		route.MissingDetails = guidance
		if state.Workflow() != domain.IssueWorkflowBacklog {
			if state.Workflow() != domain.IssueWorkflowOpen {
				return nil, false, c.wrapError("orchestration-route", route.IssueID, fmt.Errorf("%w: backlog routing requires open lifecycle, got %s", domain.ErrConflict, state.Workflow()))
			}
			next, err := domain.NewIssueState(domain.IssueStateParts{Workflow: domain.IssueWorkflowBacklog, Review: domain.IssueReviewNone, CloseOutcome: domain.IssueCloseNone, Archive: state.Archive()})
			if err != nil {
				return nil, false, err
			}
			if err := domain.ValidateIssueStateTransition(state, next); err != nil {
				return nil, false, err
			}
			write := issueStateWriteValuesFromState(next, nil)
			nowRaw := now.Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `UPDATE issues SET disposition=?, engagement=?, visibility=?, status=?, lifecycle_state=?, closed_outcome=?, review_state=?, updated_at=? WHERE id=? AND visibility='live'`, write.Disposition, write.Engagement, write.Visibility, write.LegacyStatus, write.Lifecycle, write.ClosedOutcome, write.Review, nowRaw, route.IssueID); err != nil {
				return nil, false, err
			}
			if err := c.appendIssueObservationEvent(ctx, tx, route.IssueID, domain.IssueEventIssueStatusChanged, map[string]any{"from_status": string(task.Status), "to_status": string(write.LegacyStatus), "from_lifecycle": string(state.Workflow()), "to_lifecycle": string(next.Workflow()), "orchestration_route": true}); err != nil {
				return nil, false, err
			}
		}
	case domain.OrchestrationRouteInteraction:
		if state.Workflow() == domain.IssueWorkflowClosed || state.Workflow() == domain.IssueWorkflowBacklog {
			return nil, false, c.wrapError("orchestration-route", route.IssueID, fmt.Errorf("%w: interaction routing requires open or active lifecycle, got %s", domain.ErrConflict, state.Workflow()))
		}
		existing, found, err := unresolvedInteractionForUpdate(ctx, tx, route.IssueID, route.Interaction.DecisionKey)
		if err != nil {
			return nil, false, err
		}
		if found {
			interaction = &existing
		} else {
			raw, err := json.Marshal(route.Interaction)
			if err != nil {
				return nil, false, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO interaction_requests (id, issue_id, decision_key, state, revision, request_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, route.Interaction.ID, route.Interaction.IssueID, route.Interaction.DecisionKey, route.Interaction.State, route.Interaction.Revision, raw, route.Interaction.CreatedAt.UTC().Format(time.RFC3339Nano), route.Interaction.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return nil, false, fmt.Errorf("create routed interaction %s: %w", route.Interaction.ID, err)
			}
			copy := *route.Interaction
			interaction, interactionCreated = &copy, true
			if err := c.appendIssueObservationEvent(ctx, tx, route.IssueID, domain.IssueEventHumanInputRequested, map[string]any{"interaction_id": copy.ID, "decision_key": copy.DecisionKey, "question": copy.Question, "reason": route.Reason, "orchestration_scope": copy.OrchestrationScope}); err != nil {
				return nil, false, err
			}
		}
	default:
		return nil, false, fmt.Errorf("unsupported orchestration route kind %q", route.Kind)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE issues SET updated_at=? WHERE id=? AND visibility='live'`, now.Format(time.RFC3339Nano), route.IssueID); err != nil {
		return nil, false, err
	}

	if err := c.appendIssueObservationEvent(ctx, tx, route.IssueID, domain.IssueEventOrchestrationRouted, map[string]any{"kind": route.Kind, "reason": route.Reason, "missing_details": route.MissingDetails, "actor_id": actorID, "interaction_id": interactionID(interaction)}); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return interaction, interactionCreated, nil
}

func orchestrationRouteTaskForUpdate(ctx context.Context, tx *sql.Tx, issueID string) (domain.Task, domain.IssueState, error) {
	var task domain.Task
	var priority int
	var ownerID, ownerKind, claimedAt, expiresAt string
	var cols issueStateColumns
	err := tx.QueryRowContext(ctx, `SELECT i.id, i.title, COALESCE(i.description,''), COALESCE(i.acceptance,''), i.status, i.priority, COALESCE(i.disposition,''), COALESCE(i.engagement,''), COALESCE(i.visibility,''), i.archived_at,
		COALESCE((SELECT owner_id FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'),''),
		COALESCE((SELECT owner_kind FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'),''),
		COALESCE((SELECT claimed_at FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'),''),
		COALESCE((SELECT expires_at FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution'),'')
		FROM issues i WHERE i.id=? AND i.visibility='live'`, strings.TrimSpace(issueID)).Scan(&task.ID, &task.Title, &task.Description, &task.Acceptance, &cols.LegacyStatus, &priority, &cols.Disposition, &cols.Engagement, &cols.Visibility, &cols.ArchivedAt, &ownerID, &ownerKind, &claimedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.IssueState{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Task{}, domain.IssueState{}, err
	}
	task.Status, task.Priority = domain.Status(cols.LegacyStatus), domain.Priority(priority)
	task.Ownership = parseIssueOwnership(ownerID, ownerKind, claimedAt, expiresAt)
	state, err := issueStateFromColumns(issueID, task.Priority, cols)
	task.State = state
	return task, state, err
}

func unresolvedInteractionForUpdate(ctx context.Context, tx *sql.Tx, issueID, decisionKey string) (domain.InteractionRequest, bool, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT request_json FROM interaction_requests WHERE issue_id=? AND decision_key=? AND state IN ('open','discussing','answer_proposed')`, issueID, decisionKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.InteractionRequest{}, false, nil
	}
	if err != nil {
		return domain.InteractionRequest{}, false, err
	}
	var request domain.InteractionRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return domain.InteractionRequest{}, false, err
	}
	return request, true, nil
}

func releaseOrchestrationExecutionLease(ctx context.Context, c *Client, tx *sql.Tx, task domain.Task, actorID string, now time.Time, kind domain.OrchestrationRouteKind) error {
	if task.Ownership == nil {
		return nil
	}
	if task.Ownership.IsActive(now) && !task.Ownership.OwnedBy(actorID, now) {
		return fmt.Errorf("%w: execution lease owned by %s", domain.ErrConflict, task.Ownership.OwnerID)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_coordination_leases WHERE issue_id=? AND purpose=?`, task.ID, domain.CoordinationLeaseExecution); err != nil {
		return err
	}
	return c.appendIssueObservationEvent(ctx, tx, task.ID.String(), domain.IssueEventIssueOwnershipChanged, map[string]any{"action": "released", "released_by": actorID, "purpose": domain.CoordinationLeaseExecution, "orchestration_route": kind})
}

func interactionID(request *domain.InteractionRequest) string {
	if request == nil {
		return ""
	}
	return request.ID
}
