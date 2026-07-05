package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestSlugifyDecisionTitle(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"   ":                          "",
		"Use SQLite":                   "use-sqlite",
		"Polymorphic / link table":     "polymorphic-link-table",
		"???":                          "",
		"  --leading and trailing--  ": "leading-and-trailing",
		"UPPER/case":                   "upper-case",
		"a b c":                        "a-b-c",
	}
	for input, want := range cases {
		got := slugifyDecisionTitle(input)
		if got != want {
			t.Errorf("slugifyDecisionTitle(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDecisionMDFilename(t *testing.T) {
	d := issues.Decision{LocalID: "dec-7", Title: "Use SQLite for the decision store"}
	got := decisionMDFilename(d)
	want := "dec-7-use-sqlite-for-the-decision-store.md"
	if got != want {
		t.Errorf("filename = %q, want %q", got, want)
	}

	// Edge case: empty title falls back to id-only.
	d2 := issues.Decision{LocalID: "dec-8", Title: ""}
	if got := decisionMDFilename(d2); got != "dec-8.md" {
		t.Errorf("empty-title filename = %q, want dec-8.md", got)
	}
}

func TestDecisionMDRepoDirPrefersRequestRepoDir(t *testing.T) {
	got, err := decisionMDRepoDir("/repo/root", "/repo/worktree")
	if err != nil {
		t.Fatalf("decisionMDRepoDir error: %v", err)
	}
	if got != "/repo/worktree" {
		t.Fatalf("decisionMDRepoDir = %q, want request repo", got)
	}

	got, err = decisionMDRepoDir("/repo/root", " ")
	if err != nil {
		t.Fatalf("decisionMDRepoDir fallback error: %v", err)
	}
	if got != "/repo/root" {
		t.Fatalf("decisionMDRepoDir fallback = %q, want root", got)
	}
}

func TestDecisionMDRepoDirRequiresTarget(t *testing.T) {
	if _, err := decisionMDRepoDir(" ", " "); err == nil {
		t.Fatal("decisionMDRepoDir with no target error = nil, want error")
	}
}

func TestDecisionMDRepoDirRejectsRelativeTarget(t *testing.T) {
	if _, err := decisionMDRepoDir("/repo/root", "relative/repo"); err == nil {
		t.Fatal("decisionMDRepoDir with relative target error = nil, want error")
	}
}

func TestRenderDecisionMarkdownContainsAllSections(t *testing.T) {
	d := issues.Decision{
		LocalID:      "dec-1",
		Title:        "Use SQLite for the decision store",
		Rationale:    "Reuse existing schema; new datastore not worth the operational cost.",
		Context:      "Need durable local storage for decisions.",
		Consequences: "Decisions colocated with issues + spec_requirements.",
		CreatedAt:    time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC),
	}
	noteImpl := "primary implementation path"
	outgoing := []issues.DecisionLink{
		{
			DecisionID: "dec-1",
			TargetKind: issues.DecisionTargetIssue,
			TargetID:   "cgn",
			Relation:   issues.DecisionRelationAppliesTo,
			Note:       &noteImpl,
		},
	}
	body := string(renderDecisionMarkdown(d, outgoing, nil))

	for _, want := range []string{
		"# dec-1: Use SQLite for the decision store",
		"Created: 2026-05-01",
		"Updated: 2026-05-02",
		"## Rationale",
		"## Context",
		"## Consequences",
		"## Links",
		"applies-to issue:cgn — primary implementation path",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered markdown missing %q\n---\n%s", want, body)
		}
	}
}

func TestRenderDecisionMarkdownIncludesRevisedByHeader(t *testing.T) {
	d := issues.Decision{
		LocalID:   "dec-1",
		Title:     "Use SQLite",
		Rationale: "Initial pick.",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	incoming := []issues.DecisionLink{
		{DecisionID: "dec-7", TargetKind: issues.DecisionTargetDecision, TargetID: "dec-1", Relation: issues.DecisionRelationRevises},
	}
	body := string(renderDecisionMarkdown(d, nil, incoming))
	if !strings.Contains(body, "Revised by: dec-7") {
		t.Errorf("missing revised-by header: %s", body)
	}
	// The revises link is surfaced as a header, NOT duplicated in the Links section.
	if strings.Contains(body, "(incoming) revises decision:dec-7") {
		t.Errorf("revises link should not also appear under Links: %s", body)
	}
}

func TestRenderDecisionMarkdownIsDeterministic(t *testing.T) {
	d := issues.Decision{
		LocalID:   "dec-1",
		Title:     "x",
		Rationale: "y",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	// Build two link slices in different orders; the rendered output should be identical.
	a := []issues.DecisionLink{
		{DecisionID: "dec-1", TargetKind: issues.DecisionTargetIssue, TargetID: "az-2", Relation: issues.DecisionRelationAppliesTo},
		{DecisionID: "dec-1", TargetKind: issues.DecisionTargetIssue, TargetID: "az-1", Relation: issues.DecisionRelationAppliesTo},
	}
	b := []issues.DecisionLink{
		{DecisionID: "dec-1", TargetKind: issues.DecisionTargetIssue, TargetID: "az-1", Relation: issues.DecisionRelationAppliesTo},
		{DecisionID: "dec-1", TargetKind: issues.DecisionTargetIssue, TargetID: "az-2", Relation: issues.DecisionRelationAppliesTo},
	}
	if string(renderDecisionMarkdown(d, a, nil)) != string(renderDecisionMarkdown(d, b, nil)) {
		t.Errorf("rendering not deterministic across link order")
	}
}
