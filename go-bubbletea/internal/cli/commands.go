package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	commandSessionStart  = "session.start"
	commandSessionAttach = "session.attach"
	commandSessionStop   = "session.stop"
	commandSessionStatus = "session.status"
)

type Dependencies struct {
	Config       *config.Config
	DaemonClient *daemonclient.Client
	Logger       *slog.Logger
	ProjectID    string
}

func NewDependencies(cfg *config.Config) (*Dependencies, error) {
	logger := slog.Default()

	repoDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}

	projectID := filepath.Base(repoDir)
	transport, err := newLocalDaemonTransport(cfg, repoDir, projectID, logger)
	if err != nil {
		return nil, err
	}

	return &Dependencies{
		Config:       cfg,
		DaemonClient: daemonclient.New(transport),
		Logger:       logger,
		ProjectID:    projectID,
	}, nil
}

func StartCommand(deps *Dependencies, beadID string) error {
	ctx := context.Background()

	deps.Logger.Info("starting session", "bead_id", beadID)

	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionStart, deps.ProjectID, beadID, "main"))
	if err != nil {
		return fmt.Errorf("failed to start session: %w", err)
	}
	if err := responseError(resp, "failed to start session"); err != nil {
		return err
	}

	return printCommandOutput(resp)
}

func AttachCommand(deps *Dependencies, beadID string) error {
	ctx := context.Background()

	deps.Logger.Info("attaching to session", "bead_id", beadID)

	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionAttach, deps.ProjectID, beadID, ""))
	if err != nil {
		return fmt.Errorf("failed to attach to session: %w", err)
	}
	if err := responseError(resp, "failed to attach to session"); err != nil {
		return err
	}

	return printCommandOutput(resp)
}

func KillCommand(deps *Dependencies, beadID string) error {
	ctx := context.Background()

	deps.Logger.Info("killing session", "bead_id", beadID)

	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionStop, deps.ProjectID, beadID, ""))
	if err != nil {
		return fmt.Errorf("failed to kill session: %w", err)
	}
	if err := responseError(resp, "failed to kill session"); err != nil {
		return err
	}

	return printCommandOutput(resp)
}

func StatusCommand(deps *Dependencies, beadID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deps.Logger.Info("checking session status", "bead_id", beadID)

	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionStatus, deps.ProjectID, beadID, ""))
	if err != nil {
		return fmt.Errorf("failed to list tmux sessions: %w", err)
	}
	if err := responseError(resp, "failed to list tmux sessions"); err != nil {
		return err
	}

	return printCommandOutput(resp)
}

func PrintUsage() {
	usage := `Usage: az [command] [arguments]

Commands:
  (no command)         Start the Azedarach TUI
  start <bead-id>      Start a Claude session for a bead
  attach <bead-id>     Attach to an existing session
  kill <bead-id>       Kill a session
  status [bead-id]     Show session status (all or specific bead)
  help                 Show this help message

Examples:
  az                   # Start TUI
  az start az-123      # Start session for bead az-123
  az attach az-123     # Attach to az-123's session
  az kill az-123       # Kill az-123's session
  az status            # Show all active sessions
  az status az-123     # Show status for az-123

For more information, see: https://github.com/riordanpawley/azedarach
`
	fmt.Print(usage)
}

type sessionRequestBody struct {
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	BaseBranch string `json:"base_branch,omitempty"`
}

type commandOutputBody struct {
	Output string `json:"output"`
}

func newSessionRequest(command, projectID, sessionID, baseBranch string) protocol.RequestEnvelope {
	body, _ := json.Marshal(sessionRequestBody{
		ProjectID:  projectID,
		SessionID:  sessionID,
		BaseBranch: baseBranch,
	})

	return protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       fmt.Sprintf("%s-%d", command, time.Now().UTC().UnixNano()),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         command,
		Meta: protocol.Metadata{
			ProjectID: projectID,
			SessionID: sessionID,
		},
		SentAt: time.Now().UTC(),
		Body:   body,
	}
}

func responseError(resp protocol.ResponseEnvelope, prefix string) error {
	if resp.OK {
		return nil
	}
	if resp.Error == nil {
		return fmt.Errorf("%s: daemon command failed", prefix)
	}
	return errors.New(resp.Error.Message)
}

func printCommandOutput(resp protocol.ResponseEnvelope) error {
	if len(resp.Body) == 0 {
		return nil
	}

	var out commandOutputBody
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return fmt.Errorf("failed to decode daemon response: %w", err)
	}

	if out.Output != "" {
		fmt.Print(out.Output)
	}

	return nil
}
