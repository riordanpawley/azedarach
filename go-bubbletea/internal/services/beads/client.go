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

// List fetches all beads using `bd list --format=json`
func (c *Client) List(ctx context.Context) ([]domain.Task, error) {
	c.logger.Debug("fetching beads list")

	out, err := c.runner.Run(ctx, "bd", "list", "--format=json")
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

// Search queries beads using `bd search query --format=json`
func (c *Client) Search(ctx context.Context, query string) ([]domain.Task, error) {
	c.logger.Debug("searching beads", "query", query)

	out, err := c.runner.Run(ctx, "bd", "search", query, "--format=json")
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

// Ready fetches unblocked tasks using `bd ready --format=json`
func (c *Client) Ready(ctx context.Context) ([]domain.Task, error) {
	c.logger.Debug("fetching ready beads")

	out, err := c.runner.Run(ctx, "bd", "ready", "--format=json")
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
