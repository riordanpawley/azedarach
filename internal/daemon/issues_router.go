package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type daemonProjectIDContextKey struct{}

func withDaemonProjectIDContext(ctx context.Context, projectID string) context.Context {
	return context.WithValue(ctx, daemonProjectIDContextKey{}, protocol.NormalizeProjectID(projectID))
}

func daemonProjectIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return protocol.DefaultProjectID
	}
	projectID, _ := ctx.Value(daemonProjectIDContextKey{}).(string)
	return protocol.NormalizeProjectID(projectID)
}

func (d *Daemon) issueClientForProject(projectID string) *issues.Client {
	if d == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)

	d.issueClientsMu.Lock()
	defer d.issueClientsMu.Unlock()

	if d.issueClientsByProject == nil {
		d.issueClientsByProject = make(map[string]*issues.Client)
	}
	if d.issueClientsByRoot == nil {
		d.issueClientsByRoot = make(map[string]*issues.Client)
	}
	if client, ok := d.issueClientsByProject[projectID]; ok && client != nil {
		return client
	}

	repoDir := d.resolveRepoDirForProjectLocked(projectID)
	if repoDir == "" {
		if d.issues != nil {
			d.issueClientsByProject[projectID] = d.issues
			return d.issues
		}
		return nil
	}
	repoKey := daemonStoreRootKey(repoDir)

	if client, ok := d.issueClientsByRoot[repoDir]; ok && client != nil {
		d.issueClientsByProject[projectID] = client
		if repoKey != repoDir {
			d.issueClientsByRoot[repoKey] = client
		}
		return client
	}
	if client, ok := d.issueClientsByRoot[repoKey]; ok && client != nil {
		d.issueClientsByProject[projectID] = client
		return client
	}

	client := issues.NewClient(repoDir, d.cfg.Logger)
	d.issueClientsByRoot[repoKey] = client
	d.issueClientsByProject[projectID] = client
	if d.issues == nil {
		d.issues = client
	}
	return client
}

// issueClientForExistingProjectStore resolves a registry project without
// creating its root, .azedarach directory, or database. It is reserved for
// background discovery paths where a stale registry entry must remain
// unavailable rather than being revived as a blank project.
func (d *Daemon) issueClientForExistingProjectStore(projectID string) (*issues.Client, error) {
	if d == nil {
		return nil, fmt.Errorf("issue store unavailable")
	}
	projectID = protocol.NormalizeProjectID(projectID)
	if err, unhealthy := d.projectIssueStoreHealthError(projectID); unhealthy {
		return nil, err
	}

	d.issueClientsMu.Lock()
	repoDir, exact := d.resolveRepoDirForProjectExactLocked(projectID)
	d.issueClientsMu.Unlock()
	if !exact || strings.TrimSpace(repoDir) == "" {
		return nil, d.recordProjectIssueStoreUnavailable(projectID, fmt.Errorf("registered project root unavailable"))
	}
	repoDir = filepath.Clean(repoDir)
	info, err := os.Stat(repoDir)
	if err != nil {
		return nil, d.recordProjectIssueStoreUnavailable(projectID, fmt.Errorf("registered project root %s unavailable: %w", repoDir, err))
	}
	if !info.IsDir() {
		return nil, d.recordProjectIssueStoreUnavailable(projectID, fmt.Errorf("registered project root %s is not a directory", repoDir))
	}
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		return nil, d.recordProjectIssueStoreUnavailable(projectID, fmt.Errorf("registered project issue store %s unavailable: %w", dbPath, err))
	}
	if !dbInfo.Mode().IsRegular() {
		return nil, d.recordProjectIssueStoreUnavailable(projectID, fmt.Errorf("registered project issue store %s is not a regular file", dbPath))
	}

	canonicalProjectID := projectID
	if hashProjectID, hashErr := appconfig.ProjectIDForRoot(repoDir); hashErr == nil {
		canonicalProjectID = protocol.NormalizeProjectID(hashProjectID)
	}
	repoKey := daemonStoreRootKey(repoDir)
	d.issueClientsMu.Lock()
	defer d.issueClientsMu.Unlock()
	if d.issueClientsByProject == nil {
		d.issueClientsByProject = make(map[string]*issues.Client)
	}
	if d.issueClientsByRoot == nil {
		d.issueClientsByRoot = make(map[string]*issues.Client)
	}
	if client := d.issueClientsByProject[projectID]; client != nil {
		return client, nil
	}
	if client := d.issueClientsByProject[canonicalProjectID]; client != nil {
		d.issueClientsByProject[projectID] = client
		return client, nil
	}
	if client := d.issueClientsByRoot[repoKey]; client != nil {
		d.issueClientsByProject[projectID] = client
		d.issueClientsByProject[canonicalProjectID] = client
		return client, nil
	}
	client := issues.NewClientAtPath(dbPath, d.cfg.Logger, issues.WithExistingDatabaseOnly())
	d.issueClientsByRoot[repoKey] = client
	d.issueClientsByProject[projectID] = client
	d.issueClientsByProject[canonicalProjectID] = client
	return client, nil
}

