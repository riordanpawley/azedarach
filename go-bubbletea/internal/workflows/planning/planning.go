package planning

import (
	"errors"
	"fmt"
	"sort"
)

type LifecycleState string

const (
	StateInput     LifecycleState = "input"
	StateExecution LifecycleState = "execution"
	StateReview    LifecycleState = "review"
	StateResult    LifecycleState = "result"
)

type RelationType string

const (
	RelationDependsOn RelationType = "depends_on"
	RelationBlocks    RelationType = "blocks"
)

type RelationshipScope string

const (
	ScopeAll        RelationshipScope = "all"
	ScopeNode       RelationshipScope = "node"
	ScopeUpstream   RelationshipScope = "upstream"
	ScopeDownstream RelationshipScope = "downstream"
)

var (
	ErrInvalidLifecycleTransition = errors.New("invalid lifecycle transition")
	ErrUnknownRelationType        = errors.New("unknown relation type")
	ErrUnknownNode                = errors.New("unknown node")
	ErrInvalidRelationDirection   = errors.New("invalid relation direction")
	ErrDependencyCycle            = errors.New("dependency cycle detected")
	ErrInvalidScope               = errors.New("invalid scope")
	ErrPlanNotReady               = errors.New("plan can only be applied in result state")
)

type Node struct {
	ID string
}

type Relationship struct {
	From string
	To   string
	Type RelationType
}

type Graph struct {
	Nodes         []Node
	Relationships []Relationship
}

type Scope struct {
	Mode  RelationshipScope
	Focus string
}

type Planner struct {
	state   LifecycleState
	graph   Graph
	scope   Scope
	applied bool
}

func NewPlanner(graph Graph) *Planner {
	return &Planner{
		state: StateInput,
		graph: graph,
		scope: Scope{Mode: ScopeAll},
	}
}

func (p *Planner) State() LifecycleState {
	return p.state
}

func (p *Planner) Scope() Scope {
	return p.scope
}

func (p *Planner) Transition(next LifecycleState) error {
	if !isAllowedTransition(p.state, next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidLifecycleTransition, p.state, next)
	}
	p.state = next
	return nil
}

func (p *Planner) SwitchScope(mode RelationshipScope, focus string) error {
	if mode != ScopeAll && mode != ScopeNode && mode != ScopeUpstream && mode != ScopeDownstream {
		return fmt.Errorf("%w: %s", ErrInvalidScope, mode)
	}
	if mode != ScopeAll {
		if _, ok := p.nodeIDs()[focus]; !ok {
			return fmt.Errorf("%w: %s", ErrUnknownNode, focus)
		}
	}
	p.scope = Scope{Mode: mode, Focus: focus}
	return nil
}

func (p *Planner) ScopedRelationships() []Relationship {
	relationships := copyRelationships(p.graph.Relationships)
	sortRelationships(relationships)

	switch p.scope.Mode {
	case ScopeAll:
		return relationships
	case ScopeNode:
		return p.filterNodeScope(relationships, p.scope.Focus)
	case ScopeUpstream:
		return p.filterTraversalScope(relationships, p.scope.Focus, true)
	case ScopeDownstream:
		return p.filterTraversalScope(relationships, p.scope.Focus, false)
	default:
		return nil
	}
}

func (p *Planner) ValidateGraph() error {
	return validateGraph(p.graph)
}

func (p *Planner) ApplyPlan() error {
	if p.state != StateResult {
		return fmt.Errorf("%w: current state is %s", ErrPlanNotReady, p.state)
	}
	if err := p.ValidateGraph(); err != nil {
		return err
	}
	p.applied = true
	return nil
}

func (p *Planner) Applied() bool {
	return p.applied
}

func validateGraph(graph Graph) error {
	nodes := make(map[string]struct{}, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if node.ID == "" {
			return fmt.Errorf("%w: node id cannot be empty", ErrUnknownNode)
		}
		nodes[node.ID] = struct{}{}
	}

	relationships := graph.Relationships
	for _, rel := range relationships {
		if rel.Type != RelationDependsOn && rel.Type != RelationBlocks {
			return fmt.Errorf("%w: %s", ErrUnknownRelationType, rel.Type)
		}
		if _, ok := nodes[rel.From]; !ok {
			return fmt.Errorf("%w: %s", ErrUnknownNode, rel.From)
		}
		if _, ok := nodes[rel.To]; !ok {
			return fmt.Errorf("%w: %s", ErrUnknownNode, rel.To)
		}
		if rel.From == rel.To {
			return fmt.Errorf("%w: self reference %s", ErrDependencyCycle, rel.From)
		}
	}

	if hasMixedDirectionConflicts(relationships) {
		return ErrInvalidRelationDirection
	}

	adjacency := make(map[string][]string, len(nodes))
	for id := range nodes {
		adjacency[id] = nil
	}

	for _, rel := range relationships {
		from, to := canonicalDependencyDirection(rel)
		adjacency[from] = append(adjacency[from], to)
	}

	visited := make(map[string]bool, len(nodes))
	inStack := make(map[string]bool, len(nodes))

	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if visited[id] {
			continue
		}
		if hasCycle(id, adjacency, visited, inStack) {
			return ErrDependencyCycle
		}
	}

	return nil
}

