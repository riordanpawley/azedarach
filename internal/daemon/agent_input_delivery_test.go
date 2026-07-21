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
	"sync"
	"testing"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type recordingAuthoritativeReceiver struct {
	mu              sync.Mutex
	accepted        map[string]string
	calls           int
	payloads        []string
	failAfterAccept bool
	sink            func(string)
}

type refusingAuthoritativeReceiver struct{ outcome string }

type rejectedSubmissionReceiver struct{ calls int }

type blockingAuthoritativeReceiver struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

type transitionDuringSubmissionReceiver struct{ transition func() error }

type expiredSubmissionFenceReceiver struct{ advance func() }

func (r refusingAuthoritativeReceiver) DeliverAgentInput(context.Context, authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	return authoritativeAgentInputAcknowledgement{}, agentInputRefusalError{outcome: r.outcome}
}

func (r *rejectedSubmissionReceiver) DeliverAgentInput(ctx context.Context, request authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	r.calls++
	if _, err := request.BeginSubmission(ctx); err != nil {
		return authoritativeAgentInputAcknowledgement{}, err
	}
	if _, err := request.RevalidateSubmissionFence(ctx); err != nil {
		return authoritativeAgentInputAcknowledgement{}, err
	}
	return authoritativeAgentInputAcknowledgement{}, agentInputRefusalError{outcome: "not_ready", safeToRetry: true}
}

