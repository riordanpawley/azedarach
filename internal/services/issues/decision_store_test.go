package issues

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecisionStore_SemanticIDsDoNotCollideAcrossIsolatedStores(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	clients := []*Client{newTestClient(t), newTestClient(t)}
	titles := []string{"Use SQLite / WAL?", "Use SQLite / WAL?", "Ship offline mode"}
	seen := make(map[string]struct{})
	validID := regexp.MustCompile(`^dec-[a-z0-9]+(?:-[a-z0-9]+)*-[0-9a-f]{32}$`)

	for _, client := range clients {
		for _, title := range titles {
			decision, err := client.RecordDecision(ctx, RecordDecisionParams{Title: title, Rationale: "durable rationale"})
			require.NoError(t, err)
			assert.True(t, validID.MatchString(decision.LocalID), decision.LocalID)
			assert.NotContains(t, decision.LocalID, "/")
			if _, exists := seen[decision.LocalID]; exists {
				t.Fatalf("decision id collided across isolated stores: %s", decision.LocalID)
			}
			seen[decision.LocalID] = struct{}{}
		}
	}
}

func TestDecisionStore_ImportsLegacyAndSemanticIDs(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	legacy, err := client.ImportDecision(ctx, ImportDecisionParams{LocalID: "dec-41", NumericID: 41, Title: "Legacy", Rationale: "preserve it"})
	require.NoError(t, err)
	assert.Equal(t, "dec-41", legacy.LocalID)

	semanticID := "dec-use-sqlite-0123456789abcdef0123456789abcdef"
	semantic, err := client.ImportDecision(ctx, ImportDecisionParams{LocalID: semanticID, Title: "Use SQLite", Rationale: "portable import"})
	require.NoError(t, err)
	assert.Equal(t, semanticID, semantic.LocalID)
	_, err = client.GetDecision(ctx, semanticID)
	require.NoError(t, err)
}

func TestDecisionStore_RecordIsIdempotentForStableKey(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	params := RecordDecisionParams{Title: "Use SQLite", Rationale: "portable", IdempotencyKey: "decision.record-01234567-89ab-cdef-0123-456789abcdef"}

	first, err := client.RecordDecision(ctx, params)
	require.NoError(t, err)
	replayed, err := client.RecordDecision(ctx, params)
	require.NoError(t, err)
	assert.Equal(t, first, replayed)

	db, err := client.dbHandle()
	require.NoError(t, err)
	var decisions, audits int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decisions WHERE local_id = ?`, first.LocalID).Scan(&decisions))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_audit_log WHERE entity_type = ? AND entity_id = ?`, decisionEntityKind, first.LocalID).Scan(&audits))
	assert.Equal(t, 1, decisions)
	assert.Equal(t, 1, audits)

	params.Rationale = "different payload"
	_, err = client.RecordDecision(ctx, params)
	require.ErrorIs(t, err, domain.ErrConflict)
}

