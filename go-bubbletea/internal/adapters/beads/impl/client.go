package impl

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/riordanpawley/azedarach/internal/adapters/beads"
	adapterrors "github.com/riordanpawley/azedarach/internal/adapters/errors"
)

type Contracts interface {
	beads.Client
	AddDependency(ctx context.Context, taskID string, dependsOnID string) error
}

type Client struct {
	mu sync.Mutex

	contracts Contracts
	hydrated  bool
	tasks     []beads.Task
	deps      map[string]map[string]struct{}
}

func NewClient(contracts Contracts) *Client {
	return &Client{
		contracts: contracts,
		tasks:     []beads.Task{},
		deps:      map[string]map[string]struct{}{},
	}
}

func (c *Client) Ready(ctx context.Context) ([]beads.Task, error) {
	return c.List(ctx)
}

func (c *Client) List(ctx context.Context) ([]beads.Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hydrated {
		return cloneTasks(c.tasks), nil
	}

	tasks, err := c.contracts.Ready(ctx)
	if err != nil {
		return nil, mapAdapterError("beads.ready", err)
	}

	c.tasks = cloneTasks(tasks)
	c.hydrated = true
	return cloneTasks(c.tasks), nil
}

func (c *Client) Create(ctx context.Context, req beads.CreateTaskRequest) (beads.Task, error) {
	task, err := c.contracts.Create(ctx, req)
	if err != nil {
		return beads.Task{}, mapAdapterError("beads.create", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hydrated {
		c.tasks = append(c.tasks, task)
	}

	return task, nil
}

func (c *Client) UpdateStatus(ctx context.Context, taskID string, status string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureHydratedLocked(ctx); err != nil {
		return err
	}

	idx := indexByID(c.tasks, taskID)
	if idx < 0 {
		return fmt.Errorf("task %q not found", taskID)
	}

	prior := c.tasks[idx]
	mutationErr := c.optimisticMutationLocked(
		func() {
			current := c.tasks[idx]
			current.Status = status
			c.tasks[idx] = current
		},
		func() {
			c.tasks[idx] = prior
		},
		func() error {
			return c.contracts.UpdateStatus(ctx, taskID, status)
		},
	)
	if mutationErr != nil {
		return mapAdapterError("beads.update_status", mutationErr)
	}

	return nil
}

func (c *Client) AddDependency(ctx context.Context, taskID string, dependsOnID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	state, ok := c.deps[taskID]
	if !ok {
		state = map[string]struct{}{}
		c.deps[taskID] = state
	}

	mutationErr := c.optimisticMutationLocked(
		func() {
			state[dependsOnID] = struct{}{}
		},
		func() {
			delete(state, dependsOnID)
		},
		func() error {
			return c.contracts.AddDependency(ctx, taskID, dependsOnID)
		},
	)
	if mutationErr != nil {
		return mapAdapterError("beads.dep_add", mutationErr)
	}

	return nil
}

func (c *Client) optimisticMutationLocked(apply func(), rollback func(), commit func() error) error {
	apply()
	if err := commit(); err != nil {
		rollback()
		return err
	}
	return nil
}

func (c *Client) ensureHydratedLocked(ctx context.Context) error {
	if c.hydrated {
		return nil
	}

	tasks, err := c.contracts.Ready(ctx)
	if err != nil {
		return mapAdapterError("beads.ready", err)
	}

	c.tasks = cloneTasks(tasks)
	c.hydrated = true
	return nil
}

func cloneTasks(in []beads.Task) []beads.Task {
	return append([]beads.Task(nil), in...)
}

func indexByID(tasks []beads.Task, taskID string) int {
	for idx, task := range tasks {
		if task.ID == taskID {
			return idx
		}
	}
	return -1
}

func mapAdapterError(op string, err error) error {
	if err == nil {
		return nil
	}
	if adapterrors.IsLockContention(err) {
		return err
	}

	message := strings.ToLower(err.Error())
	for _, marker := range lockMarkers {
		if strings.Contains(message, marker) {
			return adapterrors.NewLockContention(op, "beads lock contention", err)
		}
	}

	return err
}

var lockMarkers = []string{
	"database is locked",
	"lock wait timeout",
	"could not acquire lock",
	"resource busy",
	"already locked",
}
