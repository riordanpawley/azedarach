package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

const (
	fakeAdvisorModePreInitFailure     = "pre-init-failure"
	fakeAdvisorModeVisibleNotReady    = "visible-without-readiness"
	fakeAdvisorModeReady              = "post-init-readiness"
	fakeAdvisorModeExitAfterReadiness = "exit-after-readiness"
	fakeAdvisorProcessEnvironment     = "GO_WANT_AZEDARACH_FAKE_ADVISOR"
	fakeAdvisorToolEnvironment        = "AZEDARACH_FAKE_ADVISOR_TOOL"
	fakeAdvisorModeEnvironment        = "AZEDARACH_FAKE_ADVISOR_MODE"
	fakeAdvisorEventFDEnvironment     = "AZEDARACH_FAKE_ADVISOR_EVENT_FD"
	fakeAdvisorControlFDEnvironment   = "AZEDARACH_FAKE_ADVISOR_CONTROL_FD"
)

type fakeAdvisorLaunchObservation struct {
	SessionID string
	Tool      string
	Mode      string
	Event     string
	Live      bool
}

type fakeAdvisorTmuxProcess struct {
	cmd     *exec.Cmd
	control *os.File
	done    chan struct{}
	pid     int
	tool    string
	live    bool
}

// fakeAdvisorTmuxRunner is deliberately process-backed. Its new-session path
// executes the exact single command generated for tmux, while the remaining
// methods expose only the runtime facts used by the production readiness gate.
type fakeAdvisorTmuxRunner struct {
	mu        sync.Mutex
	mode      string
	processes map[string]*fakeAdvisorTmuxProcess
	observed  chan fakeAdvisorLaunchObservation
}

func newFakeAdvisorTmuxRunner() *fakeAdvisorTmuxRunner {
	return &fakeAdvisorTmuxRunner{
		processes: make(map[string]*fakeAdvisorTmuxProcess),
		observed:  make(chan fakeAdvisorLaunchObservation, 16),
	}
}

func (r *fakeAdvisorTmuxRunner) setMode(mode string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode = mode
}

func (r *fakeAdvisorTmuxRunner) isLive(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	process := r.processes[sessionID]
	return process != nil && process.live
}

func (r *fakeAdvisorTmuxRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("missing tmux command")
	}
	switch args[0] {
	case "new-session":
		return "", r.newSession(args)
	case "has-session":
		sessionID, err := tmuxTargetArg(args)
		if err != nil {
			return "", err
		}
		if r.isLive(sessionID) {
			return "", nil
		}
		return "", errors.New("missing session")
	case "list-panes":
		r.mu.Lock()
		defer r.mu.Unlock()
		var lines []string
		for sessionID, process := range r.processes {
			if process.live {
				lines = append(lines, fmt.Sprintf("%s\t%%1\t%d\t%s", sessionID, process.pid, process.tool))
			}
		}
		return strings.Join(lines, "\n"), nil
	case "kill-session":
		sessionID, err := tmuxTargetArg(args)
		if err != nil {
			return "", err
		}
		return "", r.killSession(sessionID)
	case "capture-pane":
		return "", nil
	default:
		return "", nil
	}
}

func tmuxTargetArg(args []string) (string, error) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "-t" {
			return args[index+1], nil
		}
	}
	return "", fmt.Errorf("tmux command has no target: %v", args)
}

func tmuxNamedArg(args []string, name string) (string, error) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1], nil
		}
	}
	return "", fmt.Errorf("tmux command has no %s argument: %v", name, args)
}

