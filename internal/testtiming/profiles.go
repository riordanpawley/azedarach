package testtiming

import (
	"fmt"
	"slices"
	"strings"
)

var profiles = map[string]Profile{
	"cold": {
		Name: "cold", Description: "complete uncached semantic suite",
		Packages: []string{"./..."}, GoTestArgs: []string{"-json", "-count=1", "-timeout=15m"}, CleanCache: true,
	},
	"cached": {
		Name: "cached", Description: "complete suite with the Go test cache explicitly permitted",
		Packages: []string{"./..."}, GoTestArgs: []string{"-json", "-timeout=15m"},
	},
	"focused": {
		Name: "focused", Description: "developer-selected packages and optional test regexp",
		Packages: []string{"./internal/testtiming"}, GoTestArgs: []string{"-json", "-count=1", "-timeout=5m"},
	},
	"race": {
		Name: "race", Description: "complete uncached suite under the race detector",
		Packages: []string{"./..."}, GoTestArgs: []string{"-json", "-race", "-count=1", "-timeout=30m"},
	},
	"integration": {
		Name: "integration", Description: "real daemon, transport, git, tmux, and monitor boundary packages",
		Packages:   []string{"./internal/daemon/testharness", "./internal/ipc/transport", "./internal/services/git", "./internal/services/tmux", "./internal/services/monitor"},
		GoTestArgs: []string{"-json", "-count=1", "-timeout=15m"},
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