func (r *recordingAuthoritativeReceiver) DeliverAgentInput(_ context.Context, request authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	if request.CompleteSessionTakeover != nil {
		if _, err := request.CompleteSessionTakeover(context.Background()); err != nil {
			return authoritativeAgentInputAcknowledgement{}, err
		}
	}
	if request.BeginSubmission == nil {
		return authoritativeAgentInputAcknowledgement{}, errors.New("missing submission boundary")
	}
	if _, err := request.BeginSubmission(context.Background()); err != nil {
		return authoritativeAgentInputAcknowledgement{}, err
	}
	if request.RevalidateSubmissionFence == nil {
		return authoritativeAgentInputAcknowledgement{}, errors.New("missing final incarnation validation")
	}
	if _, err := request.RevalidateSubmissionFence(context.Background()); err != nil {
		return authoritativeAgentInputAcknowledgement{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.payloads = append(r.payloads, request.Delivery.Payload)
	if r.sink != nil {
		r.sink(request.Delivery.Payload)
	}
	key := request.Delivery.ProjectID + "\x00" + request.Delivery.IntentKey + "\x00" + request.Delivery.Target.AgentIncarnation
	ack := r.accepted[key]
	if ack == "" {
		ack = "native-ack-" + request.Delivery.IntentKey
		r.accepted[key] = ack
	}
	if r.failAfterAccept {
		r.failAfterAccept = false
		return authoritativeAgentInputAcknowledgement{}, errors.New("simulated crash after receiver acceptance")
	}
	return authoritativeAgentInputAcknowledgement{ProjectID: request.Delivery.ProjectID, IntentKey: request.Delivery.IntentKey, AgentIncarnation: request.Delivery.Target.AgentIncarnation, LeaseToken: request.LeaseToken, AcknowledgementToken: ack}, nil
}

func (r *blockingAuthoritativeReceiver) DeliverAgentInput(ctx context.Context, request authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	if _, err := request.BeginSubmission(ctx); err != nil {
		return authoritativeAgentInputAcknowledgement{}, err
	}
	if _, err := request.RevalidateSubmissionFence(ctx); err != nil {
		return authoritativeAgentInputAcknowledgement{}, err
	}
	r.mu.Lock()
	r.calls++
	if r.calls == 1 {
		close(r.entered)
	}
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return authoritativeAgentInputAcknowledgement{}, context.Cause(ctx)
	case <-r.release:
	}
	return authoritativeAgentInputAcknowledgement{ProjectID: request.Delivery.ProjectID, IntentKey: request.Delivery.IntentKey, AgentIncarnation: request.Delivery.Target.AgentIncarnation, LeaseToken: request.LeaseToken, AcknowledgementToken: "accepted-" + request.Delivery.IntentKey}, nil
}

func (r transitionDuringSubmissionReceiver) DeliverAgentInput(ctx context.Context, request authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	if _, err := request.BeginSubmission(ctx); err != nil {
		return authoritativeAgentInputAcknowledgement{}, err
	}
	if err := r.transition(); err != nil {
		return authoritativeAgentInputAcknowledgement{}, err
	}
	if _, err := request.RevalidateSubmissionFence(ctx); err != nil {
		var refusal agentInputRefusalError
		if errors.As(err, &refusal) {
			return authoritativeAgentInputAcknowledgement{}, refusal
		}
		if errors.Is(err, errCodexPaneIdentityChanged) {
			return authoritativeAgentInputAcknowledgement{}, agentInputRefusalError{outcome: "stale_incarnation", safeToRetry: true}
		}
		return authoritativeAgentInputAcknowledgement{}, err
	}
	return authoritativeAgentInputAcknowledgement{}, errors.New("accepted stale incarnation")
}

func (r expiredSubmissionFenceReceiver) DeliverAgentInput(ctx context.Context, request authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	if _, err := request.BeginSubmission(ctx); err != nil {
		return authoritativeAgentInputAcknowledgement{}, err
	}
	r.advance()
	if _, err := request.RevalidateSubmissionFence(ctx); err != nil {
		return authoritativeAgentInputAcknowledgement{}, err
	}
	return authoritativeAgentInputAcknowledgement{}, errors.New("expired submission fence was accepted")
}

func agentInputFixture(t *testing.T) (*daemonstate.RuntimeStateStore, *issues.Client, domain.AgentInputDeliveryRequest) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(dir, "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	issueClient := issues.NewClientAtPath(filepath.Join(dir, "issues.db"), logger)
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	now := time.Now().UTC()
	target := domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 123, AgentIncarnation: "inc-1", AgentThreadID: "thread-1"}
	if err := runtimeStore.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{ProjectID: "p", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 123, AgentIncarnation: "inc-1", AgentThreadID: "thread-1", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtimeStore.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{ProjectID: "p", SessionID: "az-1", ObservedState: daemonstate.SessionStateRunning, Activity: "idle", ActivitySource: "hooks", UpdatedAt: now, ObservedVersion: now.UnixNano()}); err != nil {
		t.Fatal(err)
	}
	return runtimeStore, issueClient, domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "az-1", Target: target, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "line one\nline two › ❯", IntentKey: "intent-1", ExpiresAt: now.Add(time.Minute)}
}

func TestAgentInputDeliveryFailsClosedWithoutAuthoritativeReceiver(t *testing.T) {
	runtimeStore, client, request := agentInputFixture(t)
	service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, nil, "one")
	result, err := service.Deliver(context.Background(), request)
	if err != nil || result.Outcome != domain.AgentInputWaitingNotReady {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	// A restarted daemon observes the same durable queued intent; no tmux write
	// or synthetic prompt inference can turn it into an acknowledgement.
	restarted := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, nil, "two")
	result, err = restarted.Deliver(context.Background(), request)
	if err != nil || result.Outcome != domain.AgentInputWaitingNotReady {
		t.Fatalf("restart result=%+v err=%v", result, err)
	}
}

