package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const rootedOrchestratorBootstrapNonceEnvironment = "AZEDARACH_ROOTED_ORCHESTRATOR_BOOT_NONCE"

var errRootedBootstrapQueued = errors.New("rooted bootstrap authoritative delivery pending")

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
	packet, err := domain.BuildWorkflowContextPacket(domain.WorkflowContextInput{
		Role: domain.WorkflowRoleIntegrator, IssueID: rootID, SourceRevision: domain.WorkflowIssueContextRevision(task), Summary: task.Title,
		Requirements: domain.WorkflowIssueRequirements(task),
	})
	if err != nil {
		return "", fmt.Errorf("build rooted orchestrator bounded context: %w", err)
	}
	encoded, err := domain.MarshalWorkflowContextPacket(packet)
	if err != nil {
		return "", fmt.Errorf("marshal rooted orchestrator bounded context: %w", err)
	}
	return buildRootedOrchestratorPrompt(rootID, task.Type.String(), packet.Summary) + "\n\nBounded semantic workflow context (authoritative for this phase; do not reconstruct it from inherited transcript or workflow scrollback):\n" + string(encoded), nil
}

func (d *Daemon) ensureRootedOrchestratorBootstrap(ctx context.Context, projectID string, scope domain.OrchestrationScope, sessionID, prompt string, launchedHere bool) (string, error) {
	if source := sourceForInvariant(daemonInvariantOrchestrationRootedBootstrap); source != daemonInvariantSourceHybrid || !usesProjectionSource(source) || !usesTmuxSource(source) {
		return "", fmt.Errorf("unsupported rooted bootstrap invariant source: %s", source)
	}
	identity, err := domain.NewOrchestratorIdentity(d.canonicalProjectID(projectID), scope)
	if err != nil {
		return "", err
	}
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return "", errors.New("rooted bootstrap acknowledgement store unavailable")
	}
	authority := daemonstate.NewRootedBootstrapAcknowledgementAuthority(store)
	promptHash := rootedOrchestratorPromptHash(prompt)
	managedIdentity, managedFound, managedErr := store.GetManagedAgentIdentity(ctx, d.canonicalProjectID(projectID), sessionID, "agent")
	if managedErr != nil {
		return "", fmt.Errorf("refresh rooted managed-agent identity: %w", managedErr)
	}
	if !launchedHere {
		acknowledgement, acknowledged, projectionErr := authority.Get(ctx, identity)
		if projectionErr != nil {
			return "", fmt.Errorf("refresh rooted bootstrap acknowledgement: %w", projectionErr)
		}
		runtimeNonce, found, nonceErr := d.tmux.EnvironmentValue(ctx, sessionID, rootedOrchestratorBootstrapNonceEnvironment)
		if nonceErr != nil {
			return "", fmt.Errorf("read rooted orchestrator runtime nonce: %w", nonceErr)
		}
		if found && acknowledged && managedFound && acknowledgement.SessionID == sessionID && acknowledgement.PromptHash == promptHash && acknowledgement.RuntimeNonce == runtimeNonce && acknowledgement.TmuxPaneID == managedIdentity.TmuxPaneID && acknowledgement.PanePID == managedIdentity.PanePID && acknowledgement.AgentIncarnation == managedIdentity.AgentIncarnation && acknowledgement.AgentThreadID == managedIdentity.AgentThreadID {
			return "verified", nil
		}
	}
	runtimeNonce, err := newRootedOrchestratorRuntimeNonce()
	if err != nil {
		return "", err
	}
	if err := d.tmux.SetEnvironment(ctx, sessionID, rootedOrchestratorBootstrapNonceEnvironment, runtimeNonce); err != nil {
		return "", fmt.Errorf("set rooted orchestrator runtime nonce: %w", err)
	}
	managedIdentity, managedFound, managedErr = store.GetManagedAgentIdentity(ctx, d.canonicalProjectID(projectID), sessionID, "agent")
	if managedErr != nil {
		return "", fmt.Errorf("refresh rooted managed-agent identity before acknowledgement: %w", managedErr)
	}
	now := time.Now().UTC()
	acknowledgement := daemonstate.RootedBootstrapAcknowledgement{
		Identity: identity, SessionID: sessionID, PromptHash: promptHash,
		RuntimeNonce: runtimeNonce, TmuxPaneID: managedIdentity.TmuxPaneID, PanePID: managedIdentity.PanePID, AgentIncarnation: managedIdentity.AgentIncarnation, AgentThreadID: managedIdentity.AgentThreadID, AcknowledgedAt: now, UpdatedAt: now,
	}
	if !managedFound || strings.TrimSpace(managedIdentity.AgentIncarnation) == "" {
		return "", errors.New("rooted bootstrap acknowledgement requires exact managed-agent identity")
	}
	if strings.EqualFold(strings.TrimSpace(d.runtimeConfigForProject(projectID).CLITool), "codex") && strings.TrimSpace(managedIdentity.AgentThreadID) == "" {
		return "", errors.New("rooted Codex bootstrap acknowledgement requires exact durable thread id")
	}
	if d.agentInputService() == nil {
		// The daemon may be reconstructing before its app-server authority is
		// available. Keep the exact target unacknowledged; reconciliation will
		// retry once the authoritative delivery capability is installed.
		return "", errRootedBootstrapQueued
	}
	result, deliveryErr := d.agentInputService().Deliver(ctx, domain.AgentInputDeliveryRequest{
		ProjectID: projectID, SessionID: sessionID,
		Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: domain.ManagedAgentPaneID(managedIdentity.LogicalPaneID), TmuxPaneID: managedIdentity.TmuxPaneID, PanePID: managedIdentity.PanePID, AgentIncarnation: managedIdentity.AgentIncarnation, AgentThreadID: managedIdentity.AgentThreadID},
		Tool:   d.runtimeConfigForProject(projectID).CLITool, Kind: domain.AgentInputMessageRootedBootstrap, Payload: prompt,
		IntentKey: fmt.Sprintf("rooted-bootstrap:%s:%s:%s", identity.Scope.RootIssueID, runtimeNonce, managedIdentity.AgentIncarnation), ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	})
	if deliveryErr != nil {
		return "", fmt.Errorf("deliver rooted bootstrap through authoritative path: %w", deliveryErr)
	}
	if result.Outcome != domain.AgentInputDelivered {
		return "", errRootedBootstrapQueued
	}
	if err := authority.Acknowledge(ctx, acknowledgement); err != nil {
		return "", fmt.Errorf("persist rooted bootstrap acknowledgement: %w", err)
	}
	if launchedHere {
		return "seeded", nil
	}
	return "repaired", nil
}

