package git

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

type raceShardRecord struct {
	Action string `json:"Action"`
	Output string `json:"Output"`
}

type raceShardFIFO struct {
	path string
	file *os.File
}

type raceSharderProcessResult struct {
	err error
}

func prepareRaceSharder(t *testing.T) (string, string) {
	t.Helper()

	repo := t.TempDir()
	runGit(t, repo, "init")
	scriptDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	source := filepath.Join("..", "..", "..", "scripts", "test-daemon-race-sharded.sh")
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read race sharder: %v", err)
	}
	script := filepath.Join(scriptDir, "test-daemon-race-sharded.sh")
	if err := os.WriteFile(script, content, 0o755); err != nil {
		t.Fatalf("write race sharder: %v", err)
	}

	fakeBin := filepath.Join(repo, "fake-bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	fakeGo := `#!/bin/sh
case " $* " in
  *" -list "*)
	case " $* " in *" -race "*) ;; *) exit 8 ;; esac
    if [ "${FAKE_GO_SCENARIO:-success}" = discovery_fail ]; then
      echo TestPartial
      exit 7
    fi
    printf '%s\n' TestA TestB TestC TestD TestE
    exit 0
    ;;
esac
pattern=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = -run ]; then pattern="$2"; break; fi
  shift
done
printf '{"Action":"output","Output":"%s"}\n' "$pattern"
case "${FAKE_GO_SCENARIO:-success}" in
  shard_fail)
    case "$pattern" in *TestB*) exit 9 ;; esac
    ;;
  partial)
    printf '{"Action":"output"'
    exit 9
    ;;
  timeout)
	"${0%/*}/fake-go-timeout-child" &
    child=$!
	signal_timeout() {
	  printf '{"Action":"output","Output":"late-term"}\n'
	  read -r _ <"$FAKE_GO_CHILD_TERM_FIFO"
	  printf 'term\n' >"$FAKE_GO_TERM_FIFO"
	  wait "$child"
	}
	trap signal_timeout TERM INT HUP
    wait "$child"
    ;;
	concurrency)
	  while ! mkdir "$FAKE_GO_CONCURRENCY_DIR/lock" 2>/dev/null; do sleep 0.01; done
	  active=0
	  [ ! -s "$FAKE_GO_CONCURRENCY_DIR/active" ] || active="$(cat "$FAKE_GO_CONCURRENCY_DIR/active")"
	  active=$((active + 1))
	  printf '%s\n' "$active" >"$FAKE_GO_CONCURRENCY_DIR/active"
	  maximum=0
	  [ ! -s "$FAKE_GO_CONCURRENCY_DIR/max" ] || maximum="$(cat "$FAKE_GO_CONCURRENCY_DIR/max")"
	  if [ "$active" -gt "$maximum" ]; then printf '%s\n' "$active" >"$FAKE_GO_CONCURRENCY_DIR/max"; fi
	  rmdir "$FAKE_GO_CONCURRENCY_DIR/lock"
	  sleep 0.3
	  while ! mkdir "$FAKE_GO_CONCURRENCY_DIR/lock" 2>/dev/null; do sleep 0.01; done
	  active="$(cat "$FAKE_GO_CONCURRENCY_DIR/active")"
	  printf '%s\n' $((active - 1)) >"$FAKE_GO_CONCURRENCY_DIR/active"
	  rmdir "$FAKE_GO_CONCURRENCY_DIR/lock"
	  ;;
esac
`
	if err := os.WriteFile(filepath.Join(fakeBin, "go"), []byte(fakeGo), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}
	fakeTimeoutChild := `#!/bin/sh
signal_term() {
  printf term-ignored >"$FAKE_GO_CHILD_TERM"
  printf 'child-term\n' >"$FAKE_GO_CHILD_TERM_FIFO"
}
trap signal_term TERM INT HUP
if [ -n "${FAKE_GO_READY_FIFO:-}" ]; then
  printf '%s\n' "$$" >"$FAKE_GO_READY_FIFO"
