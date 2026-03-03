package beads

import "context"

type Task struct {
	ID     string
	Title  string
	Status string
}

type CreateTaskRequest struct {
	Title       string
	Description string
	Type        string
	Priority    int
}

type Client interface {
	Ready(ctx context.Context) ([]Task, error)
	Create(ctx context.Context, req CreateTaskRequest) (Task, error)
	UpdateStatus(ctx context.Context, taskID string, status string) error
}
