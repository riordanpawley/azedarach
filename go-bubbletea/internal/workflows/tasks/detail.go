package tasks

import (
	"errors"
	"fmt"
	"sort"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type DetailPathway string

const (
	PathwayInspect DetailPathway = "inspect"
	PathwayAdd     DetailPathway = "add_relation"
	PathwayRemove  DetailPathway = "remove_relation"
)

type RelationEdge struct {
	From     string
	To       string
	Relation domain.Relation
}

type DetailModel struct {
	taskID    string
	pathway   DetailPathway
	relations []RelationEdge
}

var (
	ErrInvalidPathway           = errors.New("invalid detail pathway")
	ErrInvalidPathwayTransition = errors.New("invalid detail pathway transition")
	ErrInvalidRelation          = errors.New("invalid relation")
	ErrRelationEndpointRequired = errors.New("relation endpoints are required")
	ErrRelationSelfReference    = errors.New("self relation is not allowed")
	ErrRelationAlreadyExists    = errors.New("relation already exists")
	ErrRelationEquivalentExists = errors.New("equivalent relation already exists")
	ErrRelationNotFound         = errors.New("relation not found")
)

func NewDetailModel(taskID string, existing []RelationEdge) (DetailModel, error) {
	m := DetailModel{taskID: taskID, pathway: PathwayInspect}
	for _, rel := range existing {
		if err := m.validateRelation(rel); err != nil {
			return DetailModel{}, err
		}
		m.relations = append(m.relations, rel)
	}
	m.sortRelations()
	return m, nil
}

func (m DetailModel) TaskID() string {
	return m.taskID
}

func (m DetailModel) Pathway() DetailPathway {
	return m.pathway
}

func (m DetailModel) Relations() []RelationEdge {
	out := make([]RelationEdge, len(m.relations))
	copy(out, m.relations)
	return out
}

func (m DetailModel) TransitionPathway(next DetailPathway) (DetailModel, error) {
	if !next.valid() {
		return m, fmt.Errorf("%w: %s", ErrInvalidPathway, next)
	}
	if m.pathway == next {
		return m, nil
	}
	if !isPathTransitionAllowed(m.pathway, next) {
		return m, fmt.Errorf("%w: %s -> %s", ErrInvalidPathwayTransition, m.pathway, next)
	}
	m.pathway = next
	return m, nil
}

func (m DetailModel) AddRelation(rel RelationEdge) (DetailModel, error) {
	if err := m.validateRelation(rel); err != nil {
		return m, err
	}

	for _, existing := range m.relations {
		if existing == rel {
			return m, fmt.Errorf("%w: %s %s -> %s", ErrRelationAlreadyExists, rel.Relation, rel.From, rel.To)
		}
		if isEquivalentDependency(existing, rel) {
			return m, fmt.Errorf("%w: %s %s -> %s", ErrRelationEquivalentExists, rel.Relation, rel.From, rel.To)
		}
	}

	m.relations = append(m.relations, rel)
	m.sortRelations()
	return m, nil
}

func (m DetailModel) RemoveRelation(rel RelationEdge) (DetailModel, error) {
	if err := m.validateRelation(rel); err != nil {
		return m, err
	}

	idx := -1
	for i := range m.relations {
		if m.relations[i] == rel {
			idx = i
			break
		}
	}
	if idx < 0 {
		return m, fmt.Errorf("%w: %s %s -> %s", ErrRelationNotFound, rel.Relation, rel.From, rel.To)
	}

	next := make([]RelationEdge, 0, len(m.relations)-1)
	next = append(next, m.relations[:idx]...)
	next = append(next, m.relations[idx+1:]...)
	m.relations = next
	m.sortRelations()
	return m, nil
}

func (m DetailModel) validateRelation(rel RelationEdge) error {
	if rel.From == "" || rel.To == "" {
		return ErrRelationEndpointRequired
	}
	if rel.From == rel.To {
		return ErrRelationSelfReference
	}
	if !rel.Relation.Valid() {
		return fmt.Errorf("%w: %s", ErrInvalidRelation, rel.Relation)
	}
	return nil
}

func (m *DetailModel) sortRelations() {
	sort.Slice(m.relations, func(i, j int) bool {
		if m.relations[i].From != m.relations[j].From {
			return m.relations[i].From < m.relations[j].From
		}
		if m.relations[i].To != m.relations[j].To {
			return m.relations[i].To < m.relations[j].To
		}
		return m.relations[i].Relation < m.relations[j].Relation
	})
}

func (p DetailPathway) valid() bool {
	switch p {
	case PathwayInspect, PathwayAdd, PathwayRemove:
		return true
	default:
		return false
	}
}

func isPathTransitionAllowed(current DetailPathway, next DetailPathway) bool {
	switch current {
	case PathwayInspect:
		return next == PathwayAdd || next == PathwayRemove
	case PathwayAdd, PathwayRemove:
		return next == PathwayInspect
	default:
		return false
	}
}

func isEquivalentDependency(a RelationEdge, b RelationEdge) bool {
	caFrom, caTo, caOK := canonicalDependency(a)
	cbFrom, cbTo, cbOK := canonicalDependency(b)
	if !caOK || !cbOK {
		return false
	}
	return caFrom == cbFrom && caTo == cbTo
}

func canonicalDependency(rel RelationEdge) (from string, to string, ok bool) {
	switch rel.Relation {
	case domain.RelationBlocks:
		return rel.From, rel.To, true
	case domain.RelationDependsOn:
		return rel.To, rel.From, true
	default:
		return "", "", false
	}
}
