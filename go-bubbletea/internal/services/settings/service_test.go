package settings

import (
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/testkit"
)

func TestServiceLoadDefaultsWhenFileMissing(t *testing.T) {
	t.Parallel()

	defaults := DefaultSettings()
	svc := NewService(filepath.Join(t.TempDir(), "settings.json"), defaults)

	loaded, err := svc.Load()
	testkit.AssertNoError(t, err, "load defaults should not fail")
	testkit.AssertEqual(t, loaded.SchemaVersion, defaults.SchemaVersion, "schema version should default")
	testkit.AssertEqual(t, loaded.Lane, defaults.Lane, "lane should default")
	testkit.AssertEqual(t, loaded.ProjectID, defaults.ProjectID, "project should default")
}

func TestServiceRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	svc := NewService(path, DefaultSettings())

	input := Settings{
		SchemaVersion: SchemaVersion,
		ProjectID:     "proj-1",
		Lane:          "F",
	}

	err := svc.Save(input)
	testkit.AssertNoError(t, err, "save should succeed")

	loaded, err := svc.Load()
	testkit.AssertNoError(t, err, "load should succeed")
	testkit.AssertEqual(t, loaded.SchemaVersion, input.SchemaVersion, "schema version should round-trip")
	testkit.AssertEqual(t, loaded.ProjectID, input.ProjectID, "project should round-trip")
	testkit.AssertEqual(t, loaded.Lane, input.Lane, "lane should round-trip")
}
