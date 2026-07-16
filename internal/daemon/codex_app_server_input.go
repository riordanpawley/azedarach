package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

const (
	codexInputGateStateVersion       = 2
	codexInputGateRestoreLimit       = 5 * time.Second
	codexSubmissionFenceSafetyMargin = time.Second
)

var errCodexPaneIdentityChanged = errors.New("managed Codex pane identity changed at submission boundary")
var errCodexSessionFenceLost = errors.New("managed Codex session fence changed or expired")
var errCodexGateRestoreIncomplete = errors.New("managed Codex input gate restoration incomplete")

type codexInputTmux interface {
	ListAttachedClients(context.Context, string) ([]tmux.AttachedClientInfo, error)
	SetClientReadOnly(context.Context, string, bool) error
	SetPaneInputEnabled(context.Context, string, bool) error
	PaneInputEnabled(context.Context, string) (bool, error)
	SetSessionReadOnlyAttachHooks(context.Context, string, string, string, bool) error
	ListPaneInfos(context.Context) ([]tmux.PaneInfo, error)
}

type codexAppServerRPC interface {
	Call(context.Context, string, any, any) error
	Notify(string, any) error
	Close() error
}

type codexAppServerInputAuthority struct {
	tmux           codexInputTmux
	gateDir        string
	logger         *slog.Logger
	runtimeConfig  func(string) daemonProjectRuntimeConfig
	startRPC       func(context.Context, string) (codexAppServerRPC, error)
	issueClients   func(string) *issues.Client
	recoveryOwner  string
	now            func() time.Time
	leaseDuration  time.Duration
	safetyMargin   time.Duration
	removeGateFile func(string) error
	recoveryWait   func(context.Context, time.Time) bool
	mu             sync.Mutex
	sessionMux     map[string]*sync.Mutex
	recoveryMux    sync.Mutex
	recoveryQueued map[string]struct{}
}

func newCodexAppServerInputAuthority(adapter codexInputTmux, daemonSocketPath string, logger *slog.Logger, runtimeConfig func(string) daemonProjectRuntimeConfig) *codexAppServerInputAuthority {
	gateDir := filepath.Join(filepath.Dir(strings.TrimSpace(daemonSocketPath)), "agent-input-gates")
	return &codexAppServerInputAuthority{tmux: adapter, gateDir: gateDir, logger: logger, runtimeConfig: runtimeConfig, startRPC: startCodexAppServerRPC, recoveryOwner: "daemon-agent-input-recovery", now: func() time.Time { return time.Now().UTC() }, leaseDuration: agentInputSessionLeaseDuration, safetyMargin: codexSubmissionFenceSafetyMargin, removeGateFile: os.Remove, recoveryWait: waitForCodexGateRecovery, sessionMux: map[string]*sync.Mutex{}, recoveryQueued: map[string]struct{}{}}
}