func TestAgentInputRetryContinuesAfterUnrelatedEligibilityFailure(t *testing.T) {
	runtimeStore, client, first := agentInputFixture(t)
	second := first
	second.IntentKey = "intent-2"
	second.Payload = "second"
	ctx := context.Background()
	for _, request := range []domain.AgentInputDeliveryRequest{first, second} {
		if _, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	receiver := &recordingAuthoritativeReceiver{accepted: map[string]string{}}
	service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "retry")
	service.deliveryEligible = func(_ context.Context, request domain.AgentInputDeliveryRequest, _ time.Time) (bool, error) {
		if request.IntentKey == first.IntentKey {
			return false, errors.New("projection unavailable")
		}
		return true, nil
	}
	err := service.RetryPending(ctx, first.ProjectID, 10)
	if err == nil || !strings.Contains(err.Error(), first.IntentKey) {
		t.Fatalf("retry error = %v, want fail-visible first intent", err)
	}
	if receiver.calls != 1 || len(receiver.payloads) != 1 || receiver.payloads[0] != second.Payload {
		t.Fatalf("receiver calls=%d payloads=%v, want unrelated second intent delivered", receiver.calls, receiver.payloads)
	}
}

func TestAgentInputDeliveryRejectsSupersededActionAtFinalFence(t *testing.T) {
	runtimeStore, client, request := agentInputFixture(t)
	receiver := &recordingAuthoritativeReceiver{accepted: map[string]string{}}
	service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "supersede")
	checks := 0
	service.deliveryEligible = func(context.Context, domain.AgentInputDeliveryRequest, time.Time) (bool, error) {
		checks++
		return checks < 3, nil
	}
	result, err := service.Deliver(context.Background(), request)
	if err != nil || result.Outcome != domain.AgentInputRejectedStaleTarget {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if receiver.calls != 0 {
		t.Fatalf("superseded action reached receiver %d times", receiver.calls)
	}
	intent, err := client.EnsureAgentInputDeliveryIntent(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != "stale" {
		t.Fatalf("superseded intent state=%q, want stale", intent.State)
	}
}

func TestAgentInputDeliveryFailsClosedForDisabledOrUnsupportedCodexCapability(t *testing.T) {
	tests := []struct {
		name         string
		config       daemonProjectRuntimeConfig
		deliveryTool string
	}{
		{name: "app server disabled", config: daemonProjectRuntimeConfig{CLITool: "codex"}, deliveryTool: "codex"},
		{name: "configured tool unsupported", config: daemonProjectRuntimeConfig{CLITool: "claude", CodexAppServer: true}, deliveryTool: "codex"},
		{name: "delivery tool unsupported", config: daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}, deliveryTool: "claude"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeStore, client, request := agentInputFixture(t)
			request.Tool = test.deliveryTool
			authority := newCodexAppServerInputAuthority(&fakeCodexInputTmux{}, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig { return test.config })
			started := false
			authority.startRPC = func(context.Context, string) (codexAppServerRPC, error) {
				started = true
				return &fakeCodexRPC{}, nil
			}
			service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, authority, "one")
			result, err := service.Deliver(context.Background(), request)
			if err != nil || result.Outcome != domain.AgentInputWaitingNotReady {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if started {
				t.Fatal("app-server proxy started without exact enabled capability")
			}
			intent, err := client.EnsureAgentInputDeliveryIntent(context.Background(), request)
			if err != nil || intent.State != "queued" {
				t.Fatalf("intent=%+v err=%v", intent, err)
			}
		})
	}
}

func TestAgentInputDeliveryMapsNativeClientRefusals(t *testing.T) {
	tests := []struct {
		outcome string
		want    domain.AgentInputDeliveryOutcome
	}{
		{outcome: "composer_nonempty", want: domain.AgentInputWaitingInputNonempty},
		{outcome: "human_attached", want: domain.AgentInputWaitingHumanAttached},
		{outcome: "not_ready", want: domain.AgentInputWaitingNotReady},
		{outcome: "stale_incarnation", want: domain.AgentInputRejectedStaleTarget},
	}
	for _, test := range tests {
		t.Run(test.outcome, func(t *testing.T) {
			runtimeStore, client, request := agentInputFixture(t)
			service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, refusingAuthoritativeReceiver{outcome: test.outcome}, "one")
			result, err := service.Deliver(context.Background(), request)
			if err != nil || result.Outcome != test.want {
				t.Fatalf("result=%+v err=%v, want %s", result, err, test.want)
			}
		})
	}
}