func TestDecisionStore_RecordAndLinks(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	seedIssue(t, client, "cgn", "Add decisions storage")

	requirement, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "cgn-req-1",
		Title:   "Persist decisions in store",
		Status:  RequirementStatusOpen,
	})
	require.NoError(t, err)

	first, err := client.RecordDecision(ctx, RecordDecisionParams{
		Title:     "Use SQLite for the decision store",
		Rationale: "Existing schema fits; a new datastore isn't worth the operational cost.",
		Context:   "Need durable local storage for decisions alongside issues.",
	})
	require.NoError(t, err)
	assert.Regexp(t, `^dec-use-sqlite-for-the-decision-store-[0-9a-f]{32}$`, first.LocalID)
	assert.NotEmpty(t, first.Title)
	assert.NotEmpty(t, first.Rationale)
	assert.WithinDuration(t, time.Now(), first.CreatedAt, 5*time.Second)

	got, err := client.GetDecision(ctx, first.LocalID)
	require.NoError(t, err)
	assert.Equal(t, first.Title, got.Title)
	assert.Equal(t, first.Rationale, got.Rationale)

	// Auto-allocation communicates the topic without depending on row order.
	second, err := client.RecordDecision(ctx, RecordDecisionParams{
		Title:     "Polymorphic decision_links table",
		Rationale: "One table covers issue/requirement/decision targets without parallel schemas.",
	})
	require.NoError(t, err)
	assert.Regexp(t, `^dec-polymorphic-decision-links-table-[0-9a-f]{32}$`, second.LocalID)
	assert.NotEqual(t, first.LocalID, second.LocalID)

	// Required-field validation.
	_, err = client.RecordDecision(ctx, RecordDecisionParams{Title: " "})
	require.Error(t, err)
	_, err = client.RecordDecision(ctx, RecordDecisionParams{Title: "ok"})
	require.Error(t, err)

	// Update only specified fields.
	newRationale := "Reuse existing SQLite tables; document the link in spec."
	updated, err := client.UpdateDecision(ctx, first.LocalID, UpdateDecisionParams{Rationale: &newRationale})
	require.NoError(t, err)
	assert.Equal(t, newRationale, updated.Rationale)
	assert.Equal(t, first.Title, updated.Title)

	// Link decisions to issues, requirements, and other decisions.
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: first.LocalID,
		TargetKind: DecisionTargetIssue,
		TargetID:   "cgn",
		Relation:   DecisionRelationAppliesTo,
	})
	require.NoError(t, err)
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: second.LocalID,
		TargetKind: DecisionTargetRequirement,
		TargetID:   requirement.LocalID,
		Relation:   DecisionRelationAppliesTo,
	})
	require.NoError(t, err)
	revisesLink, err := client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: second.LocalID,
		TargetKind: DecisionTargetDecision,
		TargetID:   first.LocalID,
		Relation:   DecisionRelationRevises,
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionRelationRevises, revisesLink.Relation)

	// Self-link is rejected.
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: first.LocalID,
		TargetKind: DecisionTargetDecision,
		TargetID:   first.LocalID,
		Relation:   DecisionRelationRevises,
	})
	require.Error(t, err)

	// Linking to a non-existent target returns ErrNotFound.
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: first.LocalID,
		TargetKind: DecisionTargetIssue,
		TargetID:   "no-such-issue",
	})
	require.ErrorIs(t, err, domain.ErrNotFound)

	// Listing by issue surfaces decisions that apply to that issue.
	filtered, err := client.ListDecisions(ctx, DecisionFilter{IssueID: "cgn"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, first.LocalID, filtered[0].LocalID)

	// Listing by requirement surfaces decisions that apply to that requirement.
	filtered, err = client.ListDecisions(ctx, DecisionFilter{RequirementID: "cgn-req-1"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, second.LocalID, filtered[0].LocalID)

	// Query search hits title/rationale/context.
	filtered, err = client.ListDecisions(ctx, DecisionFilter{Query: "polymorphic"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, second.LocalID, filtered[0].LocalID)

	// Removing a link and re-adding works (soft-delete then revive).
	require.NoError(t, client.RemoveDecisionLink(ctx, first.LocalID, DecisionTargetIssue, "cgn"))
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: first.LocalID,
		TargetKind: DecisionTargetIssue,
		TargetID:   "cgn",
		Relation:   DecisionRelationAppliesTo,
	})
	require.NoError(t, err)
	links, err := client.ListDecisionLinks(ctx, DecisionLinkFilter{DecisionID: first.LocalID})
	require.NoError(t, err)
	assert.Len(t, links, 1)

	// Deleting a decision soft-deletes its active links.
	require.NoError(t, client.DeleteDecision(ctx, first.LocalID))
	_, err = client.GetDecision(ctx, first.LocalID)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestDecisionRevisionAdvancesOnlyForMaterialSemantics(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "scope", Type: domain.TypeTask, Status: domain.StatusOpen})
	require.NoError(t, err)
	decision, err := client.RecordDecision(ctx, RecordDecisionParams{Title: "contract", Rationale: "initial"})
	require.NoError(t, err)
	createdRevision, err := client.DecisionRevision(ctx, decision.LocalID)
	require.NoError(t, err)
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: DecisionTargetIssue, TargetID: issueID, Relation: DecisionRelationInforms})
	require.NoError(t, err)
	benignRevision, err := client.DecisionRevision(ctx, decision.LocalID)
	require.NoError(t, err)
	assert.Equal(t, createdRevision, benignRevision, "benign link must not force worker reconciliation")
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: DecisionTargetIssue, TargetID: issueID, Relation: DecisionRelationGoverns})
	require.NoError(t, err)
	materialRevision, err := client.DecisionRevision(ctx, decision.LocalID)
	require.NoError(t, err)
	assert.Greater(t, materialRevision, benignRevision)
	newRationale := "amended"
	_, err = client.UpdateDecision(ctx, decision.LocalID, UpdateDecisionParams{Rationale: &newRationale})
	require.NoError(t, err)
	updatedRevision, err := client.DecisionRevision(ctx, decision.LocalID)
	require.NoError(t, err)
	assert.Greater(t, updatedRevision, materialRevision)
}

