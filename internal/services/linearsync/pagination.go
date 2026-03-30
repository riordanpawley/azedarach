package linearsync

import (
	"context"
	"strings"
)

const MaxPageSize = 250

type PageInfo struct {
	HasNextPage bool
	EndCursor   *string
}

type Page[T any] struct {
	Nodes    []T
	PageInfo PageInfo
}

type WorkflowState struct {
	ID   string
	Name string
}

type IssueLabel struct {
	ID   string
	Name string
}

type MetadataClient interface {
	WorkflowStates(context.Context, int, *string) (Page[WorkflowState], error)
	IssueLabels(context.Context, int, *string) (Page[IssueLabel], error)
}

func CapPageSize(requested int) int {
	if requested <= 0 || requested > MaxPageSize {
		return MaxPageSize
	}
	return requested
}

func CollectAll[T any](
	ctx context.Context,
	requestedPageSize int,
	fetch func(context.Context, int, *string) (Page[T], error),
) ([]T, error) {
	pageSize := CapPageSize(requestedPageSize)
	var after *string
	records := make([]T, 0, pageSize)

	for {
		page, err := fetch(ctx, pageSize, after)
		if err != nil {
			return nil, err
		}

		records = append(records, page.Nodes...)

		if !page.PageInfo.HasNextPage || page.PageInfo.EndCursor == nil {
			return records, nil
		}

		nextCursor := strings.TrimSpace(*page.PageInfo.EndCursor)
		if nextCursor == "" {
			return records, nil
		}
		after = &nextCursor
	}
}

func FetchWorkflowStateNameByID(ctx context.Context, client MetadataClient) (map[string]string, error) {
	return collectNameIndex(ctx, client.WorkflowStates, func(state WorkflowState) (string, string) {
		return state.ID, state.Name
	})
}

func FetchIssueLabelNameByID(ctx context.Context, client MetadataClient) (map[string]string, error) {
	return collectNameIndex(ctx, client.IssueLabels, func(label IssueLabel) (string, string) {
		return label.ID, label.Name
	})
}

func collectNameIndex[T any](
	ctx context.Context,
	fetch func(context.Context, int, *string) (Page[T], error),
	project func(T) (string, string),
) (map[string]string, error) {
	records, err := CollectAll(ctx, MaxPageSize, fetch)
	if err != nil {
		return nil, err
	}

	index := make(map[string]string, len(records))
	for _, record := range records {
		id, name := project(record)
		if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" {
			continue
		}
		index[id] = name
	}

	return index, nil
}