func waitForCodexGateRecovery(ctx context.Context, at time.Time) bool {
	delay := time.Until(at)
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (a *codexAppServerInputAuthority) scheduleGateRecovery(ctx context.Context, statePath string, at time.Time) {
	if at.IsZero() || a.recoveryWait == nil {
		return
	}
	a.recoveryMux.Lock()
	if _, exists := a.recoveryQueued[statePath]; exists {
		a.recoveryMux.Unlock()
		return
	}
	a.recoveryQueued[statePath] = struct{}{}
	a.recoveryMux.Unlock()
	go func() {
		if !a.recoveryWait(ctx, at) {
			a.recoveryMux.Lock()
			delete(a.recoveryQueued, statePath)
			a.recoveryMux.Unlock()
			return
		}
		a.recoveryMux.Lock()
		delete(a.recoveryQueued, statePath)
		a.recoveryMux.Unlock()
		if err := a.RecoverStaleGates(ctx); err != nil && a.logger != nil {
			a.logger.Warn("retry stale Codex input gate recovery", "gate", filepath.Base(statePath), "error", err)
		}
	}()
}

// RecoverStaleGates performs one bounded startup pass over gates whose daemon
// exited before flag restoration completed. Failures remain on disk for the
// next startup and are surfaced to the daemon log without blocking service.
func (a *codexAppServerInputAuthority) RecoverStaleGates(ctx context.Context) error {
	if a == nil || a.tmux == nil || a.issueClients == nil {
		return nil
	}
	entries, err := filepath.Glob(filepath.Join(a.gateDir, "gate-*.json"))
	if err != nil {
		return err
	}
	var errs []error
	for _, statePath := range entries {
		raw, readErr := os.ReadFile(statePath)
		if readErr != nil {
			errs = append(errs, readErr)
			continue
		}
		var state codexInputGateState
		if json.Unmarshal(raw, &state) != nil || state.Version != codexInputGateStateVersion || state.ProjectID == "" || state.SessionID == "" || state.AgentIncarnation == "" || state.FenceToken == "" || state.PaneID == "" || state.HookID == "" {
			errs = append(errs, fmt.Errorf("invalid stale Codex input gate %s", filepath.Base(statePath)))
			continue
		}
		client := a.issueClients(state.ProjectID)
		if client == nil {
			errs = append(errs, fmt.Errorf("recover %s: issue store unavailable", filepath.Base(statePath)))
			continue
		}
		recoveryLease, acquired, claimErr := client.ClaimAgentInputDeliverySessionLeaseRecovery(ctx, state.ProjectID, state.SessionID, state.AgentIncarnation, state.FenceToken, a.recoveryOwner, a.now(), a.leaseDuration)
		if claimErr != nil {
			errs = append(errs, fmt.Errorf("recover %s: claim durable recovery fence: %w", filepath.Base(statePath), claimErr))
			continue
		}
		if !acquired {
			a.scheduleGateRecovery(ctx, statePath, recoveryLease.LeaseExpires)
			continue
		}
		state.LeaseOwner = recoveryLease.LeaseOwner
		state.FenceToken = recoveryLease.LeaseToken
		gate := &codexInputGate{tmux: a.tmux, state: state, statePath: statePath, removeFile: a.removeGateFile}
		gate.renewRestoreFence = func(restoreCtx context.Context) (bool, error) {
			_, renewed, err := client.RenewAgentInputDeliverySessionLease(restoreCtx, state.ProjectID, state.SessionID, state.AgentIncarnation, recoveryLease.LeaseToken, a.now(), a.leaseDuration)
			return renewed, err
		}
		if persistErr := gate.persist(); persistErr != nil {
			releaseErr := client.ReleaseAgentInputDeliverySessionLease(context.WithoutCancel(ctx), state.ProjectID, state.SessionID, state.AgentIncarnation, recoveryLease.LeaseToken)
			errs = append(errs, fmt.Errorf("recover %s: persist recovery fence: %w", filepath.Base(statePath), errors.Join(persistErr, releaseErr)))
			continue
		}
		recoverCtx, cancel := context.WithTimeout(ctx, codexInputGateRestoreLimit)
		restoreErr := gate.Restore(recoverCtx)
		cancel()
		if restoreErr != nil {
			errs = append(errs, fmt.Errorf("recover %s: %w", filepath.Base(statePath), restoreErr))
		} else if releaseErr := client.ReleaseAgentInputDeliverySessionLease(context.WithoutCancel(ctx), state.ProjectID, state.SessionID, state.AgentIncarnation, recoveryLease.LeaseToken); releaseErr != nil {
			errs = append(errs, fmt.Errorf("recover %s: release durable recovery fence: %w", filepath.Base(statePath), releaseErr))
		}
	}
	return errors.Join(errs...)
}

func (a *codexAppServerInputAuthority) DeliverAgentInput(ctx context.Context, request authoritativeAgentInputRequest) (ack authoritativeAgentInputAcknowledgement, retErr error) {
	if a == nil || a.tmux == nil || a.runtimeConfig == nil || !strings.EqualFold(strings.TrimSpace(request.Delivery.Tool), "codex") {
		return ack, errAuthoritativeAgentInputUnavailable
	}
	config := a.runtimeConfig(request.Delivery.ProjectID)
	tool := strings.TrimSpace(config.CLITool)
	if !config.CodexAppServer || !strings.EqualFold(tool, "codex") {
		return ack, errAuthoritativeAgentInputUnavailable
	}
	rpc, err := a.startRPC(ctx, tool)
	if err != nil {
		return ack, fmt.Errorf("start Codex app-server proxy: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, rpc.Close()) }()
	var initialized map[string]any
	if err := rpc.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "azedarach-daemon", "title": "Azedarach daemon", "version": "1"}}, &initialized); err != nil {
		return ack, fmt.Errorf("initialize Codex app-server delivery client: %w", err)
	}
	if err := rpc.Notify("initialized", map[string]any{}); err != nil {
		return ack, fmt.Errorf("complete Codex app-server initialization: %w", err)
	}
	var resumed struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	threadID := strings.TrimSpace(request.Delivery.Target.AgentIncarnation)
	if err := rpc.Call(ctx, "thread/resume", map[string]any{"threadId": threadID, "excludeTurns": true}, &resumed); err != nil {
		return ack, fmt.Errorf("resume exact Codex thread: %w", err)
	}
	if strings.TrimSpace(resumed.Thread.ID) != threadID {
		return ack, errors.New("Codex app-server resumed a different thread")
	}

	lock := a.sessionLock(request.Delivery.ProjectID, request.Delivery.SessionID)
	lock.Lock()
	defer lock.Unlock()
	if err := a.recoverSupersededGate(ctx, request); err != nil {
		return ack, codexGateRefusal(err, false)
	}
	gate, err := a.acquireGate(ctx, request)
	if err != nil {
		return ack, codexGateRefusal(err, false)
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexInputGateRestoreLimit)
		defer cancel()
		if restoreErr := gate.Restore(restoreCtx); restoreErr != nil {
			if a.logger != nil {
				a.logger.Error("restore Codex automated-input client gate", "project_id", request.Delivery.ProjectID, "session_id", request.Delivery.SessionID, "error", restoreErr)
			}
			retErr = errors.Join(retErr, fmt.Errorf("%w: %v", errCodexGateRestoreIncomplete, restoreErr))
		}
	}()
	if err := gate.Revalidate(ctx, request); err != nil {
		return ack, codexGateRefusal(err, false)
	}
	if request.BeginSubmission == nil {
		return ack, errors.New("missing durable app-server submission boundary")
	}
	if _, err := request.BeginSubmission(ctx); err != nil {
		return ack, err
	}
	// The durable callback revalidates the projected thread incarnation. Check
	// the live pane and every attached client once more after that write and
	// immediately before turn/start. Because no RPC has been attempted yet, a
	// failure here is authoritatively safe to resolve as queued or stale.
	if err := gate.Revalidate(ctx, request); err != nil {
		return ack, codexGateRefusal(err, true)
	}
	if request.RevalidateSubmissionFence == nil {
		return ack, codexGateRefusal(errCodexPaneIdentityChanged, true)
	}
	fenceExpiry, err := request.RevalidateSubmissionFence(ctx)
	if err != nil {
		var refusal agentInputRefusalError
		if errors.As(err, &refusal) {
			return ack, refusal
		}
		return ack, fmt.Errorf("revalidate durable submission fence: %w", err)
	}
	acceptanceDeadline := fenceExpiry.Add(-a.safetyMargin)
	if !a.now().Before(acceptanceDeadline) {
		return ack, errCodexSessionFenceLost
	}
	// Durable fencing excludes other daemons. Recheck the native tmux fence
	// after that transaction so pane input, rather than the asynchronous client
	// hook, is the final human-input exclusion boundary before turn/start.
	if err := gate.Revalidate(ctx, request); err != nil {
		return ack, codexGateRefusal(err, true)
	}
	messageID := codexDeliveryMessageID(request.Delivery.IntentKey, threadID)
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	params := map[string]any{
		"threadId":            threadID,
		"clientUserMessageId": messageID,
		"input":               []map[string]string{{"type": "text", "text": request.Delivery.Payload}},
	}
	acceptCtx, cancelAccept := context.WithDeadline(ctx, acceptanceDeadline)
	defer cancelAccept()
	if err := rpc.Call(acceptCtx, "turn/start", params, &started); err != nil {
		return ack, fmt.Errorf("submit Codex automated turn: %w", err)
	}
	turnID := strings.TrimSpace(started.Turn.ID)
	if turnID == "" {
		return ack, errors.New("Codex turn/start omitted accepted turn id")
	}
	return authoritativeAgentInputAcknowledgement{ProjectID: request.Delivery.ProjectID, IntentKey: request.Delivery.IntentKey,
		AgentIncarnation: threadID, LeaseToken: request.LeaseToken, AcknowledgementToken: turnID}, nil
}

