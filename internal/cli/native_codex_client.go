package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"golang.org/x/term"
)

// NativeCodexClientOptions configures the Azedarach-owned Codex app-server
// client. This client, rather than the stock TUI, owns the human composer.
type NativeCodexClientOptions struct {
	Prompt        string
	Resume        bool
	Yolo          bool
	RecoverIntent string
	RecoverAction string
	RecoverThread string
}

type codexRPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type codexRPCClient struct {
	command       *exec.Cmd
	stdin         io.WriteCloser
	mu            sync.Mutex
	nextID        atomic.Int64
	waitMu        sync.Mutex
	waits         map[string]chan codexRPCMessage
	events        chan codexRPCMessage
	requests      chan codexRPCMessage
	droppedEvents atomic.Uint64
	disconnected  atomic.Bool
	done          chan struct{}
	termMu        sync.RWMutex
	terminalErr   error
}

func startCodexRPC(ctx context.Context) (*codexRPCClient, error) {
	command := exec.CommandContext(ctx, "codex", "app-server", "proxy")
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	c := &codexRPCClient{command: command, stdin: stdin, waits: map[string]chan codexRPCMessage{}, events: make(chan codexRPCMessage, 128), requests: make(chan codexRPCMessage, 32), done: make(chan struct{})}
	go c.read(stdout)
	return c, nil
}

func (c *codexRPCClient) read(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var message codexRPCMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
		if len(message.ID) != 0 && message.Method != "" {
			select {
			case c.requests <- message:
			default:
				_ = c.sendRPCError(message.ID, -32000, "unsupported request backpressure")
			}
			continue
		}
		if len(message.ID) != 0 && message.Method == "" {
			c.waitMu.Lock()
			wait := c.waits[string(message.ID)]
			delete(c.waits, string(message.ID))
			c.waitMu.Unlock()
			if wait != nil {
				wait <- message
			}
			continue
		}
		select {
		case c.events <- message:
		default:
			c.droppedEvents.Add(1)
			if message.Method == "turn/completed" {
				select {
				case <-c.events:
				default:
				}
				c.events <- message
			}
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.terminalDisconnect(err)
}

func (c *codexRPCClient) terminalDisconnect(err error) {
	if err == nil {
		err = io.EOF
	}
	c.disconnected.Store(true)
	c.termMu.Lock()
	if c.terminalErr == nil {
		c.terminalErr = err
		close(c.done)
	}
	c.termMu.Unlock()
}

func (c *codexRPCClient) sendRPCError(id json.RawMessage, code int, message string) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": map[string]any{"code": code, "message": message}})
}

func (c *codexRPCClient) sendRPCResult(id json.RawMessage, result any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func (c *codexRPCClient) send(value any) error {
	if c.disconnected.Load() {
		c.termMu.RLock()
		defer c.termMu.RUnlock()
		return c.terminalErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return json.NewEncoder(c.stdin).Encode(value)
}

func (c *codexRPCClient) call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	idBytes, _ := json.Marshal(id)
	wait := make(chan codexRPCMessage, 1)
	c.waitMu.Lock()
	c.waits[string(idBytes)] = wait
	c.waitMu.Unlock()
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		c.waitMu.Lock()
		delete(c.waits, string(idBytes))
		c.waitMu.Unlock()
		return err
	}
	select {
	case <-ctx.Done():
		c.waitMu.Lock()
		delete(c.waits, string(idBytes))
		c.waitMu.Unlock()
		return ctx.Err()
	case message := <-wait:
		if message.Error != nil {
			return fmt.Errorf("codex %s: %s", method, message.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(message.Result, result)
		}
		return nil
	case <-c.done:
		c.waitMu.Lock()
		delete(c.waits, string(idBytes))
		c.waitMu.Unlock()
		c.termMu.RLock()
		defer c.termMu.RUnlock()
		return c.terminalErr
	}
}

type nativeCodexRegistration struct {
	ProtocolVersion  int    `json:"protocol_version"`
	ProjectID        string `json:"project_id"`
	SessionID        string `json:"session_id"`
	LogicalPaneID    string `json:"logical_pane_id"`
	TmuxPaneID       string `json:"tmux_pane_id"`
	PanePID          int    `json:"pane_pid"`
	AgentIncarnation string `json:"agent_incarnation"`
	Tool             string `json:"tool"`
}

type nativeCodexEnvelope struct {
	ProjectID        string `json:"project_id"`
	IntentKey        string `json:"intent_key"`
	AgentIncarnation string `json:"agent_incarnation"`
	LeaseToken       string `json:"lease_token"`
	Payload          string `json:"payload"`
}

type nativeCodexResponse struct {
	ProjectID            string `json:"project_id"`
	IntentKey            string `json:"intent_key"`
	AgentIncarnation     string `json:"agent_incarnation"`
	LeaseToken           string `json:"lease_token"`
	Outcome              string `json:"outcome"`
	AcknowledgementToken string `json:"acknowledgement_token,omitempty"`
}

type nativeCodexDelivery struct {
	envelope nativeCodexEnvelope
	reply    chan nativeCodexResponse
}

type nativeCodexHumanRequest struct {
	id     json.RawMessage
	method string
	params json.RawMessage
}

type nativeCodexPermissionResponse struct {
	Permissions json.RawMessage `json:"permissions"`
	Scope       string          `json:"scope,omitempty"`
}

func codexRequestNeedsHumanDecision(method string) bool {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval":
		return true
	default:
		return false
	}
}

