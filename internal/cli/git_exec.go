package cli

import (
	"os"
	"strings"
)

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

// GitExecEnvWithoutRoutingVarsForTests exposes sanitized git env for tests
// outside the internal/cli package that need deterministic git subprocesses.
func GitExecEnvWithoutRoutingVarsForTests() []string {
	return gitExecEnvWithoutRoutingVars()
}