func (a *codexAppServerInputAuthority) recoverSupersededGate(ctx context.Context, request authoritativeAgentInputRequest) error {
	previousToken := strings.TrimSpace(request.PreviousSessionLeaseToken)
	if previousToken == "" {
		return nil
	}
	entries, err := filepath.Glob(filepath.Join(a.gateDir, "gate-*.json"))
	if err != nil {
		return err
	}
	type persistedGate struct {
		path  string
		state codexInputGateState
	}
	var matched []persistedGate
	for _, statePath := range entries {
		raw, readErr := os.ReadFile(statePath)
		if readErr != nil {
			return readErr
		}
		var state codexInputGateState
		if err := json.Unmarshal(raw, &state); err != nil || state.Version != codexInputGateStateVersion || state.ProjectID == "" || state.SessionID == "" || state.AgentIncarnation == "" || state.FenceToken == "" {
			return fmt.Errorf("invalid persisted Codex gate during session takeover: %s", filepath.Base(statePath))
		}
		if state.ProjectID == request.Delivery.ProjectID && state.SessionID == request.Delivery.SessionID && state.AgentIncarnation == request.PreviousAgentIncarnation && state.FenceToken == previousToken {
			matched = append(matched, persistedGate{path: statePath, state: state})
		}
	}
	if len(matched) > 1 {
		return errors.New("multiple persisted Codex gates matched one superseded session fence")
	}
	if len(matched) == 0 {
		return nil
	}
	state := matched[0].state
	state.AgentIncarnation = request.Delivery.Target.AgentIncarnation
	state.LeaseOwner = request.SessionLeaseOwner
	state.FenceToken = request.SessionLeaseToken
	gate := &codexInputGate{tmux: a.tmux, state: state, statePath: matched[0].path, renewRestoreFence: request.RenewRestoreFence, removeFile: a.removeGateFile}
	if err := gate.persist(); err != nil {
		return fmt.Errorf("persist superseded Codex gate takeover: %w", err)
	}
	restoreCtx, cancel := context.WithTimeout(ctx, codexInputGateRestoreLimit)
	defer cancel()
	if err := gate.Restore(restoreCtx); err != nil {
		return fmt.Errorf("%w: restore superseded Codex gate: %v", errCodexGateRestoreIncomplete, err)
	}
	return nil
}

