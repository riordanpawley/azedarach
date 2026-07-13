package issues

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouteOrchestrationCandidateBacklogsAndReleasesOnlyExecution(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	id, err := client.Create(ctx, CreateTaskParams{Title: "Thin issue", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	require.NoError(t, err)
	_, err = client.ClaimOwnershipWithRuntime(ctx, "proj", id, OwnershipClaimParams{OwnerID: "steward", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseExecution})
	require.NoError(t, err)
	_, err = client.ClaimOwnershipWithRuntime(ctx, "proj", id, OwnershipClaimParams{OwnerID: "steward", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseOrchestration})
	require.NoError(t, err)

	result, err := client.RouteOrchestrationCandidate(ctx, "proj", "steward", domain.OrchestrationCandidateRoute{
		IssueID: id, Kind: domain.OrchestrationRouteBacklog, Reason: "contract is not executable", MissingDetails: []string{"placeholder"},
	})
	require.NoError(t, err)
	assert.Equal(t, domain.IssueWorkflowBacklog, result.Task.State.Workflow())
	assert.Nil(t, result.Task.Ownership)
	require.Len(t, result.Task.CoordinationLeases, 1)
	assert.Equal(t, domain.CoordinationLeaseOrchestration, result.Task.CoordinationLeases[0].Purpose)
	assert.ElementsMatch(t, []string{"add explicit implementation scope", "add explicit acceptance criteria"}, result.Route.MissingDetails)

	events, err := client.ListIssueObservationEvents(ctx, id, IssueObservationEventListOptions{})
	require.NoError(t, err)
	var routed bool
	for _, event := range events {
		if event.Type == domain.IssueEventOrchestrationRouted {
			routed = true
			assert.Equal(t, string(domain.OrchestrationRouteBacklog), event.Payload["kind"])
		}
	}
	assert.True(t, routed, "durable routing evidence missing")
}

func TestRouteOrchestrationCandidateCreatesIdempotentInteractionAndProjectsWaitingHuman(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	id, err := client.Create(ctx, CreateTaskParams{Title: "Needs decision", Description: "Choose deployment policy", Acceptance: "Policy is explicit", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	require.NoError(t, err)
	_, err = client.ClaimOwnershipWithRuntime(ctx, "proj", id, OwnershipClaimParams{OwnerID: "steward", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseExecution})
	require.NoError(t, err)
	now := time.Now().UTC()
	request := domain.InteractionRequest{ID: "route-1", IssueID: id, DecisionKey: "deployment-policy", OrchestrationScope: "project", Question: "Which deployment policy should be used?", Why: "The choice changes product behavior", RequiredDecisions: []string{"select the deployment policy"}, Significance: domain.InteractionSignificanceMaterial, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose deployment policy"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
	route := domain.OrchestrationCandidateRoute{IssueID: id, Kind: domain.OrchestrationRouteInteraction, Reason: "product judgment required", Interaction: &request}

	first, err := client.RouteOrchestrationCandidate(ctx, "proj", "steward", route)
	require.NoError(t, err)
	require.True(t, first.InteractionCreated)
	assert.Nil(t, first.Task.Ownership)
	projected, err := client.InteractionsForIssue(ctx, id)
	require.NoError(t, err)
	assert.True(t, domain.IssueWaitingHuman(id, projected))

	second, err := client.RouteOrchestrationCandidate(ctx, "proj", "steward", route)
	require.NoError(t, err)
	assert.False(t, second.InteractionCreated)
	require.NotNil(t, second.Interaction)
	assert.Equal(t, "route-1", second.Interaction.ID)
	requests, err := client.InteractionsForIssue(ctx, id)
	require.NoError(t, err)
	require.Len(t, requests, 1)
}

func TestRouteOrchestrationCandidateRejectsForeignExecutionOwner(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	id, err := client.Create(ctx, CreateTaskParams{Title: "Thin issue", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	require.NoError(t, err)
	_, err = client.ClaimOwnershipWithRuntime(ctx, "proj", id, OwnershipClaimParams{OwnerID: "worker", OwnerKind: "agent", Purpose: domain.CoordinationLeaseExecution})
	require.NoError(t, err)
	_, err = client.RouteOrchestrationCandidate(ctx, "proj", "steward", domain.OrchestrationCandidateRoute{IssueID: id, Kind: domain.OrchestrationRouteBacklog, Reason: "thin", MissingDetails: []string{"scope"}})
	require.ErrorIs(t, err, domain.ErrConflict)
	task, err := client.GetWithRuntime(ctx, "proj", id)
	require.NoError(t, err)
	assert.Equal(t, domain.IssueWorkflowOpen, task.State.Workflow())
	require.NotNil(t, task.Ownership)
	assert.Equal(t, "worker", task.Ownership.OwnerID)
}

func TestRouteOrchestrationCandidateInteractionIsIdempotentAcrossClients(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	first := newTestClientAtPath(t, path, slog.Default())
	second := newTestClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = first.CloseDB(); _ = second.CloseDB() })
	id, err := first.Create(ctx, CreateTaskParams{Title: "Needs decision", Description: "Choose policy", Acceptance: "Policy chosen", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	require.NoError(t, err)
	now := time.Now().UTC()
	request := domain.InteractionRequest{ID: "shared-route", IssueID: id, DecisionKey: "policy", OrchestrationScope: "project", Question: "Which policy?", Why: "Product judgment required", RequiredDecisions: []string{"select policy"}, Significance: domain.InteractionSignificanceMaterial, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose policy"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
	route := domain.OrchestrationCandidateRoute{IssueID: id, Kind: domain.OrchestrationRouteInteraction, Reason: "product judgment", Interaction: &request}

	clients := []*Client{first, second}
	results := make([]OrchestrationRouteResult, len(clients))
	errs := make([]error, len(clients))
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = clients[i].RouteOrchestrationCandidate(ctx, "proj", "steward", route)
		}(i)
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	created := 0
	for _, result := range results {
		if result.InteractionCreated {
			created++
		}
	}
	assert.Equal(t, 1, created)
	requests, err := first.InteractionsForIssue(ctx, id)
	require.NoError(t, err)
	require.Len(t, requests, 1)
}
