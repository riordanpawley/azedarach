package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestLoadSelectedBoardViewPreferenceCanonicalizesLegacyAlias(t *testing.T) {
	repoDir := t.TempDir()
	d := &Daemon{cfg: Config{RepoDir: repoDir}}
	projectID := d.canonicalProjectID("default")
	path := filepath.Join(repoDir, ".azedarach", boardViewPreferenceFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir preference dir: %v", err)
	}
	seed, err := json.Marshal(boardViewPreferenceFile{SelectedViewByProject: map[string]string{projectID: "activity"}})
	if err != nil {
		t.Fatalf("encode preference: %v", err)
	}
	if err := os.WriteFile(path, seed, 0o644); err != nil {
		t.Fatalf("write preference: %v", err)
	}

	got, ok := d.loadSelectedBoardViewPreference("default")
	if !ok || got != string(domain.BoardViewOrchestrationID) {
		t.Fatalf("loaded preference = %q, %t; want orchestration, true", got, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated preference: %v", err)
	}
	var prefs boardViewPreferenceFile
	if err := json.Unmarshal(data, &prefs); err != nil {
		t.Fatalf("decode migrated preference: %v", err)
	}
	if got := prefs.SelectedViewByProject[projectID]; got != string(domain.BoardViewOrchestrationID) {
		t.Fatalf("persisted preference = %q, want orchestration", got)
	}
}