func codexGateRefusal(err error, safeToRetry bool) agentInputRefusalError {
	outcome := "human_attached"
	if errors.Is(err, errCodexPaneIdentityChanged) {
		outcome = "stale_incarnation"
	}
	return agentInputRefusalError{outcome: outcome, safeToRetry: safeToRetry, cause: err}
}

func (a *codexAppServerInputAuthority) sessionLock(projectID, sessionID string) *sync.Mutex {
	key := strings.TrimSpace(projectID) + "\x00" + strings.TrimSpace(sessionID)
	a.mu.Lock()
	defer a.mu.Unlock()
	lock := a.sessionMux[key]
	if lock == nil {
		lock = &sync.Mutex{}
		a.sessionMux[key] = lock
	}
	return lock
}

func codexDeliveryMessageID(intentKey, threadID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(intentKey) + "\x00" + strings.TrimSpace(threadID)))
	return "az-" + hex.EncodeToString(sum[:16])
}

type codexInputGateState struct {
	Version          int             `json:"version"`
	ProjectID        string          `json:"project_id"`
	SessionID        string          `json:"session_id"`
	AgentIncarnation string          `json:"agent_incarnation"`
	LeaseOwner       string          `json:"lease_owner"`
	FenceToken       string          `json:"fence_token"`
	PaneID           string          `json:"pane_id"`
	PaneInputEnabled bool            `json:"pane_input_enabled"`
	HookID           string          `json:"hook_id"`
	EventsPath       string          `json:"events_path"`
	OriginalReadOnly map[string]bool `json:"original_read_only"`
}

