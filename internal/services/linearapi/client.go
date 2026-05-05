package linearapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultEndpoint = "https://api.linear.app/graphql"

type Client struct {
	Endpoint   string
	APIKey     string
	HTTPClient *http.Client
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

func (c *Client) ListIssues(ctx context.Context, teamKey, projectName string) ([]Issue, error) {
	filter := map[string]any{}
	if strings.TrimSpace(teamKey) != "" {
		filter["team"] = map[string]any{"key": map[string]any{"eq": strings.TrimSpace(teamKey)}}
	}
	if strings.TrimSpace(projectName) != "" {
		filter["project"] = map[string]any{"name": map[string]any{"eq": strings.TrimSpace(projectName)}}
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

func (c *Client) UpdateIssue(ctx context.Context, id string, input IssueInput) (Issue, error) {
	query := `
mutation AzedarachUpdateIssue($id: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $id, input: $input) {
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
  }
}`
	var out struct {
		IssueUpdate struct {
			Success bool      `json:"success"`
			Issue   issueNode `json:"issue"`
		} `json:"issueUpdate"`
	}
	if err := c.graphql(ctx, query, map[string]any{"id": strings.TrimSpace(id), "input": inputMap(input)}, &out); err != nil {
		return Issue{}, err
	}
	if !out.IssueUpdate.Success {
		return Issue{}, errors.New("linear issue update was not successful")
	}
	return out.IssueUpdate.Issue.toIssue(), nil
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
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read linear graphql response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("linear graphql status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
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
