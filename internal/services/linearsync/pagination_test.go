package linearsync

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type pageCall struct {
	first    int
	after    string
	hasAfter bool
}

type fakeMetadataClient struct {
	workflowPages []Page[WorkflowState]
	labelPages    []Page[IssueLabel]

	workflowCalls []pageCall
	labelCalls    []pageCall

	workflowErr error
	labelErr    error
}

func (f *fakeMetadataClient) WorkflowStates(_ context.Context, first int, after *string) (Page[WorkflowState], error) {
	f.workflowCalls = append(f.workflowCalls, recordPageCall(first, after))
	if f.workflowErr != nil {
		return Page[WorkflowState]{}, f.workflowErr
	}
	if len(f.workflowCalls) > len(f.workflowPages) {
		return Page[WorkflowState]{}, fmt.Errorf("unexpected workflow page request %d", len(f.workflowCalls))
	}
	return f.workflowPages[len(f.workflowCalls)-1], nil
}

func (f *fakeMetadataClient) IssueLabels(_ context.Context, first int, after *string) (Page[IssueLabel], error) {
	f.labelCalls = append(f.labelCalls, recordPageCall(first, after))
	if f.labelErr != nil {
		return Page[IssueLabel]{}, f.labelErr
	}
	if len(f.labelCalls) > len(f.labelPages) {
		return Page[IssueLabel]{}, fmt.Errorf("unexpected label page request %d", len(f.labelCalls))
	}
	return f.labelPages[len(f.labelCalls)-1], nil
}

func recordPageCall(first int, after *string) pageCall {
	call := pageCall{first: first}
	if after != nil {
		call.after = *after
		call.hasAfter = true
	}
	return call
}

func cursor(value string) *string {
	v := value
	return &v
}

func TestCapPageSize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		requested int
		want      int
	}{
		{name: "zero", requested: 0, want: MaxPageSize},
		{name: "negative", requested: -10, want: MaxPageSize},
		{name: "under cap", requested: 120, want: 120},
		{name: "at cap", requested: MaxPageSize, want: MaxPageSize},
		{name: "over cap", requested: MaxPageSize + 1, want: MaxPageSize},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := CapPageSize(tc.requested); got != tc.want {
				t.Fatalf("CapPageSize(%d) = %d, want %d", tc.requested, got, tc.want)
			}
		})
	}
}

func TestFetchWorkflowStateNameByIDCapsAndTraversesPages(t *testing.T) {
	t.Parallel()

	client := &fakeMetadataClient{
		workflowPages: []Page[WorkflowState]{
			{
				Nodes: []WorkflowState{
					{ID: "ws-1", Name: "Backlog"},
					{ID: "ws-2", Name: "In Progress"},
				},
				PageInfo: PageInfo{HasNextPage: true, EndCursor: cursor("cursor-1")},
			},
			{
				Nodes: []WorkflowState{
					{ID: "ws-3", Name: "Review"},
				},
				PageInfo: PageInfo{HasNextPage: true, EndCursor: cursor("cursor-2")},
			},
			{
				Nodes: []WorkflowState{
					{ID: "ws-4", Name: "Done"},
				},
				PageInfo: PageInfo{HasNextPage: false},
			},
		},
	}

	got, err := FetchWorkflowStateNameByID(context.Background(), client)
	if err != nil {
		t.Fatalf("FetchWorkflowStateNameByID: %v", err)
	}

	want := map[string]string{
		"ws-1": "Backlog",
		"ws-2": "In Progress",
		"ws-3": "Review",
		"ws-4": "Done",
	}
	if len(got) != len(want) {
		t.Fatalf("workflow index length = %d, want %d: %+v", len(got), len(want), got)
	}
	for id, name := range want {
		if got[id] != name {
			t.Fatalf("workflow index[%q] = %q, want %q", id, got[id], name)
		}
	}

	if len(client.workflowCalls) != 3 {
		t.Fatalf("workflow call count = %d, want 3", len(client.workflowCalls))
	}
	for i, call := range client.workflowCalls {
		if call.first != MaxPageSize {
			t.Fatalf("workflow call %d first = %d, want %d", i, call.first, MaxPageSize)
		}
	}
	if client.workflowCalls[0].hasAfter {
		t.Fatalf("first workflow page unexpectedly set after cursor: %+v", client.workflowCalls[0])
	}
	if !client.workflowCalls[1].hasAfter || client.workflowCalls[1].after != "cursor-1" {
		t.Fatalf("second workflow page after = %+v, want cursor-1", client.workflowCalls[1])
	}
	if !client.workflowCalls[2].hasAfter || client.workflowCalls[2].after != "cursor-2" {
		t.Fatalf("third workflow page after = %+v, want cursor-2", client.workflowCalls[2])
	}
}

func TestFetchIssueLabelNameByIDCapsAndTraversesPages(t *testing.T) {
	t.Parallel()

	client := &fakeMetadataClient{
		labelPages: []Page[IssueLabel]{
			{
				Nodes: []IssueLabel{
					{ID: "lb-1", Name: "bug"},
				},
				PageInfo: PageInfo{HasNextPage: true, EndCursor: cursor("cursor-a")},
			},
			{
				Nodes: []IssueLabel{
					{ID: "lb-2", Name: "ops"},
					{ID: "lb-3", Name: "priority"},
				},
				PageInfo: PageInfo{HasNextPage: false},
			},
		},
	}

	got, err := FetchIssueLabelNameByID(context.Background(), client)
	if err != nil {
		t.Fatalf("FetchIssueLabelNameByID: %v", err)
	}

	want := map[string]string{
		"lb-1": "bug",
		"lb-2": "ops",
		"lb-3": "priority",
	}
	if len(got) != len(want) {
		t.Fatalf("label index length = %d, want %d: %+v", len(got), len(want), got)
	}
	for id, name := range want {
		if got[id] != name {
			t.Fatalf("label index[%q] = %q, want %q", id, got[id], name)
		}
	}

	if len(client.labelCalls) != 2 {
		t.Fatalf("label call count = %d, want 2", len(client.labelCalls))
	}
	for i, call := range client.labelCalls {
		if call.first != MaxPageSize {
			t.Fatalf("label call %d first = %d, want %d", i, call.first, MaxPageSize)
		}
	}
	if client.labelCalls[0].hasAfter {
		t.Fatalf("first label page unexpectedly set after cursor: %+v", client.labelCalls[0])
	}
	if !client.labelCalls[1].hasAfter || client.labelCalls[1].after != "cursor-a" {
		t.Fatalf("second label page after = %+v, want cursor-a", client.labelCalls[1])
	}
}

func TestCollectAllStopsWhenCursorIsMissing(t *testing.T) {
	t.Parallel()

	calls := 0
	records, err := CollectAll(context.Background(), 999, func(_ context.Context, first int, after *string) (Page[int], error) {
		calls++
		if first != MaxPageSize {
			return Page[int]{}, errors.New("page size was not capped")
		}
		if after != nil {
			return Page[int]{}, errors.New("unexpected cursor continuation")
		}
		return Page[int]{
			Nodes: []int{1, 2},
			PageInfo: PageInfo{
				HasNextPage: true,
				EndCursor:   nil,
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("CollectAll: %v", err)
	}
	if calls != 1 {
		t.Fatalf("CollectAll call count = %d, want 1", calls)
	}
	if len(records) != 2 {
		t.Fatalf("CollectAll records = %v, want 2 items", records)
	}
}