func TestDecisionPropagationOutboxFailureRollsBackDecisionMutationAndAudit(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	seedIssue(t, client, "outbox-worker", "Outbox worker")
	issueID := "outbox-worker"
	decision, err := client.RecordDecision(ctx, RecordDecisionParams{Title: "before", Rationale: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	beforeRevision, err := client.DecisionRevision(ctx, decision.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	afterTitle := "must roll back"
	_, err = client.UpdateDecisionWithPropagation(ctx, decision.LocalID, UpdateDecisionParams{Title: &afterTitle}, DecisionPropagationIntent{
		ChangedIssueIDs: []string{issueID},
		Payload:         map[string]any{"invalid": make(chan struct{})},
	})
	if err == nil || !strings.Contains(err.Error(), "marshal decision propagation payload") {
		t.Fatalf("update error=%v, want atomic outbox serialization failure", err)
	}
	after, err := client.GetDecision(ctx, decision.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != decision.Title {
		t.Fatalf("title=%q, want rolled back %q", after.Title, decision.Title)
	}
	afterRevision, err := client.DecisionRevision(ctx, decision.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRevision != beforeRevision {
		t.Fatalf("revision=%d, want rolled back %d", afterRevision, beforeRevision)
	}
}

func TestDecisionStore_QuerySearchUsesFTSAndCoversDecisionFields(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	cases := []struct {
		name   string
		params RecordDecisionParams
		query  string
	}{
		{
			name: "title",
			params: RecordDecisionParams{
				Title:     "Aurora decision title",
				Rationale: "Keep title matching indexed.",
			},
			query: "aurora",
		},
		{
			name: "rationale",
			params: RecordDecisionParams{
				Title:     "Rationale indexed",
				Rationale: "Borealis reasoning belongs in the search surface.",
			},
			query: "borealis",
		},
		{
			name: "context",
			params: RecordDecisionParams{
				Title:     "Context indexed",
				Rationale: "Decision rationale.",
				Context:   "Quasar context belongs in the search surface.",
			},
			query: "quasar",
		},
		{
			name: "consequences",
			params: RecordDecisionParams{
				Title:        "Consequences indexed",
				Rationale:    "Decision rationale.",
				Consequences: "Nebula consequences belong in the search surface.",
			},
			query: "nebula",
		},
	}

	wantByQuery := map[string]string{}
	for _, tc := range cases {
		created, err := client.RecordDecision(ctx, tc.params)
		require.NoError(t, err, tc.name)
		wantByQuery[tc.query] = created.LocalID
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.ListDecisions(ctx, DecisionFilter{Query: tc.query})
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, wantByQuery[tc.query], got[0].LocalID)
		})
	}

	query, args, empty := decisionListQuery(DecisionFilter{Query: "quasar"})
	require.False(t, empty)
	assert.Contains(t, query, "decision_search_fts MATCH ?")
	assert.NotContains(t, strings.ToUpper(query), " LIKE ")

	db, err := client.dbHandle()
	require.NoError(t, err)
	plan := explainQueryPlan(t, ctx, db, query, args...)
	assert.Contains(t, plan, "SCAN decision_search_fts VIRTUAL TABLE INDEX", plan)
	assert.Contains(t, plan, "SEARCH d USING INTEGER PRIMARY KEY", plan)

	newRationale := "Pulsar reasoning replaces the original indexed rationale."
	_, err = client.UpdateDecision(ctx, wantByQuery["borealis"], UpdateDecisionParams{Rationale: &newRationale})
	require.NoError(t, err)
	staleMatches, err := client.ListDecisions(ctx, DecisionFilter{Query: "borealis"})
	require.NoError(t, err)
	assert.Empty(t, staleMatches)
	updatedMatches, err := client.ListDecisions(ctx, DecisionFilter{Query: "pulsar"})
	require.NoError(t, err)
	require.Len(t, updatedMatches, 1)
	assert.Equal(t, wantByQuery["borealis"], updatedMatches[0].LocalID)

	require.NoError(t, client.DeleteDecision(ctx, wantByQuery["quasar"]))
	deletedMatches, err := client.ListDecisions(ctx, DecisionFilter{Query: "quasar"})
	require.NoError(t, err)
	assert.Empty(t, deletedMatches)

	deletedMatches, err = client.ListDecisions(ctx, DecisionFilter{Query: "quasar", IncludeDeleted: true})
	require.NoError(t, err)
	require.Len(t, deletedMatches, 1)
	assert.Equal(t, wantByQuery["quasar"], deletedMatches[0].LocalID)
	assert.NotNil(t, deletedMatches[0].DeletedAt)
}

func TestDecisionStore_AuditLogIsolatedFromSpecAudit(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	seedIssue(t, client, "cgn", "issue")

	_, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "cgn-req-1",
		Title:   "req",
		Status:  RequirementStatusOpen,
	})
	require.NoError(t, err)
	_, err = client.AddSpecLink(ctx, AddSpecLinkParams{
		IssueID:       "cgn",
		RequirementID: "cgn-req-1",
		Role:          LinkRoleImplements,
	})
	require.NoError(t, err)

	decision, err := client.RecordDecision(ctx, RecordDecisionParams{
		Title:     "test decision",
		Rationale: "test rationale",
	})
	require.NoError(t, err)
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: decision.LocalID,
		TargetKind: DecisionTargetIssue,
		TargetID:   "cgn",
		Relation:   DecisionRelationAppliesTo,
	})
	require.NoError(t, err)

	db, err := client.dbHandle()
	require.NoError(t, err)

	specEntities := scanDistinct(t, db, "spec_audit_log")
	for _, et := range specEntities {
		assert.NotContains(t, []string{"decision", "decision_link"}, et,
			"spec_audit_log must not contain decision audit rows; found %q", et)
	}

	decisionEntities := scanDistinct(t, db, "decision_audit_log")
	assert.ElementsMatch(t, []string{"decision", "decision_link"}, decisionEntities)
}

