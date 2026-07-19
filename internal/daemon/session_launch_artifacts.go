package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
)

type sessionPromptHandoff struct {
	PromptPath string
}

const sessionPromptHandoffPollInterval = 100 * time.Millisecond

type sessionLaunchMode string

const (
	sessionLaunchInitial sessionLaunchMode = "initial"
	sessionLaunchResume  sessionLaunchMode = "resume"
)

// sessionLaunchSpec is the canonical input shared by every session launch
// path. Tool-specific argument selection happens before the artifact is
// written; tmux only ever receives the bounded artifact command.
type sessionLaunchSpec struct {
	Mode                sessionLaunchMode
	ProjectID           string
	IssueID             string
	SessionID           string
	Yolo                bool
	ImagePaths          []string
	Prompt              string
	InitReadyPath       string
	StartupEnvCommands  []string
	CommandPayload      string
	Shell               string
	SanitizeEnvironment bool
	LogicalPaneID       string
	AgentIncarnation    string
}

type sessionLaunchArtifact struct {
	Command       string
	ScriptPath    string
	PromptHandoff sessionPromptHandoff
}

func (a sessionLaunchArtifact) remove() {
	a.PromptHandoff.remove()
	if strings.TrimSpace(a.ScriptPath) != "" {
		_ = os.Remove(a.ScriptPath)
	}
}

func (d *Daemon) prepareSessionLaunchArtifact(spec sessionLaunchSpec) (sessionLaunchArtifact, error) {
	prompt := strings.TrimSpace(spec.Prompt)
	tool := strings.TrimSpace(d.runtimeConfigForProject(spec.ProjectID).CLITool)
	if spec.Mode == sessionLaunchResume && strings.EqualFold(tool, "codex") {
		prompt = ""
	}
	handoff := sessionPromptHandoff{}
	if prompt != "" {
		var err error
		handoff, err = prepareSessionPromptHandoff(d.sessionLaunchArtifactDir(), prompt)
		if err != nil {
			return sessionLaunchArtifact{}, err
		}
	}
	shell, payload := d.buildSessionLaunchArtifactPayload(spec, handoff)
	if strings.TrimSpace(spec.CommandPayload) != "" {
		payload = sessionLaunchContextExport(spec) + sessionLaunchIdentityExport(spec) + spec.CommandPayload
	}
	if strings.TrimSpace(spec.Shell) != "" {
		shell = spec.Shell
	}
	path, command, err := prepareSessionLaunchScript(d.sessionLaunchArtifactDir(), shell, payload)
	if err != nil {
		handoff.remove()
		return sessionLaunchArtifact{}, err
	}
	if spec.SanitizeEnvironment {
		command = "exec " + singleQuoteForShell(advisorEnvExecutable) + " -u BASH_ENV -u ENV -u ZDOTDIR -u ZSH_ENV -u FISH_CONFIG_DIR " + singleQuoteForShell(advisorShellExecutable) + " " + singleQuoteForShell(filepath.ToSlash(path))
	}
	return sessionLaunchArtifact{Command: command, ScriptPath: path, PromptHandoff: handoff}, nil
}

func sessionLaunchContextExport(spec sessionLaunchSpec) string {
	command := sessionLaunchContextExportCommand(spec.ProjectID, spec.IssueID, spec.SessionID)
	if command == "" {
		return ""
	}
	return command + "; "
}

func sessionLaunchIdentityExport(spec sessionLaunchSpec) string {
	if strings.TrimSpace(spec.AgentIncarnation) == "" {
		return ""
	}
	logicalPaneID := strings.TrimSpace(spec.LogicalPaneID)
	if logicalPaneID == "" {
		logicalPaneID = "agent"
	}
	return "export AZEDARACH_LOGICAL_PANE_ID=" + singleQuoteForShell(logicalPaneID) +
		" AZEDARACH_AGENT_INCARNATION=" + singleQuoteForShell(spec.AgentIncarnation) +
		" AZEDARACH_PANE_PID=$$; "
}
func (d *Daemon) sessionLaunchArtifactDir() string {
	base := ""
	if lockPath := strings.TrimSpace(d.cfg.LockPath); lockPath != "" {
		base = filepath.Dir(lockPath)
	} else if repoDir := strings.TrimSpace(d.cfg.RepoDir); repoDir != "" {
		base = filepath.Join(repoDir, ".azedarach", "run")
	} else {
		base = appconfig.GlobalDaemonRuntimeDir()
	}
	return filepath.Join(base, sessionLaunchArtifactDirName)
}

func ensureSessionLaunchArtifactDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("missing session launch artifact directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session launch artifact directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect session launch artifact directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("session launch artifact path is not a directory: %s", dir)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure session launch artifact directory: %w", err)
		}
	}
	return nil
}