func (r *fakeAdvisorTmuxRunner) newSession(args []string) error {
	sessionID, err := tmuxNamedArg(args, "-s")
	if err != nil {
		return err
	}
	workdir, err := tmuxNamedArg(args, "-c")
	if err != nil {
		return err
	}
	if len(args) == 0 || strings.TrimSpace(args[len(args)-1]) == "" {
		return errors.New("new-session has no generated command")
	}
	r.mu.Lock()
	mode := r.mode
	r.mu.Unlock()
	if mode == "" {
		return errors.New("fake advisor mode is unset")
	}

	eventRead, eventWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		_ = eventRead.Close()
		_ = eventWrite.Close()
		return err
	}
	command := exec.Command("/bin/sh", "-c", args[len(args)-1])
	command.Dir = workdir
	command.Env = append(os.Environ(),
		"TMUX_PANE=%1",
		fakeAdvisorModeEnvironment+"="+mode,
		fakeAdvisorEventFDEnvironment+"=3",
		fakeAdvisorControlFDEnvironment+"=4",
	)
	command.ExtraFiles = []*os.File{eventWrite, controlRead}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = eventRead.Close()
		_ = eventWrite.Close()
		_ = controlRead.Close()
		_ = controlWrite.Close()
		return err
	}
	_ = eventWrite.Close()
	_ = controlRead.Close()
	process := &fakeAdvisorTmuxProcess{
		cmd: command, control: controlWrite, done: make(chan struct{}),
		pid: command.Process.Pid, live: true,
	}
	r.mu.Lock()
	r.processes[sessionID] = process
	r.mu.Unlock()
	go func() {
		_ = command.Wait()
		_ = process.control.Close()
		r.mu.Lock()
		process.live = false
		r.mu.Unlock()
		close(process.done)
	}()

	event, readErr := bufio.NewReader(eventRead).ReadString('\n')
	_ = eventRead.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		_ = r.killSession(sessionID)
		return fmt.Errorf("read fake advisor lifecycle event: %w", readErr)
	}
	event = strings.TrimSpace(event)
	if strings.HasPrefix(event, "error:") || event == "" {
		_ = r.killSession(sessionID)
		r.observed <- fakeAdvisorLaunchObservation{SessionID: sessionID, Mode: mode, Event: event}
		return fmt.Errorf("fake advisor did not consume generated readiness configuration: %s", event)
	}
	_, tool, found := strings.Cut(event, ":")
	if !found || strings.TrimSpace(tool) == "" {
		_ = r.killSession(sessionID)
		r.observed <- fakeAdvisorLaunchObservation{SessionID: sessionID, Mode: mode, Event: event}
		return fmt.Errorf("fake advisor event %q has no tool identity", event)
	}
	process.tool = strings.TrimSpace(tool)
	if mode == fakeAdvisorModePreInitFailure || mode == fakeAdvisorModeExitAfterReadiness {
		<-process.done
	}
	r.observed <- fakeAdvisorLaunchObservation{SessionID: sessionID, Tool: process.tool, Mode: mode, Event: event, Live: r.isLive(sessionID)}
	return nil
}

func (r *fakeAdvisorTmuxRunner) killSession(sessionID string) error {
	r.mu.Lock()
	process := r.processes[sessionID]
	if process == nil || !process.live {
		r.mu.Unlock()
		return nil
	}
	process.live = false
	r.mu.Unlock()
	_ = process.control.Close()
	_ = syscall.Kill(-process.pid, syscall.SIGKILL)
	<-process.done
	return nil
}

func (r *fakeAdvisorTmuxRunner) closeAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.processes))
	for sessionID := range r.processes {
		ids = append(ids, sessionID)
	}
	r.mu.Unlock()
	for _, sessionID := range ids {
		_ = r.killSession(sessionID)
	}
}

// TestAdvisorFakeToolProcess is invoked only through a generated advisor
// launch artifact. Each tool parses its own daemon-generated configuration and
// crosses readiness by dispatching that configuration's startup event.
func TestAdvisorFakeToolProcess(t *testing.T) {
	if os.Getenv(fakeAdvisorProcessEnvironment) != "1" {
		return
	}
	if err := runFakeAdvisorToolProcess(); err != nil {
		fakeAdvisorEmit("error:" + err.Error())
		t.Fatalf("fake advisor process: %v", err)
	}
}

func runFakeAdvisorToolProcess() error {
	tool := strings.TrimSpace(os.Getenv(fakeAdvisorToolEnvironment))
	mode := strings.TrimSpace(os.Getenv(fakeAdvisorModeEnvironment))
	ready, err := fakeAdvisorReadinessAction(tool, os.Args)
	if err != nil {
		return err
	}
	switch mode {
	case fakeAdvisorModePreInitFailure:
		fakeAdvisorEmit("consumed:" + tool)
		return errors.New("controlled pre-initialization failure")
	case fakeAdvisorModeVisibleNotReady:
		fakeAdvisorEmit("consumed:" + tool)
		return fakeAdvisorWaitForControl()
	case fakeAdvisorModeReady, fakeAdvisorModeExitAfterReadiness:
		if err := ready(); err != nil {
			return fmt.Errorf("dispatch generated readiness configuration: %w", err)
		}
		fakeAdvisorEmit("ready:" + tool)
		if mode == fakeAdvisorModeExitAfterReadiness {
			return nil
		}
		return fakeAdvisorWaitForControl()
	default:
		return fmt.Errorf("unsupported fake advisor mode %q", mode)
	}
}

