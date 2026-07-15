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
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"golang.org/x/term"
)

// NativeCodexClientOptions configures the Azedarach-owned Codex app-server
// client. This client, rather than the stock TUI, owns the human composer.
type NativeCodexClientOptions struct {
	Prompt string
	Resume bool
	Yolo   bool
}

type codexRPCMessage struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type codexRPCClient struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex
	nextID  atomic.Int64
	waitMu  sync.Mutex
	waits   map[int64]chan codexRPCMessage
	events  chan codexRPCMessage
	done    chan error
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
	c := &codexRPCClient{command: command, stdin: stdin, waits: map[int64]chan codexRPCMessage{}, events: make(chan codexRPCMessage, 128), done: make(chan error, 1)}
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
		if message.ID != 0 {
			c.waitMu.Lock()
			wait := c.waits[message.ID]
			delete(c.waits, message.ID)
			c.waitMu.Unlock()
			if wait != nil {
				wait <- message
			}
			continue
		}
		if message.Method == "turn/completed" {
			select {
			case c.events <- message:
			default:
				<-c.events
				c.events <- message
			}
		} else {
			select {
			case c.events <- message:
			default:
			}
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.done <- err
}

func (c *codexRPCClient) send(value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return json.NewEncoder(c.stdin).Encode(value)
}

func (c *codexRPCClient) call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	wait := make(chan codexRPCMessage, 1)
	c.waitMu.Lock()
	c.waits[id] = wait
	c.waitMu.Unlock()
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		c.waitMu.Lock()
		delete(c.waits, id)
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
	case err := <-c.done:
		return err
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