func (d *Daemon) resolveRepoDirForProjectLocked(projectID string) string {
	projectID = protocol.NormalizeProjectID(projectID)
	baseRepoDir := strings.TrimSpace(d.cfg.RepoDir)
	if baseRepoDir == "" {
		return ""
	}

	if matchedRepoDir, ok := d.resolveRepoDirForProjectExactLocked(projectID); ok {
		return matchedRepoDir
	}
	return baseRepoDir
}

func (d *Daemon) resolveRepoDirForProjectExactLocked(projectID string) (string, bool) {
	projectID = protocol.NormalizeProjectID(projectID)
	baseRepoDir := strings.TrimSpace(d.cfg.RepoDir)
	if baseRepoDir == "" {
		return "", false
	}

	// Always accept canonical routes for this daemon's root repo.
	baseCandidates := make([]string, 0, 3)
	baseCandidates = append(baseCandidates, protocol.DefaultProjectID, protocol.NormalizeProjectID(filepath.Base(baseRepoDir)))
	if hashProjectID, err := appconfig.ProjectIDForRoot(baseRepoDir); err == nil {
		baseCandidates = append(baseCandidates, protocol.NormalizeProjectID(hashProjectID))
	}
	for _, candidate := range baseCandidates {
		if projectID == candidate {
			return baseRepoDir, true
		}
	}

	registry, err := appconfig.LoadProjectsRegistry()
	if err != nil || registry == nil {
		return "", false
	}
	for _, project := range registry.Projects {
		repoDir := strings.TrimSpace(project.Path)
		if repoDir == "" {
			continue
		}
		candidates := []string{
			protocol.NormalizeProjectID(project.ID),
			protocol.NormalizeProjectID(project.Name),
			protocol.NormalizeProjectID(filepath.Base(repoDir)),
		}
		if hashProjectID, hashErr := appconfig.ProjectIDForRoot(repoDir); hashErr == nil {
			candidates = append(candidates, protocol.NormalizeProjectID(hashProjectID))
		}
		for _, candidate := range candidates {
			if projectID == candidate {
				return repoDir, true
			}
		}
	}

	return "", false
}

func (d *Daemon) resolveRepoDirForProject(projectID string) string {
	if d == nil {
		return ""
	}
	d.issueClientsMu.Lock()
	defer d.issueClientsMu.Unlock()
	return strings.TrimSpace(d.resolveRepoDirForProjectLocked(projectID))
}