type codexInputGate struct {
	tmux              codexInputTmux
	state             codexInputGateState
	statePath         string
	restored          bool
	renewRestoreFence func(context.Context) (bool, error)
	removeFile        func(string) error
}

func (a *codexAppServerInputAuthority) acquireGate(ctx context.Context, request authoritativeAgentInputRequest) (result *codexInputGate, retErr error) {
	if err := os.MkdirAll(a.gateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create input gate directory: %w", err)
	}
	token, err := randomGateToken()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(a.gateDir, "gate-"+token)
	eventsPath := base + ".events"
	file, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create input gate event ledger: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	hookIndex, err := strconv.ParseUint(token[:8], 16, 32)
	if err != nil {
		_ = os.Remove(eventsPath)
		return nil, err
	}
	if strings.TrimSpace(request.SessionLeaseOwner) == "" || strings.TrimSpace(request.SessionLeaseToken) == "" || request.RenewRestoreFence == nil {
		_ = os.Remove(eventsPath)
		return nil, errCodexSessionFenceLost
	}
	state := codexInputGateState{Version: codexInputGateStateVersion, ProjectID: request.Delivery.ProjectID, SessionID: request.Delivery.SessionID, AgentIncarnation: request.Delivery.Target.AgentIncarnation, LeaseOwner: request.SessionLeaseOwner, FenceToken: request.SessionLeaseToken,
		PaneID: request.Delivery.Target.TmuxPaneID, HookID: strconv.FormatUint(hookIndex, 10), EventsPath: eventsPath, OriginalReadOnly: map[string]bool{}}
	gate := &codexInputGate{tmux: a.tmux, state: state, statePath: base + ".json", renewRestoreFence: request.RenewRestoreFence, removeFile: a.removeGateFile}
	state.PaneInputEnabled, err = a.tmux.PaneInputEnabled(ctx, state.PaneID)
	if err != nil {
		_ = os.Remove(eventsPath)
		return nil, err
	}
	gate.state = state
	if err := gate.persist(); err != nil {
		_ = os.Remove(eventsPath)
		return nil, err
	}
	if state.PaneInputEnabled {
		if err := a.tmux.SetPaneInputEnabled(ctx, state.PaneID, false); err != nil {
			_ = os.Remove(eventsPath)
			return nil, err
		}
	}
	failed := true
	defer func() {
		if failed {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexInputGateRestoreLimit)
			defer cancel()
			if restoreErr := gate.Restore(restoreCtx); restoreErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("%w: %v", errCodexGateRestoreIncomplete, restoreErr))
			}
		}
	}()
	if err := a.tmux.SetSessionReadOnlyAttachHooks(ctx, state.SessionID, state.HookID, state.EventsPath, true); err != nil {
		return nil, err
	}
	clients, err := a.tmux.ListAttachedClients(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	for _, client := range clients {
		state.OriginalReadOnly[client.ClientName] = client.ReadOnly
	}
	gate.state = state
	if err := gate.persist(); err != nil {
		return nil, err
	}
	for _, client := range clients {
		if !client.ReadOnly {
			if err := a.tmux.SetClientReadOnly(ctx, client.ClientName, true); err != nil {
				return nil, err
			}
		}
	}
	if err := gate.Revalidate(ctx, request); err != nil {
		return nil, err
	}
	failed = false
	return gate, nil
}