// NativeCodexClient runs the production app-server client used by managed
// Codex sessions. Human bytes and daemon deliveries meet in one event loop, so
// the daemon can never submit through a second terminal or resume process.
func NativeCodexClient(ctx context.Context, deps *Dependencies, opts NativeCodexClientOptions) error {
	if deps == nil || deps.DaemonClient == nil {
		return errors.New("native Codex client requires daemon client")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
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
	clientState, err := loadNativeCodexState(statePath, opts.Resume)
	if err != nil {
		return err
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

	rpc, err := startCodexRPC(ctx)
	if err != nil {
		return fmt.Errorf("start Codex app-server proxy: %w", err)
	}
	defer rpc.command.Process.Kill() //nolint:errcheck
	var initialize map[string]any
	if err := rpc.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "azedarach", "title": "Azedarach", "version": "1"}}, &initialize); err != nil {
		return err
	}
	if err := rpc.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return err
	}
	threadID, err := nativeCodexThread(ctx, rpc, cwd, opts.Resume)
	if err != nil {
		return err
	}

	deliveries := make(chan nativeCodexDelivery)
	go nativeCodexAuthorityLoop(ctx, os.Getenv("AZEDARACH_AGENT_INPUT_SOCKET"), nativeCodexRegistration{
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
	go readNativeCodexInput(os.Stdin, input)
	composer := make([]byte, 0, 256)
	active := false
	signalActivity := func(event string) {
		_, _ = deps.DaemonClient.RuntimeSignalIngest(context.WithoutCancel(ctx), protocol.RuntimeSignalIngestCommandBody{
			Source: protocol.RuntimeSignalSourceAgentHook, Kind: protocol.RuntimeSignalKindAgentActivityChanged,
			ProjectID: projectID, IssueID: issueID, SessionID: sessionID, Worktree: cwd, TmuxPane: pane,
			LogicalPaneID: "agent", PanePID: panePID, AgentIncarnation: incarnation, Agent: "codex", Hook: event, Event: event,
		})
	}
	if strings.TrimSpace(opts.Prompt) != "" {
		if err := nativeCodexStartTurn(ctx, rpc, threadID, cwd, opts.Prompt, opts.Yolo); err != nil {
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
		case err := <-rpc.done:
			return fmt.Errorf("Codex app-server disconnected: %w", err)
		case event := <-rpc.events:
			if event.Method == "item/agentMessage/delta" {
				var delta struct {
					Delta string `json:"delta"`
				}
				if json.Unmarshal(event.Params, &delta) == nil {
					fmt.Fprint(os.Stdout, delta.Delta)
				}
			}
			if event.Method == "turn/completed" {
				active = false
				signalActivity("idle_prompt")
				fmt.Fprint(os.Stdout, "\n› "+string(composer))
			}
		case b := <-input:
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
				if err := nativeCodexStartTurn(ctx, rpc, threadID, cwd, text, opts.Yolo); err != nil {
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
			response, newlyActive := acceptNativeCodexDelivery(delivery.envelope, composer, active, statePath, &clientState, func() error {
				return nativeCodexStartTurn(ctx, rpc, threadID, cwd, delivery.envelope.Payload, opts.Yolo)
			})
			active = active || newlyActive
			if newlyActive {
				signalActivity("user_prompt_submit")
			}
			delivery.reply <- response
		}
	}
}

func acceptNativeCodexDelivery(envelope nativeCodexEnvelope, composer []byte, active bool, statePath string, state *nativeCodexState, submit func() error) (nativeCodexResponse, bool) {
	response := nativeCodexResponse{ProjectID: envelope.ProjectID, IntentKey: envelope.IntentKey,
		AgentIncarnation: envelope.AgentIncarnation, LeaseToken: envelope.LeaseToken}
	key := envelope.IntentKey + "\x00" + envelope.AgentIncarnation
	if ack := state.Accepted[key]; ack != "" {
		response.Outcome, response.AcknowledgementToken = "accepted", ack
		return response, false
	}
	if len(composer) != 0 {
		response.Outcome = "composer_nonempty"
		return response, false
	}
	if active || submit() != nil {
		response.Outcome = "not_ready"
		return response, false
	}
	ack, err := randomNativeCodexToken()
	if err != nil {
		response.Outcome = "not_ready"
		return response, false
	}
	state.Accepted[key] = ack
	if err := saveNativeCodexState(statePath, *state); err != nil {
		delete(state.Accepted, key)
		response.Outcome = "not_ready"
		return response, false
	}
	response.Outcome, response.AcknowledgementToken = "accepted", ack
	return response, true
}

func nativeCodexThread(ctx context.Context, rpc *codexRPCClient, cwd string, resume bool) (string, error) {
	if resume {
		var listed struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := rpc.call(ctx, "thread/list", map[string]any{"cwd": cwd, "limit": 1, "sortKey": "updated_at", "sortDirection": "desc"}, &listed); err != nil {
			return "", err
		}
		if len(listed.Data) > 0 {
			var resumed struct {
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			if err := rpc.call(ctx, "thread/resume", map[string]any{"threadId": listed.Data[0].ID}, &resumed); err != nil {
				return "", err
			}
			return resumed.Thread.ID, nil
		}
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := rpc.call(ctx, "thread/start", map[string]any{"cwd": cwd}, &started); err != nil {
		return "", err
	}
	return started.Thread.ID, nil
}

func nativeCodexStartTurn(ctx context.Context, rpc *codexRPCClient, threadID, cwd, text string, yolo bool) error {
	params := map[string]any{"threadId": threadID, "cwd": cwd, "input": []map[string]string{{"type": "text", "text": text}}}
	if yolo {
		params["approvalPolicy"] = "never"
		params["sandboxPolicy"] = map[string]any{"type": "dangerFullAccess"}
	}
	var result map[string]any
	return rpc.call(ctx, "turn/start", params, &result)
}

func nativeCodexAuthorityLoop(ctx context.Context, socket string, registration nativeCodexRegistration, deliveries chan<- nativeCodexDelivery) {
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
		if json.NewEncoder(conn).Encode(registration) != nil {
			_ = conn.Close()
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
		_ = conn.Close()
	}
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

func randomNativeCodexToken() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

type nativeCodexState struct {
	Incarnation string            `json:"incarnation"`
	Accepted    map[string]string `json:"accepted"`
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
	state := nativeCodexState{Incarnation: incarnation, Accepted: map[string]string{}}
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
