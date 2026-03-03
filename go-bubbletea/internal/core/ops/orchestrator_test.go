package ops

import "testing"

func TestOrchestratorQueueAndEventLog(t *testing.T) {
	t.Parallel()

	orch := NewOrchestrator()

	op, created, err := orch.Queue(Request{IssueKey: "bd-1", CorrelationID: "corr-1"})
	if err != nil {
		t.Fatalf("queue operation: %v", err)
	}
	if !created {
		t.Fatalf("expected operation to be created")
	}
	if op.State != StateQueued {
		t.Fatalf("expected queued, got %s", op.State)
	}

	running, ok := orch.StartNext()
	if !ok {
		t.Fatalf("expected queued operation to start")
	}
	if running.ID != op.ID {
		t.Fatalf("expected %s to start, got %s", op.ID, running.ID)
	}
	if running.State != StateRunning {
		t.Fatalf("expected running, got %s", running.State)
	}

	succeeded, err := orch.Succeed(op.ID)
	if err != nil {
		t.Fatalf("succeed operation: %v", err)
	}
	if succeeded.State != StateSucceeded {
		t.Fatalf("expected succeeded, got %s", succeeded.State)
	}

	events := orch.Events("corr-1")
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].State != StateQueued || events[1].State != StateRunning || events[2].State != StateSucceeded {
		t.Fatalf("unexpected state sequence: %v, %v, %v", events[0].State, events[1].State, events[2].State)
	}
}

func TestOrchestratorPerIssueSerialization(t *testing.T) {
	t.Parallel()

	orch := NewOrchestrator()

	a1, _, err := orch.Queue(Request{IssueKey: "bd-1", CorrelationID: "c-a1"})
	if err != nil {
		t.Fatalf("queue a1: %v", err)
	}
	a2, _, err := orch.Queue(Request{IssueKey: "bd-1", CorrelationID: "c-a2"})
	if err != nil {
		t.Fatalf("queue a2: %v", err)
	}
	b1, _, err := orch.Queue(Request{IssueKey: "bd-2", CorrelationID: "c-b1"})
	if err != nil {
		t.Fatalf("queue b1: %v", err)
	}

	started1, ok := orch.StartNext()
	if !ok || started1.ID != a1.ID {
		t.Fatalf("expected first start to be a1, got %+v", started1)
	}

	started2, ok := orch.StartNext()
	if !ok || started2.ID != b1.ID {
		t.Fatalf("expected second start to be b1 due to issue lock, got %+v", started2)
	}

	if _, err := orch.Succeed(a1.ID); err != nil {
		t.Fatalf("succeed a1: %v", err)
	}

	started3, ok := orch.StartNext()
	if !ok || started3.ID != a2.ID {
		t.Fatalf("expected third start to be a2 after a1 completion, got %+v", started3)
	}
}

func TestOrchestratorIdempotencyDedupesQueue(t *testing.T) {
	t.Parallel()

	orch := NewOrchestrator()

	first, created, err := orch.Queue(Request{IssueKey: "bd-1", IdempotencyKey: "idem-1", CorrelationID: "corr-1"})
	if err != nil {
		t.Fatalf("queue first: %v", err)
	}
	if !created {
		t.Fatalf("first operation must be created")
	}

	second, created, err := orch.Queue(Request{IssueKey: "bd-1", IdempotencyKey: "idem-1", CorrelationID: "corr-2"})
	if err != nil {
		t.Fatalf("queue second: %v", err)
	}
	if created {
		t.Fatalf("second operation should be deduped")
	}
	if second.ID != first.ID {
		t.Fatalf("expected deduped operation id %s, got %s", first.ID, second.ID)
	}

	if _, ok := orch.StartNext(); !ok {
		t.Fatalf("expected first start to exist")
	}
	if _, ok := orch.StartNext(); ok {
		t.Fatalf("expected deduped queue to contain only one runnable operation")
	}

	events := orch.Events("corr-1")
	if len(events) != 2 {
		t.Fatalf("expected queued+running for original operation, got %d events", len(events))
	}
}

func TestOrchestratorCancellationTerminalStates(t *testing.T) {
	t.Parallel()

	orch := NewOrchestrator()

	queued, _, err := orch.Queue(Request{IssueKey: "bd-1"})
	if err != nil {
		t.Fatalf("queue queued op: %v", err)
	}

	cancelledQueued, err := orch.Cancel(queued.ID, "user requested")
	if err != nil {
		t.Fatalf("cancel queued: %v", err)
	}
	if cancelledQueued.State != StateCancelled {
		t.Fatalf("expected cancelled queued op, got %s", cancelledQueued.State)
	}
	if _, ok := orch.StartNext(); ok {
		t.Fatalf("cancelled queued op should not start")
	}

	running, _, err := orch.Queue(Request{IssueKey: "bd-2"})
	if err != nil {
		t.Fatalf("queue running op: %v", err)
	}
	started, ok := orch.StartNext()
	if !ok || started.ID != running.ID {
		t.Fatalf("expected started running op, got %+v", started)
	}

	cancelledRunning, err := orch.Cancel(running.ID, "timeout")
	if err != nil {
		t.Fatalf("cancel running: %v", err)
	}
	if cancelledRunning.State != StateCancelled {
		t.Fatalf("expected cancelled running op, got %s", cancelledRunning.State)
	}

	again, err := orch.Cancel(running.ID, "second cancel")
	if err != nil {
		t.Fatalf("cancel already-cancelled op: %v", err)
	}
	if again.State != StateCancelled {
		t.Fatalf("terminal cancel should stay cancelled, got %s", again.State)
	}

	succeeded, _, err := orch.Queue(Request{IssueKey: "bd-3"})
	if err != nil {
		t.Fatalf("queue succeeded op: %v", err)
	}
	if _, ok := orch.StartNext(); !ok {
		t.Fatalf("expected to start succeeded op")
	}
	if _, err := orch.Succeed(succeeded.ID); err != nil {
		t.Fatalf("succeed op: %v", err)
	}

	unchanged, err := orch.Cancel(succeeded.ID, "late cancel")
	if err != nil {
		t.Fatalf("cancel succeeded op: %v", err)
	}
	if unchanged.State != StateSucceeded {
		t.Fatalf("terminal succeeded state should stay succeeded, got %s", unchanged.State)
	}
}
