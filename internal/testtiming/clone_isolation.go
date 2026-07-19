package testtiming

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/riordanpawley/azedarach/internal/testisolation"
)

const (
	cloneIsolationSingleCommand           = "single-command"
	cloneIsolationPackageIsolatedParallel = "package-isolated-parallel"
)

type packageEnvironment struct {
	Package string
	Env     []string
}

func cloneIsolationBeforeExecution(profile Profile, env []string) CloneIsolationEvidence {
	if !profile.PackageIsolatedDBClones {
		return CloneIsolationEvidence{Mode: cloneIsolationSingleCommand}
	}
	return CloneIsolationEvidence{
		Mode: cloneIsolationPackageIsolatedParallel,
		Configured: strings.TrimSpace(environmentValue(env, "AZEDARACH_USER_DB_CLONE")) != "" ||
			len(splitPathList(environmentValue(env, "AZEDARACH_PROJECT_DB_CLONES"))) > 0,
	}
}

func preparePackageCloneEnvironments(ctx context.Context, profile Profile, root, workingDir string, base []string) ([]packageEnvironment, CloneIsolationEvidence, error) {
	if !profile.PackageIsolatedDBClones {
		return nil, CloneIsolationEvidence{Mode: cloneIsolationSingleCommand}, nil
	}
	userSource := strings.TrimSpace(environmentValue(base, "AZEDARACH_USER_DB_CLONE"))
	projectSources := splitPathList(environmentValue(base, "AZEDARACH_PROJECT_DB_CLONES"))
	evidence := CloneIsolationEvidence{
		Mode:       cloneIsolationPackageIsolatedParallel,
		Configured: userSource != "" || len(projectSources) > 0,
	}
	environments := make([]packageEnvironment, 0, len(profile.Packages))
	for packageIndex, packageName := range profile.Packages {
		authorities := profile.PackageCloneAuthorities[packageName]
		packageRoot := filepath.Join(root, "migration-clones", fmt.Sprintf("%02d-%s", packageIndex, safePackageName(packageName)))
		packageIsolation, err := testisolation.New(filepath.Join(packageRoot, "environment"), workingDir)
		if err != nil {
			return nil, evidence, fmt.Errorf("prepare %s test environment: %w", packageName, err)
		}
		env := withEnv(packageIsolation.Environ(base), "AZEDARACH_USER_DB_CLONE", "")
		env = withEnv(env, "AZEDARACH_PROJECT_DB_CLONES", "")
		identity := PackageCloneIdentity{Package: packageName, ProjectDBs: []string{}}
		if userSource != "" && slices.Contains(authorities, CloneAuthorityUser) {
			destination := filepath.Join(packageRoot, "user", "azedarach.db")
			if err := testisolation.SnapshotDatabaseClone(ctx, userSource, destination, workingDir); err != nil {
				return nil, evidence, fmt.Errorf("prepare %s user database clone: %w", packageName, err)
			}
			env = withEnv(env, "AZEDARACH_USER_DB_CLONE", destination)
			identity.UserDB = destination
		}
		if len(projectSources) > 0 && slices.Contains(authorities, CloneAuthorityProject) {
			projectDestinations := make([]string, 0, len(projectSources))
			for projectIndex, source := range projectSources {
				destination := filepath.Join(packageRoot, "projects", fmt.Sprintf("%02d", projectIndex), "azedarach.db")
				if err := testisolation.SnapshotDatabaseClone(ctx, source, destination, workingDir); err != nil {
					return nil, evidence, fmt.Errorf("prepare %s project database clone %d: %w", packageName, projectIndex, err)
				}
				projectDestinations = append(projectDestinations, destination)
			}
			env = withEnv(env, "AZEDARACH_PROJECT_DB_CLONES", strings.Join(projectDestinations, string(os.PathListSeparator)))
			identity.ProjectDBs = projectDestinations
		}
		environments = append(environments, packageEnvironment{Package: packageName, Env: env})
		if evidence.Configured {
			evidence.Packages = append(evidence.Packages, identity)
		}
	}
	return environments, evidence, nil
}

func environmentValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

func splitPathList(value string) []string {
	var paths []string
	for _, path := range filepath.SplitList(strings.TrimSpace(value)) {
		if path = strings.TrimSpace(path); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func safePackageName(packageName string) string {
	name := strings.TrimPrefix(packageName, "./")
	name = strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(name)
	if name == "" || name == "." {
		return "package"
	}
	return name
}