func TestAgentInputDeliveryDurableDeduplicationAcrossDaemons(t *testing.T) {
	runtimeStore, client, request := agentInputFixture(t)
	receiver := &recordingAuthoritativeReceiver{accepted: map[string]string{}}
	one := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "one")
	two := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "two")
	for i, service := range []*agentInputDeliveryService{one, two} {
		result, err := service.Deliver(context.Background(), request)
		if err != nil || result.Outcome != domain.AgentInputDelivered {
			t.Fatalf("delivery %d result=%+v err=%v", i, result, err)
		}
	}
	if receiver.calls != 1 {
		t.Fatalf("receiver calls=%d want 1", receiver.calls)
	}
}

func TestAgentInputDeliveryConcurrentDaemonsClaimOnce(t *testing.T) {
	runtimeStore, client, request := agentInputFixture(t)
	receiver := &recordingAuthoritativeReceiver{accepted: map[string]string{}}
	services := []*agentInputDeliveryService{
		newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "one"),
		newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "two"),
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan domain.AgentInputDeliveryResult, 16)
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(service *agentInputDeliveryService) {
			defer wg.Done()
			<-start
			result, err := service.Deliver(context.Background(), request)
			results <- result
			errs <- err
		}(services[i%2])
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if result.Outcome != domain.AgentInputDelivered && result.Outcome != domain.AgentInputWaitingNotReady {
			t.Fatalf("result=%+v", result)
		}
	}
	if receiver.calls != 1 {
		t.Fatalf("receiver calls=%d want 1", receiver.calls)
	}
}

func TestAgentInputDeliveryCrossDaemonSessionLeaseExcludesDifferentIntents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtimePath := filepath.Join(dir, "runtime.db")
	issuePath := filepath.Join(dir, "issues.db")
	stores := []*daemonstate.RuntimeStateStore{
		daemonstate.NewRuntimeStateStoreAtPath(runtimePath, logger),
		daemonstate.NewRuntimeStateStoreAtPath(runtimePath, logger),
	}
	clients := []*issues.Client{issues.NewClientAtPath(issuePath, logger), issues.NewClientAtPath(issuePath, logger)}
	t.Cleanup(func() {
		for _, store := range stores {
			_ = store.Close()
		}
		for _, client := range clients {
			_ = client.CloseDB()
		}
	})
	now := time.Now().UTC()
	target := domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 123, AgentIncarnation: "inc-shared"}
	if err := stores[0].UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{ProjectID: "p", SessionID: "az-shared", LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 123, AgentIncarnation: target.AgentIncarnation, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stores[0].ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{ProjectID: "p", SessionID: "az-shared", ObservedState: daemonstate.SessionStateRunning, Activity: "idle", ActivitySource: "hooks", UpdatedAt: now, ObservedVersion: now.UnixNano()}); err != nil {
		t.Fatal(err)
	}
	receiver := &blockingAuthoritativeReceiver{entered: make(chan struct{}), release: make(chan struct{})}
	services := []*agentInputDeliveryService{
		newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return stores[0] }, func(string) *issues.Client { return clients[0] }, receiver, "daemon-one"),
		newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return stores[1] }, func(string) *issues.Client { return clients[1] }, receiver, "daemon-two"),
	}
	requestOne := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "az-shared", Target: target, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "one", IntentKey: "intent-one", ExpiresAt: now.Add(time.Minute)}
	requestTwo := requestOne
	requestTwo.IntentKey = "intent-two"
	requestTwo.Payload = "two"
	firstDone := make(chan error, 1)
	go func() {
		result, err := services[0].Deliver(ctx, requestOne)
		if err == nil && result.Outcome != domain.AgentInputDelivered {
			err = errors.New("first delivery was not acknowledged")
		}
		firstDone <- err
	}()
	<-receiver.entered
	result, err := services[1].Deliver(ctx, requestTwo)
	if err != nil || result.Outcome != domain.AgentInputWaitingNotReady {
		t.Fatalf("overlapping delivery result=%+v err=%v", result, err)
	}
	receiver.mu.Lock()
	calls := receiver.calls
	receiver.mu.Unlock()
	if calls != 1 {
		t.Fatalf("receiver calls while first gate active=%d, want 1", calls)
	}
	close(receiver.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	result, err = services[1].Deliver(ctx, requestTwo)
	if err != nil || result.Outcome != domain.AgentInputDelivered {
		t.Fatalf("post-release delivery result=%+v err=%v", result, err)
	}
}

