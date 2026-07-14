package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectDefaultsDoNotCreateStaleWorktreeArtifacts(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	data, err := os.ReadFile(filepath.Join(repoRoot, ConfigDirName, ConfigFileName))
	require.NoError(t, err)
	var projectDefaults struct {
		Worktree struct {
			SyncInitCommands  []string `json:"syncInitCommands"`
			AsyncInitCommands []string `json:"asyncInitCommands"`
		} `json:"worktree"`
	}
	require.NoError(t, json.Unmarshal(data, &projectDefaults))
	assert.Equal(t, []string{"direnv allow"}, projectDefaults.Worktree.SyncInitCommands)
	assert.Empty(t, projectDefaults.Worktree.AsyncInitCommands)

	for _, command := range projectDefaults.Worktree.SyncInitCommands {
		assert.NotContains(t, command, ".gocache")
		assert.NotContains(t, command, ".gopath")
	}

	assertFileExcludesText(t, filepath.Join(repoRoot, ".envrc"), "reference-effect")
	assertFileExcludesText(t, filepath.Join(repoRoot, ".codex", "config.toml"), "floop")
	assertFileExcludesText(t, filepath.Join(repoRoot, ".gitignore"), "/reference-effect")
}

func TestEnvrcSharesExternalDirenvLayoutAcrossWorktrees(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	envrc, err := os.ReadFile(filepath.Join(repoRoot, ".envrc"))
	require.NoError(t, err)
	cacheEnv, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "go-cache-env.sh"))
	require.NoError(t, err)

	testRoot, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	mainCheckout := filepath.Join(testRoot, "main checkout")
	linkedCheckout := filepath.Join(testRoot, "linked checkout")
	cacheRoot := filepath.Join(testRoot, "cache")
	require.NoError(t, os.Mkdir(mainCheckout, 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(mainCheckout, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mainCheckout, ".envrc"), envrc, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(mainCheckout, "scripts", "go-cache-env.sh"), cacheEnv, 0o755))
	runGit(t, mainCheckout, "init")
	runGit(t, mainCheckout, "add", ".envrc", "scripts/go-cache-env.sh")
	runGit(t, mainCheckout, "-c", "user.name=Azedarach Test", "-c", "user.email=test@example.invalid", "commit", "-m", "test fixture")
	runGit(t, mainCheckout, "worktree", "add", "--detach", linkedCheckout, "HEAD")

	mainLayout := evaluateEnvrcLayout(t, mainCheckout, cacheRoot)
	linkedLayout := evaluateEnvrcLayout(t, linkedCheckout, cacheRoot)

	assert.Equal(t, mainLayout, linkedLayout)
	assert.True(t, strings.HasPrefix(mainLayout, cacheRoot+string(os.PathSeparator)))
	assert.True(t, strings.HasPrefix(linkedLayout, cacheRoot+string(os.PathSeparator)))
	assert.NoDirExists(t, filepath.Join(mainCheckout, ".direnv"))
	assert.NoDirExists(t, filepath.Join(linkedCheckout, ".direnv"))
	assert.NoDirExists(t, filepath.Join(linkedCheckout, ".azedarach"))
}

