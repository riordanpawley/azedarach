package linearapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultEndpoint = "https://api.linear.app/graphql"

const MaxIssueUpdateBatchSize = 25

type Client struct {
	Endpoint   string
	APIKey     string
	HTTPClient *http.Client

	mu      sync.Mutex
	metrics Metrics
}

type Issue struct {
	ID          string
	Identifier  string
	Title       string
	Description string
	URL         string
	Priority    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	State       State
	Assignee    Assignee
	Labels      []string
	ProjectName string
}

type State struct {
	Name string
	Type string
}

type Assignee struct {
	Name  string
	Email string
}

type IssueInput struct {
	Title       string
	Description *string
	Priority    *int
}

type IssueUpdateRequest struct {
	ID    string
	Input IssueInput
}

type RateLimit struct {
	Limit     int       `json:"limit,omitempty"`
	Remaining int       `json:"remaining,omitempty"`
	Reset     time.Time `json:"reset,omitempty"`
}

type Metrics struct {
	RequestCount int       `json:"request_count"`
	RateLimit    RateLimit `json:"rate_limit"`
}

type APIError struct {
	StatusCode int
	Body       string
	RateLimit  RateLimit
}

func (e *APIError) Error() string {
	return fmt.Sprintf("linear graphql status %d: %s", e.StatusCode, strings.TrimSpace(e.Body))
}

func (e *APIError) Retryable() bool {
	return e != nil && (e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500)
}

type ListIssuesOptions struct {
	TeamKey      string
	Project      string
	AssigneeID   string
	UpdatedAfter *time.Time
}

