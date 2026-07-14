package daemon

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestDecisionSyncIssueWorktreeDoesNotExportForeignDecisionAcrossRestart(t *testing.T) {
	ctx := context.Background()
	client, repoDir := newTestIssueClient(t)
	runDecisionGit(t, repoDir, "init")
	runDecisionGit(t, repoDir, "config", "user.name", "Decision Test")
	runDecisionGit(t, repoDir, "config", "user.email", "decision@example.com")
	if err := os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte(".azedarach/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDecisionGit(t, repoDir, "add", ".gitignore", "README.md")
	runDecisionGit(t, repoDir, "commit", "-m", "seed")

	issueA, err := client.Create(ctx, issues.CreateTaskParams{Title: "Issue A", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatalf("create issue A: %v", err)
	}
	issueB, err := client.Create(ctx, issues.CreateTaskParams{Title: "Issue B", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatalf("create issue B: %v", err)
	}
	decisionA, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "Owned by A", Rationale: "A rationale"})
	if err != nil {
		t.Fatal(err)
	}
	decisionB, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "Owned by B", Rationale: "B rationale"})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ decision, issue string }{{decisionA.LocalID, issueA}, {decisionB.LocalID, issueB}} {
		if _, err := client.AddDecisionLink(ctx, issues.AddDecisionLinkParams{DecisionID: pair.decision, TargetKind: issues.DecisionTargetIssue, TargetID: pair.issue, Relation: issues.DecisionRelationAppliesTo}); err != nil {
			t.Fatal(err)
		}
	}

	service := newTestIssueDecisionService(client, repoDir)
	service.daemon.git = gitservice.NewClient(gitservice.NewExecRunner(repoDir), nil)
	if _, err := service.SyncMD(ctx, protocol.DecisionSyncMDRequestBody{RepoDir: repoDir, FullProject: true}); err != nil {
		t.Fatalf("seed full sync: %v", err)
	}
	runDecisionGit(t, repoDir, "add", "docs/decisions")
	runDecisionGit(t, repoDir, "commit", "-m", "seed decisions")

	worktreeA := filepath.Join(t.TempDir(), "worktree-a")
	worktreeB := filepath.Join(t.TempDir(), "worktree-b")
	runDecisionGit(t, repoDir, "worktree", "add", "-b", "tester/"+issueA+"/decision-a", worktreeA)
	runDecisionGit(t, repoDir, "worktree", "add", "-b", "tester/"+issueB+"/decision-b", worktreeB)
	updated := "A changed after B branched"
	if _, err := client.UpdateDecision(ctx, decisionA.LocalID, issues.UpdateDecisionParams{Rationale: &updated}); err != nil {
		t.Fatal(err)
	}
	createdAfterBranch, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "Created by A after B branched", Rationale: "Must not appear in B"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AddDecisionLink(ctx, issues.AddDecisionLinkParams{DecisionID: createdAfterBranch.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: issueA, Relation: issues.DecisionRelationAppliesTo}); err != nil {
		t.Fatal(err)
	}
	revision, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "A revision", Rationale: "Also must not appear in B"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AddDecisionLink(ctx, issues.AddDecisionLinkParams{DecisionID: revision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: issueA, Relation: issues.DecisionRelationAppliesTo}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AddDecisionLink(ctx, issues.AddDecisionLinkParams{DecisionID: revision.LocalID, TargetKind: issues.DecisionTargetDecision, TargetID: decisionA.LocalID, Relation: issues.DecisionRelationRevises}); err != nil {
		t.Fatal(err)
	}

	result, err := service.SyncMD(ctx, protocol.DecisionSyncMDRequestBody{RepoDir: worktreeB})
	if err != nil {
		t.Fatalf("issue B sync: %v", err)
	}
	if result.TargetIssueID != issueB || result.TargetRevision == "" || result.FullProject {
		t.Fatalf("target = %+v", result)
	}
	if result.Changed {
		t.Fatalf("B sync changed foreign decision: %+v", result)
	}
	if !hasSkippedDecisionResult(result.Results, decisionA.LocalID) {
		t.Fatalf("foreign decision provenance not reported: %+v", result.Results)
	}
	assertDecisionWorktreeClean(t, worktreeB)
	for _, foreign := range []issues.Decision{createdAfterBranch, revision} {
		if _, err := os.Stat(filepath.Join(worktreeB, decisionMDSubdir, decisionMDFilename(foreign))); !os.IsNotExist(err) {
			t.Fatalf("foreign %s appeared in B: %v", foreign.LocalID, err)
		}
	}

	scratch := filepath.Join(t.TempDir(), "integration-scratch")
	runDecisionGit(t, repoDir, "worktree", "add", "--detach", scratch, "HEAD")
	if _, err := service.SyncMD(ctx, protocol.DecisionSyncMDRequestBody{RepoDir: scratch}); err == nil {
		t.Fatal("detached integration worktree accepted issue-scoped decision sync")
	}
	assertDecisionWorktreeClean(t, scratch)

	// Reconstruct the service to model daemon install/restart, mutate A again,
	// and prove the old review worktree remains byte-for-byte clean.
	secondClient := issues.NewClient(repoDir, nil)
	t.Cleanup(func() { _ = secondClient.CloseDB() })
	restarted := newTestIssueDecisionService(secondClient, repoDir)
	restarted.daemon.git = gitservice.NewClient(gitservice.NewExecRunner(repoDir), nil)
	updatedAgain := "A changed after daemon restart"
	if _, err := secondClient.UpdateDecision(ctx, decisionA.LocalID, issues.UpdateDecisionParams{Rationale: &updatedAgain}); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.SyncMD(ctx, protocol.DecisionSyncMDRequestBody{RepoDir: worktreeA}); err != nil {
		t.Fatalf("issue A sync after restart: %v", err)
	}
	assertDecisionWorktreeClean(t, worktreeB)
}

