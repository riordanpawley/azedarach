package daemon

import (
	"fmt"
)

func (d *Daemon) buildSessionLaunchCommand(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt string) string {
	return d.buildSessionLaunchCommandWithInitReadyPath(projectID, issueID, sessionID, yolo, imagePaths, initialPrompt, "")
}

func (d *Daemon) buildSessionLaunchCommandWithInitReadyPath(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt, initReadyPath string) string {
	return d.buildSessionLaunchCommandWithInitReadyPathAndEnv(projectID, issueID, sessionID, yolo, imagePaths, initialPrompt, initReadyPath, nil)
}

func (d *Daemon) buildSessionLaunchCommandWithInitReadyPathAndEnv(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt, initReadyPath string, startupEnvCommands []string) string {
	shell, inner := d.buildSessionLaunchComponentsWithInitReadyPathAndEnvPolicy(projectID, issueID, sessionID, yolo, imagePaths, initialPrompt, initReadyPath, startupEnvCommands, false)
	return fmt.Sprintf("%s -i -c %s", shell, singleQuoteForShell(inner))
}

func (d *Daemon) buildSessionLaunchScriptPayloadWithInitReadyPathAndEnv(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initReadyPath string, startupEnvCommands []string, promptHandoff sessionPromptHandoff) (string, string) {
	return d.buildSessionLaunchComponentsWithInitReadyPathAndEnvPolicy(projectID, issueID, sessionID, yolo, imagePaths, promptHandoff.bootstrapPrompt(), initReadyPath, startupEnvCommands, false)
}

func (d *Daemon) buildSessionResumeCommand(projectID, issueID, sessionID string, yolo bool, imagePaths []string) string {
	tool := d.runtimeConfigForProject(projectID).CLITool
	switch tool {
	case "codex":
		return d.buildCodexResumeCommand(projectID, issueID, yolo, imagePaths)
	case "opencode":
		return d.buildOpenCodeContinueCommand(projectID, issueID, sessionRestartContinuePrompt)
	case "", "claude":
		return d.buildClaudeContinueCommand(projectID, issueID, yolo, sessionRestartContinuePrompt)
	default:
		return d.buildConfiguredToolContinueCommand(projectID, issueID, sessionRestartContinuePrompt)
	}
}
