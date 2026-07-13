package testtiming

import (
	"fmt"
	"slices"
	"strings"
)

var profiles = map[string]Profile{
	"cold": {
		Name: "cold", Description: "complete uncached semantic suite",
		Packages: []string{"./..."}, GoTestArgs: []string{"-json", "-count=1", "-timeout=8m", "-p=2"}, CleanCache: true,
	},
	"cached": {
		Name: "cached", Description: "complete suite with the Go test cache explicitly permitted",
		Packages: []string{"./..."}, GoTestArgs: []string{"-json", "-timeout=15m"},
	},
	"focused": {
		Name: "focused", Description: "fast test-tooling self-check or developer-selected scope",
		Packages: []string{"./internal/testtiming"}, GoTestArgs: []string{"-json", "-count=1", "-timeout=5m"},
	},
	"race": {
		Name: "race", Description: "focused shared-state and process-lifecycle contracts under the race detector",
		Packages: []string{"./internal/services/issues", "./internal/client/daemonprocess", "./internal/services/git"}, GoTestArgs: []string{"-json", "-race", "-count=1", "-timeout=10m", "-run", "Test(MigratedIssueTestTemplateClonesAreIsolatedAndComplete|LauncherStartProcessSupervisorPreservesExitAfterSignalPermissionRace|RealProcessProfileLauncher.*|MergeCleanlyTransactionalAllowsConcurrentScratchValidationAndRejectsStaleFinalApply)$"},
	},
	"integration": {
		Name: "integration", Description: "real subprocess lifecycle contracts",
		Packages:   []string{"./internal/daemon", "./internal/client/daemonprocess", "./internal/services/git", "./internal/services/tmux"},
		GoTestArgs: []string{"-json", "-count=1", "-timeout=15m", "-run", "RealProcessProfile"},
	},
	"migration-clone": {
		Name: "migration-clone", Description: "fresh, historical, repair, drift, rollback, and clone isolation contracts",
		Packages:   []string{"./internal/services/issues", "./internal/daemon/userstore", "./internal/daemon/state", "./internal/daemon"},
		GoTestArgs: []string{"-json", "-count=1", "-timeout=15m", "-run", "(Migration|Migrate|Migrates|Migrated|Repair|SchemaDrift)"},
	},
	"boundary": {
		Name: "boundary", Description: "thin-client and session-projection executable boundary guards",
		Packages:   []string{"./internal/tui", "./internal/cli"},
		GoTestArgs: []string{"-json", "-count=1", "-timeout=5m", "-run", "^(TestIntegrationBoundaryGuard_|TestMigrationGuard_|TestSessionProjectionGuard_)"},
	},
}

func ProfileNames() []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func ResolveProfile(name string, packages []string, run string) (Profile, error) {
	p, ok := profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("unknown profile %q (choose %s)", name, strings.Join(ProfileNames(), ", "))
	}
	if name != "focused" && (len(packages) > 0 || run != "") {
		return Profile{}, fmt.Errorf("profile %q has canonical scope; --package and --run are available only with the focused profile", name)
	}
	p.Packages = slices.Clone(p.Packages)
	p.GoTestArgs = slices.Clone(p.GoTestArgs)
	if len(packages) > 0 {
		p.Packages = slices.Clone(packages)
	}
	if run != "" {
		p.GoTestArgs = append(p.GoTestArgs, "-run", run)
	}
	return p, nil
}

func (p Profile) Command() []string {
	command := []string{"go", "test"}
	command = append(command, p.GoTestArgs...)
	return append(command, p.Packages...)
}

func (p Profile) CachePolicy() string {
	if p.CleanCache {
		return "cleared-and-bypassed"
	}
	if slices.Contains(p.GoTestArgs, "-count=1") {
		return "bypassed"
	}
	return "permitted"
}