func TestAgentInputDeliveryRevalidatesSamePaneThreadTransitionAfterSubmissionBoundary(t *testing.T) {
	runtimeStore, client, request := agentInputFixture(t)
	receiver := transitionDuringSubmissionReceiver{transition: func() error {
		now := time.Now().UTC().Add(time.Second)
		return runtimeStore.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{ProjectID: request.ProjectID, SessionID: request.SessionID, LogicalPaneID: string(request.Target.LogicalPaneID), TmuxPaneID: request.Target.TmuxPaneID, PanePID: request.Target.PanePID, AgentIncarnation: "inc-2", ObservedAt: now})
	}}
	service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "one")
	result, err := service.Deliver(context.Background(), request)
	if err != nil || result.Outcome != domain.AgentInputRejectedStaleTarget {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	intent, err := client.EnsureAgentInputDeliveryIntent(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != "stale" {
		t.Fatalf("intent state=%q, want stale", intent.State)
	}
}

func TestAgentInputDeliveryRevalidatesHookReadinessAfterSubmissionBoundary(t *testing.T) {
	runtimeStore, client, request := agentInputFixture(t)
	receiver := transitionDuringSubmissionReceiver{transition: func() error {
		now := time.Now().UTC().Add(time.Second)
		_, _, err := runtimeStore.ApplyPhysicalSessionObservation(context.Background(), daemonstate.PhysicalSessionObservation{ProjectID: request.ProjectID, SessionID: request.SessionID, ObservedState: daemonstate.SessionStateRunning, Activity: "busy", ActivitySource: "hooks", UpdatedAt: now, ObservedVersion: now.UnixNano()})
		return err
	}}
	service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "one")
	result, err := service.Deliver(context.Background(), request)
	if err != nil || result.Outcome != domain.AgentInputWaitingNotReady {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	intent, err := client.EnsureAgentInputDeliveryIntent(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != "queued" {
		t.Fatalf("intent state=%q, want queued", intent.State)
	}
}

func TestAgentInputDeliveryDoesNotRetryAfterAmbiguousAcceptance(t *testing.T) {
	runtimeStore, client, request := agentInputFixture(t)
	receiver := &recordingAuthoritativeReceiver{accepted: map[string]string{}, failAfterAccept: true}
	one := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "one")
	if result, err := one.Deliver(context.Background(), request); err == nil || result.Outcome != domain.AgentInputFailed {
		t.Fatalf("first result=%+v err=%v", result, err)
	}
	two := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "two")
	if result, err := two.Deliver(context.Background(), request); err != nil || result.Outcome != domain.AgentInputWaitingNotReady {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
	if receiver.calls != 1 || len(receiver.accepted) != 1 {
		t.Fatalf("receiver calls=%d accepted=%d, want one non-retried submission", receiver.calls, len(receiver.accepted))
	}
}

func TestAgentInputDeliveryExpiredFinalFenceRemainsAmbiguous(t *testing.T) {
	runtimeStore, client, request := agentInputFixture(t)
	// Keep context deadlines comfortably in the future while advancing the
	// injected service clock across the durable lease boundary immediately.
	now := time.Now().UTC().Add(time.Minute)
	request.ExpiresAt = now.Add(time.Minute)
	service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, expiredSubmissionFenceReceiver{advance: func() { now = now.Add(41 * time.Millisecond) }}, "one")
	service.now = func() time.Time { return now }
	service.sessionLeaseDuration = 40 * time.Millisecond
	service.sessionLeaseHeartbeat = time.Second
	result, err := service.Deliver(context.Background(), request)
	if err == nil || result.Outcome != domain.AgentInputFailed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	intent, loadErr := client.EnsureAgentInputDeliveryIntent(context.Background(), request)
	if loadErr != nil || intent.State != "ambiguous" {
		t.Fatalf("intent=%+v err=%v", intent, loadErr)
	}
}

