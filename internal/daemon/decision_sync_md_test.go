package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestDecisionSyncImportExplicitRoundTrip(t *testing.T) {
	ctx := context.Background()
	sourceClient, sourceRepo := newTestIssueClient(t)
	sourceService := newTestIssueDecisionService(sourceClient, sourceRepo)
	created, err := sourceClient.RecordDecision(ctx, issues.RecordDecisionParams{
		Title:     "Exchange explicitly",
		Rationale: "Decision Markdown crosses Git only after an explicit export.",
	})
	if err != nil {
		t.Fatalf("record source decision: %v", err)
	}
	syncResult, err := sourceService.SyncMD(ctx, protocol.DecisionSyncMDRequestBody{RepoDir: sourceRepo})
	if err != nil {
		t.Fatalf("explicit sync: %v", err)
	}
	if !syncResult.Changed || len(syncResult.Files) != 1 {
		t.Fatalf("sync result = %+v, want one exported file", syncResult)
	}

	targetClient, targetRepo := newTestIssueClient(t)
	targetService := newTestIssueDecisionService(targetClient, targetRepo)
	checkResult, err := targetService.ImportMD(ctx, protocol.DecisionImportMDRequestBody{Check: true, RepoDir: sourceRepo})
	if err != nil {
		t.Fatalf("explicit import check: %v", err)
	}
	if len(checkResult.Files) != 1 || !checkResult.Files[0].NewRecord || checkResult.Imported != 0 {
		t.Fatalf("import check result = %+v, want one planned new record", checkResult)
	}
	importResult, err := targetService.ImportMD(ctx, protocol.DecisionImportMDRequestBody{RepoDir: sourceRepo})
	if err != nil {
		t.Fatalf("explicit import: %v", err)
	}
	if importResult.Imported != 1 || importResult.Conflicts != 0 {
		t.Fatalf("import result = %+v, want one clean import", importResult)
	}
	imported, err := targetClient.GetDecision(ctx, created.LocalID)
	if err != nil {
		t.Fatalf("get imported decision: %v", err)
	}
	if imported.Title != created.Title || imported.Rationale != created.Rationale {
		t.Fatalf("imported decision = %+v, want title/rationale from %+v", imported, created)
	}
}

func newTestIssueDecisionService(client *issues.Client, repoDir string) issueDecisionService {
	return issueDecisionService{daemon: &Daemon{
		cfg:    Config{RepoDir: repoDir},
		issues: client,
		issueClientsByProject: map[string]*issues.Client{
			protocol.DefaultProjectID: client,
		},
		issueClientsByRoot: map[string]*issues.Client{
			repoDir: client,
		},
	}}
}

func TestReconcileDecisionMarkdownFullSyncRenamesAndDeletes(t *testing.T) {
	repoDir := t.TempDir()
	targetDir := filepath.Join(repoDir, decisionMDSubdir)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir decisions: %v", err)
	}

	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	oldDecision := issues.Decision{LocalID: "dec-1", Title: "Old title", CreatedAt: now, UpdatedAt: now}
	newDecision := issues.Decision{LocalID: "dec-1", Title: "New title", CreatedAt: now, UpdatedAt: now}
	addedDecision := issues.Decision{LocalID: "dec-3", Title: "Added", CreatedAt: now, UpdatedAt: now}
	deletedDecision := issues.Decision{LocalID: "dec-2", Title: "Deleted", CreatedAt: now, UpdatedAt: now}

	write := func(name string, body []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(targetDir, name), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write(decisionMDFilename(oldDecision), []byte("partially edited malformed decision\n"))
	write("dec-1-duplicate.md", renderDecisionMarkdown(oldDecision, nil, nil))
	write(decisionMDFilename(deletedDecision), renderDecisionMarkdown(deletedDecision, nil, nil))
	write("notes.md", []byte("human notes; not a decision export\n"))

	exports := []decisionMDExport{
		{Decision: newDecision, Body: renderDecisionMarkdown(newDecision, nil, nil)},
		{Decision: addedDecision, Body: renderDecisionMarkdown(addedDecision, nil, nil)},
	}
	wantChanged := []string{
		"docs/decisions/dec-1-duplicate.md",
		"docs/decisions/dec-1-new-title.md",
		"docs/decisions/dec-1-old-title.md",
		"docs/decisions/dec-2-deleted.md",
		"docs/decisions/dec-3-added.md",
	}

	checked, err := reconcileDecisionMarkdown(repoDir, exports, true)
	if err != nil {
		t.Fatalf("check reconciliation: %v", err)
	}
	if !reflect.DeepEqual(checked, wantChanged) {
		t.Fatalf("check changes = %v, want %v", checked, wantChanged)
	}
	if _, err := os.Stat(filepath.Join(targetDir, decisionMDFilename(oldDecision))); err != nil {
		t.Fatalf("check mode mutated old path: %v", err)
	}

	changed, err := reconcileDecisionMarkdown(repoDir, exports, false)
	if err != nil {
		t.Fatalf("apply reconciliation: %v", err)
	}
	if !reflect.DeepEqual(changed, wantChanged) {
		t.Fatalf("apply changes = %v, want %v", changed, wantChanged)
	}
	for _, obsolete := range []string{decisionMDFilename(oldDecision), "dec-1-duplicate.md", decisionMDFilename(deletedDecision)} {
		if _, err := os.Stat(filepath.Join(targetDir, obsolete)); !os.IsNotExist(err) {
			t.Fatalf("obsolete %s still exists: %v", obsolete, err)
		}
	}
	for _, live := range []issues.Decision{newDecision, addedDecision} {
		body, err := os.ReadFile(filepath.Join(targetDir, decisionMDFilename(live)))
		if err != nil {
			t.Fatalf("read canonical %s: %v", live.LocalID, err)
		}
		if got, want := string(body), string(renderDecisionMarkdown(live, nil, nil)); got != want {
			t.Fatalf("canonical %s content mismatch\ngot:\n%s\nwant:\n%s", live.LocalID, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(targetDir, "notes.md")); err != nil {
		t.Fatalf("non-decision markdown was removed: %v", err)
	}

	unchanged, err := reconcileDecisionMarkdown(repoDir, exports, false)
	if err != nil {
		t.Fatalf("idempotent reconciliation: %v", err)
	}
	if len(unchanged) != 0 {
		t.Fatalf("idempotent changes = %v, want none", unchanged)
	}
}

func TestIsDecisionMDFilename(t *testing.T) {
	for _, name := range []string{"dec-1.md", "dec-42-title.md"} {
		if !isDecisionMDFilename(name) {
			t.Errorf("isDecisionMDFilename(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"notes.md", "dec-x-title.md", "dec-.md", "dec-1.txt"} {
		if isDecisionMDFilename(name) {
			t.Errorf("isDecisionMDFilename(%q) = true, want false", name)
		}
	}
}

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
	if !strings.HasSuffix(body, "\n") {
		t.Fatalf("rendered markdown must end with one newline")
	}
	if strings.HasSuffix(body, "\n\n") {
		t.Fatalf("rendered markdown has blank line at EOF")
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
