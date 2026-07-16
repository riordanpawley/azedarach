package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
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

func (r refusingAuthoritativeReceiver) DeliverAgentInput(context.Context, authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	return authoritativeAgentInputAcknowledgement{}, agentInputRefusalError{outcome: r.outcome}
}

func (r *rejectedSubmissionReceiver) DeliverAgentInput(ctx context.Context, request authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	r.calls++
	if err := request.BeginSubmission(ctx); err != nil {
		return authoritativeAgentInputAcknowledgement{}, err
	}
	return authoritativeAgentInputAcknowledgement{}, agentInputRefusalError{outcome: "not_ready", safeToRetry: true}
}

func (r *recordingAuthoritativeReceiver) DeliverAgentInput(_ context.Context, request authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	if request.BeginSubmission == nil {
		return authoritativeAgentInputAcknowledgement{}, errors.New("missing submission boundary")
	}
	if err := request.BeginSubmission(context.Background()); err != nil {
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
	target := domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 123, AgentIncarnation: "inc-1"}
	if err := runtimeStore.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{ProjectID: "p", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 123, AgentIncarnation: "inc-1", ObservedAt: now}); err != nil {
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