func TestEnvrcPrefersInstalledPairAcrossMainAndLinkedWorktrees(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	envrc, err := os.ReadFile(filepath.Join(repoRoot, ".envrc"))
	require.NoError(t, err)
	cacheEnv, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "go-cache-env.sh"))
	require.NoError(t, err)

	testRoot, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	mainCheckout := filepath.Join(testRoot, "main")
	linkedCheckout := filepath.Join(testRoot, "linked")
	installBin := filepath.Join(testRoot, "installed", "bin")
	activeGeneration := filepath.Join(installBin, ".azedarach-generations", "generation.active")
	staleGeneration := filepath.Join(installBin, ".azedarach-generations", "generation.stale")
	scratchBin := filepath.Join(testRoot, "scratch", "bin")
	aliasedStaleBin := filepath.Join(testRoot, "aliased-stale", "bin")
	mismatchedBin := filepath.Join(testRoot, "mismatched", "bin")
	require.NoError(t, os.MkdirAll(filepath.Join(mainCheckout, "scripts"), 0o755))
	require.NoError(t, os.MkdirAll(activeGeneration, 0o755))
	require.NoError(t, os.MkdirAll(staleGeneration, 0o755))
	require.NoError(t, os.MkdirAll(scratchBin, 0o755))
	require.NoError(t, os.MkdirAll(aliasedStaleBin, 0o755))
	require.NoError(t, os.MkdirAll(mismatchedBin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mainCheckout, ".envrc"), envrc, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(mainCheckout, "scripts", "go-cache-env.sh"), cacheEnv, 0o755))
	writeVersionFixture(t, filepath.Join(activeGeneration, "az"), "dev (installed)")
	writeVersionFixture(t, filepath.Join(activeGeneration, "azd"), "dev (installed)")
	writeVersionFixture(t, filepath.Join(staleGeneration, "az"), "dev (stale-installed)")
	writeVersionFixture(t, filepath.Join(staleGeneration, "azd"), "dev (stale-installed)")
	writeVersionFixture(t, filepath.Join(scratchBin, "az"), "dev (installed)")
	writeVersionFixture(t, filepath.Join(scratchBin, "azd"), "dev (installed)")
	writeVersionFixture(t, filepath.Join(mismatchedBin, "az"), "dev (stale-client)")
	writeVersionFixture(t, filepath.Join(mismatchedBin, "azd"), "dev (stale-daemon)")
	require.NoError(t, os.Symlink(filepath.Join(".azedarach-generations", "generation.active"), filepath.Join(installBin, ".azedarach-current")))
	require.NoError(t, os.Symlink(filepath.Join(".azedarach-current", "az"), filepath.Join(installBin, "az")))
	require.NoError(t, os.Symlink(filepath.Join(".azedarach-current", "azd"), filepath.Join(installBin, "azd")))
	require.NoError(t, os.Symlink(staleGeneration, filepath.Join(aliasedStaleBin, ".azedarach-current")))
	require.NoError(t, os.Symlink(filepath.Join(".azedarach-current", "az"), filepath.Join(aliasedStaleBin, "az")))
	require.NoError(t, os.Symlink(filepath.Join(".azedarach-current", "azd"), filepath.Join(aliasedStaleBin, "azd")))
	runGit(t, mainCheckout, "init")
	runGit(t, mainCheckout, "add", ".envrc", "scripts/go-cache-env.sh")
	runGit(t, mainCheckout, "-c", "user.name=Azedarach Test", "-c", "user.email=test@example.invalid", "commit", "-m", "test fixture")
	runGit(t, mainCheckout, "worktree", "add", "--detach", linkedCheckout, "HEAD")
	for _, checkout := range []string{mainCheckout, linkedCheckout} {
		require.NoError(t, os.MkdirAll(filepath.Join(checkout, "bin"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(checkout, "bin", "az"), []byte("#!/bin/sh\n"), 0o755))
	}

	for _, checkout := range []string{mainCheckout, linkedCheckout} {
		for managedPathName, managedPath := range map[string]string{
			"public-control":         installBin,
			"active-generation-only": activeGeneration,
		} {
			t.Run(filepath.Base(checkout)+"/"+managedPathName, func(t *testing.T) {
				const script = `
set -eu
nix() { :; }
use() { :; }
PATH_add() { PATH="$1:$PATH"; export PATH; }
PATH_rm() {
  local remove="$1" entry next=""
  local old_ifs="$IFS"
  IFS=:
  for entry in $PATH; do
    if [ "$entry" != "$remove" ]; then
      next="${next:+$next:}$entry"
    fi
  done
  IFS="$old_ifs"
  PATH="$next"
  export PATH
}
watch_file() { :; }
dotenv() { :; }
source_env_if_exists() { :; }
cd "$CHECKOUT"
. ./.envrc >/dev/null
command -v az >"$AZ_RESULT"
command -v azd >"$AZD_RESULT"
sh -c 'command -v az; command -v azd' >"$CHILD_RESULT"
(sh -c 'command -v az; command -v azd' >"$BACKGROUND_RESULT") &
wait
`
				azResult := filepath.Join(t.TempDir(), "az-result")
				azdResult := filepath.Join(t.TempDir(), "azd-result")
				childResult := filepath.Join(t.TempDir(), "child-result")
				backgroundResult := filepath.Join(t.TempDir(), "background-result")
				initialPath := filepath.Join(mainCheckout, "bin") + ":" + staleGeneration + ":" + aliasedStaleBin + ":" + scratchBin + ":" + mismatchedBin + ":" + managedPath + ":/usr/bin:/bin:/usr/sbin:/sbin"
				if checkout == linkedCheckout {
					initialPath = filepath.Join(linkedCheckout, "bin") + ":" + initialPath
				}
				cmd := exec.Command("bash", "-c", script)
				cmd.Env = append(envWithout("AZEDARACH_GO_CACHE_ROOT", "AZEDARACH_GOCACHE", "GOCACHE"),
					"CHECKOUT="+checkout,
					"AZ_RESULT="+azResult,
					"AZD_RESULT="+azdResult,
					"CHILD_RESULT="+childResult,
					"BACKGROUND_RESULT="+backgroundResult,
					"HOME="+t.TempDir(),
					"ISSUE_BACKEND=none",
					"AZEDARACH_DIRENV_MANUAL_NIX_RELOAD=0",
					"PATH="+initialPath,
				)
				output, runErr := cmd.CombinedOutput()
				require.NoErrorf(t, runErr, "evaluate .envrc pair routing: %s", output)
				gotAz, err := os.ReadFile(azResult)
				require.NoError(t, err)
				gotAzd, err := os.ReadFile(azdResult)
				require.NoError(t, err)
				assert.Equal(t, filepath.Join(activeGeneration, "az"), strings.TrimSpace(string(gotAz)))
				assert.Equal(t, filepath.Join(activeGeneration, "azd"), strings.TrimSpace(string(gotAzd)))
				wantChildren := filepath.Join(activeGeneration, "az") + "\n" + filepath.Join(activeGeneration, "azd")
				for _, result := range []string{childResult, backgroundResult} {
					got, readErr := os.ReadFile(result)
					require.NoError(t, readErr)
					assert.Equal(t, wantChildren, strings.TrimSpace(string(got)))
				}
			})
		}
	}
}