func (c *Client) ListIssues(ctx context.Context, opts ListIssuesOptions) ([]Issue, error) {
	filter := map[string]any{}
	if strings.TrimSpace(opts.TeamKey) != "" {
		filter["team"] = map[string]any{"key": map[string]any{"eq": strings.TrimSpace(opts.TeamKey)}}
	}
	if strings.TrimSpace(opts.Project) != "" {
		filter["project"] = map[string]any{"name": map[string]any{"eq": strings.TrimSpace(opts.Project)}}
	}
	if strings.TrimSpace(opts.AssigneeID) != "" {
		filter["assignee"] = map[string]any{"id": map[string]any{"eq": strings.TrimSpace(opts.AssigneeID)}}
	}
	if opts.UpdatedAfter != nil && !opts.UpdatedAfter.IsZero() {
		filter["updatedAt"] = map[string]any{"gt": opts.UpdatedAfter.UTC().Format(time.RFC3339Nano)}
	}
	query := `
query AzedarachIssues($filter: IssueFilter, $after: String) {
  issues(first: 50, after: $after, filter: $filter, orderBy: updatedAt) {
    nodes {
      id
      identifier
      title
      description
      url
      priority
      createdAt
      updatedAt
      state { name type }
      assignee { name email }
      labels { nodes { name } }
      project { name }
    }
    pageInfo { hasNextPage endCursor }
  }
}`
	var all []Issue
	var after *string
	for {
		var out struct {
			Issues struct {
				Nodes    []issueNode `json:"nodes"`
				PageInfo struct {
					HasNextPage bool    `json:"hasNextPage"`
					EndCursor   *string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"issues"`
		}
		if err := c.graphql(ctx, query, map[string]any{"filter": filter, "after": after}, &out); err != nil {
			return nil, err
		}
		for _, node := range out.Issues.Nodes {
			all = append(all, node.toIssue())
		}
		if !out.Issues.PageInfo.HasNextPage || out.Issues.PageInfo.EndCursor == nil {
			break
		}
		after = out.Issues.PageInfo.EndCursor
	}
	return all, nil
}

func (c *Client) ViewerID(ctx context.Context) (string, error) {
	query := `
query AzedarachViewer {
  viewer { id }
}`
	var out struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
	}
	if err := c.graphql(ctx, query, map[string]any{}, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Viewer.ID) == "" {
		return "", errors.New("linear viewer id missing")
	}
	return strings.TrimSpace(out.Viewer.ID), nil
}

func (c *Client) UpdateIssue(ctx context.Context, id string, input IssueInput) (Issue, error) {
	updated, err := c.UpdateIssues(ctx, []IssueUpdateRequest{{ID: id, Input: input}})
	if err != nil {
		return Issue{}, err
	}
	if len(updated) != 1 {
		return Issue{}, errors.New("linear issue update response missing issue")
	}
	return updated[0], nil
}

func (c *Client) UpdateIssues(ctx context.Context, requests []IssueUpdateRequest) ([]Issue, error) {
	cleaned := make([]IssueUpdateRequest, 0, len(requests))
	for _, req := range requests {
		req.ID = strings.TrimSpace(req.ID)
		if req.ID == "" {
			return nil, errors.New("linear issue update id is required")
		}
		cleaned = append(cleaned, req)
	}
	if len(cleaned) == 0 {
		return nil, nil
	}
	if len(cleaned) > MaxIssueUpdateBatchSize {
		return nil, fmt.Errorf("linear issue update batch size %d exceeds max %d", len(cleaned), MaxIssueUpdateBatchSize)
	}
	return c.updateIssuesSingleRequest(ctx, cleaned)
}

func (c *Client) updateIssuesSingleRequest(ctx context.Context, requests []IssueUpdateRequest) ([]Issue, error) {
	vars := map[string]any{}
	var varDefs strings.Builder
	var fields strings.Builder
	for i, req := range requests {
		if i > 0 {
			varDefs.WriteString(", ")
			fields.WriteString("\n")
		}
		idName := fmt.Sprintf("id%d", i)
		inputName := fmt.Sprintf("input%d", i)
		alias := fmt.Sprintf("u%d", i)
		varDefs.WriteString(fmt.Sprintf("$%s: String!, $%s: IssueUpdateInput!", idName, inputName))
		fields.WriteString(fmt.Sprintf(`  %s: issueUpdate(id: $%s, input: $%s) {
    success
    issue {
      id
      identifier
      title
      description
      url
      priority
      createdAt
      updatedAt
      state { name type }
      assignee { name email }
      labels { nodes { name } }
      project { name }
    }
  }`, alias, idName, inputName))
		vars[idName] = req.ID
		vars[inputName] = inputMap(req.Input)
	}
	query := fmt.Sprintf("mutation AzedarachUpdateIssues(%s) {\n%s\n}", varDefs.String(), fields.String())
	out := map[string]struct {
		Success bool      `json:"success"`
		Issue   issueNode `json:"issue"`
	}{}
	if err := c.graphql(ctx, query, vars, &out); err != nil {
		return nil, err
	}
	issues := make([]Issue, 0, len(requests))
	for i := range requests {
		alias := fmt.Sprintf("u%d", i)
		payload, ok := out[alias]
		if !ok {
			return nil, fmt.Errorf("linear issue update response missing %s", alias)
		}
		if !payload.Success {
			return nil, fmt.Errorf("linear issue update %s was not successful", requests[i].ID)
		}
		issues = append(issues, payload.Issue.toIssue())
	}
	return issues, nil
}

func (c *Client) graphql(ctx context.Context, query string, variables map[string]any, out any) error {
	endpoint := strings.TrimSpace(c.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	apiKey := strings.TrimSpace(c.APIKey)
	if apiKey == "" {
		return errors.New("LINEAR_API_KEY is required")
	}
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("marshal linear graphql request: %w", err)
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create linear graphql request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("linear graphql request: %w", err)
	}
	defer resp.Body.Close()
	rateLimit := parseRateLimit(resp.Header)
	c.recordRequest(rateLimit)
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read linear graphql response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(respBody)), RateLimit: rateLimit}
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("decode linear graphql response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("linear graphql error: %s", envelope.Errors[0].Message)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("linear graphql response missing data")
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decode linear graphql data: %w", err)
	}
	return nil
}

func (c *Client) Metrics() Metrics {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.metrics
}

func (c *Client) recordRequest(rateLimit RateLimit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metrics.RequestCount++
	if rateLimit != (RateLimit{}) {
		c.metrics.RateLimit = rateLimit
	}
}

func parseRateLimit(header http.Header) RateLimit {
	return RateLimit{
		Limit:     parseHeaderInt(header.Get("X-RateLimit-Requests-Limit")),
		Remaining: parseHeaderInt(header.Get("X-RateLimit-Requests-Remaining")),
		Reset:     parseHeaderEpochMillis(header.Get("X-RateLimit-Requests-Reset")),
	}
}

func parseHeaderInt(raw string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(raw))
	return value
}

func parseHeaderEpochMillis(raw string) time.Time {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

func IsRetryable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Retryable()
}

type issueNode struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Priority    int    `json:"priority"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	State       struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Assignee struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"assignee"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Project struct {
		Name string `json:"name"`
	} `json:"project"`
}

func (n issueNode) toIssue() Issue {
	labels := make([]string, 0, len(n.Labels.Nodes))
	for _, label := range n.Labels.Nodes {
		if strings.TrimSpace(label.Name) != "" {
			labels = append(labels, strings.TrimSpace(label.Name))
		}
	}
	return Issue{
		ID:          strings.TrimSpace(n.ID),
		Identifier:  strings.TrimSpace(n.Identifier),
		Title:       strings.TrimSpace(n.Title),
		Description: strings.TrimSpace(n.Description),
		URL:         strings.TrimSpace(n.URL),
		Priority:    n.Priority,
		CreatedAt:   parseTime(n.CreatedAt),
		UpdatedAt:   parseTime(n.UpdatedAt),
		State:       State{Name: strings.TrimSpace(n.State.Name), Type: strings.TrimSpace(n.State.Type)},
		Assignee:    Assignee{Name: strings.TrimSpace(n.Assignee.Name), Email: strings.TrimSpace(n.Assignee.Email)},
		Labels:      labels,
		ProjectName: strings.TrimSpace(n.Project.Name),
	}
}

func parseTime(raw string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw)); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func inputMap(input IssueInput) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(input.Title) != "" {
		out["title"] = strings.TrimSpace(input.Title)
	}
	if input.Description != nil {
		out["description"] = *input.Description
	}
	if input.Priority != nil {
		out["priority"] = *input.Priority
	}
	return out
}