// NativeCodexClient runs the production app-server client used by managed
// Codex sessions. Human bytes and daemon deliveries meet in one event loop, so
// the daemon can never submit through a second terminal or resume process.
func NativeCodexClient(ctx context.Context, deps *Dependencies, opts NativeCodexClientOptions) error {
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if deps == nil || deps.DaemonClient == nil {
		return errors.New("native Codex client requires daemon client")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if opts.RecoverIntent != "" {
		return RecoverNativeCodexIntent(cwd, strings.TrimSpace(os.Getenv("AZEDARACH_SESSION_ID")), opts.RecoverIntent, opts.RecoverAction, opts.RecoverThread)
	}
	projectID := strings.TrimSpace(deps.ProjectID)
	if projectID == "" {
		projectID = protocol.DefaultProjectID
	}
	sessionID := strings.TrimSpace(os.Getenv("AZEDARACH_SESSION_ID"))
	issueID := strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID"))
	pane := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	panePID := nativeCodexPanePID()
	statePath := filepath.Join(cwd, ".azedarach", "native-agent-input", sessionID+".json")
	var clientState nativeCodexState
	var stateErr error
	if opts.Resume {
		clientState, stateErr = strictNativeCodexState(statePath)
	} else {
		clientState, stateErr = loadNativeCodexState(statePath, false)
	}
	if stateErr != nil {
		return stateErr
	}
	incarnation := clientState.Incarnation
	if sessionID == "" || pane == "" || panePID <= 0 {
		return errors.New("native Codex client requires managed session and tmux pane identity")
	}
	if _, err := deps.DaemonClient.RuntimeSignalIngest(ctx, protocol.RuntimeSignalIngestCommandBody{
		Source: protocol.RuntimeSignalSourceAgentHook, Kind: protocol.RuntimeSignalKindAgentActivityChanged,
		ProjectID: projectID, IssueID: issueID, SessionID: sessionID, Worktree: cwd, TmuxPane: pane,
		LogicalPaneID: "agent", PanePID: panePID, AgentIncarnation: incarnation, Agent: "codex",
		Hook: "session_start", Event: "session_start", Activity: "idle",
	}); err != nil {
		return fmt.Errorf("bind native Codex incarnation: %w", err)
	}

	rpc, err := startCodexRPC(childCtx)
	if err != nil {
		return fmt.Errorf("start Codex app-server proxy: %w", err)
	}
	defer func() { _ = rpc.command.Process.Kill(); _ = rpc.command.Wait() }()
	var initialize map[string]any
	if err := rpc.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "azedarach", "title": "Azedarach", "version": "1"}}, &initialize); err != nil {
		return err
	}
	if err := rpc.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return err
	}
	threadID, err := nativeCodexThread(ctx, rpc, cwd, opts.Resume, &clientState, statePath)
	if err != nil {
		return err
	}

	deliveries := make(chan nativeCodexDelivery)
	authorityDone := nativeCodexAuthorityLoop(childCtx, os.Getenv("AZEDARACH_AGENT_INPUT_SOCKET"), nativeCodexRegistration{
		ProtocolVersion: 1, ProjectID: projectID, SessionID: sessionID, LogicalPaneID: "agent", TmuxPaneID: pane,
		PanePID: panePID, AgentIncarnation: incarnation, Tool: "codex",
	}, deliveries)
	var terminalState *term.State
	if term.IsTerminal(int(os.Stdin.Fd())) {
		terminalState, err = term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return fmt.Errorf("enter native composer raw mode: %w", err)
		}
		defer term.Restore(int(os.Stdin.Fd()), terminalState) //nolint:errcheck
	}
	input := make(chan byte, 256)
	stdinDone := readNativeCodexInputContext(childCtx, os.Stdin, input)
	defer func() {
		cancel()
		select {
		case <-authorityDone:
		case <-time.After(time.Second):
		}
		select {
		case <-stdinDone:
		case <-time.After(time.Second):
		}
	}()
	composer := make([]byte, 0, 256)
	active := false
	pendingRequests := make([]nativeCodexHumanRequest, 0, 4)
	signalActivity := func(event string) {
		_, _ = deps.DaemonClient.RuntimeSignalIngest(context.WithoutCancel(ctx), protocol.RuntimeSignalIngestCommandBody{
			Source: protocol.RuntimeSignalSourceAgentHook, Kind: protocol.RuntimeSignalKindAgentActivityChanged,
			ProjectID: projectID, IssueID: issueID, SessionID: sessionID, Worktree: cwd, TmuxPane: pane,
			LogicalPaneID: "agent", PanePID: panePID, AgentIncarnation: incarnation, Agent: "codex", Hook: event, Event: event,
		})
	}
	if strings.TrimSpace(opts.Prompt) != "" {
		if err := nativeCodexStartTurn(ctx, rpc, threadID, cwd, opts.Prompt, opts.Yolo, "human:"+incarnation+":"+strconv.FormatInt(time.Now().UnixNano(), 10)); err != nil {
			return err
		}
		active = true
		signalActivity("user_prompt_submit")
	} else {
		signalActivity("idle_prompt")
	}
	fmt.Fprint(os.Stdout, "\n› ")
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-rpc.done:
			rpc.termMu.RLock()
			err := rpc.terminalErr
			rpc.termMu.RUnlock()
			return fmt.Errorf("Codex app-server disconnected: %w", err)
		case event := <-rpc.events:
			if dropped := rpc.droppedEvents.Swap(0); dropped > 0 {
				fmt.Fprintf(os.Stderr, "\nCodex event backpressure: dropped %d non-terminal events\n", dropped)
			}
			if event.Method == "turn/completed" {
				var completed struct {
					Turn struct {
						Status string `json:"status"`
						Error  any    `json:"error"`
					} `json:"turn"`
				}
				_ = json.Unmarshal(event.Params, &completed)
				active = false
				signalActivity("idle_prompt")
				if completed.Turn.Status != "completed" && completed.Turn.Status != "" {
					fmt.Fprintf(os.Stderr, "\nCodex turn %s: status=%s error=%v\n", completed.Turn.Status, completed.Turn.Status, completed.Turn.Error)
				}
				fmt.Fprint(os.Stdout, "\n› "+string(composer))
				continue
			}
			if event.Method == "item/agentMessage/delta" {
				var delta struct {
					Delta string `json:"delta"`
				}
				if json.Unmarshal(event.Params, &delta) == nil {
					fmt.Fprint(os.Stdout, delta.Delta)
				}
			}
			if strings.Contains(event.Method, "error") || strings.Contains(event.Method, "commandExecution") || strings.Contains(event.Method, "fileChange") || strings.Contains(event.Method, "reasoning") {
				fmt.Fprintf(os.Stdout, "\n[%s] %s\n", event.Method, boundedCodexEvent(event.Params, 4096))
			}
		case request := <-rpc.requests:
			if codexRequestNeedsHumanDecision(request.Method) {
				if len(pendingRequests) >= cap(pendingRequests) {
					_ = rpc.sendRPCError(request.ID, -32000, "too many concurrent approval requests")
					continue
				}
				pendingRequests = append(pendingRequests, nativeCodexHumanRequest{id: request.ID, method: request.Method, params: request.Params})
				fmt.Fprintf(os.Stdout, "\nCodex request %s: approve? [y/N] ", request.Method)
			} else {
				_ = rpc.sendRPCError(request.ID, -32601, "unsupported Codex server request method: "+request.Method)
			}
		case b := <-input:
			if len(pendingRequests) > 0 {
				if b == 'y' || b == 'Y' {
					req := pendingRequests[0]
					result := any(map[string]any{"decision": "accept"})
					if req.method == "item/permissions/requestApproval" {
						var params struct {
							Permissions json.RawMessage `json:"permissions"`
						}
						_ = json.Unmarshal(req.params, &params)
						result = nativeCodexPermissionResponse{Permissions: params.Permissions}
					}
					_ = rpc.sendRPCResult(req.id, result)
				} else if b == 'n' || b == 'N' || b == 3 {
					req := pendingRequests[0]
					if req.method == "item/permissions/requestApproval" {
						_ = rpc.sendRPCError(req.id, -32800, "permission approval declined")
					} else {
						_ = rpc.sendRPCResult(req.id, map[string]any{"decision": "decline"})
					}
				} else {
					continue
				}
				pendingRequests = pendingRequests[1:]
				fmt.Fprint(os.Stdout, "\n› ")
				continue
			}
			switch b {
			case 3:
				return nil
			case 8, 127:
				if len(composer) > 0 {
					composer = composer[:len(composer)-1]
					fmt.Fprint(os.Stdout, "\b \b")
				}
			case '\r', '\n':
				if len(composer) == 0 || active {
					continue
				}
				text := string(composer)
				composer = composer[:0]
				if err := nativeCodexStartTurn(ctx, rpc, threadID, cwd, text, opts.Yolo, "human:"+incarnation+":"+strconv.FormatInt(time.Now().UnixNano(), 10)); err != nil {
					fmt.Fprintf(os.Stderr, "\n%v\n› ", err)
					continue
				}
				active = true
				signalActivity("user_prompt_submit")
				fmt.Fprintln(os.Stdout)
			default:
				if b >= 32 {
					composer = append(composer, b)
					fmt.Fprintf(os.Stdout, "%c", b)
				}
			}
		case delivery := <-deliveries:
			recoveringPending := clientState.Pending[delivery.envelope.IntentKey+"\x00"+incarnation] != ""
			response, newlyActive := acceptNativeCodexDelivery(delivery.envelope, composer, active, statePath, &clientState, func(messageID string) error {
				if recoveringPending {
					return errors.New("pending Codex turn requires server reconciliation; refusing resubmission")
				}
				return nativeCodexStartTurn(ctx, rpc, threadID, cwd, delivery.envelope.Payload, opts.Yolo, messageID)
			})
			active = active || newlyActive
			if response.Outcome == "not_ready" && clientState.Pending[delivery.envelope.IntentKey+"\x00"+incarnation] != "" {
				fmt.Fprintf(os.Stderr, "\nCodex submission ambiguous for intent %q, thread %q; automatic retry disabled. Inspect the thread, then run: az ai native-codex-client --recover-intent %s --recover-thread %s --recover-action delivered|discard\n", delivery.envelope.IntentKey, threadID, shellQuote(delivery.envelope.IntentKey), shellQuote(threadID))
			}
			if newlyActive {
				signalActivity("user_prompt_submit")
			}
			delivery.reply <- response
		}
	}
}

