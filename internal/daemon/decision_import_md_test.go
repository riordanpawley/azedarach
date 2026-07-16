package daemon

import (
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestParseDecisionMarkdownHappyPath(t *testing.T) {
	body := `# dec-3: Use SQLite for the decision store

- Created: 2026-05-01
- Updated: 2026-05-02
- Revised by: dec-7

## Rationale

Reuse existing schema; new datastore not worth the operational cost.

## Context

Need durable local storage for decisions.

## Consequences

All decisions colocated with issues + spec_requirements.

## Links

- applies-to issue:cgn — primary implementation path
- informs requirement:cgn-req-1
- (incoming) applies-to decision:dec-9
`
	parsed, err := parseDecisionMarkdown([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.LocalID != "dec-3" || parsed.NumericID != 3 {
		t.Errorf("id = %q/%d, want dec-3/3", parsed.LocalID, parsed.NumericID)
	}
	if parsed.Title != "Use SQLite for the decision store" {
		t.Errorf("title = %q", parsed.Title)
	}
	if parsed.Rationale == nil || *parsed.Rationale != "Reuse existing schema; new datastore not worth the operational cost." {
		t.Errorf("rationale = %v", parsed.Rationale)
	}
	if parsed.Context == nil || *parsed.Context != "Need durable local storage for decisions." {
		t.Errorf("context = %v", parsed.Context)
	}
	if parsed.Consequences == nil || *parsed.Consequences != "All decisions colocated with issues + spec_requirements." {
		t.Errorf("consequences = %v", parsed.Consequences)
	}
	if parsed.RevisedBy != "dec-7" {
		t.Errorf("revised by = %q", parsed.RevisedBy)
	}
	if len(parsed.Links) != 2 {
		t.Fatalf("expected 2 outgoing links (incoming skipped), got %d: %+v", len(parsed.Links), parsed.Links)
	}
	if parsed.Links[0].Relation != "applies-to" || parsed.Links[0].TargetKind != "issue" || parsed.Links[0].TargetID != "cgn" {
		t.Errorf("first link = %+v", parsed.Links[0])
	}
	if parsed.Links[0].Note != "primary implementation path" {
		t.Errorf("first link note = %q", parsed.Links[0].Note)
	}
}

func TestParseDecisionMarkdownSemanticID(t *testing.T) {
	id := "dec-use-sqlite-0123456789abcdef0123456789abcdef"
	parsed, err := parseDecisionMarkdown([]byte("# " + id + ": Use SQLite\n\n## Rationale\n\nPortable identity.\n"))
	if err != nil {
		t.Fatalf("parse semantic decision: %v", err)
	}
	if parsed.LocalID != id || parsed.NumericID != 0 {
		t.Fatalf("id = %q/%d, want %s/0", parsed.LocalID, parsed.NumericID, id)
	}
}

func TestParseDecisionMarkdownMissingSectionsLeaveFieldsNil(t *testing.T) {
	body := `# dec-1: Title only

- Created: 2026-05-01
- Updated: 2026-05-01

## Rationale

Body text.
`
	parsed, err := parseDecisionMarkdown([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Rationale == nil {
		t.Error("expected rationale to be present")
	}
	if parsed.Context != nil {
		t.Errorf("expected context nil, got %v", *parsed.Context)
	}
	if parsed.Consequences != nil {
		t.Errorf("expected consequences nil, got %v", *parsed.Consequences)
	}
}

func TestParseDecisionMarkdownRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"no header":         "## Rationale\n\nbody\n",
		"bad id":            "# dec-abc: Title\n\n",
		"empty title":       "# dec-1: \n",
		"duplicate headers": "# dec-1: x\n\n# dec-2: y\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDecisionMarkdown([]byte(body)); err == nil {
				t.Errorf("expected error for %q", body)
			}
		})
	}
}

