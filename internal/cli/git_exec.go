package cli

import (
	"os"
	"os/exec"
	"strings"
)

func newGitCommand(projectDir string, args ...string) *exec.Cmd {
	cmdArgs := append([]string{"-C", projectDir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	cmd.Env = gitExecEnvWithoutRoutingVars()
	return cmd
}

func gitExecEnvWithoutRoutingVars() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "GIT_DIR="),
			strings.HasPrefix(kv, "GIT_WORK_TREE="),
			strings.HasPrefix(kv, "GIT_COMMON_DIR="),
			strings.HasPrefix(kv, "GIT_INDEX_FILE="),
			strings.HasPrefix(kv, "GIT_OBJECT_DIRECTORY="),
			strings.HasPrefix(kv, "GIT_ALTERNATE_OBJECT_DIRECTORIES="):
			continue
		default:
			out = append(out, kv)
		}
	}
	return out
}