func strictNativeCodexState(path string) (nativeCodexState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nativeCodexState{}, fmt.Errorf("read resume state: %w", err)
	}
	var state nativeCodexState
	if json.Unmarshal(raw, &state) != nil || state.Incarnation == "" || state.ThreadID == "" {
		return nativeCodexState{}, errors.New("resume state is missing or corrupt")
	}
	return state, nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

// RecoverNativeCodexIntent resolves an ambiguous pending submission without
// contacting Codex. The caller must inspect the exact bound thread first.
func RecoverNativeCodexIntent(cwd, sessionID, intentKey, action, threadID string) error {
	if sessionID == "" || intentKey == "" {
		return errors.New("recovery requires session and intent")
	}
	path := filepath.Join(cwd, ".azedarach", "native-agent-input", sessionID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read recovery state: %w", err)
	}
	var state nativeCodexState
	if err := json.Unmarshal(raw, &state); err != nil || state.Incarnation == "" || state.ThreadID == "" {
		return errors.New("recovery state is missing or corrupt")
	}
	if state.Pending == nil {
		state.Pending = map[string]string{}
	}
	if state.Accepted == nil {
		state.Accepted = map[string]string{}
	}
	if state.Resolved == nil {
		state.Resolved = map[string]string{}
	}
	key := intentKey + "\x00" + state.Incarnation
	if state.Pending[key] == "" {
		return errors.New("intent is not pending for this incarnation")
	}
	if threadID == "" || threadID != state.ThreadID {
		return errors.New("recovery requires exact persisted Codex thread")
	}
	switch action {
	case "discard":
		delete(state.Pending, key)
		ack, err := randomNativeCodexToken()
		if err != nil {
			return err
		}
		state.Resolved[key] = "discarded:" + ack
	case "delivered":
		ack, err := randomNativeCodexToken()
		if err != nil {
			return err
		}
		state.Accepted[key] = ack
		state.Resolved[key] = ack
		delete(state.Pending, key)
	default:
		return errors.New("recovery action must be delivered or discard")
	}
	return saveNativeCodexState(path, state)
}