func TestPlanDecisionImportClassifiesFields(t *testing.T) {
	now := time.Now().UTC()
	existing := issues.Decision{
		LocalID:      "dec-1",
		Title:        "Existing title",
		Rationale:    "Old rationale",
		Context:      "Old context",
		Consequences: "",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	newCtx := "New context"
	newRationale := "New rationale"
	parsed := parsedDecisionMD{
		LocalID:   "dec-1",
		NumericID: 1,
		Title:     "Existing title", // unchanged
		Rationale: &newRationale,    // conflict
		Context:   &newCtx,          // conflict
		// Consequences: nil          → no update
	}

	changes, conflicts := planDecisionImport(parsed, existing, false)

	if len(changes) != 0 {
		t.Errorf("expected zero clean changes (rationale/context are conflicts), got %+v", changes)
	}
	if len(conflicts) != 2 {
		t.Fatalf("expected 2 conflicts (rationale + context), got %+v", conflicts)
	}
}

func TestPlanDecisionImportTreatsEmptySQLiteFieldAsChangeNotConflict(t *testing.T) {
	existing := issues.Decision{LocalID: "dec-1", Title: "Title", Rationale: "Old rationale"}
	newCons := "Adding consequences for the first time"
	parsed := parsedDecisionMD{
		LocalID:      "dec-1",
		NumericID:    1,
		Title:        "Title",
		Consequences: &newCons,
	}
	changes, conflicts := planDecisionImport(parsed, existing, false)
	if len(conflicts) != 0 {
		t.Errorf("expected no conflicts when SQLite field was empty, got %+v", conflicts)
	}
	if len(changes) != 1 || changes[0].Field != "consequences" {
		t.Errorf("expected single consequences change, got %+v", changes)
	}
}

func TestPlanDecisionImportNewRecordHasNoConflicts(t *testing.T) {
	rationale := "Why"
	parsed := parsedDecisionMD{
		LocalID:   "dec-1",
		NumericID: 1,
		Title:     "New",
		Rationale: &rationale,
	}
	changes, conflicts := planDecisionImport(parsed, issues.Decision{}, true)
	if len(conflicts) != 0 {
		t.Errorf("new records can't have conflicts, got %+v", conflicts)
	}
	if len(changes) < 2 {
		t.Errorf("expected at least title+rationale changes, got %+v", changes)
	}
}

func TestRenderParseRoundTrip(t *testing.T) {
	d := issues.Decision{
		LocalID:      "dec-5",
		Title:        "Round-trip me",
		Rationale:    "Because it's the parser/renderer contract.",
		Context:      "Decisions feature work.",
		Consequences: "If this breaks, drift is undetectable.",
		CreatedAt:    time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
	}
	noteImpl := "primary"
	outgoing := []issues.DecisionLink{
		{DecisionID: "dec-5", TargetKind: issues.DecisionTargetIssue, TargetID: "cgn", Relation: issues.DecisionRelationAppliesTo, Note: &noteImpl},
		{DecisionID: "dec-5", TargetKind: issues.DecisionTargetRequirement, TargetID: "cgn-req-1", Relation: issues.DecisionRelationInforms},
	}
	body := renderDecisionMarkdown(d, outgoing, nil)
	parsed, err := parseDecisionMarkdown(body)
	if err != nil {
		t.Fatalf("parse rendered output: %v", err)
	}
	if parsed.LocalID != d.LocalID || parsed.Title != d.Title {
		t.Errorf("id/title mismatch after round-trip")
	}
	if parsed.Rationale == nil || *parsed.Rationale != d.Rationale {
		t.Errorf("rationale lost in round-trip")
	}
	if parsed.Context == nil || *parsed.Context != d.Context {
		t.Errorf("context lost in round-trip")
	}
	if parsed.Consequences == nil || *parsed.Consequences != d.Consequences {
		t.Errorf("consequences lost in round-trip")
	}
	if len(parsed.Links) != 2 {
		t.Errorf("expected 2 links after round-trip, got %d", len(parsed.Links))
	}
}

func TestUpdateParamsFromChangesRoutesEachField(t *testing.T) {
	parsed := parsedDecisionMD{}
	changes := []protocol.DecisionImportMDFieldChange{
		{Field: "title", NewValue: "T"},
		{Field: "rationale", NewValue: "R"},
		{Field: "context", NewValue: "C"},
		{Field: "consequences", NewValue: "Q"},
	}
	params := updateParamsFromChanges(changes, parsed)
	if params.Title == nil || *params.Title != "T" {
		t.Errorf("title not threaded")
	}
	if params.Rationale == nil || *params.Rationale != "R" {
		t.Errorf("rationale not threaded")
	}
	if params.Context == nil || *params.Context != "C" {
		t.Errorf("context not threaded")
	}
	if params.Consequences == nil || *params.Consequences != "Q" {
		t.Errorf("consequences not threaded")
	}
}
