package tmux

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestListAttachedClientsFiltersExactSessionAndParsesReadOnly(t *testing.T) {
	runner := &recordingOutputRunner{outputs: []string{"/dev/tty1\taz-dlb\tread-only,ignore-size\t1\n/dev/tty2\tother\t\t0\n/dev/tty3\taz-dlb\t\t0\n"}}
	client := NewClient(runner, slog.Default())
	got, err := client.ListAttachedClients(context.Background(), "az-dlb")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ClientName != "/dev/tty1" || !got[0].ReadOnly || got[1].ReadOnly {
		t.Fatalf("clients = %+v", got)
	}
}

func TestListAttachedClientsEmptySessionListsAllClients(t *testing.T) {
	runner := &recordingOutputRunner{outputs: []string{"/dev/tty1\taz-dlb\tread-only\t1\n/dev/tty2\tother\t\t0\n"}}
	client := NewClient(runner, slog.Default())
	got, err := client.ListAttachedClients(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SessionName != "az-dlb" || got[1].SessionName != "other" {
		t.Fatalf("clients = %+v", got)
	}
}

func TestSetClientReadOnlyChangesOnlyReadOnlyFlag(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(runner, slog.Default())
	if err := client.SetClientReadOnly(context.Background(), "tty", true); err != nil {
		t.Fatal(err)
	}
	if err := client.SetClientReadOnly(context.Background(), "tty", false); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"refresh-client", "-t", "tty", "-f", "read-only"}, {"refresh-client", "-t", "tty", "-f", "!read-only"}}
	if len(runner.commands) != len(want) {
		t.Fatalf("commands = %#v", runner.commands)
	}
	for i := range want {
		if strings.Join(runner.commands[i], "\x00") != strings.Join(want[i], "\x00") {
			t.Fatalf("command[%d] = %#v, want %#v", i, runner.commands[i], want[i])
		}
	}
}

func TestSessionReadOnlyAttachHooksRecordBeforeGating(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(runner, slog.Default())
	if err := client.SetSessionReadOnlyAttachHooks(context.Background(), "az-dlb", "9137", "/tmp/gate's.events", true); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	for _, command := range runner.commands {
		joined := strings.Join(command, " ")
		if !strings.Contains(joined, "#{q:hook_client}") || !strings.Contains(joined, "#{client_readonly}") ||
			!strings.Contains(joined, "refresh-client -t") || !strings.Contains(joined, "-f read-only") {
			t.Fatalf("hook does not record and gate client: %s", joined)
		}
	}
}

type isolatedTmuxRunner struct{ socket string }

func (r isolatedTmuxRunner) Run(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "tmux", append([]string{"-L", r.socket}, args...)...).CombinedOutput()
	return string(out), err
}

