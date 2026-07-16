package tmux

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
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
	if len(runner.commands) != 3 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	if got := runner.commands[0]; len(got) != 4 || got[0] != "set-option" || got[3] != "1" {
		t.Fatalf("hook generation was not opened first: %#v", got)
	}
	for _, command := range runner.commands[1:] {
		joined := strings.Join(command, " ")
		if !strings.Contains(joined, "#{q:hook_client}") || !strings.Contains(joined, "#{client_readonly}") ||
			!strings.Contains(joined, "refresh-client -t") || !strings.Contains(joined, "-f read-only") ||
			!strings.Contains(joined, "\\\\tcomplete\\\\n") || !strings.Contains(joined, "wait-for -L az-codex-input-gate-") ||
			!strings.Contains(joined, "wait-for -U az-codex-input-gate-") || !strings.Contains(joined, "show-options -gqv @az_codex_input_gate_") ||
			!strings.Contains(joined, "trap") {
			t.Fatalf("hook does not record and gate client: %s", joined)
		}
		recordAt := strings.Index(joined, "printf '%s\\\\t%s\\\\n'")
		gateAt := strings.Index(joined, "refresh-client -t")
		completeAt := strings.Index(joined, "printf '\\\\tcomplete\\\\n'")
		if recordAt < 0 || gateAt <= recordAt || completeAt <= gateAt {
			t.Fatalf("hook does not record, gate, then publish completion: %s", joined)
		}
	}
}