// prepareSessionPromptHandoff keeps the full prompt out of every process argv.
// The agent receives only bootstrap text naming this owner-only file.
func prepareSessionPromptHandoff(dir, prompt string) (sessionPromptHandoff, error) {
	if err := ensureSessionLaunchArtifactDir(dir); err != nil {
		return sessionPromptHandoff{}, err
	}
	file, err := os.CreateTemp(dir, sessionLaunchArtifactPrefix+"*.prompt")
	if err != nil {
		return sessionPromptHandoff{}, err
	}
	handoff := sessionPromptHandoff{
		PromptPath: file.Name(),
	}
	cleanup := func() {
		_ = file.Close()
		handoff.remove()
	}
	if _, err := file.WriteString(prompt); err != nil {
		cleanup()
		return sessionPromptHandoff{}, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return sessionPromptHandoff{}, err
	}
	return handoff, nil
}

func (h sessionPromptHandoff) remove() {
	for _, path := range []string{h.PromptPath} {
		if strings.TrimSpace(path) != "" {
			_ = os.Remove(path)
		}
	}
}

func (h sessionPromptHandoff) bootstrapPrompt() string {
	if strings.TrimSpace(h.PromptPath) == "" {
		return ""
	}
	return "Read and follow the complete worker instructions in " + filepath.ToSlash(h.PromptPath) + ". Delete that file immediately after reading it."
}

// waitForSessionPromptHandoffConsumed treats removal of the owner-only prompt
// file as the agent's acknowledgement that it received the complete prompt.
// Startup must not report success while only the short bootstrap instruction
// has been delivered.
func waitForSessionPromptHandoffConsumed(ctx context.Context, handoff sessionPromptHandoff) error {
	path := strings.TrimSpace(handoff.PromptPath)
	if path == "" {
		return nil
	}
	consumed := func() (bool, error) {
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	if done, err := consumed(); done || err != nil {
		return err
	}
	ticker := time.NewTicker(sessionPromptHandoffPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done, err := consumed()
			if err != nil {
				return fmt.Errorf("inspect prompt handoff %s after cancellation: %w", filepath.Base(path), err)
			}
			if done {
				return nil
			}
			return fmt.Errorf("prompt handoff %s was not consumed: %w", filepath.Base(path), ctx.Err())
		case <-ticker.C:
			done, err := consumed()
			if err != nil {
				return fmt.Errorf("inspect prompt handoff %s: %w", filepath.Base(path), err)
			}
			if done {
				return nil
			}
		}
	}
}

type sessionLaunchArtifactCleaner struct {
	dir    string
	cursor *os.File
}

func (c *sessionLaunchArtifactCleaner) close() {
	if c.cursor != nil {
		_ = c.cursor.Close()
		c.cursor = nil
	}
}

func (c *sessionLaunchArtifactCleaner) cleanupBatch(ctx context.Context, now time.Time) (inspected, removed int, err error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	if err := ensureSessionLaunchArtifactDir(c.dir); err != nil {
		return 0, 0, err
	}
	if c.cursor == nil {
		c.cursor, err = os.Open(c.dir)
		if err != nil {
			return 0, 0, err
		}
	}
	for inspected < sessionLaunchArtifactCleanupLimit {
		if err := ctx.Err(); err != nil {
			return inspected, removed, err
		}
		entries, readErr := c.cursor.ReadDir(1)
		if errors.Is(readErr, io.EOF) {
			c.close()
			return inspected, removed, nil
		}
		if readErr != nil {
			c.close()
			return inspected, removed, readErr
		}
		if len(entries) == 0 {
			continue
		}
		inspected++
		entry := entries[0]
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.HasPrefix(entry.Name(), sessionLaunchArtifactPrefix) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < sessionLaunchArtifactMaxAge {
			continue
		}
		if removeErr := os.Remove(filepath.Join(c.dir, entry.Name())); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
			removed++
		}
	}
	return inspected, removed, nil
}

func (c *sessionLaunchArtifactCleaner) run(ctx context.Context, interval time.Duration, onBatch func(int, int, error)) {
	defer c.close()
	runBatch := func() {
		inspected, removed, err := c.cleanupBatch(ctx, time.Now())
		if onBatch != nil {
			onBatch(inspected, removed, err)
		}
	}
	runBatch()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runBatch()
		}
	}
}

func (d *Daemon) startSessionLaunchArtifactCleanup(ctx context.Context) {
	cleaner := &sessionLaunchArtifactCleaner{dir: d.sessionLaunchArtifactDir()}
	go cleaner.run(ctx, sessionLaunchArtifactCleanupEvery, func(inspected, removed int, err error) {
		if err != nil && !errors.Is(err, context.Canceled) && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session launch artifact cleanup failed", "error", err)
		} else if removed > 0 && d.cfg.Logger != nil {
			d.cfg.Logger.Info("removed stale session launch artifacts", "inspected", inspected, "removed", removed)
		}
	})
}
