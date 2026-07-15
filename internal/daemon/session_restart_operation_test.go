package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type exactRestartRunner struct {
	store            *daemonstate.RuntimeStateStore
	project, session string
	pid              int
	respawns         int
}

func (r *exactRestartRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "list-panes":
		return r.session + "\t%12\t" + fmt.Sprint(r.pid), nil
	case "respawn-pane":
		r.respawns++
		r.pid++
		command := args[len(args)-1]
		for _, matches := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(command, -1) {
			if len(matches) == 2 {
				if body, err := os.ReadFile(matches[1]); err == nil {
					inc := regexp.MustCompile(`AZEDARACH_AGENT_INCARNATION='([^']+)'`).FindStringSubmatch(string(body))
					if len(inc) == 2 {
						_ = r.store.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{ProjectID: r.project, SessionID: r.session, LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: r.pid, AgentIncarnation: inc[1], ObservedAt: time.Now().Add(time.Second)})
						break
					}
				}
			}
		}
	}
	return "", nil
}

func TestRestartManagedAgentPaneRequiresForceAndAcknowledgesReplacement(t *testing.T) {
	ctx := context.Background()
	project, session := "project", "az-1"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	old := daemonstate.ManagedAgentIdentity{ProjectID: project, SessionID: session, LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 100, AgentIncarnation: "old", ObservedAt: time.Now()}
	if err := store.UpsertManagedAgentIdentity(ctx, old); err != nil {
		t.Fatal(err)
	}
	runner := &exactRestartRunner{store: store, project: project, session: session, pid: 100}
	d := &Daemon{cfg: Config{RepoDir: t.TempDir(), CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()}, tmux: tmux.NewClient(runner, slog.Default()), runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{project: store}}
	target := sessionRestartAllTarget{ProjectID: project, SessionID: session, IssueID: "one", Activity: "busy", TmuxReady: true, ActiveIntent: true}
	refused := d.restartManagedAgentPane(ctx, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{})
	if refused.Outcome != "busy" || !refused.Skipped || runner.respawns != 0 {
		t.Fatalf("refused=%+v respawns=%d", refused, runner.respawns)
	}
	restarted := d.restartManagedAgentPane(ctx, target, protocol.SessionRestartAllRequestBody{ForceBusy: true}, protocol.SessionRestartAllItem{})
	if !restarted.Restarted || restarted.Outcome != "busy_forced" || restarted.OldIdentity.PanePID == restarted.NewIdentity.PanePID || runner.respawns != 1 {
		t.Fatalf("restarted=%+v respawns=%d", restarted, runner.respawns)
	}
	if got := strings.Join([]string{restarted.Stages[0].Name, restarted.Stages[len(restarted.Stages)-1].Name}, ","); got != "preflight,publish" {
		t.Fatalf("stage bounds=%s", got)
	}
}