func boundedCodexEvent(raw json.RawMessage, limit int) string {
	if len(raw) > limit {
		raw = raw[:limit]
	}
	return string(raw)
}

func acceptNativeCodexDelivery(envelope nativeCodexEnvelope, composer []byte, active bool, statePath string, state *nativeCodexState, submit func(string) error, saver ...func(string, nativeCodexState) error) (nativeCodexResponse, bool) {
	save := saveNativeCodexState
	if len(saver) > 0 && saver[0] != nil {
		save = saver[0]
	}
	response := nativeCodexResponse{ProjectID: envelope.ProjectID, IntentKey: envelope.IntentKey,
		AgentIncarnation: envelope.AgentIncarnation, LeaseToken: envelope.LeaseToken}
	key := envelope.IntentKey + "\x00" + envelope.AgentIncarnation
	if state.Accepted == nil {
		state.Accepted = map[string]string{}
	}
	if state.Pending == nil {
		state.Pending = map[string]string{}
	}
	if state.Resolved == nil {
		state.Resolved = map[string]string{}
	}
	if resolution := state.Resolved[key]; resolution != "" {
		if strings.HasPrefix(resolution, "discarded:") {
			resolution = strings.TrimPrefix(resolution, "discarded:")
		}
		response.Outcome, response.AcknowledgementToken = "accepted", resolution
		return response, false
	}
	if ack := state.Accepted[key]; ack != "" {
		response.Outcome, response.AcknowledgementToken = "accepted", ack
		return response, false
	}
	if len(composer) != 0 {
		response.Outcome = "composer_nonempty"
		return response, false
	}
	if active {
		response.Outcome = "not_ready"
		return response, false
	}
	messageID := state.Pending[key]
	if messageID != "" {
		response.Outcome = "not_ready"
		return response, false
	}
	if messageID == "" {
		messageID = envelope.IntentKey + ":" + envelope.AgentIncarnation
		state.Pending[key] = messageID
		if err := save(statePath, *state); err != nil {
			delete(state.Pending, key)
			response.Outcome = "not_ready"
			return response, false
		}
	}
	if submit(messageID) != nil {
		response.Outcome = "not_ready"
		return response, false
	}
	ack, err := randomNativeCodexToken()
	if err != nil {
		response.Outcome = "not_ready"
		return response, false
	}
	state.Accepted[key] = ack
	delete(state.Pending, key)
	if err := save(statePath, *state); err != nil {
		delete(state.Accepted, key)
		state.Pending[key] = messageID
		response.Outcome = "not_ready"
		return response, false
	}
	response.Outcome, response.AcknowledgementToken = "accepted", ack
	return response, true
}

