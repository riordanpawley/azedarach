package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/observability"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type readWaitDeadlineTransport struct {
	deadlines []time.Time
}

func (t *readWaitDeadlineTransport) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	return protocol.HelloAck{Accepted: true}, nil
}

func (t *readWaitDeadlineTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		panic("expected deadline on bounded read context")
	}
	t.deadlines = append(t.deadlines, deadline)

	body, err := json.Marshal(protocol.TaskListSnapshotPayload{
		SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
		ProtocolVersion:  req.ProtocolVersion,
		SnapshotRevision: 0,
		ProjectID:        req.Meta.ProjectID,
		LastCheckedAt:    time.Unix(1700000000, 0).UTC(),
		Freshness:        protocol.TaskListFreshnessFresh,
		Tasks:            []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen}},
	})
	if err != nil {
		return protocol.ResponseEnvelope{}, err
	}

	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		OK:              true,
		Body:            body,
	}, nil
}

func (t *readWaitDeadlineTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, errors.New("not implemented")
}

type blockingReadTransport struct{}

func (blockingReadTransport) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	return protocol.HelloAck{Accepted: true}, nil
}

func (blockingReadTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	<-ctx.Done()
	return protocol.ResponseEnvelope{}, ctx.Err()
}

func (blockingReadTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, errors.New("not implemented")
}

func TestReadWaitPolicyNormalizesBudgets(t *testing.T) {
	policy := ReadWaitPolicy{}.Normalize()

	if policy.Default != DefaultReadWaitBudget {
		t.Fatalf("default budget = %s, want %s", policy.Default, DefaultReadWaitBudget)
	}
	if policy.Explicit != ExplicitReadWaitBudget {
		t.Fatalf("explicit budget = %s, want %s", policy.Explicit, ExplicitReadWaitBudget)
	}
	if got := policy.Budget(ReadWaitModeDefault); got != policy.Default {
		t.Fatalf("default mode budget = %s, want %s", got, policy.Default)
	}
	if got := policy.Budget(ReadWaitModeExplicit); got <= policy.Default {
		t.Fatalf("explicit mode budget = %s, want > %s", got, policy.Default)
	}
}

func TestListTasksSnapshotWithModeUsesLongerExplicitBudget(t *testing.T) {
	transport := &readWaitDeadlineTransport{}
	client := New(transport).
		WithProjectID("proj-read").
		WithReadWaitPolicy(ReadWaitPolicy{
			Default:  50 * time.Millisecond,
			Explicit: 200 * time.Millisecond,
		})

	if _, err := client.ListTasksSnapshotWithMode(context.Background(), ReadWaitModeDefault); err != nil {
		t.Fatalf("default snapshot error: %v", err)
	}
	if _, err := client.ListTasksSnapshotWithMode(context.Background(), ReadWaitModeExplicit); err != nil {
		t.Fatalf("explicit snapshot error: %v", err)
	}
	if len(transport.deadlines) != 2 {
		t.Fatalf("deadline count = %d, want 2", len(transport.deadlines))
	}

	if !transport.deadlines[1].After(transport.deadlines[0]) {
		t.Fatalf("explicit deadline = %s, default deadline = %s, want explicit > default", transport.deadlines[1], transport.deadlines[0])
	}
}

func TestListTasksSnapshotWithModeReturnsTimeoutError(t *testing.T) {
	t.Setenv(latencytrace.EnvVar, "")
	t.Setenv(observability.EnvVar, "true")
	latencytrace.SetConfigEnabled(false)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		latencytrace.SetConfigEnabled(false)
	})
	client := New(blockingReadTransport{}).
		WithProjectID("proj-read").
		WithReadWaitPolicy(ReadWaitPolicy{
			Default:  1 * time.Nanosecond,
			Explicit: 2 * time.Nanosecond,
		})

	_, err := client.ListTasksSnapshotWithMode(context.Background(), ReadWaitModeDefault)
	var timeoutErr *ReadWaitTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("error type = %T, want *ReadWaitTimeoutError: %v", err, err)
	}
	if timeoutErr.Mode != ReadWaitModeDefault {
		t.Fatalf("timeout mode = %q, want %q", timeoutErr.Mode, ReadWaitModeDefault)
	}
	if timeoutErr.Budget <= 0 {
		t.Fatalf("timeout budget = %s, want > 0", timeoutErr.Budget)
	}
	if timeoutErr.Hint == "" || !strings.Contains(timeoutErr.Hint, "keeping current local view") {
		t.Fatalf("timeout hint = %q, want local-view freshness hint", timeoutErr.Hint)
	}
	var timeoutSpan sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "daemonclient.task_snapshot.timeout" {
			timeoutSpan = span
		}
	}
	if timeoutSpan == nil {
		t.Fatalf("timeout span missing; ended=%d", len(recorder.Ended()))
	}
	attrs := map[string]bool{}
	for _, attr := range timeoutSpan.Attributes() {
		attrs[string(attr.Key)] = true
	}
	for _, key := range []string{"component", "phase", "command", "outcome", "error"} {
		if !attrs[key] {
			t.Fatalf("timeout span missing attribute %q", key)
		}
	}
}
