package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
)

const sessionRestartObservationTimeout = 8 * time.Second

type sessionRestartExecution struct {
	done chan struct{}
	item protocol.SessionRestartAllItem
}

func restartStage(name, status, message string, timeout time.Duration) protocol.SessionRestartStage {
	return protocol.SessionRestartStage{Name: name, Status: status, Message: message, TimeoutMS: timeout.Milliseconds()}
}

func (d *Daemon) restartManagedAgentPane(ctx context.Context, target sessionRestartAllTarget, body protocol.SessionRestartAllRequestBody, item protocol.SessionRestartAllItem) protocol.SessionRestartAllItem {
	store := d.sessionRuntimeStateStoreIfConfigured(target.ProjectID)
	if store == nil {
		item.Outcome, item.Error = "partial_failure", "managed identity store unavailable"
		return item
	}
	identities, err := store.ListManagedAgentIdentities(ctx, target.ProjectID, target.SessionID)
	if err != nil {
		item.Outcome, item.Error = "partial_failure", err.Error()
		return item
	}
	if len(identities) == 0 {
		item.Outcome = "no_agent"
		if strings.EqualFold(strings.TrimSpace(target.Activity), "no-agent") || strings.EqualFold(strings.TrimSpace(target.Activity), "shell-only") {
			item.Outcome = "shell_only"
		}
		item.Reason, item.Skipped = "no_managed_agent_identity", true
		item.Stages = append(item.Stages, restartStage("preflight", "refused", item.Reason, 0))
		return item
	}
	old := identities[0]
	for _, candidate := range identities {
		if candidate.LogicalPaneID == "agent" {
			old = candidate
			break
		}
	}
	item.OldIdentity = restartProtocolIdentity(old)
	item.OperationID = target.ProjectID + "/" + target.SessionID + "/" + old.LogicalPaneID + "/" + old.AgentIncarnation

	d.sessionRestartMu.Lock()
	if d.sessionRestartPending == nil {
		d.sessionRestartPending = make(map[string]*sessionRestartExecution)
	}
	if pending := d.sessionRestartPending[item.OperationID]; pending != nil {
		d.sessionRestartMu.Unlock()
		select {
		case <-ctx.Done():
			item.Outcome, item.Error = "partial_failure", ctx.Err().Error()
			return item
		case <-pending.done:
			return pending.item
		}
	}
	exec := &sessionRestartExecution{done: make(chan struct{})}
	d.sessionRestartPending[item.OperationID] = exec
	d.sessionRestartMu.Unlock()
	defer func() {
		d.sessionRestartMu.Lock()
		exec.item = item
		close(exec.done)
		delete(d.sessionRestartPending, item.OperationID)
		d.sessionRestartMu.Unlock()
	}()

	item.Stages = append(item.Stages, restartStage("preflight", "running", "refresh durable identity and compare live pane", 0))
	panes, err := d.tmux.ListPaneInfos(ctx)
	if err != nil {
		item.Outcome, item.Error = "partial_failure", err.Error()
		return item
	}
	matched := false
	for _, pane := range panes {
		if pane.SessionName == target.SessionID && sanitizeRuntimePaneID(pane.PaneID) == sanitizeRuntimePaneID(old.TmuxPaneID) && pane.PanePID == old.PanePID {
			matched = true
			break
		}
	}
	if !matched {
		item.Outcome, item.Reason, item.Skipped = "crashed", "managed_agent_identity_not_live", true
		item.Stages[len(item.Stages)-1].Status = "refused"
		return item
	}
	item.Stages[len(item.Stages)-1].Status = "complete"
	if strings.EqualFold(target.Activity, "busy") && !body.ForceBusy {
		item.Outcome, item.Reason, item.Skipped = "busy", "busy_requires_force", true
		item.Stages = append(item.Stages, restartStage("busy_gate", "refused", item.Reason, 0))
		return item
	}
	item.Stages = append(item.Stages, restartStage("busy_gate", "complete", target.Activity, 0))

	incarnation, err := newRestartIncarnation()
	if err != nil {
		item.Outcome, item.Error = "partial_failure", err.Error()
		return item
	}
	artifact, err := d.prepareSessionLaunchArtifact(sessionLaunchSpec{Mode: sessionLaunchResume, ProjectID: target.ProjectID, IssueID: target.IssueID, SessionID: target.SessionID, Yolo: body.Yolo, ImagePaths: body.ImagePaths, Prompt: sessionRestartContinuePrompt, LogicalPaneID: old.LogicalPaneID, AgentIncarnation: incarnation})
	if err != nil {
		item.Outcome, item.Error = "partial_failure", err.Error()
		return item
	}
	item.Stages = append(item.Stages, restartStage("prepare", "complete", "canonical launch artifact prepared", 0))
	paneTarget := strings.TrimSpace(old.TmuxPaneID)
	if !strings.HasPrefix(paneTarget, "%") {
		paneTarget = "%" + paneTarget
	}
	worktree := d.cfg.RepoDir
	if worktreeStore := d.worktreeRuntimeStateStoreIfConfigured(target.ProjectID); worktreeStore != nil && strings.TrimSpace(target.IssueID) != "" {
		if projected, found, projectErr := worktreeStore.GetWorktreeStateByIssueID(ctx, target.ProjectID, target.IssueID); projectErr == nil && found && strings.TrimSpace(projected.Path) != "" {
			worktree = projected.Path
		}
	}
	if err := d.tmux.RespawnPane(ctx, paneTarget, worktree, artifact.Command); err != nil {
		artifact.remove()
		item.Outcome, item.Error = "partial_failure", err.Error()
		return item
	}
	item.Stages = append(item.Stages, restartStage("replace", "complete", "old pane process terminated and replacement launched", 0))

	deadline := time.NewTimer(sessionRestartObservationTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			item.Outcome, item.Error = "partial_failure", ctx.Err().Error()
			return item
		case <-deadline.C:
			item.Outcome, item.Error = "partial_failure", "replacement hook incarnation was not observed before timeout"
			item.Stages = append(item.Stages, restartStage("observe", "timeout", item.Error, sessionRestartObservationTimeout))
			return item
		case <-ticker.C:
			current, found, loadErr := store.GetManagedAgentIdentity(ctx, target.ProjectID, target.SessionID, old.LogicalPaneID)
			if loadErr != nil {
				item.Outcome, item.Error = "partial_failure", loadErr.Error()
				return item
			}
			liveReplacement := false
			if found {
				if livePanes, liveErr := d.tmux.ListPaneInfos(ctx); liveErr == nil {
					for _, pane := range livePanes {
						if pane.SessionName == target.SessionID && sanitizeRuntimePaneID(pane.PaneID) == sanitizeRuntimePaneID(current.TmuxPaneID) && pane.PanePID == current.PanePID {
							liveReplacement = true
							break
						}
					}
				}
			}
			if found && liveReplacement && current.AgentIncarnation == incarnation && current.PanePID != old.PanePID && (current.TmuxPaneID != old.TmuxPaneID || current.PanePID != old.PanePID) {
				item.NewIdentity = restartProtocolIdentity(current)
				item.Restarted = true
				item.Outcome = restartSuccessOutcome(target.Activity)
				item.Stages = append(item.Stages, restartStage("observe", "complete", "distinct pane process and hook incarnation acknowledged", sessionRestartObservationTimeout), restartStage("publish", "complete", "restart result published", 0))
				return item
			}
		}
	}
}

func restartProtocolIdentity(i daemonstate.ManagedAgentIdentity) *protocol.ManagedAgentIdentity {
	return &protocol.ManagedAgentIdentity{LogicalPaneID: i.LogicalPaneID, TmuxPaneID: i.TmuxPaneID, PanePID: i.PanePID, AgentIncarnation: i.AgentIncarnation}
}
func restartSuccessOutcome(activity string) string {
	switch strings.ToLower(strings.TrimSpace(activity)) {
	case "idle":
		return "idle"
	case "waiting", "waiting_human", "waiting_ai":
		return "waiting"
	case "busy":
		return "busy_forced"
	default:
		return "unknown"
	}
}
func newRestartIncarnation() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate restart incarnation: %w", err)
	}
	return "restart-" + hex.EncodeToString(b[:]), nil
}