func nativeCodexThread(ctx context.Context, rpc *codexRPCClient, cwd string, resume bool, state *nativeCodexState, statePath string) (string, error) {
	if strings.TrimSpace(state.ThreadID) != "" {
		var resumed struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := rpc.call(ctx, "thread/resume", map[string]any{"threadId": state.ThreadID}, &resumed); err != nil {
			return "", fmt.Errorf("resume exact Codex thread %s: %w", state.ThreadID, err)
		}
		if resumed.Thread.ID == "" || resumed.Thread.ID != state.ThreadID {
			return "", errors.New("Codex resumed thread omitted id")
		}
		return resumed.Thread.ID, nil
	}
	if resume {
		return "", errors.New("missing persisted Codex thread identity")
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := rpc.call(ctx, "thread/start", map[string]any{"cwd": cwd}, &started); err != nil {
		return "", err
	}
	if started.Thread.ID == "" {
		return "", errors.New("Codex thread/start omitted id")
	}
	state.ThreadID = started.Thread.ID
	if err := saveNativeCodexState(statePath, *state); err != nil {
		return "", fmt.Errorf("persist Codex thread identity: %w", err)
	}
	return started.Thread.ID, nil
}

func nativeCodexStartTurn(ctx context.Context, rpc *codexRPCClient, threadID, cwd, text string, yolo bool, clientUserMessageID string) error {
	params := map[string]any{"threadId": threadID, "cwd": cwd, "clientUserMessageId": clientUserMessageID, "input": []map[string]string{{"type": "text", "text": text}}}
	if yolo {
		params["approvalPolicy"] = "never"
		params["sandboxPolicy"] = map[string]any{"type": "dangerFullAccess"}
	}
	var result map[string]any
	return rpc.call(ctx, "turn/start", params, &result)
}

func nativeCodexAuthorityLoop(ctx context.Context, socket string, registration nativeCodexRegistration, deliveries chan<- nativeCodexDelivery) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			conn, err := (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimSpace(socket))
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(250 * time.Millisecond):
				}
				continue
			}
			connDone := make(chan struct{})
			closerDone := make(chan struct{})
			var closeOnce sync.Once
			cleanup := func() { closeOnce.Do(func() { _ = conn.Close(); close(connDone); <-closerDone }) }
			go func() {
				select {
				case <-ctx.Done():
					_ = conn.Close()
				case <-connDone:
				}
				close(closerDone)
			}()
			if json.NewEncoder(conn).Encode(registration) != nil {
				cleanup()
				continue
			}
			reader := bufio.NewReader(conn)
		connection:
			for ctx.Err() == nil {
				var envelope nativeCodexEnvelope
				line, readErr := reader.ReadBytes('\n')
				if readErr != nil || json.Unmarshal(line, &envelope) != nil {
					break
				}
				reply := make(chan nativeCodexResponse, 1)
				select {
				case deliveries <- nativeCodexDelivery{envelope: envelope, reply: reply}:
				case <-ctx.Done():
					break connection
				}
				select {
				case response := <-reply:
					if json.NewEncoder(conn).Encode(response) != nil {
						break connection
					}
				case <-ctx.Done():
					break connection
				}
			}
			cleanup()
		}
	}()
	return done
}