fi
while ! read -r _ <"$FAKE_GO_BLOCK_FIFO"; do :; done
`
	if err := os.WriteFile(filepath.Join(fakeBin, "fake-go-timeout-child"), []byte(fakeTimeoutChild), 0o755); err != nil {
		t.Fatalf("write fake timeout child: %v", err)
	}
	return repo, script
}

func newRaceShardFIFO(t *testing.T, name string) *raceShardFIFO {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create %s FIFO: %v", name, err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s FIFO: %v", name, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return &raceShardFIFO{path: path, file: file}
}

func (f *raceShardFIFO) readLine(t *testing.T, processDone <-chan struct{}, result *raceSharderProcessResult) string {
	t.Helper()
	type readResult struct {
		line string
		err  error
	}
	read := make(chan readResult, 1)
	go func() {
		line, err := bufio.NewReader(f.file).ReadString('\n')
		read <- readResult{line: strings.TrimSpace(line), err: err}
	}()
	select {
	case got := <-read:
		if got.err != nil {
			t.Fatalf("read subprocess barrier %s: %v", f.path, got.err)
		}
		return got.line
	case <-processDone:
		t.Fatalf("race sharder exited before subprocess barrier %s: %v", f.path, result.err)
		return ""
	}
}

func (f *raceShardFIFO) writeLine(t *testing.T, value string) {
	t.Helper()
	if _, err := f.file.WriteString(value + "\n"); err != nil {
		t.Fatalf("release subprocess barrier %s: %v", f.path, err)
	}
}

func installRaceSharderTimeoutWrapper(t *testing.T, repo, timeoutPath string) {
	t.Helper()
	wrapper := `#!/bin/sh
