package issues

import (
	"context"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecisionStore_CRUDAndLinks(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	seedIssue(t, client, "cgn", "Add decisions storage")

	requirement, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "cgn-req-1",
		Title:   "Persist decisions in store",
		Status:  RequirementStatusOpen,
	})
	require.NoError(t, err)

	created, err := client.CreateDecision(ctx, CreateDecisionParams{
		LocalID:      "use-sqlite",
		Title:        "Use SQLite for decision store",
		Context:      "Need durable, low-friction store reuse across spec workflows.",
		Decision:     "Reuse the SQLite schema rather than adding a new datastore.",
		Consequences: "All decisions colocated with issues + spec_requirements.",
		Status:       DecisionStatusAccepted,
	})
	require.NoError(t, err)
	assert.Equal(t, "use-sqlite", created.LocalID)
	assert.Equal(t, DecisionStatusAccepted, created.Status)
	assert.WithinDuration(t, time.Now(), created.CreatedAt, 5*time.Second)

	got, err := client.GetDecision(ctx, "use-sqlite")
	require.NoError(t, err)
	assert.Equal(t, created.Title, got.Title)

	_, err = client.CreateDecision(ctx, CreateDecisionParams{
		LocalID: "use-sqlite",
		Title:   "duplicate",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrConflict)

	newTitle := "Use SQLite (renamed)"
	updated, err := client.UpdateDecision(ctx, "use-sqlite", UpdateDecisionParams{
		Title:  &newTitle,
		Status: decisionStatusPtr(DecisionStatusSuperseded),
	})
	require.NoError(t, err)
	assert.Equal(t, newTitle, updated.Title)
	assert.Equal(t, DecisionStatusSuperseded, updated.Status)

	// Link to an existing issue and requirement.
	issueLink, err := client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: "use-sqlite",
		TargetKind: DecisionTargetIssue,
		TargetID:   "cgn",
		Relation:   DecisionRelationImplements,
	})
	require.NoError(t, err)
	assert.Equal(t, "use-sqlite:issue:cgn", issueLink.ID)

	reqLink, err := client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: "use-sqlite",
		TargetKind: DecisionTargetRequirement,
		TargetID:   requirement.LocalID,
		Relation:   DecisionRelationImplements,
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionRelationImplements, reqLink.Relation)

	// Linking to a non-existent target returns ErrNotFound.
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: "use-sqlite",
		TargetKind: DecisionTargetIssue,
		TargetID:   "no-such-issue",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrNotFound)

	links, err := client.ListDecisionLinks(ctx, DecisionLinkFilter{DecisionID: "use-sqlite"})
	require.NoError(t, err)
	assert.Len(t, links, 2)

	// Listing decisions filtered by issue ID should find this one.
	filtered, err := client.ListDecisions(ctx, DecisionFilter{IssueID: "cgn"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, "use-sqlite", filtered[0].LocalID)

	// Filter by requirement.
	filtered, err = client.ListDecisions(ctx, DecisionFilter{RequirementID: "cgn-req-1"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)

	// Filter by status.
	filtered, err = client.ListDecisions(ctx, DecisionFilter{Statuses: []DecisionStatus{DecisionStatusSuperseded}})
	require.NoError(t, err)
	require.Len(t, filtered, 1)

	filtered, err = client.ListDecisions(ctx, DecisionFilter{Statuses: []DecisionStatus{DecisionStatusProposed}})
	require.NoError(t, err)
	assert.Empty(t, filtered)

	// Remove a link and verify it disappears.
	require.NoError(t, client.RemoveDecisionLink(ctx, "use-sqlite", DecisionTargetIssue, "cgn"))
	links, err = client.ListDecisionLinks(ctx, DecisionLinkFilter{DecisionID: "use-sqlite"})
	require.NoError(t, err)
	assert.Len(t, links, 1)

	// Re-adding the same link works (soft-undelete) and produces a single active row.
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: "use-sqlite",
		TargetKind: DecisionTargetIssue,
		TargetID:   "cgn",
		Relation:   DecisionRelationRelates,
	})
	require.NoError(t, err)
	links, err = client.ListDecisionLinks(ctx, DecisionLinkFilter{DecisionID: "use-sqlite"})
	require.NoError(t, err)
	assert.Len(t, links, 2)

	// Deleting a decision soft-deletes its active links.
	require.NoError(t, client.DeleteDecision(ctx, "use-sqlite"))
	_, err = client.GetDecision(ctx, "use-sqlite")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	links, err = client.ListDecisionLinks(ctx, DecisionLinkFilter{DecisionID: "use-sqlite", IncludeDeleted: true})
	require.NoError(t, err)
	for _, link := range links {
		assert.NotNil(t, link.DeletedAt, "expected link %s to be soft-deleted", link.ID)
	}
}

