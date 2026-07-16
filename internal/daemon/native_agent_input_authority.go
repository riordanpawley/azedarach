package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const nativeAgentInputProtocolVersion = 1
const nativeAgentInputMaxFrameBytes = 4 << 20
const nativeAgentInputMaxRegistrations = 64

func nativeAgentInputSocketPath(daemonSocketPath string) string {
	dir := filepath.Dir(strings.TrimSpace(daemonSocketPath))
	base := strings.TrimSuffix(filepath.Base(strings.TrimSpace(daemonSocketPath)), filepath.Ext(daemonSocketPath))
	if base == "" || base == "." {
		base = "azedarach"
	}
	return filepath.Join(dir, base+"-input.sock")
}

type nativeAgentInputRegistration struct {
	ProtocolVersion  int    `json:"protocol_version"`
	ProjectID        string `json:"project_id"`
	SessionID        string `json:"session_id"`
	LogicalPaneID    string `json:"logical_pane_id"`
	TmuxPaneID       string `json:"tmux_pane_id"`
	PanePID          int    `json:"pane_pid"`
	AgentIncarnation string `json:"agent_incarnation"`
	Tool             string `json:"tool"`
}

type nativeAgentInputEnvelope struct {
	ProtocolVersion  int    `json:"protocol_version"`
	ProjectID        string `json:"project_id"`
	SessionID        string `json:"session_id"`
	IntentKey        string `json:"intent_key"`
	AgentIncarnation string `json:"agent_incarnation"`
	LeaseToken       string `json:"lease_token"`
	Kind             string `json:"kind"`
	Payload          string `json:"payload"`
}

type nativeAgentInputResponse struct {
	ProjectID            string `json:"project_id"`
	IntentKey            string `json:"intent_key"`
	AgentIncarnation     string `json:"agent_incarnation"`
	LeaseToken           string `json:"lease_token"`
	Outcome              string `json:"outcome"`
	AcknowledgementToken string `json:"acknowledgement_token,omitempty"`
}

type nativeAgentInputRefusalError struct{ outcome string }

func (e nativeAgentInputRefusalError) Error() string {
	return "native agent input refused: " + e.outcome
}

type nativeAgentInputAuthority struct {
	mu                  sync.Mutex
	clients             map[string]*nativeAgentInputClient
	timeout             time.Duration
	registrationTimeout time.Duration
	registrationSlots   chan struct{}
}

type nativeAgentInputClient struct {
	registration nativeAgentInputRegistration
	conn         net.Conn
	reader       *bufio.Reader
	mu           sync.Mutex
	done         chan struct{}
	closeOnce    sync.Once
}

func newNativeAgentInputAuthority() *nativeAgentInputAuthority {
	return &nativeAgentInputAuthority{
		clients: make(map[string]*nativeAgentInputClient), timeout: 30 * time.Second,
		registrationTimeout: 10 * time.Second, registrationSlots: make(chan struct{}, nativeAgentInputMaxRegistrations),
	}
}

func nativeAgentInputClientKey(projectID, sessionID, logicalPaneID string) string {
	return strings.TrimSpace(projectID) + "\x00" + strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(logicalPaneID)
}

func (a *nativeAgentInputAuthority) Serve(ctx context.Context, socketPath string) error {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return errors.New("native agent input authority: empty socket path")
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale native agent input socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen for native agent input: %w", err)
	}
	var registrations sync.WaitGroup
	defer func() {
		_ = listener.Close()
		registrations.Wait()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("secure native agent input socket: %w", err)
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept native agent input client: %w", acceptErr)
		}
		select {
		case a.registrationSlots <- struct{}{}:
			registrations.Add(1)
			go func() {
				defer registrations.Done()
				defer func() { <-a.registrationSlots }()
				a.register(ctx, conn)
			}()
		default:
			_ = conn.Close()
		}
	}
}

func (a *nativeAgentInputAuthority) register(ctx context.Context, conn net.Conn) {
	cancelClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer cancelClose()
	if err := conn.SetReadDeadline(time.Now().Add(a.registrationTimeout)); err != nil {
		_ = conn.Close()
		return
	}
	reader := bufio.NewReaderSize(conn, nativeAgentInputMaxFrameBytes+1)
	var registration nativeAgentInputRegistration
	if err := readNativeAgentInputJSON(reader, &registration); err != nil || !validNativeAgentInputRegistration(registration) {
		_ = conn.Close()
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return
	}
	client := &nativeAgentInputClient{registration: registration, conn: conn, reader: reader, done: make(chan struct{})}
	key := nativeAgentInputClientKey(registration.ProjectID, registration.SessionID, registration.LogicalPaneID)
	a.mu.Lock()
	previous := a.clients[key]
	a.clients[key] = client
	a.mu.Unlock()
	if previous != nil {
		a.removeClient(key, previous)
	}
	select {
	case <-ctx.Done():
	case <-client.done:
	}
	a.removeClient(key, client)
}