func (g *codexInputGate) Revalidate(ctx context.Context, request authoritativeAgentInputRequest) error {
	panes, err := g.tmux.ListPaneInfos(ctx)
	if err != nil {
		return err
	}
	wantPane := strings.TrimPrefix(strings.TrimSpace(request.Delivery.Target.TmuxPaneID), "%")
	found := false
	for _, pane := range panes {
		if pane.SessionName == request.Delivery.SessionID && strings.TrimPrefix(strings.TrimSpace(pane.PaneID), "%") == wantPane && pane.PanePID == request.Delivery.Target.PanePID {
			found = true
			break
		}
	}
	if !found {
		return errCodexPaneIdentityChanged
	}
	paneInputEnabled, err := g.tmux.PaneInputEnabled(ctx, g.state.PaneID)
	if err != nil {
		return err
	}
	if paneInputEnabled {
		return errors.New("managed Codex pane input fence is not active")
	}
	clients, err := g.tmux.ListAttachedClients(ctx, g.state.SessionID)
	if err != nil {
		return err
	}
	for _, client := range clients {
		if !client.ReadOnly {
			return fmt.Errorf("tmux client %s remained writable", client.ClientName)
		}
	}
	return nil
}

func (g *codexInputGate) persist() error {
	raw, err := json.Marshal(g.state)
	if err != nil {
		return err
	}
	tmp := g.statePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, g.statePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (g *codexInputGate) Restore(ctx context.Context) error {
	if g == nil || g.restored {
		return nil
	}
	if g.renewRestoreFence == nil {
		return errCodexSessionFenceLost
	}
	renewed, err := g.renewRestoreFence(ctx)
	if err != nil {
		return fmt.Errorf("renew durable restore fence: %w", err)
	}
	if !renewed {
		return errCodexSessionFenceLost
	}
	var errs []error
	if err := g.tmux.SetPaneInputEnabled(ctx, g.state.PaneID, false); err != nil {
		// Do not remove hooks or restore writable client flags unless the native
		// pane fence is authoritative. Recovery retains the marker and lease.
		return err
	}
	if err := g.tmux.SetSessionReadOnlyAttachHooks(ctx, g.state.SessionID, g.state.HookID, g.state.EventsPath, false); err != nil {
		errs = append(errs, err)
	}
	if err := g.mergeHookEvents(); err != nil {
		errs = append(errs, err)
	}
	clients, err := g.tmux.ListAttachedClients(ctx, "")
	if err != nil {
		errs = append(errs, err)
	} else {
		for _, client := range clients {
			wasReadOnly, known := g.state.OriginalReadOnly[client.ClientName]
			if known && !wasReadOnly && client.ReadOnly {
				if err := g.tmux.SetClientReadOnly(ctx, client.ClientName, false); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	if g.state.PaneInputEnabled {
		if err := g.tmux.SetPaneInputEnabled(ctx, g.state.PaneID, true); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		removeFile := g.removeFile
		if removeFile == nil {
			removeFile = os.Remove
		}
		if err := removeFile(g.state.EventsPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove input gate event ledger: %w", err))
		}
		// The state file is the durable completion marker. Delete it last and
		// report success only after its removal is authoritative, so callers
		// retain the exact session lease while any replayable marker remains.
		if len(errs) == 0 {
			if err := removeFile(g.statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove input gate completion marker: %w", err))
			} else {
				g.restored = true
			}
		}
	}
	return errors.Join(errs...)
}

func (g *codexInputGate) mergeHookEvents() error {
	raw, err := os.ReadFile(g.state.EventsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		if _, known := g.state.OriginalReadOnly[fields[0]]; !known {
			g.state.OriginalReadOnly[fields[0]] = fields[1] == "1"
		}
	}
	return nil
}

func randomGateToken() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

type processCodexAppServerRPC struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	writeGate chan struct{}
	nextID    atomic.Int64
	waitsMu   sync.Mutex
	waits     map[string]chan codexRPCResponse
	done      chan struct{}
	termMu    sync.RWMutex
	termErr   error
	closeOne  sync.Once
}

type codexRPCResponse struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type codexRPCMethodError struct {
	Method  string
	Code    int
	Message string
}

func (e codexRPCMethodError) Error() string {
	return fmt.Sprintf("Codex %s: rpc %d: %s", e.Method, e.Code, e.Message)
}

func startCodexAppServerRPC(ctx context.Context, tool string) (codexAppServerRPC, error) {
	cmd := exec.CommandContext(ctx, tool, "app-server", "proxy")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	rpc := &processCodexAppServerRPC{cmd: cmd, stdin: stdin, writeGate: make(chan struct{}, 1), waits: map[string]chan codexRPCResponse{}, done: make(chan struct{})}
	rpc.writeGate <- struct{}{}
	go rpc.read(stdout)
	return rpc, nil
}

func (c *processCodexAppServerRPC) Call(ctx context.Context, method string, params, result any) error {
	id := strconv.FormatInt(c.nextID.Add(1), 10)
	wait := make(chan codexRPCResponse, 1)
	c.waitsMu.Lock()
	c.waits[id] = wait
	c.waitsMu.Unlock()
	if err := c.sendContext(ctx, map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": method, "params": params}); err != nil {
		c.waitsMu.Lock()
		delete(c.waits, id)
		c.waitsMu.Unlock()
		return err
	}
	select {
	case response := <-wait:
		if response.Error != nil {
			return codexRPCMethodError{Method: method, Code: response.Error.Code, Message: response.Error.Message}
		}
		if result == nil || len(response.Result) == 0 {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	case <-ctx.Done():
		c.waitsMu.Lock()
		delete(c.waits, id)
		c.waitsMu.Unlock()
		return ctx.Err()
	case <-c.done:
		c.termMu.RLock()
		defer c.termMu.RUnlock()
		return c.termErr
	}
}

func (c *processCodexAppServerRPC) send(value any) error {
	return c.sendContext(context.Background(), value)
}

func (c *processCodexAppServerRPC) sendContext(ctx context.Context, value any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.writeGate:
	}
	defer func() { c.writeGate <- struct{}{} }()
	if deadline, ok := ctx.Deadline(); ok {
		writer, ok := c.stdin.(interface{ SetWriteDeadline(time.Time) error })
		if !ok {
			return errors.New("Codex app-server stdin cannot enforce submission deadline")
		}
		if err := writer.SetWriteDeadline(deadline); err != nil {
			return fmt.Errorf("set Codex app-server write deadline: %w", err)
		}
		defer writer.SetWriteDeadline(time.Time{})
	}
	return json.NewEncoder(c.stdin).Encode(value)
}

func (c *processCodexAppServerRPC) Notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *processCodexAppServerRPC) read(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var response codexRPCResponse
		if json.Unmarshal(scanner.Bytes(), &response) != nil {
			continue
		}
		if len(response.ID) == 0 {
			continue
		}
		id := string(response.ID)
		if response.Method != "" {
			_ = c.send(map[string]any{"jsonrpc": "2.0", "id": response.ID, "error": map[string]any{"code": -32601, "message": "Azedarach automated delivery does not answer interactive requests"}})
			continue
		}
		c.waitsMu.Lock()
		wait := c.waits[id]
		delete(c.waits, id)
		c.waitsMu.Unlock()
		if wait != nil {
			wait <- response
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.termMu.Lock()
	c.termErr = err
	close(c.done)
	c.termMu.Unlock()
}

func (c *processCodexAppServerRPC) Close() error {
	var closeErr error
	c.closeOne.Do(func() {
		_ = c.stdin.Close()
		if c.cmd.Process != nil {
			if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				closeErr = err
			}
		}
		if err := c.cmd.Wait(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || !strings.Contains(strings.ToLower(exitErr.Error()), "signal: killed") {
				closeErr = errors.Join(closeErr, err)
			}
		}
	})
	return closeErr
}
