package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/stretchr/testify/require"
)

func TestRecoverHistoricalTaskClosePublicationConvergesAcrossDaemonsAfterSourceRemovalAndBaseAdvancement(t *testing.T) {
	ctx := context.Background()
	repoDir := historicalPublicationRepo(t)
	projectID := protocol.DefaultProjectID
	baseOID := runPublicationGit(t, repoDir, "rev-parse", "main~2")
	sourceOID := runPublicationGit(t, repoDir, "rev-parse", "main~1")
	currentOID := runPublicationGit(t, repoDir, "rev-parse", "main")
	require.NotEqual(t, sourceOID, currentOID)

	dbPath := filepath.Join(t.TempDir(), "issues.db")
	clients := []*issues.Client{
		newMigratedIssueClientAtPath(t, dbPath, slog.Default()),
		newMigratedIssueClientAtPath(t, dbPath, slog.Default()),
	}
	t.Cleanup(func() {
		for _, client := range clients {
			_ = client.CloseDB()
		}
	})
	issueID, err := clients[0].Create(ctx, issues.CreateTaskParams{Title: "historical publication", Type: domain.TypeBug, Status: domain.StatusInReview})
	require.NoError(t, err)
	validation := appendHistoricalValidation(t, ctx, clients[0], issueID, baseOID, sourceOID, "clean")
	review := appendHistoricalReview(t, ctx, clients[0], issueID, baseOID, sourceOID, "accepted")
	integration := taskCloseIntegrationResult{
		Requested: true, Integrated: true, ReceiptRecovered: true, ConfiguredBaseTarget: true, TargetID: "base",
		SourceBranch: "feature-removed", TargetBranch: "main", BaseOID: baseOID, SourceOID: sourceOID, TargetOID: sourceOID,
	}
	require.NoError(t, (&Daemon{issueClientsByProject: map[string]*issues.Client{projectID: clients[0]}}).persistTaskCloseIntegrationReceipt(ctx, projectID, issueID, repoDir, integration))

	daemons := []*Daemon{
		historicalPublicationDaemon(t, repoDir, projectID, clients[0]),
		historicalPublicationDaemon(t, repoDir, projectID, clients[1]),
	}
	ready := make(chan struct{}, len(daemons))
	start := make(chan struct{})
	errs := make(chan error, len(daemons))
	var wg sync.WaitGroup
	for _, d := range daemons {
		wg.Add(1)
		go func(d *Daemon) {
			defer wg.Done()
			ready <- struct{}{}
			<-start
			errs <- d.persistTaskCloseIntegrationPublication(ctx, projectID, issueID, repoDir, integration)
		}(d)
	}
	for range daemons {
		<-ready
	}
	close(start)
	wg.Wait()
	close(errs)
	for recoverErr := range errs {
		require.NoError(t, recoverErr)
	}
	receipts, err := clients[0].ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, receipts, 2, "concurrent recovery must append one historical binding")
	wantID := observationPayloadString(receipts[1].Payload, "historical_recovery_binding_id")
	require.NotEmpty(t, wantID)
	require.Equal(t, wantID, observationPayloadString(receipts[1].Payload, "historical_recovery_binding_id"))
	require.EqualValues(t, review.ID, receipts[1].Payload["historical_review_event_id"])
	require.EqualValues(t, validation.ID, receipts[1].Payload["historical_validation_event_id"])

	require.NoError(t, daemons[1].persistTaskCloseIntegrationPublication(ctx, projectID, issueID, repoDir, integration))
	receipts, err = clients[1].ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, receipts, 2, "idempotent replay must not append another binding")
}

