package impl

import (
	"context"
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/adapters/beads"
	adapterrors "github.com/riordanpawley/azedarach/internal/adapters/errors"
)

func TestUpdateStatusOptimisticMutationSuccess(t *testing.T) {
	t.Parallel()

	backend := &fakeContracts{
		readyTasks: []beads.Task{{ID: "az-1", Title: "Task", Status: "open"}},
	}

	client := NewClient(backend)
	ctx := context.Background()

	if _, err := client.List(ctx); err != nil {
		t.Fatalf("hydrate tasks: %v", err)
	}

	if err := client.UpdateStatus(ctx, "az-1", "in_progress"); err != nil {
		t.Fatalf("update status: %v", err)
	}

	tasks, err := client.List(ctx)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if tasks[0].Status != "in_progress" {
		t.Fatalf("expected optimistic status to persist, got %q", tasks[0].Status)
	}
	if backend.updateCalls != 1 {
		t.Fatalf("expected one backend update call, got %d", backend.updateCalls)
	}
}

func TestUpdateStatusRollbackOnFailure(t *testing.T) {
	t.Parallel()

	backend := &fakeContracts{
		readyTasks: []beads.Task{{ID: "az-1", Title: "Task", Status: "open"}},
		updateErr:  errors.New("write failed"),
	}

	client := NewClient(backend)
	ctx := context.Background()

	if _, err := client.List(ctx); err != nil {
		t.Fatalf("hydrate tasks: %v", err)
	}

	err := client.UpdateStatus(ctx, "az-1", "done")
	if err == nil {
		t.Fatalf("expected update failure")
	}

	tasks, listErr := client.List(ctx)
	if listErr != nil {
		t.Fatalf("list tasks: %v", listErr)
	}
	if tasks[0].Status != "open" {
		t.Fatalf("expected rollback to restore status, got %q", tasks[0].Status)
	}
}

func TestUpdateStatusMapsLockContention(t *testing.T) {
	t.Parallel()

	backend := &fakeContracts{
		readyTasks: []beads.Task{{ID: "az-1", Title: "Task", Status: "open"}},
		updateErr:  errors.New("database is locked by another writer"),
	}

	client := NewClient(backend)
	ctx := context.Background()

	if _, err := client.List(ctx); err != nil {
		t.Fatalf("hydrate tasks: %v", err)
	}

	err := client.UpdateStatus(ctx, "az-1", "blocked")
	if err == nil {
		t.Fatalf("expected lock contention error")
	}
	if !adapterrors.IsLockContention(err) {
		t.Fatalf("expected lock contention taxonomy, got %v", err)
	}
}

type fakeContracts struct {
	readyTasks   []beads.Task
	createTask   beads.Task
	readyErr     error
	createErr    error
	updateErr    error
	addDepErr    error
	updateCalls  int
	addDepCalls  int
	createCalls  int
	readyCalls   int
	lastStatus   string
	lastTaskID   string
	lastDepFrom  string
	lastDepTo    string
	createdTasks []beads.CreateTaskRequest
}

func (f *fakeContracts) Ready(_ context.Context) ([]beads.Task, error) {
	f.readyCalls++
	if f.readyErr != nil {
		return nil, f.readyErr
	}
	return append([]beads.Task(nil), f.readyTasks...), nil
}

func (f *fakeContracts) Create(_ context.Context, req beads.CreateTaskRequest) (beads.Task, error) {
	f.createCalls++
	f.createdTasks = append(f.createdTasks, req)
	if f.createErr != nil {
		return beads.Task{}, f.createErr
	}
	if f.createTask.ID != "" {
		return f.createTask, nil
	}
	return beads.Task{ID: "created-1", Title: req.Title, Status: "open"}, nil
}

func (f *fakeContracts) UpdateStatus(_ context.Context, taskID string, status string) error {
	f.updateCalls++
	f.lastTaskID = taskID
	f.lastStatus = status
	return f.updateErr
}

func (f *fakeContracts) AddDependency(_ context.Context, taskID string, dependsOnID string) error {
	f.addDepCalls++
	f.lastDepFrom = taskID
	f.lastDepTo = dependsOnID
	return f.addDepErr
}
