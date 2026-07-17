package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	operationstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestPublicationQueueSerializesTargetAndCoalescesManagerWork(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	merged := make(chan string, 2)
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	d.publicationClose = func(_ context.Context, operation domain.PublicationOperation) error {
		if operation.OperationID == "publication-1" {
			close(firstStarted)
			<-releaseFirst
		} else {
			close(secondStarted)
		}
		return nil
	}
	d.publicationStateChanged = func(operation domain.PublicationOperation) {
		if operation.State == domain.PublicationOperationMerged {
			merged <- operation.OperationID
		}
	}
	projectID := runtime.canonicalProject
	first := daemonTestPublicationOperation(projectID, "publication-1", "issue-1", "intent-1", "source-1", time.Now().UTC())
	second := daemonTestPublicationOperation(projectID, "publication-2", "issue-2", "intent-2", "source-2", time.Now().UTC().Add(time.Second))
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), first, "candidate-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), second, "candidate-2"); err != nil {
		t.Fatal(err)
	}
	if err := d.submitPublicationOperation(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := d.submitPublicationOperation(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	<-firstStarted
	select {
	case <-secondStarted:
		t.Fatal("second publication started while target resource was occupied")
	default:
	}
	close(releaseFirst)
	<-secondStarted
	seen := map[string]bool{<-merged: true, <-merged: true}
	if !seen[first.OperationID] || !seen[second.OperationID] {
		t.Fatalf("merged operations = %v", seen)
	}
}

func TestPublicationQueueFailureIsTypedAndRetainsArtifact(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	d.publicationClose = func(context.Context, domain.PublicationOperation) error {
		return errors.New("candidate validation failed: npm test: unit suite failed")
	}
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-failed", "issue", "intent", "source", time.Now().UTC())
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), operation, "candidate-failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.runPublicationOperation(context.Background(), operation.ProjectID, operation.OperationID); err == nil {
		t.Fatal("failed publication returned success")
	}
	failed, found, err := runtime.store.PublicationOperation(context.Background(), operation.OperationID)
	if err != nil || !found {
		t.Fatalf("failed operation = (%+v,%t,%v)", failed, found, err)
	}
	if failed.State != domain.PublicationOperationFailed || failed.FailureKind != "validation_or_apply_failed" || failed.FailureArtifact == "" {
		t.Fatalf("failed operation = %+v", failed)
	}
	if _, err := os.Stat(failed.FailureArtifact); err != nil {
		t.Fatalf("retained failure artifact: %v", err)
	}
}

func TestPublicationQueueRejectsStaleIdentityBeforeValidation(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	closeCalled := false
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	d.publicationIdentityCheck = func(context.Context, domain.PublicationOperation) error {
		return errors.New("publication identity stale: base revision changed from base-a to base-b")
	}
	d.publicationClose = func(context.Context, domain.PublicationOperation) error {
		closeCalled = true
		return nil
	}
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-stale", "issue", "intent", "source", time.Now().UTC())
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), operation, "candidate-stale"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.runPublicationOperation(context.Background(), operation.ProjectID, operation.OperationID); err == nil {
		t.Fatal("stale publication returned success")
	}
	stale, found, err := runtime.store.PublicationOperation(context.Background(), operation.OperationID)
	if err != nil || !found || stale.State != domain.PublicationOperationStale || stale.FailureKind != "identity_changed" || stale.FailureArtifact == "" {
		t.Fatalf("stale publication = (%+v,%t,%v)", stale, found, err)
	}
	if closeCalled {
		t.Fatal("stale publication reached validation/apply")
	}
}

func TestPublicationQueueAutomaticallyRecomputesChangedBaseAttempt(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	merged := make(chan domain.PublicationOperation, 1)
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	d.publicationIdentityCheck = func(_ context.Context, operation domain.PublicationOperation) error {
		if operation.OperationID != "publication-refresh-base" {
			return nil
		}
		replacement := refreshedPublicationOperationAttempt(operation, "base-b", operation.ValidationCommand, operation.PolicyVersion, operation.EnvironmentFingerprint)
		return &publicationRetryError{cause: errors.New("publication identity stale: base revision changed from base to base-b"), replacement: replacement}
	}
	d.publicationClose = func(context.Context, domain.PublicationOperation) error { return nil }
	d.publicationStateChanged = func(operation domain.PublicationOperation) {
		if operation.State == domain.PublicationOperationMerged {
			merged <- operation
		}
	}
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-refresh-base", "issue", "intent", "source", time.Now().UTC())
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), operation, "candidate-refresh-base"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.runPublicationOperation(context.Background(), operation.ProjectID, operation.OperationID); err == nil {
		t.Fatal("changed-base attempt returned success instead of recording stale predecessor")
	}
	replacement := <-merged
	if replacement.OperationID == operation.OperationID || replacement.BaseRevision != "base-b" || !strings.Contains(replacement.IntentKey, ":publication-retry:") {
		t.Fatalf("replacement publication = %+v", replacement)
	}
	stale, found, err := runtime.store.PublicationOperation(context.Background(), operation.OperationID)
	if err != nil || !found || stale.State != domain.PublicationOperationStale {
		t.Fatalf("stale predecessor = (%+v,%t,%v)", stale, found, err)
	}
}

