package tasks

import (
	"errors"
	"reflect"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestDetailModelRelationsDeterministicOrder(t *testing.T) {
	t.Parallel()

	model, err := NewDetailModel("az-1", []RelationEdge{
		{From: "c", To: "a", Relation: domain.RelationDiscoveredFrom},
		{From: "b", To: "a", Relation: domain.RelationBlocks},
		{From: "a", To: "b", Relation: domain.RelationDependsOn},
	})
	if err != nil {
		t.Fatalf("new detail model: %v", err)
	}

	got := model.Relations()
	want := []RelationEdge{
		{From: "a", To: "b", Relation: domain.RelationDependsOn},
		{From: "b", To: "a", Relation: domain.RelationBlocks},
		{From: "c", To: "a", Relation: domain.RelationDiscoveredFrom},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected sorted relations: got=%v want=%v", got, want)
	}
}

func TestDetailModelAddRelationValidation(t *testing.T) {
	t.Parallel()

	model, err := NewDetailModel("az-1", nil)
	if err != nil {
		t.Fatalf("new detail model: %v", err)
	}

	_, err = model.AddRelation(RelationEdge{From: "", To: "b", Relation: domain.RelationBlocks})
	if !errors.Is(err, ErrRelationEndpointRequired) {
		t.Fatalf("expected endpoint required error, got %v", err)
	}

	_, err = model.AddRelation(RelationEdge{From: "a", To: "a", Relation: domain.RelationBlocks})
	if !errors.Is(err, ErrRelationSelfReference) {
		t.Fatalf("expected self reference error, got %v", err)
	}

	_, err = model.AddRelation(RelationEdge{From: "a", To: "b", Relation: domain.Relation("weird")})
	if !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("expected invalid relation error, got %v", err)
	}
}

func TestDetailModelSafeRelationEditing(t *testing.T) {
	t.Parallel()

	model, err := NewDetailModel("az-1", nil)
	if err != nil {
		t.Fatalf("new detail model: %v", err)
	}

	model, err = model.AddRelation(RelationEdge{From: "a", To: "b", Relation: domain.RelationDependsOn})
	if err != nil {
		t.Fatalf("add relation: %v", err)
	}

	_, err = model.AddRelation(RelationEdge{From: "a", To: "b", Relation: domain.RelationDependsOn})
	if !errors.Is(err, ErrRelationAlreadyExists) {
		t.Fatalf("expected already exists error, got %v", err)
	}

	_, err = model.AddRelation(RelationEdge{From: "b", To: "a", Relation: domain.RelationBlocks})
	if !errors.Is(err, ErrRelationEquivalentExists) {
		t.Fatalf("expected equivalent relation error, got %v", err)
	}

	_, err = model.RemoveRelation(RelationEdge{From: "a", To: "c", Relation: domain.RelationBlocks})
	if !errors.Is(err, ErrRelationNotFound) {
		t.Fatalf("expected relation not found error, got %v", err)
	}

	model, err = model.RemoveRelation(RelationEdge{From: "a", To: "b", Relation: domain.RelationDependsOn})
	if err != nil {
		t.Fatalf("remove relation: %v", err)
	}

	if len(model.Relations()) != 0 {
		t.Fatalf("expected no relations after remove, got %v", model.Relations())
	}
}

func TestDetailPathwayTransitions(t *testing.T) {
	t.Parallel()

	model, err := NewDetailModel("az-1", nil)
	if err != nil {
		t.Fatalf("new detail model: %v", err)
	}

	model, err = model.TransitionPathway(PathwayAdd)
	if err != nil {
		t.Fatalf("transition to add: %v", err)
	}

	_, err = model.TransitionPathway(PathwayRemove)
	if !errors.Is(err, ErrInvalidPathwayTransition) {
		t.Fatalf("expected invalid path transition error, got %v", err)
	}

	model, err = model.TransitionPathway(PathwayInspect)
	if err != nil {
		t.Fatalf("transition to inspect: %v", err)
	}

	if model.Pathway() != PathwayInspect {
		t.Fatalf("expected inspect pathway, got %s", model.Pathway())
	}
}
