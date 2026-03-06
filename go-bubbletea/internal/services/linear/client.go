package linear

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// Client wraps the issues CLI for task management operations
type Client struct {
	runner CommandRunner
	logger *slog.Logger
}

// NewClient creates a new Linear client with dependency injection
func NewClient(runner CommandRunner, logger *slog.Logger) *Client {
	return &Client{
		runner: runner,
		logger: logger,
	}
}

// List fetches all issues using `az issue list --json`
func (c *Client) List(ctx context.Context) ([]domain.Task, error) {
	c.logger.Debug("fetching issues list")

	out, err := c.runner.Run(ctx, "az", "issue", "list", "--json")
	if err != nil {
		return nil, &domain.IssueTrackerError{Op: "list", Err: err}
	}

	var tasks []domain.Task
	if err := json.Unmarshal(out, &tasks); err != nil {
		return nil, &domain.IssueTrackerError{Op: "list", Message: "failed to parse JSON", Err: err}
	}

	c.logger.Debug("fetched issues", "count", len(tasks))
	return tasks, nil
}

// Search queries issues using `az issue search query --json`
func (c *Client) Search(ctx context.Context, query string) ([]domain.Task, error) {
	c.logger.Debug("searching issues", "query", query)

	out, err := c.runner.Run(ctx, "az", "issue", "search", query, "--json")
	if err != nil {
		return nil, &domain.IssueTrackerError{Op: "search", Message: query, Err: err}
	}

	var tasks []domain.Task
	if err := json.Unmarshal(out, &tasks); err != nil {
		return nil, &domain.IssueTrackerError{Op: "search", Message: "failed to parse JSON", Err: err}
	}

	c.logger.Debug("found issues", "count", len(tasks))
	return tasks, nil
}

// Ready fetches unblocked tasks using `az issue ready --json`
func (c *Client) Ready(ctx context.Context) ([]domain.Task, error) {
	c.logger.Debug("fetching ready issues")

	out, err := c.runner.Run(ctx, "az", "issue", "ready", "--json")
	if err != nil {
		return nil, &domain.IssueTrackerError{Op: "ready", Err: err}
	}

	var tasks []domain.Task
	if err := json.Unmarshal(out, &tasks); err != nil {
		return nil, &domain.IssueTrackerError{Op: "ready", Message: "failed to parse JSON", Err: err}
	}

	c.logger.Debug("found ready issues", "count", len(tasks))
	return tasks, nil
}

// Update changes a issue's status using `az issue update id --status=status`
func (c *Client) Update(ctx context.Context, id string, status domain.Status) error {
	c.logger.Debug("updating issue status", "id", id, "status", status)

	_, err := c.runner.Run(ctx, "az", "issue", "update", id, "--status="+string(status))
	if err != nil {
		return &domain.IssueTrackerError{Op: "update", IssueID: id, Err: err}
	}

	c.logger.Debug("issue updated", "id", id)
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

// Create creates a new task using `az issue create "title" -t type -p priority --json`
func (c *Client) Create(ctx context.Context, params CreateTaskParams) (string, error) {
	c.logger.Debug("creating issue", "title", params.Title)

	args := []string{"create", params.Title, "--json"}
	args = append(args, "-t", string(params.Type))
	args = append(args, "-p", string(rune('0'+params.Priority)))

	if params.ParentID != nil {
		args = append(args, "--parent", *params.ParentID)
	}

	out, err := c.runner.Run(ctx, "az", append([]string{"issue"}, args...)...)
	if err != nil {
		return "", &domain.IssueTrackerError{Op: "create", Message: params.Title, Err: err}
	}

	// Response from az issue create --json is the created task
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
		return "", &domain.IssueTrackerError{Op: "create", Message: "failed to parse JSON", Err: err}
	}

	c.logger.Debug("issue created", "id", task.ID)
	return task.ID, nil
}

// Close marks a issue as complete using `az issue close id --reason=reason`
func (c *Client) Close(ctx context.Context, id string, reason string) error {
	c.logger.Debug("closing issue", "id", id, "reason", reason)

	args := []string{"close", id}
	if reason != "" {
		args = append(args, "--reason="+reason)
	}

	_, err := c.runner.Run(ctx, "az", append([]string{"issue"}, args...)...)
	if err != nil {
		return &domain.IssueTrackerError{Op: "close", IssueID: id, Err: err}
	}

	c.logger.Debug("issue closed", "id", id)
	return nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	c.logger.Debug("deleting issue", "id", id)

	_, err := c.runner.Run(ctx, "az", "issue", "delete", id)
	if err != nil {
		return &domain.IssueTrackerError{Op: "delete", IssueID: id, Err: err}
	}

	c.logger.Debug("issue deleted", "id", id)
	return nil
}

// Archive archives a issue using `az issue archive id`
func (c *Client) Archive(ctx context.Context, id string) error {
	c.logger.Debug("archiving issue", "id", id)

	_, err := c.runner.Run(ctx, "az", "issue", "archive", id)
	if err != nil {
		return &domain.IssueTrackerError{Op: "archive", IssueID: id, Err: err}
	}

	c.logger.Debug("issue archived", "id", id)
	return nil
}

type UpdateTaskParams struct {
	Title       string
	Description string
	Type        domain.TaskType
	Priority    domain.Priority
}

func (c *Client) UpdateDetails(ctx context.Context, id string, params UpdateTaskParams) error {
	c.logger.Debug("updating issue details", "id", id)

	args := []string{"update", id}
	if params.Title != "" {
		args = append(args, "--title="+params.Title)
	}

	args = append(args, "--type="+string(params.Type))
	args = append(args, "--priority="+string(rune('0'+params.Priority)))

	_, err := c.runner.Run(ctx, "az", append([]string{"issue"}, args...)...)
	if err != nil {
		return &domain.IssueTrackerError{Op: "update-details", IssueID: id, Err: err}
	}

	c.logger.Debug("issue details updated", "id", id)
	return nil
}