func validNativeAgentInputRegistration(r nativeAgentInputRegistration) bool {
	return r.ProtocolVersion == nativeAgentInputProtocolVersion && strings.TrimSpace(r.ProjectID) != "" &&
		strings.TrimSpace(r.SessionID) != "" && strings.TrimSpace(r.LogicalPaneID) != "" &&
		strings.TrimSpace(r.TmuxPaneID) != "" && r.PanePID > 0 && strings.TrimSpace(r.AgentIncarnation) != "" &&
		strings.TrimSpace(r.Tool) != ""
}

func (a *nativeAgentInputAuthority) DeliverAgentInput(ctx context.Context, request authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	key := nativeAgentInputClientKey(request.Delivery.ProjectID, request.Delivery.SessionID, string(request.Delivery.Target.LogicalPaneID))
	a.mu.Lock()
	client := a.clients[key]
	a.mu.Unlock()
	if client == nil || !registrationMatchesDelivery(client.registration, request) {
		return authoritativeAgentInputAcknowledgement{}, errAuthoritativeAgentInputUnavailable
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	deadline := time.Now().Add(a.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := client.conn.SetDeadline(deadline); err != nil {
		return authoritativeAgentInputAcknowledgement{}, fmt.Errorf("set native agent input deadline: %w", err)
	}
	stopCancellationDeadline := context.AfterFunc(ctx, func() { _ = client.conn.SetDeadline(time.Now()) })
	defer stopCancellationDeadline()
	envelope := nativeAgentInputEnvelope{ProtocolVersion: nativeAgentInputProtocolVersion, ProjectID: request.Delivery.ProjectID,
		SessionID: request.Delivery.SessionID, IntentKey: request.Delivery.IntentKey, AgentIncarnation: request.Delivery.Target.AgentIncarnation,
		LeaseToken: request.LeaseToken, Kind: string(request.Delivery.Kind), Payload: request.Delivery.Payload}
	if err := json.NewEncoder(client.conn).Encode(envelope); err != nil {
		a.removeClient(key, client)
		return authoritativeAgentInputAcknowledgement{}, errAuthoritativeAgentInputUnavailable
	}
	var response nativeAgentInputResponse
	if err := readNativeAgentInputJSON(client.reader, &response); err != nil {
		a.removeClient(key, client)
		return authoritativeAgentInputAcknowledgement{}, errAuthoritativeAgentInputUnavailable
	}
	if response.ProjectID != envelope.ProjectID || response.IntentKey != envelope.IntentKey || response.AgentIncarnation != envelope.AgentIncarnation || response.LeaseToken != envelope.LeaseToken {
		a.removeClient(key, client)
		return authoritativeAgentInputAcknowledgement{}, errors.New("native agent input response did not match exact delivery")
	}
	if response.Outcome != "accepted" {
		return authoritativeAgentInputAcknowledgement{}, nativeAgentInputRefusalError{outcome: response.Outcome}
	}
	if strings.TrimSpace(response.AcknowledgementToken) == "" {
		a.removeClient(key, client)
		return authoritativeAgentInputAcknowledgement{}, errors.New("native agent input response omitted acknowledgement")
	}
	return authoritativeAgentInputAcknowledgement{ProjectID: response.ProjectID, IntentKey: response.IntentKey,
		AgentIncarnation: response.AgentIncarnation, LeaseToken: response.LeaseToken, AcknowledgementToken: response.AcknowledgementToken}, nil
}

func registrationMatchesDelivery(r nativeAgentInputRegistration, request authoritativeAgentInputRequest) bool {
	d := request.Delivery
	return r.ProtocolVersion == nativeAgentInputProtocolVersion && r.ProjectID == d.ProjectID && r.SessionID == d.SessionID &&
		r.LogicalPaneID == string(d.Target.LogicalPaneID) && r.TmuxPaneID == d.Target.TmuxPaneID && r.PanePID == d.Target.PanePID &&
		r.AgentIncarnation == d.Target.AgentIncarnation && strings.EqualFold(r.Tool, d.Tool)
}

func (a *nativeAgentInputAuthority) removeClient(key string, client *nativeAgentInputClient) {
	a.mu.Lock()
	if a.clients[key] == client {
		delete(a.clients, key)
	}
	a.mu.Unlock()
	client.closeOnce.Do(func() {
		if client.done != nil {
			close(client.done)
		}
		_ = client.conn.Close()
	})
}

func readNativeAgentInputJSON(reader *bufio.Reader, target any) error {
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return err
	}
	if len(line) > nativeAgentInputMaxFrameBytes {
		return errors.New("native agent input frame too large")
	}
	return json.Unmarshal(line, target)
}
