package beads

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// Client wraps the beads CLI for task management operations
type Client struct {
	runner CommandRunner
	logger *slog.Logger
}

// NewClient creates a new Beads client with dependency injection
func NewClient(runner CommandRunner, logger *slog.Logger) *Client {
	return &Client{
		runner: runner,
		logger: logger,
	}
}

// List fetches all beads using `bd list --json`
func (c *Client) List(ctx context.Context) ([]domain.Task, error) {
	c.logger.Debug("fetching beads list")

	out, err := c.runner.Run(ctx, "bd", "list", "--json")
	if err != nil {
		return nil, &domain.BeadsError{Op: "list", Err: err}
	}

	var tasks []domain.Task
	if err := json.Unmarshal(out, &tasks); err != nil {
		return nil, &domain.BeadsError{Op: "list", Message: "failed to parse JSON", Err: err}
	}

	c.logger.Debug("fetched beads", "count", len(tasks))
	return tasks, nil
}

// Search queries beads using `bd search query --json`
func (c *Client) Search(ctx context.Context, query string) ([]domain.Task, error) {
	c.logger.Debug("searching beads", "query", query)

	out, err := c.runner.Run(ctx, "bd", "search", query, "--json")
	if err != nil {
		return nil, &domain.BeadsError{Op: "search", Message: query, Err: err}
	}

	var tasks []domain.Task
	if err := json.Unmarshal(out, &tasks); err != nil {
		return nil, &domain.BeadsError{Op: "search", Message: "failed to parse JSON", Err: err}
	}

	c.logger.Debug("found beads", "count", len(tasks))
	return tasks, nil
}

// Ready fetches unblocked tasks using `bd ready --json`
func (c *Client) Ready(ctx context.Context) ([]domain.Task, error) {
	c.logger.Debug("fetching ready beads")

	out, err := c.runner.Run(ctx, "bd", "ready", "--json")
	if err != nil {
		return nil, &domain.BeadsError{Op: "ready", Err: err}
	}

	var tasks []domain.Task
	if err := json.Unmarshal(out, &tasks); err != nil {
		return nil, &domain.BeadsError{Op: "ready", Message: "failed to parse JSON", Err: err}
	}

	c.logger.Debug("found ready beads", "count", len(tasks))
	return tasks, nil
}

// Update changes a bead's status using `bd update id --status=status`
func (c *Client) Update(ctx context.Context, id string, status domain.Status) error {
	c.logger.Debug("updating bead status", "id", id, "status", status)

	_, err := c.runner.Run(ctx, "bd", "update", id, "--status="+string(status))
	if err != nil {
		return &domain.BeadsError{Op: "update", BeadID: id, Err: err}
	}

	c.logger.Debug("bead updated", "id", id)
	return nil
}

// CreateTaskParams contains parameters for creating a new task
type CreateTaskParams struct {
	Title       string
	Description string
	Type        domain.TaskType
	Priority    domain.Priority
	ParentID    *string
}

// Create creates a new task using `bd create "title" -t type -p priority --json`
func (c *Client) Create(ctx context.Context, params CreateTaskParams) (string, error) {
	c.logger.Debug("creating bead", "title", params.Title)

	args := []string{"create", params.Title, "--json"}
	args = append(args, "-t", string(params.Type))
	args = append(args, "-p", string(rune('0'+params.Priority)))

	if params.ParentID != nil {
		args = append(args, "--parent", *params.ParentID)
	}

	out, err := c.runner.Run(ctx, "bd", args...)
	if err != nil {
		return "", &domain.BeadsError{Op: "create", Message: params.Title, Err: err}
	}

	// Response from bd create --json is the created task
	var task domain.Task
	if err := json.Unmarshal(out, &task); err != nil {
		// If it's not a full task, it might just be the ID as a string
		// Let's try to see if it's a simple JSON object with an id field
		var idResult struct {
			ID string `json:"id"`
		}
		if err2 := json.Unmarshal(out, &idResult); err2 == nil && idResult.ID != "" {
			return idResult.ID, nil
		}
		return "", &domain.BeadsError{Op: "create", Message: "failed to parse JSON", Err: err}
	}

	c.logger.Debug("bead created", "id", task.ID)
	return task.ID, nil
}

// Close marks a bead as complete using `bd close id --reason=reason`
func (c *Client) Close(ctx context.Context, id string, reason string) error {
	c.logger.Debug("closing bead", "id", id, "reason", reason)

	args := []string{"close", id}
	if reason != "" {
		args = append(args, "--reason="+reason)
	}

	_, err := c.runner.Run(ctx, "bd", args...)
	if err != nil {
		return &domain.BeadsError{Op: "close", BeadID: id, Err: err}
	}

	c.logger.Debug("bead closed", "id", id)
	return nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	c.logger.Debug("deleting bead", "id", id)

	_, err := c.runner.Run(ctx, "bd", "delete", id)
	if err != nil {
		return &domain.BeadsError{Op: "delete", BeadID: id, Err: err}
	}

	c.logger.Debug("bead deleted", "id", id)
	return nil
}

// Archive archives a bead using `bd archive id`
func (c *Client) Archive(ctx context.Context, id string) error {
	c.logger.Debug("archiving bead", "id", id)

	_, err := c.runner.Run(ctx, "bd", "archive", id)
	if err != nil {
		return &domain.BeadsError{Op: "archive", BeadID: id, Err: err}
	}

	c.logger.Debug("bead archived", "id", id)
	return nil
}

type UpdateTaskParams struct {
	Title       string
	Description string
	Type        domain.TaskType
	Priority    domain.Priority
}

func (c *Client) UpdateDetails(ctx context.Context, id string, params UpdateTaskParams) error {
	c.logger.Debug("updating bead details", "id", id)

	args := []string{"update", id}
	if params.Title != "" {
		args = append(args, "--title="+params.Title)
	}

	args = append(args, "--type="+string(params.Type))
	args = append(args, "--priority="+string(rune('0'+params.Priority)))

	_, err := c.runner.Run(ctx, "bd", args...)
	if err != nil {
		return &domain.BeadsError{Op: "update-details", BeadID: id, Err: err}
	}

	c.logger.Debug("bead details updated", "id", id)
	return nil
}