func TestRealTmuxSessionReadOnlyAttachHookGatesAndRecordsNewClient(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	socket := "az-input-gate-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	runner := isolatedTmuxRunner{socket: socket}
	client := NewClient(runner, slog.Default())
	if _, err := runner.Run(ctx, "new-session", "-d", "-s", "gate", "sleep 10"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = runner.Run(context.Background(), "kill-server") })
	recordPath := filepath.Join(t.TempDir(), "attach.events")
	if err := os.WriteFile(recordPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.SetSessionReadOnlyAttachHooks(ctx, "gate", "9137", recordPath, true); err != nil {
		t.Fatal(err)
	}
	attachOutputPath := filepath.Join(t.TempDir(), "attach.output")
	attachOutput, err := os.OpenFile(attachOutputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	attach := exec.CommandContext(ctx, "script", "-q", "/dev/null", "tmux", "-L", socket, "attach-session", "-t", "gate")
	attach.Env = realTmuxTestTerminalEnv()
	attach.Stdout = attachOutput
	attach.Stderr = attachOutput
	stdin, err := attach.StdinPipe()
	if err != nil {
		_ = attachOutput.Close()
		t.Fatal(err)
	}
	if err := attach.Start(); err != nil {
		_ = stdin.Close()
		_ = attachOutput.Close()
		t.Fatal(err)
	}
	attachDone := make(chan error, 1)
	go func() { attachDone <- attach.Wait() }()
	attachExited := false
	stopAttach := func() (error, string) {
		var waitErr error
		_ = stdin.Close()
		if !attachExited {
			_ = attach.Process.Kill()
			select {
			case waitErr = <-attachDone:
			case <-time.After(time.Second):
				waitErr = errors.New("timed out waiting for attach subprocess exit")
			}
			attachExited = true
		}
		_ = attachOutput.Sync()
		raw, readErr := os.ReadFile(attachOutputPath)
		if readErr != nil {
			return errors.Join(waitErr, readErr), ""
		}
		return waitErr, string(raw)
	}
	t.Cleanup(func() {
		_, _ = stopAttach()
		_ = attachOutput.Close()
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case attachErr := <-attachDone:
			attachExited = true
			_, output := stopAttach()
			t.Fatalf("tmux attach subprocess exited before the client was gated: err=%v output=%q", attachErr, output)
		default:
		}
		clients, listErr := client.ListAttachedClients(ctx, "gate")
		raw, readErr := os.ReadFile(recordPath)
		if listErr == nil && readErr == nil && len(clients) == 1 && clients[0].ReadOnly && strings.Contains(string(raw), "\t0\n") {
			break
		}
		if time.Now().After(deadline) {
			attachErr, output := stopAttach()
			t.Fatalf("new client was not synchronously recorded and gated: clients=%+v list_err=%v record=%q record_err=%v attach_err=%v attach_output=%q", clients, listErr, raw, readErr, attachErr, output)
		}
		time.Sleep(25 * time.Millisecond)
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\t0\n") {
		t.Fatalf("original writable flag was not recorded: %q", raw)
	}
}

func TestRealTmuxPaneInputFenceBlocksImmediateAttachInputBeforeDelayedHook(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	if _, err := exec.LookPath("expect"); err != nil {
		t.Skip("expect unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	socket := "az-input-attach-fence-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	runner := isolatedTmuxRunner{socket: socket}
	client := NewClient(runner, slog.Default())
	const readyMarker = "AZEDARACH_ATTACH_READY_9138>"
	if _, err := runner.Run(ctx, "new-session", "-d", "-s", "gate", "PS1='"+readyMarker+" '; export PS1; exec /bin/sh -i"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = runner.Run(context.Background(), "kill-server") })
	const target = "gate:"
	paneOutputPath := filepath.Join(t.TempDir(), "pane.output")
	if err := os.WriteFile(paneOutputPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, "pipe-pane", "-t", target, "cat >> "+shellSingleQuote(paneOutputPath)); err != nil {
		t.Fatal(err)
	}

	// Prove the exact real attach, readiness, and send path before using absence
	// of pane output as evidence. The helper cannot succeed until it observes the
	// deterministic prompt from the attached tmux terminal.
	const allowed = "AZEDARACH_ALLOWED_ATTACH_SENTINEL_9138"
	allowedOutput, err := runRealTmuxAttachInput(ctx, socket, readyMarker, allowed, "", "")
	if err != nil {
		t.Fatalf("send unfenced control input through attached tmux client: %v output=%q", err, allowedOutput)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		output, captureErr := runner.Run(ctx, "capture-pane", "-p", "-t", target)
		paneOutput, readErr := os.ReadFile(paneOutputPath)
		if captureErr == nil && readErr == nil && strings.Contains(output, allowed) && strings.Contains(string(paneOutput), allowed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("proven attach path did not deliver unfenced control input: pane=%q capture_err=%v live_output=%q read_err=%v attach_output=%q", output, captureErr, paneOutput, readErr, allowedOutput)
		}
		time.Sleep(25 * time.Millisecond)
	}

	if err := client.SetPaneInputEnabled(ctx, target, false); err != nil {
		t.Fatal(err)
	}

	recordPath := filepath.Join(t.TempDir(), "attach.events")
	hookStartedPath := filepath.Join(t.TempDir(), "attach-hook.started")
	hookCompletedPath := filepath.Join(t.TempDir(), "attach-hook.completed")
	if err := os.WriteFile(recordPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Deliberately preserve the vulnerable shape from the regression: tmux runs
	// the shell hook asynchronously from client input, and the refresh is late.
	// The pane-wide fence must be sufficient even while this hook is sleeping.
	record := "umask 077; : > " + shellSingleQuote(hookStartedPath) + "; sleep 1; __az_client=#{q:hook_client}; printf '%s\\t%s\\n' \"$__az_client\" #{client_readonly} >> " + shellSingleQuote(recordPath) + "; tmux refresh-client -t \"$__az_client\" -f read-only; : > " + shellSingleQuote(hookCompletedPath)
	for _, hook := range []string{"client-attached", "client-session-changed"} {
		name := hook + "[9138]"
		if _, err := runner.Run(ctx, "set-hook", "-t", "gate", name, "run-shell "+tmuxDoubleQuote(record)); err != nil {
			t.Fatal(err)
		}
	}

	const blocked = "AZEDARACH_BLOCKED_ATTACH_SENTINEL_9138"
	blockedOutput, err := runRealTmuxAttachInput(ctx, socket, readyMarker, blocked, hookStartedPath, hookCompletedPath)
	if err != nil {
		t.Fatalf("send immediate input through attached tmux client: %v output=%q", err, blockedOutput)
	}
	// Observe through the authoritative end of the adversarial window. pipe-pane
	// writes independently while run-shell blocks tmux's command queue, allowing
	// the test to detect admitted input immediately instead of sampling early.
	hookCtx, cancelHook := context.WithTimeout(ctx, 3*time.Second)
	defer cancelHook()
	for {
		paneOutput, readErr := os.ReadFile(paneOutputPath)
		if readErr != nil {
			t.Fatalf("read live pane output before delayed hook completion: %v", readErr)
		}
		if strings.Contains(string(paneOutput), blocked) {
			t.Fatalf("input reached the pane while the delayed attach hook was pending: %q", paneOutput)
		}
		_, completionErr := os.Stat(hookCompletedPath)
		if completionErr == nil {
			break
		}
		if !errors.Is(completionErr, os.ErrNotExist) {
			t.Fatalf("inspect delayed attach hook completion marker: %v", completionErr)
		}
		select {
		case <-hookCtx.Done():
			t.Fatalf("delayed attach hook did not complete: %v attach_output=%q", context.Cause(hookCtx), blockedOutput)
		case <-time.After(10 * time.Millisecond):
		}
	}
	paneOutput, err := os.ReadFile(paneOutputPath)
	if err != nil {
		t.Fatalf("read live pane output after delayed hook completion: %v", err)
	}
	if strings.Contains(string(paneOutput), blocked) {
		t.Fatalf("input reached pane output by delayed hook completion: %q", paneOutput)
	}
	output, err := runner.Run(ctx, "capture-pane", "-p", "-t", target)
	if err != nil {
		t.Fatalf("capture fenced pane after delayed hook completion: %v attach_output=%q", err, blockedOutput)
	}
	if strings.Contains(output, blocked) {
		t.Fatalf("input reached the pane after the delayed attach hook completed: %q", output)
	}
}

func runRealTmuxAttachInput(ctx context.Context, socket, readyMarker, sentinel, hookStartedPath, hookCompletedPath string) ([]byte, error) {
	const program = `
set timeout 5
log_user 1
spawn tmux -L $env(AZ_TEST_TMUX_SOCKET) attach-session -t gate
expect {
    -exact "$env(AZ_TEST_TMUX_READY)" {}
    timeout { send_user "timed out waiting for attached terminal readiness\n"; exit 2 }
    eof { send_user "tmux attach exited before terminal readiness\n"; exit 3 }
}
if {$env(AZ_TEST_TMUX_HOOK_STARTED) ne ""} {
    set hook_deadline [expr {[clock milliseconds] + 2000}]
    while {![file exists $env(AZ_TEST_TMUX_HOOK_STARTED)]} {
        if {[clock milliseconds] >= $hook_deadline} {
            send_user "timed out waiting for delayed attach hook to start\n"
            exit 4
        }
        after 10
    }
    if {[file exists $env(AZ_TEST_TMUX_HOOK_COMPLETED)]} {
        send_user "delayed attach hook completed before input was sent\n"
        exit 5
    }
}
send -- "$env(AZ_TEST_TMUX_SENTINEL)\r"
after 100
exit 0
`
	command := exec.CommandContext(ctx, "expect", "-c", program)
	command.Env = realTmuxTestTerminalEnv()
	command.Env = append(command.Env,
		"AZ_TEST_TMUX_SOCKET="+socket,
		"AZ_TEST_TMUX_READY="+readyMarker,
		"AZ_TEST_TMUX_SENTINEL="+sentinel,
		"AZ_TEST_TMUX_HOOK_STARTED="+hookStartedPath,
		"AZ_TEST_TMUX_HOOK_COMPLETED="+hookCompletedPath,
	)
	return command.CombinedOutput()
}

func realTmuxTestTerminalEnv() []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "TERM=") {
			environment = append(environment, entry)
		}
	}
	return append(environment, "TERM=xterm-256color")
}

func TestRealTmuxPaneInputFencePreservesUnsubmittedDraft(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	socket := "az-input-draft-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	runner := isolatedTmuxRunner{socket: socket}
	client := NewClient(runner, slog.Default())
	if _, err := runner.Run(ctx, "new-session", "-d", "-s", "draft", "sh"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = runner.Run(context.Background(), "kill-server") })
	const draft = "azedarach-unsent-draft-7f9c"
	const target = "draft:"
	if _, err := runner.Run(ctx, "send-keys", "-t", target, "-l", draft); err != nil {
		t.Fatal(err)
	}
	before, err := runner.Run(ctx, "capture-pane", "-p", "-t", target)
	if err != nil || !strings.Contains(before, draft) {
		t.Fatalf("draft not visible before fence: output=%q err=%v", before, err)
	}
	if err := client.SetPaneInputEnabled(ctx, target, false); err != nil {
		t.Fatal(err)
	}
	if err := client.SetPaneInputEnabled(ctx, target, true); err != nil {
		t.Fatal(err)
	}
	after, err := runner.Run(ctx, "capture-pane", "-p", "-t", target)
	if err != nil || !strings.Contains(after, draft) {
		t.Fatalf("pane input fence changed draft: output=%q err=%v", after, err)
	}
}