func TestPublicationQueueRecoveryResubmitsNonterminalIntent(t *testing.T) {
	for _, crashState := range []domain.PublicationOperationState{
		domain.PublicationOperationQueued,
		domain.PublicationOperationPreparing,
		domain.PublicationOperationValidating,
		domain.PublicationOperationPassed,
	} {
		t.Run(string(crashState), func(t *testing.T) {
			repo := t.TempDir()
			firstRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
			operation := daemonTestPublicationOperation(firstRuntime.canonicalProject, "publication-recover", "issue", "intent", "source", time.Now().UTC())
			stored, _, err := firstRuntime.store.EnqueuePublication(context.Background(), operation, "candidate-recover")
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now().UTC()
			for _, next := range []domain.PublicationOperationState{domain.PublicationOperationPreparing, domain.PublicationOperationValidating, domain.PublicationOperationPassed} {
				if stored.State == crashState {
					break
				}
				stored, err = firstRuntime.store.UpdatePublicationOperation(context.Background(), stored.OperationID, operationPublicationUpdate(stored.State, next, &started))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := firstRuntime.Close(); err != nil {
				t.Fatal(err)
			}

			restarted := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
			t.Cleanup(func() { _ = restarted.Close() })
			merged := make(chan struct{})
			d := &Daemon{operationRuntime: restarted, cfg: Config{RepoDir: repo, ScopedRuntime: true}}
			d.publicationClose = func(context.Context, domain.PublicationOperation) error { return nil }
			d.publicationStateChanged = func(operation domain.PublicationOperation) {
				if operation.State == domain.PublicationOperationMerged {
					close(merged)
				}
			}
			d.recoverPublicationOperations(context.Background())
			<-merged
			recovered, found, err := restarted.store.PublicationOperation(context.Background(), operation.OperationID)
			if err != nil || !found || recovered.State != domain.PublicationOperationMerged {
				t.Fatalf("recovered operation = (%+v,%t,%v)", recovered, found, err)
			}
		})
	}
}

func TestPublicationCandidateAdmissionRecordsAndReusesExactEvidence(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	first := daemonTestPublicationOperation(runtime.canonicalProject, "publication-admit-1", "issue-1", "intent-1", "source", time.Now().UTC())
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), first, "candidate-admit-1"); err != nil {
		t.Fatal(err)
	}
	reused, finish, err := d.publicationCandidateAdmission(first.ProjectID, first.OperationID)(context.Background(), "candidate-head")
	if err != nil || reused || finish == nil {
		t.Fatalf("first admission = (reused=%t, finish=%t, err=%v)", reused, finish != nil, err)
	}
	if err := finish(domain.IntegrationCandidateValidationAttempt{CandidateHead: "candidate-head", Status: domain.IntegrationCandidateValidationPassed}); err != nil {
		t.Fatal(err)
	}
	firstStored, found, err := runtime.store.PublicationOperation(context.Background(), first.OperationID)
	if err != nil || !found || firstStored.ValidationRequestID == "" {
		t.Fatalf("first publication validation identity = (%+v,%t,%v)", firstStored, found, err)
	}

	second := daemonTestPublicationOperation(runtime.canonicalProject, "publication-admit-2", "issue-2", "intent-2", "source", time.Now().UTC().Add(time.Second))
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), second, "candidate-admit-2"); err != nil {
		t.Fatal(err)
	}
	reused, finish, err = d.publicationCandidateAdmission(second.ProjectID, second.OperationID)(context.Background(), "candidate-head")
	if err != nil || !reused || finish != nil {
		t.Fatalf("reused admission = (reused=%t, finish=%t, err=%v)", reused, finish != nil, err)
	}
	secondStored, found, err := runtime.store.PublicationOperation(context.Background(), second.OperationID)
	if err != nil || !found || secondStored.ReusedEvidenceID != firstStored.ValidationRequestID {
		t.Fatalf("reused publication validation identity = (%+v,%t,%v), want %s", secondStored, found, err, firstStored.ValidationRequestID)
	}
}