func readNativeCodexInput(input *os.File, output chan<- byte) {
	buffer := make([]byte, 1)
	for {
		if _, err := input.Read(buffer); err != nil {
			return
		}
		output <- buffer[0]
	}
}

func readNativeCodexInputContext(ctx context.Context, input *os.File, output chan<- byte) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fd, err := syscall.Dup(int(input.Fd()))
		if err != nil {
			return
		}
		owned := os.NewFile(uintptr(fd), "azedarach-native-stdin")
		if err := syscall.SetNonblock(fd, true); err != nil {
			return
		}
		defer owned.Close()
		buffer := make([]byte, 1)
		for {
			n, err := syscall.Read(fd, buffer)
			if n == 0 && err == nil {
				return
			}
			if n > 0 {
				select {
				case output <- buffer[0]:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
					if errors.Is(err, syscall.EINTR) {
						continue
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(5 * time.Millisecond):
					}
					continue
				}
				return
			}
		}
	}()
	return done
}

func randomNativeCodexToken() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

type nativeCodexState struct {
	Incarnation string            `json:"incarnation"`
	ThreadID    string            `json:"thread_id"`
	Accepted    map[string]string `json:"accepted"`
	Pending     map[string]string `json:"pending"`
	Resolved    map[string]string `json:"resolved"`
}

func loadNativeCodexState(path string, resume bool) (nativeCodexState, error) {
	if resume {
		data, err := os.ReadFile(path)
		if err == nil {
			var state nativeCodexState
			if json.Unmarshal(data, &state) == nil && strings.TrimSpace(state.Incarnation) != "" {
				if state.Accepted == nil {
					state.Accepted = map[string]string{}
				}
				if state.Pending == nil {
					state.Pending = map[string]string{}
				}
				if state.Resolved == nil {
					state.Resolved = map[string]string{}
				}
				return state, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nativeCodexState{}, err
		}
	}
	incarnation, err := randomNativeCodexToken()
	if err != nil {
		return nativeCodexState{}, err
	}
	state := nativeCodexState{Incarnation: incarnation, Accepted: map[string]string{}, Pending: map[string]string{}, Resolved: map[string]string{}}
	return state, saveNativeCodexState(path, state)
}

func saveNativeCodexState(path string, state nativeCodexState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func nativeCodexPanePID() int {
	pid, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("AZEDARACH_PANE_PID")))
	if pid > 0 {
		return pid
	}
	return os.Getppid()
}