func TestRecoverHistoricalTaskClosePublicationFailsClosed(t *testing.T) {
	ctx := context.Background()
	repoDir := historicalPublicationRepo(t)
	projectID := protocol.DefaultProjectID
	baseOID := runPublicationGit(t, repoDir, "rev-parse", "main~2")
	sourceOID := runPublicationGit(t, repoDir, "rev-parse", "main~1")
	runPublicationGit(t, repoDir, "checkout", "-b", "divergent", baseOID)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "divergent.txt"), []byte("divergent\n"), 0o644))
	runPublicationGit(t, repoDir, "add", "divergent.txt")
	runPublicationGit(t, repoDir, "commit", "-m", "divergent source")
	uncontainedOID := runPublicationGit(t, repoDir, "rev-parse", "HEAD")
	runPublicationGit(t, repoDir, "checkout", "main")
	runPublicationGit(t, repoDir, "branch", "-D", "divergent")

	for _, tc := range []struct {
		name       string
		review     string
		validation string
		mutate     func(*taskCloseIntegrationResult, *Daemon)
		after      func(*testing.T, *issues.Client, string)
	}{
		{name: "missing review", validation: "clean"},
		{name: "returned review", review: "returned", validation: "clean"},
		{name: "missing validation", review: "accepted"},
		{name: "failed validation", review: "accepted", validation: "failed"},
		{name: "retarget", review: "accepted", validation: "clean", mutate: func(_ *taskCloseIntegrationResult, d *Daemon) { d.cfg.BaseBranch = "release" }},
		{name: "typed target changed", review: "accepted", validation: "clean", mutate: func(integration *taskCloseIntegrationResult, _ *Daemon) { integration.TargetID = "ancestor" }},
		{name: "source not contained", review: "accepted", validation: "clean", mutate: func(integration *taskCloseIntegrationResult, _ *Daemon) { integration.SourceOID = uncontainedOID }},
		{name: "later returned review", review: "accepted", validation: "clean", after: func(t *testing.T, client *issues.Client, issueID string) {
			appendHistoricalReview(t, ctx, client, issueID, baseOID, sourceOID, "returned")
		}},
		{name: "later failed validation", review: "accepted", validation: "clean", after: func(t *testing.T, client *issues.Client, issueID string) {
			appendHistoricalValidation(t, ctx, client, issueID, baseOID, sourceOID, "failed")
		}},
		{name: "validation after acceptance", review: "accepted", validation: "clean", after: func(t *testing.T, client *issues.Client, issueID string) {
			appendHistoricalValidation(t, ctx, client, issueID, baseOID, sourceOID, "clean")
		}},
		{name: "daemon review authority exists", review: "accepted", validation: "clean", after: func(t *testing.T, client *issues.Client, issueID string) {
			_, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
				Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept",
				Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"},
			})
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
			t.Cleanup(func() { _ = client.CloseDB() })
			issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: tc.name, Type: domain.TypeBug, Status: domain.StatusInReview})
			require.NoError(t, err)
			if tc.validation != "" {
				appendHistoricalValidation(t, ctx, client, issueID, baseOID, sourceOID, tc.validation)
			}
			if tc.review != "" {
				appendHistoricalReview(t, ctx, client, issueID, baseOID, sourceOID, tc.review)
			}
			if tc.after != nil {
				tc.after(t, client, issueID)
			}
			integration := taskCloseIntegrationResult{Requested: true, Integrated: true, ReceiptRecovered: true, ConfiguredBaseTarget: true, TargetID: "base", SourceBranch: "feature-removed", TargetBranch: "main", BaseOID: baseOID, SourceOID: sourceOID, TargetOID: sourceOID}
			d := historicalPublicationDaemon(t, repoDir, projectID, client)
			if tc.mutate != nil {
				tc.mutate(&integration, d)
			}
			_, err = d.recoverHistoricalTaskClosePublication(ctx, projectID, issueID, repoDir, integration)
			require.Error(t, err)
		})
	}
}

func historicalPublicationRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	runPublicationGit(t, repoDir, "init", "-b", "main")
	runPublicationGit(t, repoDir, "config", "user.email", "test@example.com")
	runPublicationGit(t, repoDir, "config", "user.name", "Test")
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".azedarach", "config.json"), []byte("{\n  \"gate\": {\"command\": \"consumer verify\", \"environmentFingerprint\": \"consumer\"},\n  \"publicationEvidence\": {\"policyVersion\": \"consumer-v1\"}\n}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0o644))
	runPublicationGit(t, repoDir, "add", "base.txt")
	runPublicationGit(t, repoDir, "commit", "-m", "base")
	runPublicationGit(t, repoDir, "checkout", "-b", "feature-removed")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("feature\n"), 0o644))
	runPublicationGit(t, repoDir, "add", "feature.txt")
	runPublicationGit(t, repoDir, "commit", "-m", "feature")
	runPublicationGit(t, repoDir, "checkout", "main")
	runPublicationGit(t, repoDir, "merge", "--ff-only", "feature-removed")
	runPublicationGit(t, repoDir, "branch", "-D", "feature-removed")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "later.txt"), []byte("later\n"), 0o644))
	runPublicationGit(t, repoDir, "add", "later.txt")
	runPublicationGit(t, repoDir, "commit", "-m", "later base advancement")
	return repoDir
}

func historicalPublicationDaemon(t *testing.T, repoDir, projectID string, client *issues.Client) *Daemon {
	t.Helper()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir, logger: slog.Default()})
	t.Cleanup(func() { _ = runtime.Close() })
	return &Daemon{
		cfg:                   Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		git:                   gitservice.NewClient(gitservice.NewExecRunner(repoDir), slog.Default()),
		operationRuntime:      runtime,
		issueClientsByProject: map[string]*issues.Client{projectID: client},
		issueClientsByRoot:    map[string]*issues.Client{repoDir: client, daemonStoreRootKey(repoDir): client},
	}
}

func appendHistoricalValidation(t *testing.T, ctx context.Context, client *issues.Client, issueID, baseOID, candidateOID, result string) domain.IssueObservationEvent {
	t.Helper()
	event, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventHistoricalValidationCompleted, Source: "agent", SourceCommand: "az issue record",
		Payload: map[string]any{"base_revision": baseOID, "candidate_revision": candidateOID, "result": result},
	})
	require.NoError(t, err)
	return event
}

func appendHistoricalReview(t *testing.T, ctx context.Context, client *issues.Client, issueID, baseOID, candidateOID, result string) domain.IssueObservationEvent {
	t.Helper()
	eventType := domain.IssueEventHistoricalReviewAccepted
	if result != "accepted" {
		eventType = domain.IssueEventHistoricalReviewReturned
	}
	event, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: eventType, Source: "agent", SourceCommand: "az issue record",
		Payload: map[string]any{"base_revision": baseOID, "candidate_revision": candidateOID, "review_result": result},
	})
	require.NoError(t, err)
	return event
}
