package linearapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestClientListIssuesUsesFilterAndAuthorization(t *testing.T) {
	var gotAuth string
	var gotVariables map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotVariables); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"issues": {
					"nodes": [{
						"id": "lin-1",
						"identifier": "CHE-1",
						"title": "Mirror issue",
						"description": "Body",
						"url": "https://linear.app/acme/issue/CHE-1",
						"priority": 2,
						"createdAt": "2026-05-05T00:00:00Z",
						"updatedAt": "2026-05-05T01:00:00Z",
						"state": {"name": "In Progress", "type": "started"},
						"assignee": {"name": "Ada", "email": "ada@example.com"},
						"labels": {"nodes": [{"name": "Bug"}]},
						"project": {"name": "Chefy"}
					}],
					"pageInfo": {"hasNextPage": false, "endCursor": null}
				}
			}
		}`))
	}))
	defer server.Close()

	client := &Client{Endpoint: server.URL, APIKey: "lin_api_test", HTTPClient: server.Client()}
	issues, err := client.ListIssues(context.Background(), ListIssuesOptions{TeamKey: "CHE", Project: "Chefy"})
	if err != nil {
		t.Fatalf("ListIssues() error = %v", err)
	}
	if gotAuth != "lin_api_test" {
		t.Fatalf("Authorization header = %q", gotAuth)
	}
	variables, ok := gotVariables["variables"].(map[string]any)
	if !ok {
		t.Fatalf("variables missing: %#v", gotVariables)
	}
	if variables["filter"] == nil {
		t.Fatalf("filter missing: %#v", variables)
	}
	if len(issues) != 1 || issues[0].Identifier != "CHE-1" || issues[0].Assignee.Email != "ada@example.com" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestClientListIssuesOmitsProjectFilterWhenProjectNameBlank(t *testing.T) {
	var gotVariables map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotVariables); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"issues": {
					"nodes": [],
					"pageInfo": {"hasNextPage": false, "endCursor": null}
				}
			}
		}`))
	}))
	defer server.Close()

	client := &Client{Endpoint: server.URL, APIKey: "lin_api_test", HTTPClient: server.Client()}
	if _, err := client.ListIssues(context.Background(), ListIssuesOptions{TeamKey: "CHE"}); err != nil {
		t.Fatalf("ListIssues() error = %v", err)
	}
	variables, ok := gotVariables["variables"].(map[string]any)
	if !ok {
		t.Fatalf("variables missing: %#v", gotVariables)
	}
	filter, ok := variables["filter"].(map[string]any)
	if !ok {
		t.Fatalf("filter missing: %#v", variables)
	}
	if _, ok := filter["team"]; !ok {
		t.Fatalf("team filter missing: %#v", filter)
	}
	if _, ok := filter["project"]; ok {
		t.Fatalf("project filter should be omitted when project name is blank: %#v", filter)
	}
}

func TestClientListIssuesAppliesAssigneeFilter(t *testing.T) {
	var gotVariables map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotVariables); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"issues": {
					"nodes": [],
					"pageInfo": {"hasNextPage": false, "endCursor": null}
				}
			}
		}`))
	}))
	defer server.Close()

	client := &Client{Endpoint: server.URL, APIKey: "lin_api_test", HTTPClient: server.Client()}
	if _, err := client.ListIssues(context.Background(), ListIssuesOptions{TeamKey: "CHE", Project: "Chefy", AssigneeID: "usr_123"}); err != nil {
		t.Fatalf("ListIssues() error = %v", err)
	}
	variables, ok := gotVariables["variables"].(map[string]any)
	if !ok {
		t.Fatalf("variables missing: %#v", gotVariables)
	}
	filter, ok := variables["filter"].(map[string]any)
	if !ok {
		t.Fatalf("filter missing: %#v", variables)
	}
	assignee, ok := filter["assignee"].(map[string]any)
	if !ok {
		t.Fatalf("assignee filter missing: %#v", filter)
	}
	id, ok := assignee["id"].(map[string]any)
	if !ok || id["eq"] != "usr_123" {
		t.Fatalf("assignee id filter = %#v", assignee)
	}
}

func TestClientListIssuesAppliesUpdatedAfterFilter(t *testing.T) {
	var gotVariables map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotVariables); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`))
	}))
	defer server.Close()

	updatedAfter := time.Date(2026, 5, 5, 1, 2, 3, 4, time.UTC)
	client := &Client{Endpoint: server.URL, APIKey: "lin_api_test", HTTPClient: server.Client()}
	if _, err := client.ListIssues(context.Background(), ListIssuesOptions{TeamKey: "CHE", UpdatedAfter: &updatedAfter}); err != nil {
		t.Fatalf("ListIssues() error = %v", err)
	}
	variables := gotVariables["variables"].(map[string]any)
	filter := variables["filter"].(map[string]any)
	updatedAt, ok := filter["updatedAt"].(map[string]any)
	if !ok {
		t.Fatalf("updatedAt filter missing: %#v", filter)
	}
	if got, want := updatedAt["gt"], updatedAfter.Format(time.RFC3339Nano); got != want {
		t.Fatalf("updatedAt gt = %v, want %s", got, want)
	}
}

func TestClientViewerIDUsesActiveAPIKey(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"viewer":{"id":"usr_viewer"}}}`))
	}))
	defer server.Close()

	client := &Client{Endpoint: server.URL, APIKey: "lin_api_test", HTTPClient: server.Client()}
	viewerID, err := client.ViewerID(context.Background())
	if err != nil {
		t.Fatalf("ViewerID() error = %v", err)
	}
	if viewerID != "usr_viewer" {
		t.Fatalf("viewer id = %q, want usr_viewer", viewerID)
	}
	if gotAuth != "lin_api_test" {
		t.Fatalf("Authorization header = %q", gotAuth)
	}
}

func TestClientRecordsRateLimitHeaders(t *testing.T) {
	reset := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Requests-Limit", "5000")
		w.Header().Set("X-RateLimit-Requests-Remaining", "4999")
		w.Header().Set("X-RateLimit-Requests-Reset", strconv.FormatInt(reset.UnixMilli(), 10))
		_, _ = w.Write([]byte(`{"data":{"viewer":{"id":"usr_viewer"}}}`))
	}))
	defer server.Close()

	client := &Client{Endpoint: server.URL, APIKey: "lin_api_test", HTTPClient: server.Client()}
	if _, err := client.ViewerID(context.Background()); err != nil {
		t.Fatalf("ViewerID() error = %v", err)
	}
	metrics := client.Metrics()
	if metrics.RequestCount != 1 {
		t.Fatalf("request count = %d, want 1", metrics.RequestCount)
	}
	if metrics.RateLimit.Limit != 5000 || metrics.RateLimit.Remaining != 4999 || !metrics.RateLimit.Reset.Equal(reset) {
		t.Fatalf("rate limit = %+v", metrics.RateLimit)
	}
}

func TestClientReturnsRetryableAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`rate limited`))
	}))
	defer server.Close()

	client := &Client{Endpoint: server.URL, APIKey: "lin_api_test", HTTPClient: server.Client()}
	_, err := client.ViewerID(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v, want APIError", err, err)
	}
	if !IsRetryable(err) {
		t.Fatalf("expected retryable error: %v", err)
	}
}
