package daemon

import (
	"context"
	"fmt"
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
	projectID = protocol.NormalizeProjectID(projectID)

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

	repoDir := d.resolveIssueRepoDirLocked(projectID)
	if repoDir == "" {
		if d.issues != nil {
			d.issueClientsByProject[projectID] = d.issues
			return d.issues
		}
		return nil
	}

	if client, ok := d.issueClientsByRoot[repoDir]; ok && client != nil {
		d.issueClientsByProject[projectID] = client
		return client
	}

	client := issues.NewClient(repoDir, d.cfg.Logger)
	d.issueClientsByRoot[repoDir] = client
	d.issueClientsByProject[projectID] = client
	if d.issues == nil {
		d.issues = client
	}
	return client
}

func (d *Daemon) resolveIssueRepoDirLocked(projectID string) string {
	projectID = protocol.NormalizeProjectID(projectID)
	baseRepoDir := strings.TrimSpace(d.cfg.RepoDir)
	if baseRepoDir == "" {
		return ""
	}

	// Always accept canonical routes for this daemon's root repo.
	baseCandidates := make([]string, 0, 3)
	baseCandidates = append(baseCandidates, protocol.DefaultProjectID, protocol.NormalizeProjectID(filepath.Base(baseRepoDir)))
	if hashProjectID, err := appconfig.ProjectIDForRoot(baseRepoDir); err == nil {
		baseCandidates = append(baseCandidates, protocol.NormalizeProjectID(hashProjectID))
	}
	for _, candidate := range baseCandidates {
		if projectID == candidate {
			return baseRepoDir
		}
	}

	registry, err := appconfig.LoadProjectsRegistry()
	if err != nil || registry == nil {
		return baseRepoDir
	}
	for _, project := range registry.Projects {
		repoDir := strings.TrimSpace(project.Path)
		if repoDir == "" {
			continue
		}
		candidates := []string{
			protocol.NormalizeProjectID(project.Name),
			protocol.NormalizeProjectID(filepath.Base(repoDir)),
		}
		if hashProjectID, hashErr := appconfig.ProjectIDForRoot(repoDir); hashErr == nil {
			candidates = append(candidates, protocol.NormalizeProjectID(hashProjectID))
		}
		for _, candidate := range candidates {
			if projectID == candidate {
				return repoDir
			}
		}
	}

	return baseRepoDir
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

func (d *Daemon) issueClientFromContext(ctx context.Context) (*issues.Client, string, error) {
	projectID := daemonProjectIDFromContext(ctx)
	client := d.issueClientForProject(projectID)
	if client == nil {
		return nil, projectID, fmt.Errorf("issue store unavailable")
	}
	return client, projectID, nil
}

// ApplyTaskService implementation for project-aware task bulk apply routing.
func (d *Daemon) Create(ctx context.Context, params issues.CreateTaskParams) (string, error) {
	client, _, err := d.issueClientFromContext(ctx)
	if err != nil {
		return "", err
	}
	return client.Create(ctx, params)
}

func (d *Daemon) Update(ctx context.Context, issueID string, status domain.Status) error {
	client, _, err := d.issueClientFromContext(ctx)
	if err != nil {
		return err
	}
	return client.Update(ctx, issueID, status)
}

func (d *Daemon) UpdateDetails(ctx context.Context, issueID string, params issues.UpdateTaskParams) error {
	client, _, err := d.issueClientFromContext(ctx)
	if err != nil {
		return err
	}
	return client.UpdateDetails(ctx, issueID, params)
}

func (d *Daemon) AddDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) error {
	client, _, err := d.issueClientFromContext(ctx)
	if err != nil {
		return err
	}
	return client.AddDependency(ctx, issueID, dependsOnID, dependencyType)
}

func (d *Daemon) RemoveDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) error {
	client, _, err := d.issueClientFromContext(ctx)
	if err != nil {
		return err
	}
	return client.RemoveDependency(ctx, issueID, dependsOnID, dependencyType)
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