func TestAgentInputDeliveryRetainsSessionFenceWhenCompletionMarkerRemovalFails(t *testing.T) {
	runtimeStore, client, request := agentInputFixture(t)
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: request.SessionID}}, panes: []tmux.PaneInfo{{SessionName: request.SessionID, PaneID: request.Target.TmuxPaneID, PanePID: request.Target.PanePID}}}
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig {
		return daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}
	})
	authority.removeGateFile = func(path string) error {
		if filepath.Ext(path) == ".json" {
			return errors.New("forced completion marker removal failure")
		}
		return os.Remove(path)
	}
	authority.startRPC = func(context.Context, string) (codexAppServerRPC, error) { return &fakeCodexRPC{}, nil }
	service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, authority, "daemon-old")
	result, err := service.Deliver(context.Background(), request)
	if err == nil || result.Outcome != domain.AgentInputFailed || !errors.Is(err, errCodexGateRestoreIncomplete) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	intent, loadErr := client.EnsureAgentInputDeliveryIntent(context.Background(), request)
	if loadErr != nil || intent.State != "ambiguous" {
		t.Fatalf("intent=%+v err=%v", intent, loadErr)
	}
	if lease, acquired, claimErr := client.ClaimAgentInputDeliverySessionLease(context.Background(), request.ProjectID, request.SessionID, "inc-new", "daemon-new", time.Now(), time.Minute); claimErr != nil || acquired || lease.LeaseToken != "" {
		t.Fatalf("incomplete restore released fence lease=%+v acquired=%v err=%v", lease, acquired, claimErr)
	}
	markers, globErr := filepath.Glob(filepath.Join(authority.gateDir, "gate-*.json"))
	if globErr != nil || len(markers) != 1 {
		t.Fatalf("completion markers=%v err=%v, want one retained marker", markers, globErr)
	}
	raw, readErr := os.ReadFile(markers[0])
	if readErr != nil {
		t.Fatal(readErr)
	}
	var state codexInputGateState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(state.EventsPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("event ledger was not cleaned before retained marker: %v", statErr)
	}

	// Once the retained lease expires, a new daemon may take ownership, but the
	// first takeover phase deliberately preserves the old marker's exact fence.
	// Startup recovery must therefore leave the live takeover owner and gate
	// untouched until that owner restores the marker or its lease expires.
	newLease, acquired, claimErr := client.ClaimAgentInputDeliverySessionLease(context.Background(), request.ProjectID, request.SessionID, "inc-new", "daemon-new", time.Now().Add(10*time.Minute), time.Minute)
	if claimErr != nil || !acquired || !newLease.TakeoverPending || newLease.LeaseToken != state.FenceToken || newLease.AgentIncarnation != state.AgentIncarnation {
		t.Fatalf("new owner lease=%+v acquired=%v err=%v", newLease, acquired, claimErr)
	}
	adapter.mu.Lock()
	adapter.paneEnabled = true
	adapter.hooksEnabled = true
	adapter.activeGates = 1
	adapter.clients[0].ReadOnly = true
	adapter.setReadOnlyCalls = nil
	adapter.mu.Unlock()
	authority.issueClients = func(string) *issues.Client { return client }
	recoveryCtx, cancelRecovery := context.WithCancel(context.Background())
	operations, recoveryErr := authority.recoverStaleGates(recoveryCtx)
	if recoveryErr != nil || len(operations) != 1 {
		cancelRecovery()
		t.Fatalf("live takeover recovery scheduling: operations=%v err=%v", operations, recoveryErr)
	}
	cancelRecovery()
	<-operations[0].done
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if !adapter.paneEnabled || !adapter.hooksEnabled || adapter.activeGates != 1 || !adapter.clients[0].ReadOnly || len(adapter.setReadOnlyCalls) != 0 {
		t.Fatalf("stale marker mutated new owner gate: pane=%v hooks=%v active=%d clients=%+v calls=%v", adapter.paneEnabled, adapter.hooksEnabled, adapter.activeGates, adapter.clients, adapter.setReadOnlyCalls)
	}
}

