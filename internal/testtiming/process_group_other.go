//go:build !unix

package testtiming

import "os/exec"

func configureProcessGroup(command *exec.Cmd) error { return nil }

func startProcessGroup(command *exec.Cmd, start func(*exec.Cmd) error) error { return start(command) }

func discardProcessGroup(command *exec.Cmd) error { return nil }

func terminateProcessGroup(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}

func waitForProcessGroupExit(command *exec.Cmd) error { return nil }
