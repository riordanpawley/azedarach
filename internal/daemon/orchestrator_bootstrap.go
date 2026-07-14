package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const rootedOrchestratorBootstrapVersion = "rooted-orchestrator-v1"

type rootedOrchestratorBootstrapReceipt struct {
	Version    string    `json:"version"`
	ProjectID  string    `json:"project_id"`
	RootID     string    `json:"root_id"`
	SessionID  string    `json:"session_id"`
	PromptHash string    `json:"prompt_sha256"`
	ReceivedAt time.Time `json:"received_at"`
}

func (d *Daemon) rootedOrchestratorBootstrapPrompt(ctx context.Context, projectID string, scope domain.OrchestrationScope) (string, error) {
	rootID := strings.TrimSpace(scope.RootIssueID.String())
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return "", errors.New("issue store unavailable")
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, rootID)
	if err != nil {
		return "", fmt.Errorf("load rooted orchestrator issue %s: %w", rootID, err)
	}
	return buildRootedOrchestratorPrompt(rootID, task.Type.String(), task.Title), nil
}

func (d *Daemon) ensureRootedOrchestratorBootstrap(ctx context.Context, projectID string, scope domain.OrchestrationScope, sessionID, prompt string, launchedHere bool) (string, error) {
	receiptPath, err := d.rootedOrchestratorBootstrapReceiptPath(ctx, projectID, scope, sessionID)
	if err != nil {
		return "", err
	}
	want := rootedOrchestratorBootstrapReceipt{
		Version:    rootedOrchestratorBootstrapVersion,
		ProjectID:  d.canonicalProjectID(projectID),
		RootID:     scope.RootIssueID.String(),
		SessionID:  sessionID,
		PromptHash: rootedOrchestratorPromptHash(prompt),
	}
	if receiptMatchesRootedOrchestratorBootstrap(receiptPath, want) {
		return "verified", nil
	}
	if !launchedHere {
		handoff, handoffErr := prepareSessionPromptHandoff(d.sessionLaunchArtifactDir(), prompt)
		if handoffErr != nil {
			return "", fmt.Errorf("prepare rooted orchestrator bootstrap repair: %w", handoffErr)
		}
		defer handoff.remove()
		if err := d.tmux.PasteTextAndSubmit(ctx, sessionID, handoff.bootstrapPrompt()); err != nil {
			return "", fmt.Errorf("deliver rooted orchestrator bootstrap repair: %w", err)
		}
		if err := waitForSessionPromptHandoffConsumed(ctx, handoff); err != nil {
			return "", fmt.Errorf("confirm rooted orchestrator bootstrap repair: %w", err)
		}
	}
	want.ReceivedAt = time.Now().UTC()
	if err := writeRootedOrchestratorBootstrapReceipt(receiptPath, want); err != nil {
		return "", err
	}
	if launchedHere {
		return "seeded", nil
	}
	return "repaired", nil
}

func (d *Daemon) rootedOrchestratorBootstrapReceiptPath(ctx context.Context, projectID string, scope domain.OrchestrationScope, sessionID string) (string, error) {
	manager := d.worktreeManagerForProject(projectID)
	if manager == nil {
		return "", errors.New("worktree manager unavailable")
	}
	worktree, err := manager.Get(ctx, scope.RootIssueID.String())
	if err != nil {
		return "", fmt.Errorf("load rooted orchestrator worktree: %w", err)
	}
	return filepath.Join(worktree.Path, ".azedarach", "session-bootstrap", safeSessionAsyncInitPathSegment(sessionID), rootedOrchestratorBootstrapVersion+".json"), nil
}

func rootedOrchestratorPromptHash(prompt string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(prompt)))
	return hex.EncodeToString(sum[:])
}

func receiptMatchesRootedOrchestratorBootstrap(path string, want rootedOrchestratorBootstrapReceipt) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var got rootedOrchestratorBootstrapReceipt
	if json.Unmarshal(data, &got) != nil {
		return false
	}
	return got.Version == want.Version &&
		got.ProjectID == want.ProjectID &&
		got.RootID == want.RootID &&
		got.SessionID == want.SessionID &&
		got.PromptHash == want.PromptHash &&
		!got.ReceivedAt.IsZero()
}

func writeRootedOrchestratorBootstrapReceipt(path string, receipt rootedOrchestratorBootstrapReceipt) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create rooted orchestrator bootstrap receipt directory: %w", err)
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("encode rooted orchestrator bootstrap receipt: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".rooted-bootstrap-*.tmp")
	if err != nil {
		return fmt.Errorf("create rooted orchestrator bootstrap receipt: %w", err)
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure rooted orchestrator bootstrap receipt: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("write rooted orchestrator bootstrap receipt: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync rooted orchestrator bootstrap receipt: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close rooted orchestrator bootstrap receipt: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish rooted orchestrator bootstrap receipt: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open rooted orchestrator bootstrap receipt directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync rooted orchestrator bootstrap receipt directory: %w", err)
	}
	return nil
}
