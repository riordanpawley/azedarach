package domain

import "testing"

func TestAuthorityForRelationship(t *testing.T) {
	tests := []struct {
		name string
		kind RelationshipKind
		want RelationshipAuthority
	}{
		{name: "parent child", kind: RelationshipKind(DependencyParentChild), want: RelationshipAuthority{Hierarchy: true, GitIntegrationTarget: true}},
		{name: "legacy parent child", kind: RelationshipKind("parent_child"), want: RelationshipAuthority{Hierarchy: true, GitIntegrationTarget: true}},
		{name: "blocks", kind: RelationshipKind(DependencyBlocks), want: RelationshipAuthority{Readiness: true}},
		{name: "blocked by", kind: RelationshipKind(DependencyBlockedBy), want: RelationshipAuthority{Readiness: true}},
		{name: "created in", kind: RelationshipKind(DependencyCreatedIn), want: RelationshipAuthority{Provenance: true}},
		{name: "discovered from", kind: RelationshipKind(DependencyDiscovered), want: RelationshipAuthority{Provenance: true}},
		{name: "spec implementation", kind: RelationshipSpecImplementation, want: RelationshipAuthority{Provenance: true}},
		{name: "implementation", kind: RelationshipImplementation, want: RelationshipAuthority{Provenance: true}},
		{name: "related", kind: RelationshipKind(DependencyRelatedTo), want: RelationshipAuthority{}},
		{name: "unknown fails closed", kind: RelationshipKind("future-relationship"), want: RelationshipAuthority{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AuthorityForRelationship(tt.kind); got != tt.want {
				t.Fatalf("AuthorityForRelationship(%q) = %+v, want %+v", tt.kind, got, tt.want)
			}
		})
	}
}

func TestTaskParentIssueIDIgnoresNonHierarchyRelationships(t *testing.T) {
	task := Task{Dependencies: []Dependency{
		{ID: "creator", Type: DependencyCreatedIn},
		{ID: "related", Type: DependencyRelatedTo},
		{ID: "blocker", Type: DependencyBlocks},
		{ID: "parent", Type: DependencyParentChild},
	}}

	if got := TaskParentIssueID(task); got != "parent" {
		t.Fatalf("TaskParentIssueID() = %q, want parent", got)
	}
}