func hasMixedDirectionConflicts(relationships []Relationship) bool {
	depends := make(map[string]struct{}, len(relationships))
	blocks := make(map[string]struct{}, len(relationships))

	for _, rel := range relationships {
		key := rel.From + "->" + rel.To
		if rel.Type == RelationDependsOn {
			depends[key] = struct{}{}
		}
		if rel.Type == RelationBlocks {
			blocks[key] = struct{}{}
		}
	}

	for key := range depends {
		if _, ok := blocks[key]; ok {
			return true
		}
	}

	return false
}

func hasCycle(node string, adjacency map[string][]string, visited map[string]bool, inStack map[string]bool) bool {
	if inStack[node] {
		return true
	}
	if visited[node] {
		return false
	}

	visited[node] = true
	inStack[node] = true

	for _, next := range adjacency[node] {
		if hasCycle(next, adjacency, visited, inStack) {
			return true
		}
	}

	inStack[node] = false
	return false
}

func canonicalDependencyDirection(rel Relationship) (string, string) {
	if rel.Type == RelationDependsOn {
		return rel.To, rel.From
	}
	return rel.From, rel.To
}

func isAllowedTransition(current LifecycleState, next LifecycleState) bool {
	switch current {
	case StateInput:
		return next == StateExecution
	case StateExecution:
		return next == StateReview
	case StateReview:
		return next == StateResult
	default:
		return false
	}
}

func (p *Planner) nodeIDs() map[string]struct{} {
	out := make(map[string]struct{}, len(p.graph.Nodes))
	for _, node := range p.graph.Nodes {
		out[node.ID] = struct{}{}
	}
	return out
}

func (p *Planner) filterNodeScope(relationships []Relationship, focus string) []Relationship {
	out := make([]Relationship, 0, len(relationships))
	for _, rel := range relationships {
		if rel.From == focus || rel.To == focus {
			out = append(out, rel)
		}
	}
	return out
}

func (p *Planner) filterTraversalScope(relationships []Relationship, focus string, upstream bool) []Relationship {
	forward, reverse := p.canonicalAdjacency()
	closure := make(map[string]struct{})
	p.walkClosure(focus, forward, reverse, upstream, closure)

	out := make([]Relationship, 0, len(relationships))
	for _, rel := range relationships {
		from, to := canonicalDependencyDirection(rel)
		_, fromOK := closure[from]
		_, toOK := closure[to]
		if fromOK && toOK {
			out = append(out, rel)
		}
	}
	sortRelationships(out)
	return out
}

func (p *Planner) canonicalAdjacency() (map[string][]string, map[string][]string) {
	forward := make(map[string][]string, len(p.graph.Nodes))
	reverse := make(map[string][]string, len(p.graph.Nodes))
	for _, node := range p.graph.Nodes {
		forward[node.ID] = nil
		reverse[node.ID] = nil
	}

	for _, rel := range p.graph.Relationships {
		from, to := canonicalDependencyDirection(rel)
		forward[from] = append(forward[from], to)
		reverse[to] = append(reverse[to], from)
	}

	for id := range forward {
		sort.Strings(forward[id])
	}
	for id := range reverse {
		sort.Strings(reverse[id])
	}

	return forward, reverse
}

func (p *Planner) walkClosure(
	focus string,
	forward map[string][]string,
	reverse map[string][]string,
	upstream bool,
	closure map[string]struct{},
) {
	stack := []string{focus}
	closure[focus] = struct{}{}

	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		var nextNodes []string
		if upstream {
			nextNodes = reverse[node]
		} else {
			nextNodes = forward[node]
		}

		for _, next := range nextNodes {
			if _, ok := closure[next]; ok {
				continue
			}
			closure[next] = struct{}{}
			stack = append(stack, next)
		}
	}
}

func sortRelationships(relationships []Relationship) {
	sort.Slice(relationships, func(i, j int) bool {
		if relationships[i].From != relationships[j].From {
			return relationships[i].From < relationships[j].From
		}
		if relationships[i].To != relationships[j].To {
			return relationships[i].To < relationships[j].To
		}
		return relationships[i].Type < relationships[j].Type
	})
}

func copyRelationships(in []Relationship) []Relationship {
	out := make([]Relationship, len(in))
	copy(out, in)
	return out
}
