package domain

// RelationshipKind identifies a typed relationship between issue-domain
// records. Dependency relationships use their persisted DependencyType value;
// spec and implementation provenance use the additional kinds below.
type RelationshipKind string

const (
	RelationshipSpecImplementation RelationshipKind = "spec-implementation"
	RelationshipImplementation     RelationshipKind = "implementation"
)

// RelationshipAuthority declares which domain decisions a relationship may
// influence. The matrix is intentionally closed: an unknown relationship has
// no authority until it is added here deliberately.
type RelationshipAuthority struct {
	Hierarchy            bool
	Readiness            bool
	Provenance           bool
	GitIntegrationTarget bool
}

// AuthorityForRelationship is the canonical relationship-authority matrix.
// Git ancestry and integration authority are deliberately narrower than graph
// context: only parent-child hierarchy can nominate an ancestor worktree.
func AuthorityForRelationship(kind RelationshipKind) RelationshipAuthority {
	switch kind {
	case RelationshipKind(DependencyParentChild), RelationshipKind("parent_child"):
		return RelationshipAuthority{Hierarchy: true, GitIntegrationTarget: true}
	case RelationshipKind(DependencyBlocks), RelationshipKind(DependencyBlockedBy):
		return RelationshipAuthority{Readiness: true}
	case RelationshipKind(DependencyDiscovered), RelationshipKind(DependencyCreatedIn), RelationshipSpecImplementation, RelationshipImplementation:
		return RelationshipAuthority{Provenance: true}
	case RelationshipKind(DependencyRelatedTo):
		return RelationshipAuthority{}
	default:
		return RelationshipAuthority{}
	}
}

// DependencyAuthority returns the authority assigned to a persisted issue
// dependency type.
func DependencyAuthority(dependencyType DependencyType) RelationshipAuthority {
	return AuthorityForRelationship(RelationshipKind(dependencyType))
}
