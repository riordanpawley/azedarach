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
	if err := validatePackageCloneAuthorities(profile, userSource != "", len(projectSources) > 0); err != nil {
		return nil, evidence, err
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
		if identity.UserDB != "" || len(identity.ProjectDBs) > 0 {
			evidence.Packages = append(evidence.Packages, identity)
		}
	}
	return environments, evidence, nil
}

func validatePackageCloneAuthorities(profile Profile, userConfigured, projectConfigured bool) error {
	packages := make(map[string]struct{}, len(profile.Packages))
	for _, packageName := range profile.Packages {
		packages[packageName] = struct{}{}
	}
	consumers := map[CloneAuthority]int{}
	for packageName, authorities := range profile.PackageCloneAuthorities {
		if _, listed := packages[packageName]; !listed {
			return fmt.Errorf("clone authority mapping names unknown package %q", packageName)
		}
		seen := map[CloneAuthority]struct{}{}
		for _, authority := range authorities {
			if authority != CloneAuthorityUser && authority != CloneAuthorityProject {
				return fmt.Errorf("clone authority mapping for %s contains invalid authority %q", packageName, authority)
			}
			if _, duplicate := seen[authority]; duplicate {
				return fmt.Errorf("clone authority mapping for %s repeats authority %q", packageName, authority)
			}
			seen[authority] = struct{}{}
			consumers[authority]++
		}
	}
	if userConfigured && consumers[CloneAuthorityUser] == 0 {
		return fmt.Errorf("configured user clone authority has no consuming package")
	}
	if projectConfigured && consumers[CloneAuthorityProject] == 0 {
		return fmt.Errorf("configured project clone authority has no consuming package")
	}
	return nil
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