func TestDecisionStore_ConsequencesRoundTrip(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	created, err := client.RecordDecision(ctx, RecordDecisionParams{
		Title:        "Move sessions to remote daemon",
		Rationale:    "Latency budget exceeded.",
		Consequences: "Local-only mode breaks until offline support is added.",
	})
	require.NoError(t, err)
	assert.Equal(t, "Local-only mode breaks until offline support is added.", created.Consequences)

	got, err := client.GetDecision(ctx, created.LocalID)
	require.NoError(t, err)
	assert.Equal(t, created.Consequences, got.Consequences)

	// Update only the consequences field.
	newCons := "Local-only mode WORKS again now."
	updated, err := client.UpdateDecision(ctx, created.LocalID, UpdateDecisionParams{Consequences: &newCons})
	require.NoError(t, err)
	assert.Equal(t, newCons, updated.Consequences)
	assert.Equal(t, "Move sessions to remote daemon", updated.Title)
}

func TestDecisionStore_UpdateDecisionForOwnerRequiresExactActiveOwner(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	seedIssue(t, client, "local", "Local owner")
	seedIssue(t, client, "foreign", "Foreign owner")
	decision, err := client.RecordDecision(ctx, RecordDecisionParams{Title: "Owned decision", Rationale: "original"})
	require.NoError(t, err)
	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: DecisionTargetIssue, TargetID: "foreign", Relation: DecisionRelationAppliesTo})
	require.NoError(t, err)
	db, err := client.dbHandle()
	require.NoError(t, err)
	var auditRowsBefore int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_audit_log WHERE entity_type = ? AND entity_id = ?`, decisionEntityKind, decision.LocalID).Scan(&auditRowsBefore))

	verified, owner, err := client.UpdateDecisionForOwner(ctx, decision.LocalID, "foreign", UpdateDecisionParams{})
	require.NoError(t, err)
	assert.Equal(t, "foreign", owner.OwnerIssueID)
	assert.Equal(t, decision.UpdatedAt, verified.UpdatedAt)
	var auditRowsAfter int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_audit_log WHERE entity_type = ? AND entity_id = ?`, decisionEntityKind, decision.LocalID).Scan(&auditRowsAfter))
	assert.Equal(t, auditRowsBefore, auditRowsAfter)

	_, owner, err = client.UpdateDecisionForOwner(ctx, decision.LocalID, "local", UpdateDecisionParams{})
	require.ErrorIs(t, err, ErrDecisionOwnerMismatch)
	assert.Equal(t, []string{"foreign"}, owner.IssueIDs)
	assert.Equal(t, "foreign", owner.OwnerIssueID)

	changed := "must not apply"
	_, owner, err = client.UpdateDecisionForOwner(ctx, decision.LocalID, "local", UpdateDecisionParams{Rationale: &changed})
	require.ErrorIs(t, err, ErrDecisionOwnerMismatch)
	assert.Equal(t, "foreign", owner.OwnerIssueID)
	unchanged, err := client.GetDecision(ctx, decision.LocalID)
	require.NoError(t, err)
	assert.Equal(t, decision.Rationale, unchanged.Rationale)

	updatedRationale := "owner-authorized"
	updated, owner, err := client.UpdateDecisionForOwner(ctx, decision.LocalID, "foreign", UpdateDecisionParams{Rationale: &updatedRationale})
	require.NoError(t, err)
	assert.Equal(t, "foreign", owner.OwnerIssueID)
	assert.Equal(t, updatedRationale, updated.Rationale)

	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: DecisionTargetIssue, TargetID: "local", Relation: DecisionRelationAppliesTo})
	require.NoError(t, err)
	_, owner, err = client.UpdateDecisionForOwner(ctx, decision.LocalID, "foreign", UpdateDecisionParams{})
	require.ErrorIs(t, err, ErrDecisionOwnerMismatch)
	assert.Equal(t, []string{"foreign", "local"}, owner.IssueIDs)
	assert.Empty(t, owner.OwnerIssueID)
}

func TestDecisionStore_ValidationErrors(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	_, err := client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: "",
		TargetKind: DecisionTargetIssue,
		TargetID:   "x",
	})
	require.Error(t, err)

	_, err = client.AddDecisionLink(ctx, AddDecisionLinkParams{
		DecisionID: "dec-1",
		TargetKind: "wrong",
		TargetID:   "x",
	})
	require.Error(t, err)
}

func seedIssue(t *testing.T, c *Client, id, title string) {
	t.Helper()
	db, err := c.dbHandle()
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`
		INSERT INTO issues (id, title, description, status, disposition, engagement, visibility, lifecycle_state, closed_outcome, review_state, priority, issue_type, created_at, updated_at)
		VALUES (?, ?, ?, 'open', 'ready', 'idle', 'live', 'open', 'none', 'none', 2, 'task', ?, ?)
	`, id, title, "", now, now)
	require.NoError(t, err)
}

func scanDistinct(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT DISTINCT entity_type FROM ` + table + ` ORDER BY entity_type`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var et string
		require.NoError(t, rows.Scan(&et))
		out = append(out, et)
	}
	require.NoError(t, rows.Err())
	return out
}