printf '%s\n' "$$" >"$AZEDARACH_DAEMON_RACE_TEST_TIMEOUT_READY_FIFO"
exec "$AZEDARACH_DAEMON_RACE_TEST_TIMEOUT_PATH" "$@"
`
	if err := os.WriteFile(filepath.Join(repo, "fake-bin", "timeout"), []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write timeout wrapper: %v", err)
	}
}

func startRaceSharder(t *testing.T, repo, script string, extraEnv ...string) (*exec.Cmd, *strings.Builder, *strings.Builder, <-chan struct{}, *raceSharderProcessResult) {
	t.Helper()
	stdout := new(strings.Builder)
	stderr := new(strings.Builder)
	cmd := exec.Command(script)
	cmd.Dir = repo
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), append([]string{
		"PATH=" + filepath.Join(repo, "fake-bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
	}, extraEnv...)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start race sharder: %v", err)
	}
	result := new(raceSharderProcessResult)
	done := make(chan struct{})
	go func() {
		result.err = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	return cmd, stdout, stderr, done, result
}

func runRaceSharder(t *testing.T, repo, script string, extraEnv ...string) ([]byte, []byte, error) {
	t.Helper()
	cmd := exec.Command(script)
	cmd.Dir = repo
	stdout := new(strings.Builder)
	stderr := new(strings.Builder)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), append([]string{
		"PATH=" + filepath.Join(repo, "fake-bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
	}, extraEnv...)...)
	err := cmd.Run()
	return []byte(stdout.String()), []byte(stderr.String()), err
}

func decodeRaceShardOutput(t *testing.T, output []byte) []raceShardRecord {
	t.Helper()
	var records []raceShardRecord
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Bytes()
		if !json.Valid(line) {
			t.Fatalf("race sharder emitted invalid JSONL line %q", line)
		}
		var record raceShardRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode race sharder output: %v", err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan race sharder output: %v", err)
	}
	return records
}

func TestDaemonRaceSharderAssignsEveryTestExactlyOnceAndSkipsEmptyShards(t *testing.T) {
	repo, script := prepareRaceSharder(t)
	stdout, stderr, err := runRaceSharder(t, repo, script,
		"AZEDARACH_DAEMON_RACE_INNER=1",
		"AZEDARACH_DAEMON_RACE_SHARDS=8",
	)
	if err != nil {
		t.Fatalf("race sharder failed: %v\nstderr:\n%s", err, stderr)
	}
	records := decodeRaceShardOutput(t, stdout)
	if len(records) != 5 {
		t.Fatalf("race sharder records = %d, want 5 non-empty shards; output=%s", len(records), stdout)
	}
	for _, name := range []string{"TestA", "TestB", "TestC", "TestD", "TestE"} {
		count := 0
		for _, record := range records {
			count += strings.Count(record.Output, name)
		}
		if count != 1 {
			t.Fatalf("assignment count for %s = %d, want 1; records=%+v", name, count, records)
		}
	}
}

func TestDaemonRaceSharderCapsConcurrentProcesses(t *testing.T) {
	for _, tc := range []struct {
		name        string
		shards      string
		parallelism string
		want        string
	}{
		{name: "sequential by default", want: "1"},
		{name: "explicit bounded parallelism", shards: "4", parallelism: "2", want: "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, script := prepareRaceSharder(t)
			concurrencyDir := filepath.Join(repo, "concurrency")
			if err := os.MkdirAll(concurrencyDir, 0o755); err != nil {
				t.Fatalf("mkdir concurrency state: %v", err)
			}
			env := []string{
				"AZEDARACH_DAEMON_RACE_INNER=1",
				"FAKE_GO_SCENARIO=concurrency",
				"FAKE_GO_CONCURRENCY_DIR=" + concurrencyDir,
			}
			if tc.shards != "" {
				env = append(env, "AZEDARACH_DAEMON_RACE_SHARDS="+tc.shards)
			}
			if tc.parallelism != "" {
				env = append(env, "AZEDARACH_DAEMON_RACE_PARALLELISM="+tc.parallelism)
			}
			stdout, stderr, err := runRaceSharder(t, repo, script, env...)
			if err != nil {
				t.Fatalf("race sharder failed: %v\nstdout=%q\nstderr=%q", err, stdout, stderr)
			}
			maximum, err := os.ReadFile(filepath.Join(concurrencyDir, "max"))
			if err != nil {
				t.Fatalf("read maximum concurrency: %v", err)
			}
			if got := strings.TrimSpace(string(maximum)); got != tc.want {
				t.Fatalf("maximum shard concurrency = %s, want %s", got, tc.want)
			}
			if tc.name == "sequential by default" && !strings.Contains(string(stderr), "across 4 shards") {
				t.Fatalf("default shard count not reported as 4: %q", stderr)
			}
		})
	}
}

func TestDaemonRaceSharderPropagatesDiscoveryAndShardFailures(t *testing.T) {
	t.Run("discovery", func(t *testing.T) {
		repo, script := prepareRaceSharder(t)
		stdout, stderr, err := runRaceSharder(t, repo, script,
			"AZEDARACH_DAEMON_RACE_INNER=1",
			"FAKE_GO_SCENARIO=discovery_fail",
		)
		if err == nil || !strings.Contains(string(stderr), "test discovery failed") {
			t.Fatalf("discovery result err=%v stdout=%q stderr=%q", err, stdout, stderr)
		}
		if len(stdout) != 0 {
			t.Fatalf("discovery failure stdout = %q, want empty", stdout)
		}
	})

	t.Run("shard", func(t *testing.T) {
		repo, script := prepareRaceSharder(t)
		stdout, stderr, err := runRaceSharder(t, repo, script,
			"AZEDARACH_DAEMON_RACE_INNER=1",
			"AZEDARACH_DAEMON_RACE_SHARDS=2",
			"FAKE_GO_SCENARIO=shard_fail",
		)
		if err == nil {
			t.Fatalf("shard failure err=nil stdout=%q stderr=%q", stdout, stderr)
		}
		if records := decodeRaceShardOutput(t, stdout); len(records) != 2 {
			t.Fatalf("retained shard records = %d, want 2", len(records))
		}
	})
}

func TestDaemonRaceSharderDropsIncompleteJSONRecord(t *testing.T) {
	repo, script := prepareRaceSharder(t)
	stdout, stderr, err := runRaceSharder(t, repo, script,
		"AZEDARACH_DAEMON_RACE_INNER=1",
		"AZEDARACH_DAEMON_RACE_SHARDS=1",
		"FAKE_GO_SCENARIO=partial",
	)
	if err == nil {
		t.Fatalf("partial shard err=nil stdout=%q stderr=%q", stdout, stderr)
	}
	if records := decodeRaceShardOutput(t, stdout); len(records) != 1 {
		t.Fatalf("complete records = %d, want 1; output=%q", len(records), stdout)
	}
}

func TestDaemonRaceSharderAggregateTimeoutKillsDescendantsAndKeepsJSONL(t *testing.T) {
	timeoutPath, err := exec.LookPath("timeout")
	if err != nil {
		timeoutPath, err = exec.LookPath("gtimeout")
		if err != nil {
			t.Skip("GNU timeout unavailable")
		}
	}
	repo, script := prepareRaceSharder(t)
	installRaceSharderTimeoutWrapper(t, repo, timeoutPath)
	termMarker := filepath.Join(repo, "child-terminated")
	timeoutReady := newRaceShardFIFO(t, "timeout-ready")
	childReady := newRaceShardFIFO(t, "child-ready")
	termReady := newRaceShardFIFO(t, "term-ready")
	childTerm := newRaceShardFIFO(t, "child-term")
	childBlock := newRaceShardFIFO(t, "child-block")
	_, stdout, stderr, done, result := startRaceSharder(t, repo, script,
		"AZEDARACH_DAEMON_RACE_TIMEOUT=1h",
		"AZEDARACH_DAEMON_RACE_KILL_AFTER=1h",
		"AZEDARACH_DAEMON_RACE_SHARDS=1",
		"AZEDARACH_DAEMON_RACE_TEST_TIMEOUT_PATH="+timeoutPath,
		"AZEDARACH_DAEMON_RACE_TEST_TIMEOUT_READY_FIFO="+timeoutReady.path,
		"FAKE_GO_SCENARIO=timeout",
		"FAKE_GO_CHILD_TERM="+termMarker,
		"FAKE_GO_READY_FIFO="+childReady.path,
		"FAKE_GO_TERM_FIFO="+termReady.path,
		"FAKE_GO_CHILD_TERM_FIFO="+childTerm.path,
		"FAKE_GO_BLOCK_FIFO="+childBlock.path,
	)
	timeoutPID, err := strconv.Atoi(timeoutReady.readLine(t, done, result))
	if err != nil {
		t.Fatalf("parse timeout supervisor PID: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("kill", "-KILL", "-"+strconv.Itoa(timeoutPID)).Run() })
	childPID := childReady.readLine(t, done, result)
	if err := syscall.Kill(timeoutPID, syscall.SIGALRM); err != nil {
		t.Fatalf("trigger aggregate timeout: %v", err)
	}
	if got := termReady.readLine(t, done, result); got != "term" {
		t.Fatalf("descendant TERM acknowledgement = %q, want term", got)
	}
	if err := syscall.Kill(timeoutPID, syscall.SIGALRM); err != nil {
		t.Fatalf("trigger aggregate timeout escalation: %v", err)
	}
	<-done
	var exitErr *exec.ExitError
	if !errors.As(result.err, &exitErr) || (exitErr.ExitCode() != 124 && exitErr.ExitCode() != 137) {
		t.Fatalf("aggregate timeout err=%v stdout=%q stderr=%q", result.err, stdout, stderr)
	}
	records := decodeRaceShardOutput(t, []byte(stdout.String()))
	if len(records) != 2 || records[1].Output != "late-term" {
		t.Fatalf("timeout records = %+v, want initial and late TERM diagnostics; output=%q", records, stdout.String())
	}
	if _, statErr := os.Stat(termMarker); statErr != nil {
		t.Fatalf("timeout did not signal shard descendant: %v; stderr=%q", statErr, stderr.String())
	}
	if killErr := exec.Command("kill", "-0", childPID).Run(); killErr == nil {
		t.Fatalf("shard descendant %s still running after aggregate timeout", childPID)
	}
}

func TestDaemonRaceSharderInteractiveCancelKillsSupervisedProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		if _, err = exec.LookPath("gtimeout"); err != nil {
			t.Skip("GNU timeout unavailable")
		}
	}
	repo, script := prepareRaceSharder(t)
	timeoutPath, err := exec.LookPath("timeout")
	if err != nil {
		timeoutPath, err = exec.LookPath("gtimeout")
	}
	installRaceSharderTimeoutWrapper(t, repo, timeoutPath)
	termMarker := filepath.Join(repo, "cancel-child-term")
	timeoutReady := newRaceShardFIFO(t, "timeout-ready")
	childReady := newRaceShardFIFO(t, "child-ready")
	supervisorRelease := newRaceShardFIFO(t, "supervisor-release")
	cancelReady := newRaceShardFIFO(t, "cancel-ready")
	cancelRelease := newRaceShardFIFO(t, "cancel-release")
	termReady := newRaceShardFIFO(t, "term-ready")
	childTerm := newRaceShardFIFO(t, "child-term")
	childBlock := newRaceShardFIFO(t, "child-block")
	cmd, stdout, stderr, done, result := startRaceSharder(t, repo, script,
		"AZEDARACH_DAEMON_RACE_TIMEOUT=1h",
		"AZEDARACH_DAEMON_RACE_KILL_AFTER=1h",
		"AZEDARACH_DAEMON_RACE_CANCEL_GRACE=3600",
		"AZEDARACH_DAEMON_RACE_SHARDS=1",
		"AZEDARACH_DAEMON_RACE_TEST_TIMEOUT_PATH="+timeoutPath,
		"AZEDARACH_DAEMON_RACE_TEST_TIMEOUT_READY_FIFO="+timeoutReady.path,
		"AZEDARACH_DAEMON_RACE_TEST_SUPERVISOR_RELEASE_FIFO="+supervisorRelease.path,
		"AZEDARACH_DAEMON_RACE_TEST_CANCEL_READY_FIFO="+cancelReady.path,
		"AZEDARACH_DAEMON_RACE_TEST_CANCEL_RELEASE_FIFO="+cancelRelease.path,
		"AZEDARACH_DAEMON_RACE_TEST_TERM_ACK_FIFO="+termReady.path,
		"FAKE_GO_SCENARIO=timeout",
		"FAKE_GO_CHILD_TERM="+termMarker,
		"FAKE_GO_READY_FIFO="+childReady.path,
		"FAKE_GO_TERM_FIFO="+termReady.path,
		"FAKE_GO_CHILD_TERM_FIFO="+childTerm.path,
		"FAKE_GO_BLOCK_FIFO="+childBlock.path,
	)
	timeoutPID := timeoutReady.readLine(t, done, result)
	childPID := childReady.readLine(t, done, result)
	t.Cleanup(func() {
		if timeoutPID != "" {
			_ = exec.Command("kill", "-KILL", "-"+timeoutPID).Run()
		}
	})
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt race sharder: %v", err)
	}
	supervisorRelease.writeLine(t, "release")
	if got := cancelReady.readLine(t, done, result); got != "ready" {
		t.Fatalf("cancellation acknowledgement = %q, want ready", got)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("repeat interrupt during cancellation: %v", err)
	}
	cancelRelease.writeLine(t, "release")
	<-done
	var exitErr *exec.ExitError
	if !errors.As(result.err, &exitErr) || exitErr.ExitCode() != 130 {
		t.Fatalf("interactive cancellation err=%v stdout=%q stderr=%q", result.err, stdout.String(), stderr.String())
	}
	records := decodeRaceShardOutput(t, []byte(stdout.String()))
	if len(records) != 2 || records[1].Output != "late-term" {
		t.Fatalf("cancel records = %+v, want initial and late TERM diagnostics", records)
	}
	if killErr := exec.Command("kill", "-0", childPID).Run(); killErr == nil {
		t.Fatalf("TERM-ignoring shard descendant still alive after interactive cancellation")
	}
	if killErr := exec.Command("kill", "-0", "-"+timeoutPID).Run(); killErr == nil {
		t.Fatalf("timeout process group %s still alive after interactive cancellation", timeoutPID)
	}
}