func TestDecisionSyncRejectsCheckoutBeforeFinalRevalidation(t *testing.T) {
	ctx, client, service, _, worktree, issueID, decision := newDecisionTransferBarrierFixture(t)
	service.daemon.decisionTransferBeforeRevalidation = func(operation string, _ decisionMDTransferTarget) {
		if operation == "sync_md" {
			runDecisionGit(t, worktree, "checkout", "--detach", "HEAD~1")
		}
	}

	if _, err := service.SyncMD(ctx, protocol.DecisionSyncMDRequestBody{RepoDir: worktree}); err == nil || !strings.Contains(err.Error(), "revalidate decision sync_md target") {
		t.Fatalf("sync after checkout error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktree, decisionMDSubdir, decisionMDFilename(decision))); !os.IsNotExist(err) {
		t.Fatalf("sync wrote after checkout: %v", err)
	}
	ownerLinks, err := client.ListDecisionLinks(ctx, issues.DecisionLinkFilter{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue})
	if err != nil || len(ownerLinks) != 1 || ownerLinks[0].TargetID != issueID {
		t.Fatalf("decision ownership changed: links=%+v err=%v", ownerLinks, err)
	}
}

func TestDecisionSyncHoldsOwnerAuthorityThroughReconciliation(t *testing.T) {
	ctx, client, service, _, worktree, issueID, decision := newDecisionTransferBarrierFixture(t)
	foreignIssue, err := client.Create(ctx, issues.CreateTaskParams{Title: "Transferred owner", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	transferStarted := make(chan struct{})
	transferEntered := make(chan struct{})
	transferDone := make(chan error, 1)
	service.daemon.decisionTransferBeforeRevalidation = func(operation string, _ decisionMDTransferTarget) {
		if operation != "sync_md" {
			return
		}
		go func() {
			close(transferStarted)
			transferDone <- client.WithMutationLock(ctx, func(transferCtx context.Context) error {
				close(transferEntered)
				if err := client.RemoveDecisionLink(transferCtx, decision.LocalID, issues.DecisionTargetIssue, issueID); err != nil {
					return err
				}
				_, err := client.AddDecisionLink(transferCtx, issues.AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: foreignIssue, Relation: issues.DecisionRelationAppliesTo})
				return err
			})
		}()
		<-transferStarted
		select {
		case <-transferEntered:
			t.Fatal("decision owner transfer entered before SyncMD reconciliation released authority")
		case <-time.After(150 * time.Millisecond):
		}
	}

	result, err := service.SyncMD(ctx, protocol.DecisionSyncMDRequestBody{RepoDir: worktree})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatalf("sync result = %+v, want authorized export", result)
	}
	select {
	case <-transferEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("owner transfer did not enter after SyncMD reconciliation")
	}
	select {
	case err := <-transferDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner transfer did not complete after SyncMD reconciliation")
	}
	links, err := client.ListDecisionLinks(ctx, issues.DecisionLinkFilter{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue})
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].TargetID != foreignIssue {
		t.Fatalf("decision owner after transfer = %+v", links)
	}
	if _, err := os.Stat(filepath.Join(worktree, decisionMDSubdir, decisionMDFilename(decision))); err != nil {
		t.Fatalf("authorized reconciliation did not finish before owner transfer: %v", err)
	}
}

