package daemon

import (
	"strings"

	"github.com/riordanpawley/azedarach/internal/services/git"
)

func (d *Daemon) worktreeManagerForProject(projectID string) *git.WorktreeManager {
	if d == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)

	d.worktreeManagersMu.Lock()
	defer d.worktreeManagersMu.Unlock()

	if d.worktreeManagersByProject == nil {
		d.worktreeManagersByProject = make(map[string]*git.WorktreeManager)
	}
	if d.worktreeManagersByRoot == nil {
		d.worktreeManagersByRoot = make(map[string]*git.WorktreeManager)
	}
	if manager, ok := d.worktreeManagersByProject[projectID]; ok && manager != nil {
		return manager
	}

	repoDir := strings.TrimSpace(d.resolveRepoDirForProjectLocked(projectID))
	if repoDir == "" {
		return nil
	}
	if manager, ok := d.worktreeManagersByRoot[repoDir]; ok && manager != nil {
		d.worktreeManagersByProject[projectID] = manager
		return manager
	}

	manager := git.NewWorktreeManager(git.NewExecRunner(repoDir), repoDir, d.cfg.Logger)
	d.worktreeManagersByRoot[repoDir] = manager
	d.worktreeManagersByProject[projectID] = manager
	return manager
}