func (d *Daemon) acknowledgeDeliveredRootedBootstrap(ctx context.Context, request domain.AgentInputDeliveryRequest) error {
	if request.Kind != domain.AgentInputMessageRootedBootstrap {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(request.IntentKey, "rooted-bootstrap:"), ":")
	if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" {
		return errors.New("invalid rooted bootstrap delivery intent")
	}
	scope, err := domain.RootedOrchestrationScope(parts[0])
	if err != nil {
		return err
	}
	identity, err := domain.NewOrchestratorIdentity(d.canonicalProjectID(request.ProjectID), scope)
	if err != nil {
		return err
	}
	store := d.sessionRuntimeStateStoreIfConfigured(request.ProjectID)
	if store == nil {
		return errors.New("rooted bootstrap acknowledgement store unavailable")
	}
	current, found, err := store.GetManagedAgentIdentity(ctx, d.canonicalProjectID(request.ProjectID), request.SessionID, "agent")
	if err != nil || !found || current.AgentIncarnation != request.Target.AgentIncarnation || current.AgentThreadID != request.Target.AgentThreadID {
		return errors.New("rooted bootstrap delivery target changed before acknowledgement")
	}
	now := time.Now().UTC()
	if err := daemonstate.NewRootedBootstrapAcknowledgementAuthority(store).Acknowledge(ctx, daemonstate.RootedBootstrapAcknowledgement{Identity: identity, SessionID: request.SessionID, PromptHash: rootedOrchestratorPromptHash(request.Payload), RuntimeNonce: parts[1], TmuxPaneID: current.TmuxPaneID, PanePID: current.PanePID, AgentIncarnation: current.AgentIncarnation, AgentThreadID: current.AgentThreadID, AcknowledgedAt: now, UpdatedAt: now}); err != nil {
		return err
	}
	live, err := d.tmux.HasSession(ctx, request.SessionID)
	if err != nil {
		return fmt.Errorf("verify rooted bootstrap runtime before resume: %w", err)
	}
	if !live {
		return errors.New("rooted bootstrap runtime disappeared before resume")
	}
	lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(store).Get(ctx, identity)
	if err != nil {
		return fmt.Errorf("refresh rooted bootstrap lease before resume: %w", err)
	}
	if !found || lease.SessionID != request.SessionID {
		return errors.New("rooted bootstrap lease changed before resume")
	}
	_, err = daemonstate.NewOrchestratorLeaseAuthority(store).SetLifecycle(ctx, identity, request.SessionID, domain.OrchestratorWorking)
	return err
}

func (d *Daemon) invalidateRootedBootstrapAcknowledgement(ctx context.Context, acknowledgement daemonstate.RootedBootstrapAcknowledgement) error {
	store := d.sessionRuntimeStateStoreIfConfigured(acknowledgement.Identity.ProjectID)
	if store == nil {
		return errors.New("rooted bootstrap acknowledgement store unavailable")
	}
	return daemonstate.NewRootedBootstrapAcknowledgementAuthority(store).Invalidate(ctx, acknowledgement.Identity, acknowledgement.SessionID)
}

func rootedOrchestratorPromptHash(prompt string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(prompt)))
	return hex.EncodeToString(sum[:])
}

func newRootedOrchestratorRuntimeNonce() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate rooted orchestrator runtime nonce: %w", err)
	}
	return hex.EncodeToString(nonce[:]), nil
}
