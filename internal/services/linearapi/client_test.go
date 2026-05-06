package linearapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
	issues, err := client.ListIssues(context.Background(), "CHE", "Chefy")
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
	if _, err := client.ListIssues(context.Background(), "CHE", ""); err != nil {
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