func TestPublicationTransitionInvalidatesSnapshotsAndPublishesWatchEvent(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	hub := publish.NewHub(8, 8, nil)
	d := &Daemon{
		operationRuntime: runtime, cfg: Config{RepoDir: repo}, hub: hub,
		orchestrationSnapshotCache: map[string]orchestrationSnapshotCacheEntry{
			runtime.canonicalProject + "\x00scope": {},
		},
	}
	events, unsubscribe := hub.Subscribe(runtime.canonicalProject, 0)
	t.Cleanup(unsubscribe)
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-event", "issue", "intent", "source", time.Now().UTC())
	stored, _, err := runtime.store.EnqueuePublication(context.Background(), operation, "candidate-event")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := d.transitionPublicationOperation(context.Background(), stored, domain.PublicationOperationPreparing, nil)
	if err != nil {
		t.Fatal(err)
	}
	event := <-events
	if event.Event != protocol.EventPublicationOperationUpdated || event.Revision != 1 {
		t.Fatalf("publication event = %+v", event)
	}
	var projected domain.PublicationOperation
	if err := json.Unmarshal(event.Body, &projected); err != nil || projected.OperationID != updated.OperationID || projected.State != updated.State {
		t.Fatalf("publication event body = (%+v,%v)", projected, err)
	}
	if len(d.orchestrationSnapshotCache) != 0 {
		t.Fatalf("orchestration snapshot cache retained publication state: %+v", d.orchestrationSnapshotCache)
	}
}

func TestPublicationStoreRoutesRegisteredProjectIntentAtomically(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AZEDARACH_DISABLE_USER_DB", "1")
	defaultRepo := filepath.Join(home, "default")
	registeredRepo := filepath.Join(home, "consumer")
	for _, repo := range []string{defaultRepo, registeredRepo} {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defaultID, err := appconfig.ProjectIDForRoot(defaultRepo)
	if err != nil {
		t.Fatal(err)
	}
	registeredID, err := appconfig.ProjectIDForRoot(registeredRepo)
	if err != nil {
		t.Fatal(err)
	}
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{DefaultProject: "Default", Projects: []appconfig.Project{
		{ID: defaultID, Name: "Default", Path: defaultRepo},
		{ID: registeredID, Name: "Consumer", Path: registeredRepo},
	}}); err != nil {
		t.Fatal(err)
	}
	d := New(Config{RepoDir: defaultRepo, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(func() {
		d.closePublicationStores()
		d.closeIssueClients()
		_ = d.operationRuntime.Close()
	})
	issueClient := d.issueClientForProject(registeredID)
	if issueClient == nil {
		t.Fatal("registered issue client unavailable")
	}
	issueID, err := issueClient.Create(context.Background(), issues.CreateTaskParams{Title: "consumer publish", Description: "consumer", Acceptance: "published", Type: domain.TypeFeature, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	operation := daemonTestPublicationOperation(registeredID, "publication-registered", issueID, "intent", "source", time.Now().UTC())
	registeredStore, err := d.publicationStoreForProject(registeredID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registeredStore.PublicationOperations(context.Background(), registeredID, "", false); err != nil {
		t.Fatal(err)
	}
	params := issues.IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "test", Payload: map[string]any{
		"outcome": string(domain.ReviewOutcomeAccepted), "intent_key": operation.IntentKey, "request_fingerprint": operation.RequestFingerprint,
	}}
	if _, canonicalID, err := issueClient.AppendAcceptedReviewAndPublication(context.Background(), issueID, params, operation, "registered-candidate"); err != nil || canonicalID != operation.OperationID {
		t.Fatalf("registered atomic enqueue = (%q,%v)", canonicalID, err)
	}
	if _, found, err := registeredStore.PublicationOperation(context.Background(), operation.OperationID); err != nil || !found {
		t.Fatalf("registered publication = (found=%t, err=%v)", found, err)
	}
	if _, found, err := d.operationRuntime.store.PublicationOperation(context.Background(), operation.OperationID); err != nil || found {
		t.Fatalf("default store publication = (found=%t, err=%v), want isolated", found, err)
	}
}

func daemonTestPublicationOperation(projectID, operationID, issueID, intent, source string, created time.Time) domain.PublicationOperation {
	return domain.PublicationOperation{
		OperationID: operationID, ProjectID: projectID, IssueID: issueID, IntentKey: intent,
		RequestFingerprint: "fingerprint", ActorID: "reviewer", TargetID: "base", TargetBranch: "main",
		SourceRevision: source, BaseRevision: "base", PolicyVersion: "policy", EnvironmentFingerprint: "go:test",
		ValidationCommand: "npm test", EvidenceDigest: "evidence", State: domain.PublicationOperationQueued, CreatedAt: created,
	}
}

func operationPublicationUpdate(from, to domain.PublicationOperationState, started *time.Time) operationstore.PublicationOperationUpdate {
	return operationstore.PublicationOperationUpdate{ExpectedStates: []domain.PublicationOperationState{from}, State: to, StartedAt: started}
}