func TestDecisionImportRejectsWorktreeRelinkBeforeFinalRevalidation(t *testing.T) {
	ctx, client, service, _, worktree, issueID, decision := newDecisionTransferBarrierFixture(t)
	foreignIssue, err := client.Create(ctx, issues.CreateTaskParams{Title: "Relinked issue", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(worktree, decisionMDSubdir, decisionMDFilename(decision))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	markdown := decision
	markdown.Rationale = "must not cross relink"
	if err := os.WriteFile(path, renderDecisionMarkdown(markdown, []issues.DecisionLink{{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: issueID, Relation: issues.DecisionRelationAppliesTo}}, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	service.daemon.decisionTransferBeforeRevalidation = func(operation string, _ decisionMDTransferTarget) {
		if operation == "import_md" {
			runDecisionGit(t, worktree, "branch", "-m", "tester/"+foreignIssue+"/relinked")
		}
	}

	if _, err := service.ImportMD(ctx, protocol.DecisionImportMDRequestBody{RepoDir: worktree, Force: true}); err == nil || !strings.Contains(err.Error(), "revalidate decision import_md target") {
		t.Fatalf("import after relink error = %v", err)
	}
	after, err := client.GetDecision(ctx, decision.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Rationale != decision.Rationale {
		t.Fatalf("import crossed relink: before=%q after=%q", decision.Rationale, after.Rationale)
	}
}

func TestDecisionImportRejectsCheckoutBeforeLinkOnlyMutation(t *testing.T) {
	ctx, client, service, repoDir, _, issueID, decision := newDecisionTransferBarrierFixture(t)
	secondIssue, err := client.Create(ctx, issues.CreateTaskParams{Title: "Second provenance", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	links := []issues.DecisionLink{
		{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: issueID, Relation: issues.DecisionRelationAppliesTo},
		{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: secondIssue, Relation: issues.DecisionRelationAppliesTo},
	}
	path := filepath.Join(repoDir, decisionMDSubdir, decisionMDFilename(decision))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, renderDecisionMarkdown(decision, links, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	service.daemon.decisionTransferBeforeRevalidation = func(operation string, _ decisionMDTransferTarget) {
		if operation == "import_md_apply" {
			runDecisionGit(t, repoDir, "checkout", "--detach", "HEAD~1")
		}
	}

	result, err := service.ImportMD(ctx, protocol.DecisionImportMDRequestBody{RepoDir: repoDir, FullProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || !strings.Contains(result.Files[0].ApplyError, "revalidate decision import target") {
		t.Fatalf("link-only import result = %+v", result)
	}
	afterLinks, err := client.ListDecisionLinks(ctx, issues.DecisionLinkFilter{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue})
	if err != nil {
		t.Fatal(err)
	}
	if len(afterLinks) != 1 || afterLinks[0].TargetID != issueID {
		t.Fatalf("link-only import crossed checkout: %+v", afterLinks)
	}
}

func TestDecisionSyncAndImportSerializeOnCanonicalTarget(t *testing.T) {
	ctx, _, service, _, worktree, _, _ := newDecisionTransferBarrierFixture(t)
	worktreeAlias := filepath.Join(t.TempDir(), "worktree-alias")
	if err := os.Symlink(worktree, worktreeAlias); err != nil {
		t.Fatal(err)
	}
	entered := make(chan string, 2)
	releaseSync := make(chan struct{})
	service.daemon.decisionTransferBeforeRevalidation = func(operation string, _ decisionMDTransferTarget) {
		entered <- operation
		if operation == "sync_md" {
			<-releaseSync
		}
	}

	syncDone := make(chan error, 1)
	go func() {
		_, err := service.SyncMD(ctx, protocol.DecisionSyncMDRequestBody{RepoDir: worktree})
		syncDone <- err
	}()
	if operation := <-entered; operation != "sync_md" {
		t.Fatalf("first transfer = %q, want sync_md", operation)
	}

	importDone := make(chan error, 1)
	go func() {
		_, err := service.ImportMD(ctx, protocol.DecisionImportMDRequestBody{RepoDir: worktreeAlias, Check: true})
		importDone <- err
	}()
	select {
	case operation := <-entered:
		t.Fatalf("concurrent transfer entered canonical barrier early: %s", operation)
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseSync)
	if err := <-syncDone; err != nil {
		t.Fatalf("serialized sync: %v", err)
	}
	if operation := <-entered; operation != "import_md" {
		t.Fatalf("second transfer = %q, want import_md", operation)
	}
	if err := <-importDone; err != nil {
		t.Fatalf("serialized import: %v", err)
	}
}

func newDecisionTransferBarrierFixture(t *testing.T) (context.Context, *issues.Client, issueDecisionService, string, string, string, issues.Decision) {
	t.Helper()
	ctx := context.Background()
	client, repoDir := newTestIssueClient(t)
	runDecisionGit(t, repoDir, "init")
	runDecisionGit(t, repoDir, "config", "user.name", "Decision Test")
	runDecisionGit(t, repoDir, "config", "user.email", "decision@example.com")
	if err := os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte(".azedarach/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDecisionGit(t, repoDir, "add", ".gitignore", "README.md")
	runDecisionGit(t, repoDir, "commit", "-m", "seed")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("second revision\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDecisionGit(t, repoDir, "add", "README.md")
	runDecisionGit(t, repoDir, "commit", "-m", "second")

	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Transfer owner", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "Barrier decision", Rationale: "durable rationale"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AddDecisionLink(ctx, issues.AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: issueID, Relation: issues.DecisionRelationAppliesTo}); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "issue-worktree")
	runDecisionGit(t, repoDir, "worktree", "add", "-b", "tester/"+issueID+"/transfer", worktree)
	service := newTestIssueDecisionService(client, repoDir)
	service.daemon.git = gitservice.NewClient(gitservice.NewExecRunner(repoDir), nil)
	return ctx, client, service, repoDir, worktree, issueID, decision
}

func TestDecisionOwnerRequiresExactlyOneIssue(t *testing.T) {
	if got := decisionOwnerIssueID(nil); got != "" {
		t.Fatalf("unowned owner = %q", got)
	}
	if got := decisionOwnerIssueID([]string{"dha"}); got != "dha" {
		t.Fatalf("single owner = %q", got)
	}
	if got := decisionOwnerIssueID([]string{"dha", "dgv"}); got != "" {
		t.Fatalf("ambiguous owner = %q", got)
	}
}

func TestDecisionDeletedOwnershipUsesLinksDeletedWithDecision(t *testing.T) {
	oldDeletion := time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)
	decisionDeletion := oldDeletion.Add(time.Hour)
	decision := issues.Decision{LocalID: "dec-8", DeletedAt: &decisionDeletion}
	links := []issues.DecisionLink{
		{TargetKind: issues.DecisionTargetIssue, TargetID: "old-owner", DeletedAt: &oldDeletion},
		{TargetKind: issues.DecisionTargetIssue, TargetID: "current-owner", DeletedAt: &decisionDeletion},
	}
	if got := decisionIssueIDsAtDecisionState(decision, links); !reflect.DeepEqual(got, []string{"current-owner"}) {
		t.Fatalf("deleted ownership = %v", got)
	}
}

func hasSkippedDecisionResult(results []protocol.DecisionMDFileResult, decisionID string) bool {
	for _, result := range results {
		if result.DecisionID == decisionID && result.Skipped {
			return true
		}
	}
	return false
}

func assertDecisionWorktreeClean(t *testing.T, worktree string) {
	t.Helper()
	if output := runDecisionGit(t, worktree, "status", "--porcelain", "--", "docs/decisions"); strings.TrimSpace(output) != "" {
		t.Fatalf("decision worktree became dirty: %s", output)
	}
}

func runDecisionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

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
	syncResult, err := sourceService.SyncMD(ctx, protocol.DecisionSyncMDRequestBody{RepoDir: sourceRepo, FullProject: true})
	if err != nil {
		t.Fatalf("explicit sync: %v", err)
	}
	if !syncResult.Changed || len(syncResult.Files) != 1 {
		t.Fatalf("sync result = %+v, want one exported file", syncResult)
	}

	targetClient, _ := newTestIssueClient(t)
	targetService := newTestIssueDecisionService(targetClient, sourceRepo)
	checkResult, err := targetService.ImportMD(ctx, protocol.DecisionImportMDRequestBody{Check: true, RepoDir: sourceRepo, FullProject: true})
	if err != nil {
		t.Fatalf("explicit import check: %v", err)
	}
	if len(checkResult.Files) != 1 || !checkResult.Files[0].NewRecord || checkResult.Imported != 0 {
		t.Fatalf("import check result = %+v, want one planned new record", checkResult)
	}
	importResult, err := targetService.ImportMD(ctx, protocol.DecisionImportMDRequestBody{RepoDir: sourceRepo, FullProject: true})
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

func TestDecisionImportPreservesForeignIssueArtifact(t *testing.T) {
	ctx := context.Background()
	client, repoDir := newTestIssueClient(t)
	service := newTestIssueDecisionService(client, repoDir)
	decision := issues.Decision{LocalID: "dec-41", Title: "Foreign", Rationale: "Owned elsewhere", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	links := []issues.DecisionLink{{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: "dgv", Relation: issues.DecisionRelationAppliesTo}}
	path := filepath.Join(repoDir, decisionMDSubdir, decisionMDFilename(decision))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, renderDecisionMarkdown(decision, links, nil), 0o644); err != nil {
		t.Fatal(err)
	}

	result := service.importOneDecisionFile(ctx, client, decisionMDTransferTarget{RepoDir: repoDir, IssueID: "dha"}, path, protocol.DecisionImportMDRequestBody{})
	if !result.Skipped || result.SkipReason == "" || !reflect.DeepEqual(result.IssueIDs, []string{"dgv"}) {
		t.Fatalf("foreign import result = %+v", result)
	}
	if _, err := client.GetDecision(ctx, decision.LocalID); err == nil {
		t.Fatal("foreign decision was imported")
	}
}

func TestDecisionImportExistingForeignDurableOwnerNeverMutates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		force bool
		edit  func(*issues.Decision)
	}{
		{name: "no-op"},
		{name: "clean change", edit: func(d *issues.Decision) { d.Consequences = "markdown-only consequence" }},
		{name: "forced conflict", force: true, edit: func(d *issues.Decision) { d.Rationale = "foreign markdown rationale" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client, repoDir := newTestIssueClient(t)
			localIssue, err := client.Create(ctx, issues.CreateTaskParams{Title: "Local", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
			if err != nil {
				t.Fatal(err)
			}
			foreignIssue, err := client.Create(ctx, issues.CreateTaskParams{Title: "Foreign", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "Durably foreign", Rationale: "durable rationale"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.AddDecisionLink(ctx, issues.AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: foreignIssue, Relation: issues.DecisionRelationAppliesTo}); err != nil {
				t.Fatal(err)
			}

			markdownDecision := decision
			if tc.edit != nil {
				tc.edit(&markdownDecision)
			}
			claimedLocal := []issues.DecisionLink{{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: localIssue, Relation: issues.DecisionRelationAppliesTo}}
			path := filepath.Join(repoDir, decisionMDSubdir, decisionMDFilename(markdownDecision))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, renderDecisionMarkdown(markdownDecision, claimedLocal, nil), 0o644); err != nil {
				t.Fatal(err)
			}

			service := newTestIssueDecisionService(client, repoDir)
			result := service.importOneDecisionFile(ctx, client, decisionMDTransferTarget{RepoDir: repoDir, IssueID: localIssue}, path, protocol.DecisionImportMDRequestBody{Force: tc.force})
			if !result.Skipped || !strings.Contains(result.SkipReason, "durable decision owner") || result.Imported || result.ApplyError != "" {
				t.Fatalf("foreign-owner import result = %+v", result)
			}
			after, err := client.GetDecision(ctx, decision.LocalID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Title != decision.Title || after.Rationale != decision.Rationale || after.Context != decision.Context || after.Consequences != decision.Consequences {
				t.Fatalf("foreign decision mutated: before=%+v after=%+v", decision, after)
			}
			links, err := client.ListDecisionLinks(ctx, issues.DecisionLinkFilter{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue})
			if err != nil {
				t.Fatal(err)
			}
			if len(links) != 1 || links[0].TargetID != foreignIssue {
				t.Fatalf("foreign ownership mutated: %+v", links)
			}
		})
	}
}

func TestDecisionImportFullProjectUpdatesExistingAndImportsProvenance(t *testing.T) {
	ctx := context.Background()
	client, repoDir := newTestIssueClient(t)
	firstIssue, err := client.Create(ctx, issues.CreateTaskParams{Title: "First owner", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	secondIssue, err := client.Create(ctx, issues.CreateTaskParams{Title: "Second owner", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	service := newTestIssueDecisionService(client, repoDir)

	writeImport := func(t *testing.T, decision issues.Decision, rationale string) string {
		t.Helper()
		markdown := decision
		markdown.Rationale = rationale
		links := []issues.DecisionLink{
			{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: firstIssue, Relation: issues.DecisionRelationAppliesTo},
			{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: secondIssue, Relation: issues.DecisionRelationAppliesTo},
		}
		path := filepath.Join(repoDir, decisionMDSubdir, decisionMDFilename(markdown))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, renderDecisionMarkdown(markdown, links, nil), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("field update", func(t *testing.T) {
		decision, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "Existing full-project decision", Rationale: "durable rationale"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.AddDecisionLink(ctx, issues.AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: firstIssue, Relation: issues.DecisionRelationAppliesTo}); err != nil {
			t.Fatal(err)
		}
		path := writeImport(t, decision, "markdown rationale")

		result := service.importOneDecisionFile(ctx, client, decisionMDTransferTarget{RepoDir: repoDir, FullProject: true}, path, protocol.DecisionImportMDRequestBody{Force: true, FullProject: true})
		if !result.Imported || result.ApplyError != "" {
			t.Fatalf("full-project update result = %+v", result)
		}
		updated, err := client.GetDecision(ctx, decision.LocalID)
		if err != nil {
			t.Fatal(err)
		}
		if updated.Rationale != "markdown rationale" {
			t.Fatalf("updated rationale = %q", updated.Rationale)
		}
		links, err := client.ListDecisionLinks(ctx, issues.DecisionLinkFilter{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue})
		if err != nil || len(links) != 2 {
			t.Fatalf("full-project update links = %+v err=%v", links, err)
		}
	})

	t.Run("link only", func(t *testing.T) {
		decision, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "Existing link-only decision", Rationale: "unchanged rationale"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.AddDecisionLink(ctx, issues.AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: firstIssue, Relation: issues.DecisionRelationAppliesTo}); err != nil {
			t.Fatal(err)
		}
		path := writeImport(t, decision, decision.Rationale)

		result := service.importOneDecisionFile(ctx, client, decisionMDTransferTarget{RepoDir: repoDir, FullProject: true}, path, protocol.DecisionImportMDRequestBody{FullProject: true})
		if !result.Imported || result.ApplyError != "" || len(result.Changes) != 0 {
			t.Fatalf("full-project link-only result = %+v", result)
		}
		links, err := client.ListDecisionLinks(ctx, issues.DecisionLinkFilter{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue})
		if err != nil || len(links) != 2 {
			t.Fatalf("full-project link-only links = %+v err=%v", links, err)
		}
	})
}

func TestDecisionImportPersistsIssueProvenance(t *testing.T) {
	ctx := context.Background()
	client, repoDir := newTestIssueClient(t)
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Owner", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	service := newTestIssueDecisionService(client, repoDir)
	decision := issues.Decision{LocalID: "dec-42", Title: "Owned import", Rationale: "Keep provenance", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	links := []issues.DecisionLink{{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: issueID, Relation: issues.DecisionRelationAppliesTo}}
	path := filepath.Join(repoDir, decisionMDSubdir, decisionMDFilename(decision))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, renderDecisionMarkdown(decision, links, nil), 0o644); err != nil {
		t.Fatal(err)
	}

	result := service.importOneDecisionFile(ctx, client, decisionMDTransferTarget{RepoDir: repoDir, IssueID: issueID}, path, protocol.DecisionImportMDRequestBody{})
	if !result.Imported || result.ApplyError != "" {
		t.Fatalf("import result = %+v", result)
	}
	persisted, err := client.ListDecisionLinks(ctx, issues.DecisionLinkFilter{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue})
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0].TargetID != issueID {
		t.Fatalf("persisted provenance = %+v", persisted)
	}

	second := service.importOneDecisionFile(ctx, client, decisionMDTransferTarget{RepoDir: repoDir, IssueID: issueID}, path, protocol.DecisionImportMDRequestBody{})
	if second.Imported || second.ApplyError != "" {
		t.Fatalf("idempotent import result = %+v", second)
	}
}

func TestDecisionSyncDoesNotRenderDeletedIssueProvenance(t *testing.T) {
	ctx := context.Background()
	client, repoDir := newTestIssueClient(t)
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Former owner", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "Unlinked", Rationale: "Link was removed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AddDecisionLink(ctx, issues.AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: issueID, Relation: issues.DecisionRelationAppliesTo}); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveDecisionLink(ctx, decision.LocalID, issues.DecisionTargetIssue, issueID); err != nil {
		t.Fatal(err)
	}

	service := newTestIssueDecisionService(client, repoDir)
	if _, err := service.SyncMD(ctx, protocol.DecisionSyncMDRequestBody{RepoDir: repoDir, FullProject: true}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(repoDir, decisionMDSubdir, decisionMDFilename(decision)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "issue:"+issueID) {
		t.Fatalf("deleted provenance was rendered:\n%s", body)
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

func TestReconcileDecisionMarkdownEquivalentWorktreesProduceIdenticalRenames(t *testing.T) {
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	oldDecision := issues.Decision{LocalID: "dec-13", Title: "One signed semantic event sequence and derived views", CreatedAt: now, UpdatedAt: now}
	newDecision := issues.Decision{LocalID: "dec-13", Title: "One signed semantic event sequence per authority", CreatedAt: now, UpdatedAt: now}
	exports := []decisionMDExport{{
		Decision: newDecision,
		Body:     renderDecisionMarkdown(newDecision, nil, nil),
	}}

	reconcile := func(repoDir string) ([]string, []byte) {
		t.Helper()
		targetDir := filepath.Join(repoDir, decisionMDSubdir)
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			t.Fatalf("mkdir decisions: %v", err)
		}
		oldPath := filepath.Join(targetDir, decisionMDFilename(oldDecision))
		if err := os.WriteFile(oldPath, renderDecisionMarkdown(oldDecision, nil, nil), 0o644); err != nil {
			t.Fatalf("write old decision: %v", err)
		}
		changed, err := reconcileDecisionMarkdown(repoDir, exports, false)
		if err != nil {
			t.Fatalf("reconcile decision markdown: %v", err)
		}
		body, err := os.ReadFile(filepath.Join(targetDir, decisionMDFilename(newDecision)))
		if err != nil {
			t.Fatalf("read canonical decision: %v", err)
		}
		return changed, body
	}

	changesA, bodyA := reconcile(t.TempDir())
	changesB, bodyB := reconcile(t.TempDir())
	if !reflect.DeepEqual(changesA, changesB) {
		t.Fatalf("equivalent worktree changes differ: A=%v B=%v", changesA, changesB)
	}
	if !bytes.Equal(bodyA, bodyB) {
		t.Fatalf("equivalent worktree exports differ:\nA:\n%s\nB:\n%s", bodyA, bodyB)
	}
	wantChanges := []string{
		"docs/decisions/dec-13-one-signed-semantic-event-sequence-and-derived-vie.md",
		"docs/decisions/dec-13-one-signed-semantic-event-sequence-per-authority.md",
	}
	if !reflect.DeepEqual(changesA, wantChanges) {
		t.Fatalf("rename set = %v, want %v", changesA, wantChanges)
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