func fakeAdvisorReadinessAction(tool string, args []string) (func() error, error) {
	switch tool {
	case "codex":
		return fakeAdvisorHookAction(filepath.Join(os.Getenv("CODEX_HOME"), "hooks.json"))
	case "claude":
		settings := ""
		for index := 0; index+1 < len(args); index++ {
			if args[index] == "--settings" {
				settings = args[index+1]
				break
			}
		}
		if settings == "" {
			return nil, errors.New("generated Claude command has no --settings")
		}
		return fakeAdvisorHookAction(settings)
	case "opencode":
		return fakeOpenCodeReadinessAction()
	default:
		return nil, fmt.Errorf("unsupported fake advisor tool %q", tool)
	}
}

func fakeAdvisorHookAction(path string) (func() error, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read generated hook configuration %s: %w", path, err)
	}
	var config struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		return nil, fmt.Errorf("decode generated hook configuration: %w", err)
	}
	starts := config.Hooks["SessionStart"]
	if len(starts) != 1 || len(starts[0].Hooks) != 1 || starts[0].Hooks[0].Type != "command" || strings.TrimSpace(starts[0].Hooks[0].Command) == "" {
		return nil, errors.New("generated SessionStart hook is missing or ambiguous")
	}
	command := starts[0].Hooks[0].Command
	return func() error {
		cmd := exec.Command("/bin/sh", "-c", command)
		cmd.Env = os.Environ()
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("run %q: %w: %s", command, err, strings.TrimSpace(string(output)))
		}
		return nil
	}, nil
}

func fakeOpenCodeReadinessAction() (func() error, error) {
	workdir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	pluginPath := filepath.Join(workdir, ".opencode", "plugins", "advisor-readiness.js")
	body, err := os.ReadFile(pluginPath)
	if err != nil {
		return nil, fmt.Errorf("read generated OpenCode plugin: %w", err)
	}
	source := string(body)
	if !strings.Contains(source, "event.type !== 'session.created'") {
		return nil, errors.New("generated OpenCode plugin has no session.created readiness event")
	}
	incarnationMatch := regexp.MustCompile(`const body = ("(?:[^"\\]|\\.)*") \+`).FindStringSubmatch(source)
	signalMatch := regexp.MustCompile(`Bun\.write\(("(?:[^"\\]|\\.)*") \+ '\.tmp'`).FindStringSubmatch(source)
	if len(incarnationMatch) != 2 || len(signalMatch) != 2 {
		return nil, errors.New("generated OpenCode plugin lacks readiness identity or signal path")
	}
	incarnation, err := strconv.Unquote(incarnationMatch[1])
	if err != nil {
		return nil, err
	}
	signalPath, err := strconv.Unquote(signalMatch[1])
	if err != nil {
		return nil, err
	}
	return func() error {
		body := []byte(incarnation + "\t" + os.Getenv("TMUX_PANE") + "\t" + os.Getenv("AZEDARACH_PANE_PID") + "\n")
		if err := os.WriteFile(signalPath+".tmp", body, 0o600); err != nil {
			return err
		}
		return os.WriteFile(signalPath, body, 0o600)
	}, nil
}

func fakeAdvisorEmit(event string) {
	fd, err := strconv.Atoi(os.Getenv(fakeAdvisorEventFDEnvironment))
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(os.NewFile(uintptr(fd), "advisor-event"), event)
}

func fakeAdvisorWaitForControl() error {
	fd, err := strconv.Atoi(os.Getenv(fakeAdvisorControlFDEnvironment))
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, os.NewFile(uintptr(fd), "advisor-control"))
	return err
}

func installFakeAdvisorTools(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"codex", "claude", "opencode"} {
		path := filepath.Join(binDir, tool)
		script := "#!/bin/sh\n" + fakeAdvisorProcessEnvironment + "=1 " + fakeAdvisorToolEnvironment + "=" + tool + " exec " + singleQuoteForShell(testBinary) + " -test.run='^TestAdvisorFakeToolProcess$' -- \"$@\"\n"
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return binDir
}