func TestDecisionStore_ValidationErrors(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	_, err := client.CreateDecision(ctx, CreateDecisionParams{LocalID: " ", Title: "x"})
	require.Error(t, err)

	_, err = client.CreateDecision(ctx, CreateDecisionParams{LocalID: "d1", Title: "  "})
	require.Error(t, err)

	_, err = client.CreateDecision(ctx, CreateDecisionParams{LocalID: "d1", Title: "ok", Status: "not-a-status"})
	require.Error(t, err)

	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{DecisionID: "", TargetKind: DecisionTargetIssue, TargetID: "x"})
	require.Error(t, err)

	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{DecisionID: "d", TargetKind: "wrong", TargetID: "x"})
	require.Error(t, err)
}

func TestDecisionStore_AuditLogIsolatedFromSpecAudit(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	seedIssue(t, client, "cgn", "issue")

	_, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "cgn-req-1",
		Title:   "req",
		Status:  RequirementStatusOpen,
	})
	require.NoError(t, err)

	// Mutating spec writes to spec_audit_log only.
	_, err = client.AddSpecLink(ctx, AddSpecLinkParams{
		IssueID:       "cgn",
		RequirementID: "cgn-req-1",
		Role:          LinkRoleImplements,
	})
	require.NoError(t, err)

	// Mutating decisions writes to decision_audit_log only.
	_, err = client.CreateDecision(ctx, CreateDecisionParams{
		LocalID: "d1",
		Title:   "test decision",
		Status:  DecisionStatusAccepted,
	})
	require.NoError(t, err)
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: "d1",
		TargetKind: DecisionTargetIssue,
		TargetID:   "cgn",
		Relation:   DecisionRelationRelates,
	})
	require.NoError(t, err)

	db, err := client.dbHandle()
	require.NoError(t, err)

	var specEntities []string
	rows, err := db.Query(`SELECT DISTINCT entity_type FROM spec_audit_log ORDER BY entity_type`)
	require.NoError(t, err)
	for rows.Next() {
		var et string
		require.NoError(t, rows.Scan(&et))
		specEntities = append(specEntities, et)
	}
	require.NoError(t, rows.Close())

	for _, et := range specEntities {
		assert.NotContains(t, []string{"decision", "decision_link"}, et,
			"spec_audit_log must not contain decision audit rows; found %q", et)
	}

	var decisionEntities []string
	rows, err = db.Query(`SELECT DISTINCT entity_type FROM decision_audit_log ORDER BY entity_type`)
	require.NoError(t, err)
	for rows.Next() {
		var et string
		require.NoError(t, rows.Scan(&et))
		decisionEntities = append(decisionEntities, et)
	}
	require.NoError(t, rows.Close())

	assert.ElementsMatch(t, []string{"decision", "decision_link"}, decisionEntities,
		"decision_audit_log should carry decision and decision_link rows")
}

func TestDecisionStore_ListLinksByTarget(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	seedIssue(t, client, "cgn", "issue")

	for _, id := range []string{"alpha", "beta"} {
		_, err := client.CreateDecision(ctx, CreateDecisionParams{
			LocalID: id,
			Title:   "Decision " + id,
			Status:  DecisionStatusAccepted,
		})
		require.NoError(t, err)
		_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{
			DecisionID: id,
			TargetKind: DecisionTargetIssue,
			TargetID:   "cgn",
			Relation:   DecisionRelationImplements,
		})
		require.NoError(t, err)
	}

	links, err := client.ListDecisionLinks(ctx, DecisionLinkFilter{
		TargetKind: DecisionTargetIssue,
		TargetID:   "cgn",
	})
	require.NoError(t, err)
	require.Len(t, links, 2)
	gotIDs := []string{links[0].DecisionID, links[1].DecisionID}
	assert.ElementsMatch(t, []string{"alpha", "beta"}, gotIDs)

	// The decisions referenced by these links should be fetchable via LocalIDs filter,
	// which is what the daemon adapter uses to enrich the response.
	decisions, err := client.ListDecisions(ctx, DecisionFilter{LocalIDs: gotIDs})
	require.NoError(t, err)
	require.Len(t, decisions, 2)
}

func TestDecisionStore_FilterByQuery(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	_, err := client.CreateDecision(ctx, CreateDecisionParams{LocalID: "alpha", Title: "Use Postgres"})
	require.NoError(t, err)
	_, err = client.CreateDecision(ctx, CreateDecisionParams{LocalID: "beta", Title: "Use SQLite", Context: "lightweight"})
	require.NoError(t, err)

	filtered, err := client.ListDecisions(ctx, DecisionFilter{Query: "SQLite"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, "beta", filtered[0].LocalID)

	filtered, err = client.ListDecisions(ctx, DecisionFilter{Query: "lightweight"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
}

func seedIssue(t *testing.T, c *Client, id, title string) {
	t.Helper()
	db, err := c.dbHandle()
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`
		INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at)
		VALUES (?, ?, ?, 'open', 2, 'task', ?, ?)
	`, id, title, "", now, now)
	require.NoError(t, err)
}

func decisionStatusPtr(s DecisionStatus) *DecisionStatus {
	return &s
}
