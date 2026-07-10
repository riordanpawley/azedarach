package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const boardViewPreferenceFileName = "board-view-state.json"

type boardViewPreferenceFile struct {
	SelectedViewByProject map[string]string `json:"selected_view_by_project"`
}

func (d *Daemon) loadSelectedBoardViewPreference(projectID string) (string, bool) {
	path := d.boardViewPreferencePath(projectID)
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var prefs boardViewPreferenceFile
	if err := json.Unmarshal(data, &prefs); err != nil {
		return "", false
	}
	value := domain.NormalizeBoardViewID(prefs.SelectedViewByProject[d.canonicalProjectID(projectID)])
	if value == "" {
		return "", false
	}
	return value, true
}

func (d *Daemon) saveSelectedBoardViewPreference(projectID, viewID string) error {
	path := d.boardViewPreferencePath(projectID)
	if path == "" {
		return fmt.Errorf("board view preference path unavailable")
	}
	projectID = d.canonicalProjectID(projectID)
	viewID = domain.NormalizeBoardViewID(viewID)
	if viewID == "" {
		viewID = domain.DefaultBoardViewID
	}

	prefs := boardViewPreferenceFile{SelectedViewByProject: map[string]string{}}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &prefs)
	}
	if prefs.SelectedViewByProject == nil {
		prefs.SelectedViewByProject = map[string]string{}
	}
	prefs.SelectedViewByProject[projectID] = viewID
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (d *Daemon) boardViewPreferencePath(projectID string) string {
	repoDir := strings.TrimSpace(d.resolveRepoDirForProject(projectID))
	if repoDir == "" {
		repoDir = strings.TrimSpace(d.cfg.RepoDir)
	}
	if repoDir == "" {
		return ""
	}
	return filepath.Join(repoDir, ".azedarach", boardViewPreferenceFileName)
}