func TestAgentInputDeliveryRecoversExpiredLeaseWithoutGateMarker(t *testing.T) {
	ctx := context.Background()
	runtimeStore, client, request := agentInputFixture(t)
	now := request.ExpiresAt.Add(-time.Minute)
	oldLease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, request.ProjectID, request.SessionID, "inc-old", "daemon-old", now.Add(-2*time.Minute), time.Minute)
	if err != nil || !acquired {
		t.Fatalf("old lease=%+v acquired=%v err=%v", oldLease, acquired, err)
	}
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: request.SessionID}}, panes: []tmux.PaneInfo{{SessionName: request.SessionID, PaneID: request.Target.TmuxPaneID, PanePID: request.Target.PanePID}}}
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig {
		return daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}
	})
	authority.now = func() time.Time { return now }
	authority.startRPC = func(context.Context, string) (codexAppServerRPC, error) { return &fakeCodexRPC{}, nil }
	service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, authority, "daemon-new")
	service.now = func() time.Time { return now }
	result, err := service.Deliver(ctx, request)
	if err != nil || result.Outcome != domain.AgentInputDelivered {
		t.Fatalf("marker-free takeover result=%+v err=%v", result, err)
	}
	adapter.mu.Lock()
	if !adapter.paneEnabled || adapter.hooksEnabled || adapter.activeGates != 0 || adapter.clients[0].ReadOnly {
		adapter.mu.Unlock()
		t.Fatalf("delivery did not restore tmux after marker-free takeover: pane=%v hooks=%v active=%d clients=%+v", adapter.paneEnabled, adapter.hooksEnabled, adapter.activeGates, adapter.clients)
	}
	adapter.mu.Unlock()
	next, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, request.ProjectID, request.SessionID, "inc-next", "daemon-next", now.Add(time.Second), time.Minute)
	if err != nil || !acquired || next.TakeoverPending {
		t.Fatalf("completed marker-free takeover left stale fence: lease=%+v acquired=%v err=%v", next, acquired, err)
	}
}

func TestAgentInputDeliveryRetriesOnlyAfterAuthoritativeSubmissionRejection(t *testing.T) {
	runtimeStore, client, request := agentInputFixture(t)
	receiver := &rejectedSubmissionReceiver{}
	service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "one")
	for i := 0; i < 2; i++ {
		result, err := service.Deliver(context.Background(), request)
		if err != nil || result.Outcome != domain.AgentInputWaitingNotReady {
			t.Fatalf("delivery %d result=%+v err=%v", i, result, err)
		}
	}
	if receiver.calls != 2 {
		t.Fatalf("receiver calls=%d, want authoritative rejection to remain retryable", receiver.calls)
	}
}

func TestAgentInputDeliveryRejectsPaneReuseBeforeReceiver(t *testing.T) {
	runtimeStore, client, request := agentInputFixture(t)
	receiver := &recordingAuthoritativeReceiver{accepted: map[string]string{}}
	now := time.Now().UTC().Add(time.Second)
	if err := runtimeStore.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{ProjectID: "p", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 999, AgentIncarnation: "inc-2", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, receiver, "one")
	result, err := service.Deliver(context.Background(), request)
	if err != nil || result.Outcome != domain.AgentInputRejectedStaleTarget || receiver.calls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, receiver.calls)
	}
}