func (d *Daemon) resolveRepoDirForProjectExact(projectID string) string {
	if d == nil {
		return ""
	}
	d.issueClientsMu.Lock()
	defer d.issueClientsMu.Unlock()
	repoDir, ok := d.resolveRepoDirForProjectExactLocked(projectID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(repoDir)
}

func (d *Daemon) canonicalProjectID(projectID string) string {
	projectID = protocol.NormalizeProjectID(projectID)
	if d == nil {
		return projectID
	}
	repoDir := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
	if repoDir == "" {
		return projectID
	}
	if hashProjectID, err := appconfig.ProjectIDForRoot(repoDir); err == nil {
		if normalized := protocol.NormalizeProjectID(hashProjectID); normalized != "" {
			return normalized
		}
	}
	if repoName := protocol.NormalizeProjectID(filepath.Base(repoDir)); repoName != "" {
		return repoName
	}
	return projectID
}

func (d *Daemon) closeIssueClients() {
	if d == nil {
		return
	}
	d.issueClientsMu.Lock()
	defer d.issueClientsMu.Unlock()
	if len(d.issueClientsByRoot) == 0 {
		return
	}
	for repoDir, client := range d.issueClientsByRoot {
		if client == nil {
			continue
		}
		if err := client.CloseDB(); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("failed to close issue store", "repo_dir", repoDir, "error", err)
		}
	}
}

func daemonStoreRootKey(repoDir string) string {
	repoDir = strings.TrimSpace(repoDir)
	if resolved, err := appconfig.ResolveProjectRoot(repoDir); err == nil && strings.TrimSpace(resolved) != "" {
		return strings.TrimSpace(resolved)
	}
	return repoDir
}

func (d *Daemon) issueClientFromContext(ctx context.Context) (*issues.Client, string, error) {
	projectID := daemonProjectIDFromContext(ctx)
	client := d.issueClientForProject(projectID)
	if client == nil {
		return nil, projectID, fmt.Errorf("issue store unavailable")
	}
	return client, projectID, nil
}

// ApplyTaskService implementation for project-aware task bulk apply routing.
func (d *Daemon) Create(ctx context.Context, params issues.CreateTaskParams) (domain.Task, error) {
	client, projectID, err := d.issueClientFromContext(ctx)
	if err != nil {
		return domain.Task{}, err
	}
	return client.CreateWithRuntime(ctx, projectID, params)
}

func (d *Daemon) Update(ctx context.Context, issueID string, status domain.Status) (domain.Task, error) {
	_, projectID, err := d.issueClientFromContext(ctx)
	if err != nil {
		return domain.Task{}, err
	}
	task, _, err := d.updateTaskStatusExcludingClose(ctx, projectID, issueID, status, taskStatusUpdateOptions{})
	return task, err
}

func (d *Daemon) UpdateDetails(ctx context.Context, issueID string, params issues.UpdateTaskParams) (domain.Task, error) {
	client, projectID, err := d.issueClientFromContext(ctx)
	if err != nil {
		return domain.Task{}, err
	}
	return client.UpdateDetailsWithRuntime(ctx, projectID, issueID, params)
}

func (d *Daemon) AddDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) (domain.Task, error) {
	client, projectID, err := d.issueClientFromContext(ctx)
	if err != nil {
		return domain.Task{}, err
	}
	return client.AddDependencyWithRuntime(ctx, projectID, issueID, dependsOnID, dependencyType)
}

func (d *Daemon) RemoveDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) (domain.Task, error) {
	client, projectID, err := d.issueClientFromContext(ctx)
	if err != nil {
		return domain.Task{}, err
	}
	return client.RemoveDependencyWithRuntime(ctx, projectID, issueID, dependsOnID, dependencyType)
}

func (d *Daemon) Delete(ctx context.Context, issueID string) error {
	client, _, err := d.issueClientFromContext(ctx)
	if err != nil {
		return err
	}
	return client.Delete(ctx, issueID)
}

func (d *Daemon) Archive(ctx context.Context, issueID string) error {
	client, _, err := d.issueClientFromContext(ctx)
	if err != nil {
		return err
	}
	return client.Archive(ctx, issueID)
}

func (d *Daemon) Unarchive(ctx context.Context, issueID string) error {
	client, _, err := d.issueClientFromContext(ctx)
	if err != nil {
		return err
	}
	return client.Unarchive(ctx, issueID)
}