func TestAdvisorGeneratedReadinessActivePath(t *testing.T) {
	originalPath := os.Getenv("PATH")
	fakeBin := installFakeAdvisorTools(t)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+originalPath)
	t.Setenv("CODEX_HOME", t.TempDir())

	for _, tool := range []string{"codex", "claude", "opencode"} {
		tool := tool
		for _, failureMode := range []string{fakeAdvisorModePreInitFailure, fakeAdvisorModeVisibleNotReady, fakeAdvisorModeExitAfterReadiness} {
			failureMode := failureMode
			t.Run(tool+"/"+failureMode, func(t *testing.T) {
				ctx := withDaemonProjectIDContext(context.Background(), protocol.DefaultProjectID)
				repoDir := t.TempDir()
				client := newMigratedIssueClient(t, repoDir, slog.Default())
				issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "advisor active readiness", Type: domain.TypeTask, Status: domain.StatusOpen})
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC()
				request := domain.InteractionRequest{ID: "request-" + tool + "-" + failureMode, IssueID: issueID, DecisionKey: "choice", OrchestrationScope: "project", Question: "Which option?", Why: "Human judgment", Options: []domain.InteractionOption{{Key: "a", Label: "A"}}, Significance: domain.InteractionSignificanceRoutine, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose"}, State: domain.InteractionDiscussing, Revision: 1, CreatedAt: now, UpdatedAt: now}
				if err := client.CreateInteraction(ctx, request); err != nil {
					t.Fatal(err)
				}
				runner := newFakeAdvisorTmuxRunner()
				t.Cleanup(runner.closeAll)
				d := &Daemon{cfg: Config{RepoDir: repoDir, CLITool: tool, Logger: slog.Default()}, issues: client, tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore()}
				runner.setMode(failureMode)
				failureCtx, cancelFailure := context.WithCancel(ctx)
				type ensureResult struct {
					value advisorSessionRuntimeResult
					err   error
				}
				resultCh := make(chan ensureResult, 1)
				go func() {
					value, ensureErr := d.ensureAdvisorSessionRuntime(failureCtx, protocol.DefaultProjectID, request)
					resultCh <- ensureResult{value: value, err: ensureErr}
				}()
				observation := <-runner.observed
				sessionID := advisorSessionID(request.ID)
				if observation.Tool != tool || observation.Mode != failureMode || observation.SessionID != sessionID {
					t.Fatalf("failure launch observation = %+v", observation)
				}
				wantEvent := "consumed:" + tool
				wantLive := failureMode == fakeAdvisorModeVisibleNotReady
				if failureMode == fakeAdvisorModeExitAfterReadiness {
					wantEvent = "ready:" + tool
				}
				if observation.Event != wantEvent || observation.Live != wantLive || runner.isLive(sessionID) != wantLive {
					t.Fatalf("failure observation = %+v runtimeLive=%t", observation, runner.isLive(sessionID))
				}
				cancelFailure()
				failed := <-resultCh
				if failed.err == nil || failed.value.Started {
					t.Fatalf("failed advisor launch result=%+v err=%v", failed.value, failed.err)
				}
				store := d.sessionRuntimeStateStore(protocol.DefaultProjectID)
				projectID := d.canonicalProjectID(protocol.DefaultProjectID)
				if identity, found, err := store.GetManagedAgentIdentity(ctx, projectID, sessionID, "agent"); err != nil || found {
					t.Fatalf("failed advisor launch identity=%+v found=%t err=%v", identity, found, err)
				}
				projection, found, err := store.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleAdvisor, daemonstate.SessionScopeInteraction, request.ID)
				if err != nil || !found || projection.State != daemonstate.SessionStateStopped {
					t.Fatalf("failed advisor projection=%+v found=%t err=%v", projection, found, err)
				}
				if runner.isLive(sessionID) {
					t.Fatalf("failed advisor runtime %s survived compensation", sessionID)
				}

				runner.setMode(fakeAdvisorModeReady)
				retried, retryErr := d.ensureAdvisorSessionRuntime(ctx, protocol.DefaultProjectID, request)
				if retryErr != nil || !retried.Started || retried.Attached || !runner.isLive(sessionID) {
					t.Fatalf("advisor retry result=%+v err=%v live=%t", retried, retryErr, runner.isLive(sessionID))
				}
				retryObservation := <-runner.observed
				if retryObservation.Tool != tool || retryObservation.Mode != fakeAdvisorModeReady || retryObservation.Event != "ready:"+tool || !retryObservation.Live {
					t.Fatalf("retry observation = %+v", retryObservation)
				}
				identity, found, err := store.GetManagedAgentIdentity(ctx, projectID, sessionID, "agent")
				if err != nil || !found || identity.TmuxPaneID != "1" || identity.PanePID <= 0 || strings.TrimSpace(identity.AgentIncarnation) == "" {
					t.Fatalf("successful advisor identity=%+v found=%t err=%v", identity, found, err)
				}
			})
		}
	}
}