func TestSessionReadOnlyAttachHookLockMatchesInstalledHook(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(runner, slog.Default())
	if err := client.SetSessionReadOnlyAttachHooks(context.Background(), "az-dlb", "9137", "/tmp/gate.events", true); err != nil {
		t.Fatal(err)
	}
	if err := client.LockSessionReadOnlyAttachHooks(context.Background(), "9137", "/tmp/gate.events", true); err != nil {
		t.Fatal(err)
	}
	if err := client.LockSessionReadOnlyAttachHooks(context.Background(), "9137", "/tmp/gate.events", false); err != nil {
		t.Fatal(err)
	}
	lock := sessionReadOnlyAttachHookLock("9137", "/tmp/gate.events")
	for _, command := range runner.commands[1:3] {
		if !strings.Contains(strings.Join(command, " "), lock) {
			t.Fatalf("installed hook does not use lock %q: %#v", lock, command)
		}
	}
	want := [][]string{{"wait-for", "-L", lock}, {"wait-for", "-U", lock}}
	for i, command := range [][]string{runner.commands[3], runner.commands[4]} {
		if strings.Join(command, "\x00") != strings.Join(want[i], "\x00") {
			t.Fatalf("lock command[%d] = %#v, want %#v", i, command, want[i])
		}
	}
	if got := runner.commands[5]; len(got) != 3 || got[0] != "set-option" || got[1] != "-gu" {
		t.Fatalf("closed hook generation option was not removed: %#v", got)
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
	if err := waitForFileContent(ctx, recordPath, "\tcomplete\n"); err != nil {
		attachErr, output := stopAttach()
		t.Fatalf("wait for client gate completion: %v attach_err=%v attach_output=%q", err, attachErr, output)
	}
	clients, err := client.ListAttachedClients(ctx, "gate")
	if err != nil || len(clients) != 1 || !clients[0].ReadOnly {
		attachErr, output := stopAttach()
		t.Fatalf("new client was not gated: clients=%+v err=%v attach_err=%v attach_output=%q", clients, err, attachErr, output)
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\t0\n") {
		t.Fatalf("original writable flag was not recorded: %q", raw)
	}
}

func TestRealTmuxRestoreQuiescesDelayedDispatchedAttachHook(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	if _, err := exec.LookPath("expect"); err != nil {
		t.Skip("expect unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	socket := "az-input-restore-hook-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	runner := isolatedTmuxRunner{socket: socket}
	client := NewClient(runner, slog.Default())
	const readyMarker = "AZEDARACH_RESTORE_READY_9139>"
	if _, err := runner.Run(ctx, "new-session", "-d", "-s", "gate", "PS1='"+readyMarker+" '; export PS1; exec /bin/sh -i"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = runner.Run(context.Background(), "kill-server") })
	recordPath := filepath.Join(t.TempDir(), "restore.events")
	if err := os.WriteFile(recordPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const hookID = "9139"
	const hookStarted = "az-input-restore-hook-started-9139"
	const hookCompleted = "az-input-restore-hook-completed-9139"
	const attachRelease = "az-input-restore-attach-release-9139"
	lock := sessionReadOnlyAttachHookLock(hookID, recordPath)
	gateOption := sessionReadOnlyAttachHookGateOption(hookID, recordPath)
	if _, err := runner.Run(ctx, "set-option", "-gq", gateOption, "1"); err != nil {
		t.Fatal(err)
	}
	release := "tmux wait-for -U " + lock
	record := "umask 077; tmux wait-for -L " + lock + " || exit 1; trap " + shellSingleQuote(release) + " EXIT; trap 'exit 1' HUP INT TERM; tmux wait-for -S " + hookStarted + "; sleep 0.25; if [ \"$(tmux show-options -gqv " + gateOption + ")\" = 1 ]; then __az_client=#{q:hook_client}; printf '%s\\t%s\\n' \"$__az_client\" #{client_readonly} >> " + shellSingleQuote(recordPath) + "; tmux refresh-client -t \"$__az_client\" -f read-only; printf '\\tcomplete\\n' >> " + shellSingleQuote(recordPath) + "; fi; tmux wait-for -S " + hookCompleted
	command := "run-shell " + tmuxDoubleQuote(record)
	for _, hook := range []string{"client-attached", "client-session-changed"} {
		if _, err := runner.Run(ctx, "set-hook", "-t", "gate", hook+"["+hookID+"]", command); err != nil {
			t.Fatal(err)
		}
	}
	attachDone := make(chan error, 1)
	go func() {
		output, err := runRealTmuxAttachInput(ctx, socket, readyMarker, ":", "", "", attachRelease)
		if err != nil {
			err = fmt.Errorf("attach: %w output=%q", err, output)
		}
		attachDone <- err
	}()
	if _, err := runner.Run(ctx, "wait-for", hookStarted); err != nil {
		t.Fatal(err)
	}
	if err := client.SetSessionReadOnlyAttachHooks(ctx, "gate", hookID, recordPath, false); err != nil {
		t.Fatal(err)
	}
	if err := client.LockSessionReadOnlyAttachHooks(ctx, hookID, recordPath, true); err != nil {
		t.Fatal(err)
	}
	clients, err := client.ListAttachedClients(ctx, "gate")
	if err != nil || len(clients) != 1 || clients[0].ReadOnly {
		t.Fatalf("closed generation allowed delayed hook mutation: clients=%+v err=%v", clients, err)
	}
	if err := client.LockSessionReadOnlyAttachHooks(ctx, hookID, recordPath, false); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, "wait-for", hookCompleted); err != nil {
		t.Fatal(err)
	}
	clients, err = client.ListAttachedClients(ctx, "gate")
	if err != nil || len(clients) != 1 || clients[0].ReadOnly {
		raw, _ := os.ReadFile(recordPath)
		option, _ := runner.Run(ctx, "show-options", "-gqv", gateOption)
		hooks, _ := runner.Run(ctx, "show-hooks", "-t", "gate")
		t.Fatalf("client became read-only after delayed hook restoration: clients=%+v err=%v ledger=%q option=%q hooks=%q", clients, err, raw, option, hooks)
	}
	if _, err := runner.Run(ctx, "wait-for", "-S", attachRelease); err != nil {
		t.Fatal(err)
	}
	if err := <-attachDone; err != nil {
		t.Fatal(err)
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
	const allowedComplete = "az-input-allowed-complete-9138"
	allowedInput := "printf '" + allowed + "\\n'; tmux wait-for -S " + allowedComplete
	allowedOutput, err := runRealTmuxAttachInput(ctx, socket, readyMarker, allowedInput, "", "", allowedComplete)
	if err != nil {
		t.Fatalf("send unfenced control input through attached tmux client: %v output=%q", err, allowedOutput)
	}
	output, captureErr := runner.Run(ctx, "capture-pane", "-p", "-t", target)
	paneOutput, readErr := os.ReadFile(paneOutputPath)
	if captureErr != nil || readErr != nil || !strings.Contains(output, allowed) || !strings.Contains(string(paneOutput), allowed) {
		t.Fatalf("proven attach path did not deliver unfenced control input: pane=%q capture_err=%v live_output=%q read_err=%v attach_output=%q", output, captureErr, paneOutput, readErr, allowedOutput)
	}

	if err := client.SetPaneInputEnabled(ctx, target, false); err != nil {
		t.Fatal(err)
	}

	recordPath := filepath.Join(t.TempDir(), "attach.events")
	if err := os.WriteFile(recordPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Deliberately preserve the vulnerable shape from the regression: tmux runs
	// the shell hook asynchronously from client input, and the refresh is late.
	// The pane-wide fence must be sufficient even while this hook is sleeping.
	const hookStarted = "az-input-hook-started-9138"
	const hookRelease = "az-input-hook-release-9138"
	const hookCompleted = "az-input-hook-completed-9138"
	record := "umask 077; tmux wait-for -S " + hookStarted + "; tmux wait-for " + hookRelease + "; __az_client=#{q:hook_client}; printf '%s\\t%s\\n' \"$__az_client\" #{client_readonly} >> " + shellSingleQuote(recordPath) + "; tmux refresh-client -t \"$__az_client\" -f read-only; tmux wait-for -S " + hookCompleted
	for _, hook := range []string{"client-attached", "client-session-changed"} {
		name := hook + "[9138]"
		if _, err := runner.Run(ctx, "set-hook", "-t", "gate", name, "run-shell "+tmuxDoubleQuote(record)); err != nil {
			t.Fatal(err)
		}
	}

	const blocked = "AZEDARACH_BLOCKED_ATTACH_SENTINEL_9138"
	blockedInput := "printf '" + blocked + "\\n'; tmux wait-for -S az-input-blocked-admitted-9138"
	blockedOutput, err := runRealTmuxAttachInput(ctx, socket, readyMarker, blockedInput, hookStarted, hookRelease, hookCompleted)
	if err != nil {
		t.Fatalf("send immediate input through attached tmux client: %v output=%q", err, blockedOutput)
	}
	paneOutput, err = os.ReadFile(paneOutputPath)
	if err != nil {
		t.Fatalf("read live pane output after delayed hook completion: %v", err)
	}
	if strings.Contains(string(paneOutput), blocked) {
		t.Fatalf("input reached pane output by delayed hook completion: %q", paneOutput)
	}
	output, err = runner.Run(ctx, "capture-pane", "-p", "-t", target)
	if err != nil {
		t.Fatalf("capture fenced pane after delayed hook completion: %v attach_output=%q", err, blockedOutput)
	}
	if strings.Contains(output, blocked) {
		t.Fatalf("input reached the pane after the delayed attach hook completed: %q", output)
	}
}

func runRealTmuxAttachInput(ctx context.Context, socket, readyMarker, input, beforeSend, releaseAfterSend, completion string) ([]byte, error) {
	const program = `
set timeout 5
log_user 1
spawn tmux -L $env(AZ_TEST_TMUX_SOCKET) attach-session -t gate
expect {
    -exact "$env(AZ_TEST_TMUX_READY)" {}
    timeout { send_user "timed out waiting for attached terminal readiness\n"; exit 2 }
    eof { send_user "tmux attach exited before terminal readiness\n"; exit 3 }
}
if {$env(AZ_TEST_TMUX_BEFORE_SEND) ne ""} {
    exec tmux -L $env(AZ_TEST_TMUX_SOCKET) wait-for $env(AZ_TEST_TMUX_BEFORE_SEND)
}
send -- "$env(AZ_TEST_TMUX_INPUT)\r"
if {$env(AZ_TEST_TMUX_RELEASE_AFTER_SEND) ne ""} {
    exec tmux -L $env(AZ_TEST_TMUX_SOCKET) wait-for -S $env(AZ_TEST_TMUX_RELEASE_AFTER_SEND)
}
if {$env(AZ_TEST_TMUX_COMPLETION) ne ""} {
    exec tmux -L $env(AZ_TEST_TMUX_SOCKET) wait-for $env(AZ_TEST_TMUX_COMPLETION)
}
exit 0
`
	command := exec.CommandContext(ctx, "expect", "-c", program)
	command.Env = realTmuxTestTerminalEnv()
	command.Env = append(command.Env,
		"AZ_TEST_TMUX_SOCKET="+socket,
		"AZ_TEST_TMUX_READY="+readyMarker,
		"AZ_TEST_TMUX_INPUT="+input,
		"AZ_TEST_TMUX_BEFORE_SEND="+beforeSend,
		"AZ_TEST_TMUX_RELEASE_AFTER_SEND="+releaseAfterSend,
		"AZ_TEST_TMUX_COMPLETION="+completion,
	)
	return command.CombinedOutput()
}

func waitForFileContent(ctx context.Context, path, content string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		return err
	}
	for {
		raw, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(raw), content) {
			return nil
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case event, ok := <-watcher.Events:
			if !ok {
				return errors.New("filesystem watcher closed before expected content")
			}
			if filepath.Clean(event.Name) == filepath.Clean(path) {
				continue
			}
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return errors.New("filesystem watcher error stream closed before expected content")
			}
			return watchErr
		}
	}
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
