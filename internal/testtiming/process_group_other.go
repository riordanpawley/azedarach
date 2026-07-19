//go:build !unix

package testtiming

import "os/exec"

func configureProcessGroup(command *exec.Cmd) {}

func terminateProcessGroup(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}

func waitForProcessGroupExit(command *exec.Cmd) error { return nil }
