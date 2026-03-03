package planning

import (
	"errors"
	"reflect"
	"testing"
)

func TestLifecycleTransitions(t *testing.T) {
	t.Parallel()

	planner := NewPlanner(validGraph())

	if err := planner.Transition(StateExecution); err != nil {
		t.Fatalf("transition to execution: %v", err)
	}
	if err := planner.Transition(StateReview); err != nil {
		t.Fatalf("transition to review: %v", err)
	}
	if err := planner.Transition(StateResult); err != nil {
		t.Fatalf("transition to result: %v", err)
	}

	if planner.State() != StateResult {
		t.Fatalf("expected result state, got %s", planner.State())
	}

	if err := planner.Transition(StateExecution); !errors.Is(err, ErrInvalidLifecycleTransition) {
		t.Fatalf("expected invalid transition error, got %v", err)
	}
}

func TestCycleDetection(t *testing.T) {
	t.Parallel()

	planner := NewPlanner(Graph{
		Nodes: []Node{{ID: "A"}, {ID: "B"}, {ID: "C"}},
		Relationships: []Relationship{
			{From: "A", To: "B", Type: RelationDependsOn},
			{From: "B", To: "C", Type: RelationDependsOn},
			{From: "C", To: "A", Type: RelationDependsOn},
		},
	})

	err := planner.ValidateGraph()
	if !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

func TestInvalidRelationDirectionRejected(t *testing.T) {
	t.Parallel()

	planner := NewPlanner(Graph{
		Nodes: []Node{{ID: "A"}, {ID: "B"}},
		Relationships: []Relationship{
			{From: "A", To: "B", Type: RelationDependsOn},
			{From: "A", To: "B", Type: RelationBlocks},
		},
	})

	err := planner.ValidateGraph()
	if !errors.Is(err, ErrInvalidRelationDirection) {
		t.Fatalf("expected invalid relation direction, got %v", err)
	}
}

func TestScopeSwitchingDeterministic(t *testing.T) {
	t.Parallel()

	planner := NewPlanner(Graph{
		Nodes: []Node{{ID: "A"}, {ID: "B"}, {ID: "C"}, {ID: "D"}},
		Relationships: []Relationship{
			{From: "D", To: "B", Type: RelationDependsOn},
			{From: "C", To: "B", Type: RelationDependsOn},
			{From: "B", To: "A", Type: RelationDependsOn},
		},
	})

	if err := planner.SwitchScope(ScopeUpstream, "D"); err != nil {
		t.Fatalf("switch scope: %v", err)
	}

	first := planner.ScopedRelationships()
	second := planner.ScopedRelationships()

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("expected deterministic scope output; first=%v second=%v", first, second)
	}

	expected := []Relationship{
		{From: "B", To: "A", Type: RelationDependsOn},
		{From: "D", To: "B", Type: RelationDependsOn},
	}
	if !reflect.DeepEqual(first, expected) {
		t.Fatalf("unexpected upstream scope result: %v", first)
	}
}

func validGraph() Graph {
	return Graph{
		Nodes: []Node{{ID: "A"}, {ID: "B"}},
		Relationships: []Relationship{
			{From: "B", To: "A", Type: RelationDependsOn},
		},
	}
}
