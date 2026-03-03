package projects

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/testkit"
)

func TestRegistrySelectAndCurrent(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()

	err := registry.Register(Project{ID: "alpha", Name: "Alpha", Root: "/tmp/alpha"})
	testkit.AssertNoError(t, err, "register alpha should succeed")
	err = registry.Register(Project{ID: "beta", Name: "Beta", Root: "/tmp/beta"})
	testkit.AssertNoError(t, err, "register beta should succeed")

	err = registry.Select("beta")
	testkit.AssertNoError(t, err, "select should succeed")

	current, ok := registry.Current()
	testkit.AssertTrue(t, ok, "current should exist after select")
	testkit.AssertEqual(t, current.ID, "beta", "selected project should be current")

	listed := registry.List()
	testkit.AssertEqual(t, len(listed), 2, "list should include both projects")
	testkit.AssertEqual(t, listed[0].ID, "alpha", "list order should be stable")
	testkit.AssertEqual(t, listed[1].ID, "beta", "list order should include second")
}

func TestRegistryContextRestoreMetadata(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	err := registry.Register(Project{ID: "alpha", Name: "Alpha", Root: "/tmp/alpha"})
	testkit.AssertNoError(t, err, "register alpha should succeed")

	metadata := ContextRestore{ProjectID: "alpha", Lane: "F", IssueID: "bd-101"}
	err = registry.SetContextRestore(metadata)
	testkit.AssertNoError(t, err, "set restore metadata should succeed")

	restored := registry.ContextRestore()
	testkit.AssertEqual(t, restored.ProjectID, metadata.ProjectID, "project id should persist")
	testkit.AssertEqual(t, restored.Lane, metadata.Lane, "lane should persist")
	testkit.AssertEqual(t, restored.IssueID, metadata.IssueID, "issue id should persist")
}

func TestRegistryRejectsUnknownRestoreProject(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	err := registry.SetContextRestore(ContextRestore{ProjectID: "missing"})
	if err == nil {
		t.Fatalf("expected missing restore project to fail")
	}
}