func writeVersionFixture(t *testing.T, path, version string) {
	t.Helper()
	content := "#!/bin/sh\nprintf '%s\\n' '" + version + "'\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
}

func TestEnvrcDirenvLayoutFallsBackToHomeCache(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	home := t.TempDir()
	resultPath := filepath.Join(t.TempDir(), "layout")
	const script = `
set -eu
PATH_add() { :; }
PATH_rm() { :; }
watch_file() { :; }
dotenv() { :; }
source_env_if_exists() { :; }
cd "$CHECKOUT"
. ./.envrc >/dev/null
direnv_layout_dir >"$LAYOUT_RESULT"
`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(envWithout("XDG_CACHE_HOME", "AZEDARACH_GO_CACHE_ROOT", "AZEDARACH_GOCACHE", "GOCACHE"),
		"CHECKOUT="+repoRoot,
		"LAYOUT_RESULT="+resultPath,
		"HOME="+home,
		"ISSUE_BACKEND=none",
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
	)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "evaluate .envrc cache fallback: %s", output)
	layout, err := os.ReadFile(resultPath)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(string(layout)), filepath.Join(home, ".cache", "direnv", "layouts")+string(os.PathSeparator)))
}

func TestEnvrcRejectsCacheRedirectsFromBothLocalLoaders(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	envrc, err := os.ReadFile(filepath.Join(repoRoot, ".envrc"))
	require.NoError(t, err)
	cacheEnv, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "go-cache-env.sh"))
	require.NoError(t, err)

	for _, loader := range []string{".env.local", ".envrc.local"} {
		for _, variable := range []string{"AZEDARACH_GO_CACHE_ROOT", "AZEDARACH_GOCACHE", "GOCACHE"} {
			t.Run(loader+"/"+variable, func(t *testing.T) {
				checkout := t.TempDir()
				require.NoError(t, os.Mkdir(filepath.Join(checkout, "scripts"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(checkout, ".envrc"), envrc, 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(checkout, "scripts", "go-cache-env.sh"), cacheEnv, 0o755))
				runGit(t, checkout, "init")
				redirect := filepath.Join(t.TempDir(), "redirect")
				require.NoError(t, os.WriteFile(filepath.Join(checkout, loader), []byte(variable+"="+redirect+"\n"), 0o644))

				const script = `
set -eu
nix() { :; }
use() { :; }
PATH_add() { :; }
PATH_rm() { :; }
watch_file() { :; }
dotenv() { set -a; . "$1"; set +a; }
source_env_if_exists() { if [ -f "$1" ]; then . "$1"; fi; }
cd "$CHECKOUT"
. ./.envrc
`
				cmd := exec.Command("bash", "-c", script)
				cmd.Env = append(envWithout("AZEDARACH_GO_CACHE_ROOT", "AZEDARACH_GOCACHE", "GOCACHE"),
					"CHECKOUT="+checkout,
					"HOME="+t.TempDir(),
					"ISSUE_BACKEND=none",
					"AZEDARACH_DIRENV_MANUAL_NIX_RELOAD=0",
					"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
				)
				output, runErr := cmd.CombinedOutput()
				require.Error(t, runErr, "evaluation unexpectedly accepted redirect: %s", output)
				assert.Contains(t, string(output), variable+" must equal")
				assert.NoDirExists(t, redirect)
			})
		}
	}
}

func evaluateEnvrcLayout(t *testing.T, checkout, cacheRoot string) string {
	t.Helper()
	const script = `
set -eu
nix() { :; }
use() {
  [ "$1" = flake ]
  direnv_layout_dir >"$LAYOUT_RESULT"
}
PATH_add() { :; }
PATH_rm() { :; }
watch_file() { :; }
dotenv() { :; }
source_env_if_exists() { :; }
cd "$CHECKOUT"
. ./.envrc >/dev/null
`
	resultPath := filepath.Join(t.TempDir(), "layout")
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(envWithout("AZEDARACH_GO_CACHE_ROOT", "AZEDARACH_GOCACHE", "GOCACHE"),
		"CHECKOUT="+checkout,
		"LAYOUT_RESULT="+resultPath,
		"XDG_CACHE_HOME="+cacheRoot,
		"ISSUE_BACKEND=none",
	)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "evaluate .envrc: %s", output)
	layout, err := os.ReadFile(resultPath)
	require.NoError(t, err)
	return strings.TrimSpace(string(layout))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), output)
}

func envWithout(names ...string) []string {
	excluded := make(map[string]struct{}, len(names))
	for _, name := range names {
		excluded[name] = struct{}{}
	}
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, ok := excluded[name]; !ok {
			env = append(env, entry)
		}
	}
	return env
}

func assertFileExcludesText(t *testing.T, path string, forbidden string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Falsef(t, strings.Contains(string(data), forbidden), "%s contains stale project default %q", path, forbidden)
}
