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
