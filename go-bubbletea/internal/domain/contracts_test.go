package domain

import "testing"

func TestModeValid(t *testing.T) {
	tests := []struct {
		name  string
		value Mode
		want  bool
	}{
		{name: "normal", value: ModeNormal, want: true},
		{name: "action", value: ModeAction, want: true},
		{name: "goto", value: ModeGoto, want: true},
		{name: "select", value: ModeSelect, want: true},
		{name: "search", value: ModeSearch, want: true},
		{name: "filter", value: ModeFilter, want: true},
		{name: "sort", value: ModeSort, want: true},
		{name: "invalid", value: Mode("x"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.value.Valid(); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestViewModeValid(t *testing.T) {
	tests := []struct {
		name  string
		value ViewMode
		want  bool
	}{
		{name: "kanban", value: ViewKanban, want: true},
		{name: "list", value: ViewList, want: true},
		{name: "invalid", value: ViewMode("x"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.value.Valid(); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestStatusValid(t *testing.T) {
	tests := []struct {
		name  string
		value Status
		want  bool
	}{
		{name: "open", value: StatusOpen, want: true},
		{name: "in_progress", value: StatusInProgress, want: true},
		{name: "blocked", value: StatusBlocked, want: true},
		{name: "done", value: StatusDone, want: true},
		{name: "invalid", value: Status("x"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.value.Valid(); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestRelationValid(t *testing.T) {
	tests := []struct {
		name  string
		value Relation
		want  bool
	}{
		{name: "blocks", value: RelationBlocks, want: true},
		{name: "depends_on", value: RelationDependsOn, want: true},
		{name: "discovered_from", value: RelationDiscoveredFrom, want: true},
		{name: "invalid", value: Relation("x"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.value.Valid(); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestOperationStateValid(t *testing.T) {
	tests := []struct {
		name  string
		value OperationState
		want  bool
	}{
		{name: "queued", value: OperationQueued, want: true},
		{name: "running", value: OperationRunning, want: true},
		{name: "succeeded", value: OperationSucceeded, want: true},
		{name: "failed", value: OperationFailed, want: true},
		{name: "cancelled", value: OperationCancelled, want: true},
		{name: "rolled_back", value: OperationRolledBack, want: true},
		{name: "invalid", value: OperationState("x"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.value.Valid(); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestSortFieldValid(t *testing.T) {
	tests := []struct {
		name  string
		value SortField
		want  bool
	}{
		{name: "priority", value: SortByPriority, want: true},
		{name: "title", value: SortByTitle, want: true},
		{name: "id", value: SortByID, want: true},
		{name: "invalid", value: SortField("x"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.value.Valid(); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}
