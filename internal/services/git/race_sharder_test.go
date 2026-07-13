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
	"testing"
	"time"
)

type raceShardRecord struct {
	Action string `json:"Action"`
	Output string `json:"Output"`
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
    (
	  trap 'printf term-ignored >"$FAKE_GO_CHILD_TERM"' TERM INT HUP
      while :; do sleep 1; done
    ) &
    child=$!
    printf '%s\n' "$child" >"$FAKE_GO_CHILD_PID"
	trap 'printf '\''{"Action":"output","Output":"late-term"}\n'\''; wait "$child"' TERM INT HUP
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
	return repo, script
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
	if _, err := exec.LookPath("timeout"); err != nil {
		if _, err = exec.LookPath("gtimeout"); err != nil {
			t.Skip("GNU timeout unavailable")
		}
	}
	repo, script := prepareRaceSharder(t)
	termMarker := filepath.Join(repo, "child-terminated")
	pidFile := filepath.Join(repo, "child-pid")
	stdout, stderr, err := runRaceSharder(t, repo, script,
		"AZEDARACH_DAEMON_RACE_TIMEOUT=2s",
		"AZEDARACH_DAEMON_RACE_KILL_AFTER=1s",
		"AZEDARACH_DAEMON_RACE_SHARDS=1",
		"FAKE_GO_SCENARIO=timeout",
		"FAKE_GO_CHILD_TERM="+termMarker,
		"FAKE_GO_CHILD_PID="+pidFile,
	)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || (exitErr.ExitCode() != 124 && exitErr.ExitCode() != 137) {
		t.Fatalf("aggregate timeout err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
	records := decodeRaceShardOutput(t, stdout)
	if len(records) != 2 || records[1].Output != "late-term" {
		t.Fatalf("timeout records = %+v, want initial and late TERM diagnostics; output=%q", records, stdout)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, statErr := os.Stat(termMarker); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout did not terminate shard descendant; stderr=%q", stderr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid := strings.TrimSpace(string(pidBytes))
	if killErr := exec.Command("kill", "-0", pid).Run(); killErr == nil {
		t.Fatalf("shard descendant %s still running after aggregate timeout", pid)
	}
}

func TestDaemonRaceSharderInteractiveCancelKillsSupervisedProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		if _, err = exec.LookPath("gtimeout"); err != nil {
			t.Skip("GNU timeout unavailable")
		}
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep unavailable")
	}
	repo, script := prepareRaceSharder(t)
	termMarker := filepath.Join(repo, "cancel-child-term")
	pidFile := filepath.Join(repo, "cancel-child-pid")
	stdout := new(strings.Builder)
	stderr := new(strings.Builder)
	cmd := exec.Command(script)
	cmd.Dir = repo
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Join(repo, "fake-bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AZEDARACH_DAEMON_RACE_TIMEOUT=30s",
		"AZEDARACH_DAEMON_RACE_KILL_AFTER=30s",
		"AZEDARACH_DAEMON_RACE_CANCEL_GRACE=1",
		"AZEDARACH_DAEMON_RACE_SUPERVISOR_START_DELAY=2",
		"AZEDARACH_DAEMON_RACE_SHARDS=1",
		"FAKE_GO_SCENARIO=timeout",
		"FAKE_GO_CHILD_TERM="+termMarker,
		"FAKE_GO_CHILD_PID="+pidFile,
	)
	timeoutPID := ""
	if err := cmd.Start(); err != nil {
		t.Fatalf("start race sharder: %v", err)
	}
	t.Cleanup(func() {
		if timeoutPID != "" {
			_ = exec.Command("kill", "-KILL", "-"+timeoutPID).Run()
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("race shard did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	timeoutPIDBytes, err := exec.Command("pgrep", "-P", strconv.Itoa(cmd.Process.Pid)).Output()
	if err != nil {
		t.Fatalf("find timeout supervisor child: %v", err)
	}
	timeoutPID = strings.Fields(string(timeoutPIDBytes))[0]
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt race sharder: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("repeat interrupt during cancellation grace: %v", err)
	}
	waitResult := make(chan error, 1)
	go func() { waitResult <- cmd.Wait() }()
	select {
	case err := <-waitResult:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 130 {
			t.Fatalf("interactive cancellation err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interactive cancellation did not finish within bounded grace")
	}
	records := decodeRaceShardOutput(t, []byte(stdout.String()))
	if len(records) != 2 || records[1].Output != "late-term" {
		t.Fatalf("cancel records = %+v, want initial and late TERM diagnostics", records)
	}
	childPIDBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	if killErr := exec.Command("kill", "-0", strings.TrimSpace(string(childPIDBytes))).Run(); killErr == nil {
		t.Fatalf("TERM-ignoring shard descendant still alive after interactive cancellation")
	}
	if killErr := exec.Command("kill", "-0", "-"+timeoutPID).Run(); killErr == nil {
		t.Fatalf("timeout process group %s still alive after interactive cancellation", timeoutPID)
	}
}
