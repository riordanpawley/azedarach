package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type sessionCommandBody struct {
	ProjectID  string   `json:"project_id"`
	SessionID  string   `json:"session_id"`
	IssueID    string   `json:"issue_id,omitempty"`
	BaseBranch string   `json:"base_branch,omitempty"`
	Yolo       bool     `json:"yolo,omitempty"`
	StartWork  *bool    `json:"start_work,omitempty"`
	ImagePaths []string `json:"image_paths,omitempty"`
	Prompt     string   `json:"initial_prompt,omitempty"`
	Message    string   `json:"message,omitempty"`
}

type resolvedSessionTarget struct {
	ProjectID  string
	IssueID    string
	SessionID  string
	BaseBranch string
	Yolo       bool
	StartWork  bool
	ImagePaths []string
	Prompt     string
	Message    string
}

type sessionRecoveryResult struct {
	RecreatedTmuxSessions int `json:"recreated_tmux_sessions"`
	AlignedDaemonSessions int `json:"aligned_daemon_sessions"`
}

type issueResourceLifecycleContext struct {
	ProjectID    string
	IssueID      string
	SessionID    string
	WorktreePath string
	RootPath     string
	Branch       string
	DesiredState string
}

type issueResourceLifecycleResult struct {
	Ran []string
}

type sessionInitReadyMarker struct {
	RelativePath string
	AbsolutePath string
	CommandCount int
}

type sessionProjectionCounts struct {
	Total             int
	Active            int
	Paused            int
	TmuxAttachedCount int
}

type sessionHookActivity struct {
	Total  int
	Active int
	Paused int
}

type sessionDisplayActivity struct {
	Activity string
	Source   string
}

const (
	sessionInvariantSessionStartConflict   daemonInvariantID = daemonInvariantSessionStartConflict
	sessionInvariantSessionAttachTarget    daemonInvariantID = daemonInvariantSessionAttachTarget
	sessionInvariantSessionLifecycleTarget daemonInvariantID = daemonInvariantSessionLifecycleTarget
	sessionInvariantSessionStopTargets     daemonInvariantID = daemonInvariantSessionStopTargets
	sessionInvariantSessionReconcile       daemonInvariantID = daemonInvariantSessionReconcile

	sessionConflictWindowName   = "resolve-conflict"
	sessionActivityStartupGrace = 45 * time.Second
)

func reportSessionStartProgress(ctx context.Context, phase, message string, percent int) {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	_ = daemonops.ReportProgress(ctx, daemonops.Progress{
		Phase:   strings.TrimSpace(phase),
		Message: strings.TrimSpace(message),
		Current: int64(percent),
		Total:   100,
		Unit:    "percent",
		Percent: percent,
	})
}

type SessionLongRunningExecutor interface {
	Execute(ctx context.Context, req protocol.RequestEnvelope, command string, exec func(context.Context) (protocol.ResponseEnvelope, error)) (protocol.ResponseEnvelope, error)
}

func sessionKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func sessionProjectionIssueID(session daemonstate.Session, namingScope string) string {
	issue, issueErr := naming.ParseIssueID(strings.TrimSpace(session.IssueID))
	if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(session.ID, namingScope); ok {
		_, parsedErr := naming.ParseIssueID(parsedIssueID)
		if parsedErr == nil && issueErr != nil {
			return parsedIssueID
		}
		sessionLikePrefix := naming.ProjectSessionPrefix(namingScope) + "-"
		if parsedErr == nil && issueErr == nil && (naming.IssueIDsEqual(issue.String(), session.ID) || strings.HasPrefix(strings.ToLower(issue.String()), strings.ToLower(sessionLikePrefix))) {
			return parsedIssueID
		}
	}
	if issueErr == nil {
		return issue.String()
	}

	fallback, fallbackErr := naming.ParseIssueID(strings.TrimSpace(session.ID))
	if fallbackErr != nil {
		return ""
	}
	return fallback.String()
}

func sessionProjectionStateRank(state daemonstate.SessionState) int {
	switch daemonstate.NormalizeSessionState(state) {
	case daemonstate.SessionStateRunning:
		return 4
	case daemonstate.SessionStateStarting:
		return 3
	case daemonstate.SessionStatePaused:
		return 2
	case daemonstate.SessionStateStopped:
		return 1
	default:
		return 0
	}
}

func shouldReplaceSessionProjection(existing, candidate daemonstate.Session) bool {
	if candidate.UpdatedAt.After(existing.UpdatedAt) {
		return true
	}
	if existing.UpdatedAt.After(candidate.UpdatedAt) {
		return false
	}
	existingRank := sessionProjectionStateRank(existing.State)
	candidateRank := sessionProjectionStateRank(candidate.State)
	if candidateRank != existingRank {
		return candidateRank > existingRank
	}
	return strings.TrimSpace(candidate.ID) < strings.TrimSpace(existing.ID)
}

func sessionProjectionLatestByIssueKey(sessions []daemonstate.Session, namingScope string) map[string]daemonstate.Session {
	byIssueKey := make(map[string]daemonstate.Session, len(sessions))
	for _, session := range sessions {
		key := sessionKey(sessionProjectionIssueID(session, namingScope))
		if key == "" {
			continue
		}
		existing, exists := byIssueKey[key]
		if !exists || shouldReplaceSessionProjection(existing, session) {
			byIssueKey[key] = session
		}
	}
	return byIssueKey
}

func sessionProjectionAggregateByIssueKey(sessions []daemonstate.Session, namingScope string) map[string]daemonstate.Session {
	byIssueKey := make(map[string]daemonstate.Session, len(sessions))
	for _, session := range sessions {
		if isAgentScopedSessionID(session.ID) {
			continue
		}
		key := sessionKey(sessionProjectionIssueID(session, namingScope))
		if key == "" {
			continue
		}
		existing, exists := byIssueKey[key]
		if !exists {
			byIssueKey[key] = session
			continue
		}
		merged := existing
		if session.UpdatedAt.After(merged.UpdatedAt) {
			merged.ID = session.ID
			merged.IssueID = session.IssueID
			merged.UpdatedAt = session.UpdatedAt
			merged.Activity = session.Activity
			merged.ActivitySource = session.ActivitySource
		}
		if sessionProjectionStateRank(session.State) > sessionProjectionStateRank(merged.State) {
			merged.State = session.State
		}
		if sessionProjectionStateRank(session.ObservedState) > sessionProjectionStateRank(merged.ObservedState) {
			merged.ObservedState = session.ObservedState
		}
		if session.StartedAt != nil && !session.StartedAt.IsZero() {
			if merged.StartedAt == nil || merged.StartedAt.IsZero() || session.StartedAt.Before(*merged.StartedAt) {
				started := session.StartedAt.UTC()
				merged.StartedAt = &started
			}
		}
		merged.TmuxAttachedCount += session.TmuxAttachedCount
		if strings.TrimSpace(merged.Activity) == "" && strings.TrimSpace(session.Activity) != "" {
			merged.Activity = session.Activity
			merged.ActivitySource = session.ActivitySource
		}
		byIssueKey[key] = merged
	}
	return byIssueKey
}

func nonAgentSessionProjectionByIssue(sessions []daemonstate.Session, namingScope, issueID string) (daemonstate.Session, bool) {
	byIssueKey := sessionProjectionAggregateByIssueKey(sessions, namingScope)
	session, found := byIssueKey[sessionKey(issueID)]
	return session, found
}

func sessionProjectionCountsByIssueKey(sessions []daemonstate.Session, namingScope string) map[string]sessionProjectionCounts {
	byIssueKey := make(map[string]sessionProjectionCounts, len(sessions))
	for _, session := range sessions {
		if isAgentScopedSessionID(session.ID) {
			continue
		}
		state := session.State
		observed := session.ObservedState
		if strings.TrimSpace(string(observed)) == "" {
			observed = state
		}
		if state == daemonstate.SessionStateStopped || observed == daemonstate.SessionStateStopped {
			continue
		}
		key := sessionKey(sessionProjectionIssueID(session, namingScope))
		if key == "" {
			continue
		}
		counts := byIssueKey[key]
		counts.TmuxAttachedCount += session.TmuxAttachedCount
		counts.Total++
		switch state {
		case daemonstate.SessionStatePaused:
			counts.Paused++
		default:
			counts.Active++
		}
		byIssueKey[key] = counts
	}
	return byIssueKey
}

func isAgentScopedSessionID(sessionID string) bool {
	return strings.Contains(strings.TrimSpace(sessionID), ".pane-")
}

func sessionProjectionForReconcileByIssueKey(sessions []daemonstate.Session, namingScope string) map[string]daemonstate.Session {
	return sessionProjectionAggregateByIssueKey(sessions, namingScope)
}

func sessionProjectionForTmuxHydrationByIssueKey(sessions []daemonstate.Session, namingScope string) map[string]daemonstate.Session {
	return sessionProjectionAggregateByIssueKey(sessions, namingScope)
}

func sessionProjectionForTaskDisplayByIssueKey(snapshotByIssueKey, projectionByIssueKey map[string]daemonstate.Session) map[string]daemonstate.Session {
	byIssueKey := make(map[string]daemonstate.Session, len(snapshotByIssueKey)+len(projectionByIssueKey))
	for key, session := range snapshotByIssueKey {
		byIssueKey[key] = session
	}
	for key, session := range projectionByIssueKey {
		existing, exists := byIssueKey[key]
		if !exists || shouldReplaceSessionProjection(existing, session) {
			byIssueKey[key] = session
		}
	}
	return byIssueKey
}

func sessionProjectionStartedAtForTaskDisplay(snapshotSession daemonstate.Session, snapshotOK bool, projectionSession daemonstate.Session, projectionOK bool) *time.Time {
	var startedSource *time.Time

	if snapshotOK && snapshotSession.StartedAt != nil && !snapshotSession.StartedAt.IsZero() {
		startedSource = snapshotSession.StartedAt
	}
	if projectionOK && projectionSession.StartedAt != nil && !projectionSession.StartedAt.IsZero() {
		if startedSource == nil || startedSource.IsZero() || projectionSession.StartedAt.Before(*startedSource) {
			startedSource = projectionSession.StartedAt
		}
	}
	if startedSource == nil || startedSource.IsZero() {
		switch {
		case snapshotOK && !snapshotSession.UpdatedAt.IsZero():
			startedSource = &snapshotSession.UpdatedAt
		case projectionOK && !projectionSession.UpdatedAt.IsZero():
			startedSource = &projectionSession.UpdatedAt
		}
	}
	if startedSource == nil || startedSource.IsZero() {
		return nil
	}
	started := startedSource.UTC()
	return &started
}

func (d *Daemon) sourceForSessionInvariant(invariant daemonInvariantID) daemonInvariantSource {
	return sourceForInvariant(invariant)
}

func sessionStopPendingKey(projectID, issueID string) string {
	project := protocol.NormalizeProjectID(projectID)
	issueKey := sessionKey(issueID)
	if issueKey == "" {
		return ""
	}
	return strings.ToLower(project) + ":" + issueKey
}

func (d *Daemon) markSessionStopPending(projectID, issueID string) func() {
	projectID = d.canonicalProjectID(projectID)
	key := sessionStopPendingKey(projectID, issueID)
	if key == "" {
		return func() {}
	}

	d.sessionStopMu.Lock()
	if d.sessionStopPending == nil {
		d.sessionStopPending = map[string]int{}
	}
	d.sessionStopPending[key]++
	d.sessionStopMu.Unlock()

	return func() {
		d.sessionStopMu.Lock()
		defer d.sessionStopMu.Unlock()
		if d.sessionStopPending == nil {
			return
		}
		next := d.sessionStopPending[key] - 1
		if next <= 0 {
			delete(d.sessionStopPending, key)
			return
		}
		d.sessionStopPending[key] = next
	}
}

func (d *Daemon) isSessionStopPending(projectID, issueID string) bool {
	projectID = d.canonicalProjectID(projectID)
	key := sessionStopPendingKey(projectID, issueID)
	if key == "" {
		return false
	}

	d.sessionStopMu.Lock()
	defer d.sessionStopMu.Unlock()
	return d.sessionStopPending[key] > 0
}

func (d *Daemon) sessionNamingScope(projectID string) string {
	trimmed := protocol.TrimProjectID(projectID)
	if repoDir := strings.TrimSpace(d.resolveRepoDirForProjectExact(trimmed)); repoDir != "" {
		return repoDir
	}
	if trimmed == "" || trimmed == protocol.DefaultProjectID {
		return d.cfg.RepoDir
	}
	return trimmed
}

func (d *Daemon) decodeSessionRequest(req protocol.RequestEnvelope, requireSession bool) (resolvedSessionTarget, protocol.ResponseEnvelope, bool) {
	var cmd sessionCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return resolvedSessionTarget{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), false
	}
	cmd.ProjectID = protocol.TrimProjectID(cmd.ProjectID)
	if cmd.ProjectID == "" {
		cmd.ProjectID = req.Meta.ProjectID.String()
	}
	cmd.ProjectID = d.canonicalProjectID(cmd.ProjectID)
	cmd.IssueID = strings.TrimSpace(cmd.IssueID)
	if requireSession && cmd.SessionID == "" {
		return resolvedSessionTarget{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: project_id/session_id"), false
	}

	issueID := ""
	namingScope := d.sessionNamingScope(cmd.ProjectID)
	if cmd.IssueID != "" {
		validIssueID, issueErr := naming.ParseIssueID(cmd.IssueID)
		if issueErr != nil {
			return resolvedSessionTarget{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid issue id: %v", issueErr)), false
		}
		issueID = validIssueID.String()
	}
	sessionInput := strings.TrimSpace(cmd.SessionID)
	if sessionInput != "" {
		if issueID == "" {
			if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(sessionInput, namingScope); ok {
				sessionInput = parsedIssueID
			}
			validIssueID, issueErr := naming.ParseIssueID(sessionInput)
			if issueErr != nil {
				return resolvedSessionTarget{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid session/issue id: %v", issueErr)), false
			}
			issueID = validIssueID.String()
		} else if _, sessionErr := naming.ParseSessionIDLoose(sessionInput); sessionErr != nil {
			return resolvedSessionTarget{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid session id: %v", sessionErr)), false
		}
	}
	sessionID := ""
	if issueID != "" {
		typedIssueID, _ := naming.ParseIssueID(issueID)
		sessionID = naming.CanonicalSessionIDForIssue(namingScope, typedIssueID).String()
	}
	if issueID != "" && sessionInput != "" && cmd.IssueID != "" {
		sessionID = sessionInput
	}
	startWork := true
	if cmd.StartWork != nil {
		startWork = *cmd.StartWork
	}
	return resolvedSessionTarget{
		ProjectID:  cmd.ProjectID,
		IssueID:    issueID,
		SessionID:  sessionID,
		BaseBranch: cmd.BaseBranch,
		Yolo:       cmd.Yolo,
		StartWork:  startWork,
		ImagePaths: cmd.ImagePaths,
		Prompt:     cmd.Prompt,
		Message:    strings.TrimSpace(cmd.Message),
	}, protocol.ResponseEnvelope{}, true
}

func (d *Daemon) sessionProjectionSnapshot(ctx context.Context, projectID string) ([]daemonstate.Session, error) {
	if d == nil || d.sessionStore == nil {
		return nil, nil
	}
	projectID = d.canonicalProjectID(projectID)
	if err := d.refreshSessionInvariantCacheIfConfigured(ctx, projectID); err != nil {
		return nil, err
	}
	snapshot := d.sessionStore.ReadSnapshot(projectID)
	sessions := make([]daemonstate.Session, 0, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (d *Daemon) tmuxSessionNamesForIssue(ctx context.Context, projectID, issueID, canonicalSessionID string, source daemonInvariantSource) ([]string, error) {
	typedIssueID, issueErr := naming.ParseIssueID(strings.TrimSpace(issueID))
	if issueErr != nil {
		return nil, nil
	}
	projectID = d.canonicalProjectID(projectID)
	namingScope := d.sessionNamingScope(projectID)
	canonicalSessionID = strings.TrimSpace(canonicalSessionID)
	names := map[string]struct{}{}

	if usesProjectionSource(source) {
		snapshotSessions, err := d.sessionProjectionSnapshot(ctx, projectID)
		if err != nil {
			return nil, err
		}
		for _, session := range snapshotSessions {
			if session.State == daemonstate.SessionStateStopped {
				continue
			}
			sessionID := strings.TrimSpace(session.ID)
			if sessionID == "" {
				continue
			}
			if canonicalSessionID != "" && strings.EqualFold(sessionID, canonicalSessionID) {
				names[sessionID] = struct{}{}
				continue
			}
			projectedIssueID := sessionProjectionIssueID(session, namingScope)
			if naming.IssueIDsEqual(projectedIssueID, typedIssueID.String()) {
				names[sessionID] = struct{}{}
			}
		}
	}

	if usesTmuxSource(source) && d.tmux != nil {
		sessions, err := d.tmux.ListSessions(ctx)
		if err != nil {
			return nil, err
		}
		for _, sessionName := range sessions {
			name := strings.TrimSpace(sessionName)
			if name == "" {
				continue
			}
			if canonicalSessionID != "" && strings.EqualFold(name, canonicalSessionID) {
				names[name] = struct{}{}
				continue
			}
			projectedIssueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope)
			if ok && naming.IssueIDsEqual(projectedIssueID, typedIssueID.String()) {
				names[name] = struct{}{}
			}
		}
	}

	resolved := make([]string, 0, len(names))
	for name := range names {
		resolved = append(resolved, name)
	}
	return resolved, nil
}

func (d *Daemon) sessionExistsForInvariant(ctx context.Context, projectID, issueID, canonicalSessionID string, source daemonInvariantSource) (bool, error) {
	names, err := d.tmuxSessionNamesForIssue(ctx, projectID, issueID, canonicalSessionID, source)
	if err != nil {
		return false, err
	}
	if len(names) == 0 {
		return false, nil
	}
	target := strings.TrimSpace(canonicalSessionID)
	if target == "" {
		return true, nil
	}
	for _, name := range names {
		if strings.TrimSpace(name) == target {
			return true, nil
		}
	}
	return false, nil
}

func (d *Daemon) sessionLifecycleTargetExists(ctx context.Context, projectID, issueID, sessionID string) (bool, error) {
	source := d.sourceForSessionInvariant(sessionInvariantSessionLifecycleTarget)
	if usesTmuxSource(source) && d.tmux == nil {
		return true, nil
	}
	exists, err := d.sessionExistsForInvariant(ctx, projectID, issueID, sessionID, source)
	if exists || err != nil {
		return exists, err
	}
	if parentSessionID, ok := d.parentSessionIDForAgentScopedSession(projectID, issueID, sessionID); ok {
		return d.sessionExistsForInvariant(ctx, projectID, issueID, parentSessionID, source)
	}
	return false, nil
}

func (d *Daemon) parentSessionIDForAgentScopedSession(projectID, issueID, sessionID string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if !isAgentScopedSessionID(sessionID) {
		return "", false
	}
	issue, err := naming.ParseIssueID(strings.TrimSpace(issueID))
	if err != nil {
		return "", false
	}
	parent := naming.CanonicalSessionIDForIssue(d.sessionNamingScope(projectID), issue).String()
	if !strings.HasPrefix(sessionID, parent+".pane-") {
		return "", false
	}
	return parent, true
}

func (d *Daemon) handleSessionStart(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if d.sessionLongRunning != nil {
		return d.sessionLongRunning.Execute(ctx, req, req.Command, func(execCtx context.Context) (protocol.ResponseEnvelope, error) {
			return d.handleSessionStartDirect(execCtx, req)
		})
	}
	return d.handleSessionStartDirect(ctx, req)
}

func (d *Daemon) handleSessionStartDirect(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session start requested",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"base_branch", cmd.BaseBranch,
			"yolo", cmd.Yolo,
			"start_work", cmd.StartWork,
			"image_count", len(cmd.ImagePaths),
		)
	}
	reportSessionStartProgress(ctx, "preflight", "checking runtime state and existing session", 5)
	if err := d.ensureFreshRuntimeForIssueMutation(ctx, cmd.ProjectID, cmd.IssueID, daemonhandlers.CommandSessionStart); err != nil {
		if d.cfg.Logger != nil {
			if errors.Is(err, context.Canceled) {
				d.cfg.Logger.Warn("daemon session start aborted after freshness cancellation",
					"project_id", cmd.ProjectID,
					"issue_id", cmd.IssueID,
					"session_id", cmd.SessionID,
					"error", err,
				)
			} else if isRuntimeMutationFreshnessTimeout(err) {
				d.cfg.Logger.Warn("daemon session start continuing after freshness timeout",
					"project_id", cmd.ProjectID,
					"issue_id", cmd.IssueID,
					"session_id", cmd.SessionID,
					"error", err,
				)
			}
		}
		if errors.Is(err, context.Canceled) {
			return d.mutationFreshnessErrorResponse(req, err), nil
		}
		if !isRuntimeMutationFreshnessTimeout(err) {
			return d.mutationFreshnessErrorResponse(req, err), nil
		}
	}
	startConflictSource := d.sourceForSessionInvariant(sessionInvariantSessionStartConflict)
	exists, err := d.sessionExistsForInvariant(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID, startConflictSource)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if exists {
		return d.errorResponse(req, protocol.ErrorCodeConflict, fmt.Sprintf("session already exists: %s (use 'az attach %s' to connect)", cmd.IssueID, cmd.IssueID)), nil
	}
	reportSessionStartProgress(ctx, "worktree_preflight", "loading issue and preparing worktree", 15)
	issueClient := d.issueClientForProject(cmd.ProjectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	task, err := issueClient.GetWithRuntime(ctx, cmd.ProjectID, cmd.IssueID)
	if errors.Is(err, domain.ErrNotFound) {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("issue not found: %s", cmd.IssueID)), nil
	}
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	worktreeManager := d.worktreeManagerForProject(cmd.ProjectID)
	if worktreeManager == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "worktree manager unavailable"), nil
	}
	baseBranch, baseBranchAncestorIssueID := d.resolveSessionStartBaseBranch(ctx, cmd.ProjectID, cmd.BaseBranch, issueClient, worktreeManager, task)
	reportSessionStartProgress(ctx, "worktree_preflight", fmt.Sprintf("creating or reusing worktree from %s", baseBranch), 25)
	worktree, err := worktreeManager.CreateWithTitle(ctx, cmd.IssueID, task.Title, baseBranch)
	reusedWorktree := false
	worktreeSetupWarning := ""
	if err != nil {
		// Recovery path: git worktree add can return non-zero after materializing
		// a usable worktree (for example, hooks that fail post-checkout).
		// If we can load the worktree for the issue, continue by reusing it.
		if recoveredWorktree, recoverErr := worktreeManager.Get(ctx, cmd.IssueID); recoverErr == nil {
			worktree = recoveredWorktree
			reusedWorktree = true
			worktreeSetupWarning = fmt.Sprintf("Worktree setup warning: git worktree create reported %v; recovered existing worktree at %s. Validate setup in the worktree before relying on later checks.", err, worktree.Path)
		} else {
			if !errors.Is(err, git.ErrWorktreeAlreadyExists) {
				return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
			}
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("worktree already exists but could not be loaded: %v", recoverErr)), nil
		}
	}
	if err := d.ensureSessionStartWorktreeClean(ctx, worktreeManager, cmd.IssueID, worktree.Path); err != nil {
		cleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, worktreeManager, cmd.IssueID, worktree.Path, reusedWorktree)
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()+cleanupNote), nil
	}
	if !reusedWorktree {
		if len(d.runtimeConfigForProject(cmd.ProjectID).WorktreeInitCommands) > 0 {
			reportSessionStartProgress(ctx, "worktree_preflight", "running worktree init commands", 35)
		}
		if err := d.runWorktreeInitCommands(ctx, cmd.ProjectID, worktree.Path); err != nil {
			cleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, worktreeManager, cmd.IssueID, worktree.Path, reusedWorktree)
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("worktree init failed for %s: %v%s", cmd.IssueID, err, cleanupNote)), nil
		}
	}
	reportSessionStartProgress(ctx, "issue_resources", "preparing issue resources", 50)
	resourceCtx := d.issueResourceLifecycleContext(cmd.ProjectID, cmd.IssueID, cmd.SessionID, worktree.Path, worktree.Branch)
	resourcePrep, err := d.runIssueResourcePrepareCommands(ctx, cmd.ProjectID, resourceCtx)
	if err != nil {
		cleanupNote := d.issueResourceFailedStartCleanupNote(ctx, cmd.ProjectID, resourceCtx)
		worktreeCleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, worktreeManager, cmd.IssueID, worktree.Path, reusedWorktree)
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("issue resource prepare failed for %s: %v%s%s", cmd.IssueID, err, cleanupNote, worktreeCleanupNote)), nil
	}
	if _, err := d.runIssueResourceReconcileCommand(ctx, cmd.ProjectID, resourceCtx, "present"); err != nil {
		cleanupNote := d.issueResourceFailedStartCleanupNote(ctx, cmd.ProjectID, resourceCtx)
		worktreeCleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, worktreeManager, cmd.IssueID, worktree.Path, reusedWorktree)
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("issue resource reconcile present failed for %s: %v%s%s", cmd.IssueID, err, cleanupNote, worktreeCleanupNote)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session start worktree prepared",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"worktree", worktree.Path,
			"branch", worktree.Branch,
			"base_branch", baseBranch,
			"base_branch_ancestor_issue_id", baseBranchAncestorIssueID,
			"reused_worktree", reusedWorktree,
		)
	}
	d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, cmd.ProjectID, cmd.IssueID, worktree.Path, worktree.Branch)
	reportSessionStartProgress(ctx, "tmux_launch", "creating tmux session", 70)
	if err := d.tmux.NewSession(ctx, cmd.SessionID, worktree.Path); err != nil {
		cleanupNote := d.issueResourceFailedStartCleanupNote(ctx, cmd.ProjectID, resourceCtx)
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()+cleanupNote), nil
	}
	if err := d.exportSessionContextEnv(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID); err != nil {
		cleanupNote := d.issueResourceFailedStartRollbackNote(ctx, cmd.ProjectID, cmd.SessionID, resourceCtx)
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("export session context env: %v%s", err, cleanupNote)), nil
	}
	if err := d.exportIssueResourceSessionEnv(ctx, cmd.ProjectID, resourceCtx); err != nil {
		cleanupNote := d.issueResourceFailedStartRollbackNote(ctx, cmd.ProjectID, cmd.SessionID, resourceCtx)
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("export issue resource env: %v%s", err, cleanupNote)), nil
	}
	sessionInitMarker := sessionInitReadyMarker{}
	if cmd.StartWork {
		var markerErr error
		sessionInitMarker, markerErr = d.prepareSessionInitReadyMarker(cmd.ProjectID, worktree.Path, cmd.IssueID, cmd.SessionID)
		if markerErr != nil {
			cleanupNote := d.issueResourceFailedStartRollbackNote(ctx, cmd.ProjectID, cmd.SessionID, resourceCtx)
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("prepare session init marker: %v%s", markerErr, cleanupNote)), nil
		}
	}
	if cmd.StartWork {
		d.startSessionSideEffectCommands(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID, worktree.Path)
		initialPrompt := strings.TrimSpace(cmd.Prompt)
		if initialPrompt == "" {
			parentIssueID := ""
			if task.ParentID != nil {
				parentIssueID = strings.TrimSpace(task.ParentID.String())
			}
			initialPrompt = buildStartWorkPrompt(cmd.IssueID, task.Type.String(), task.Title, parentIssueID != "", parentIssueID)
		}
		launchCommand := d.buildSessionLaunchCommandWithInitReadyPath(cmd.ProjectID, cmd.IssueID, cmd.SessionID, cmd.Yolo, cmd.ImagePaths, initialPrompt, sessionInitMarker.RelativePath)
		if len(d.runtimeConfigForProject(cmd.ProjectID).SessionInitCommands) > 0 {
			reportSessionStartProgress(ctx, "init_commands", "launch sent; configured init commands likely running before agent hooks", 90)
		} else {
			reportSessionStartProgress(ctx, "agent_launch", "launch sent; waiting for agent activity", 90)
		}
		if err := d.tmux.SendKeys(ctx, cmd.SessionID, launchCommand); err != nil {
			cleanupNote := d.issueResourceFailedStartRollbackNote(ctx, cmd.ProjectID, cmd.SessionID, resourceCtx)
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()+cleanupNote), nil
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("daemon session start launch command sent",
				"project_id", cmd.ProjectID,
				"issue_id", cmd.IssueID,
				"session_id", cmd.SessionID,
				"prompt_bytes", len(initialPrompt),
			)
		}
	} else {
		reportSessionStartProgress(ctx, "tmux_launch", "tmux session created without agent launch", 90)
	}
	initialActivity, initialActivitySource := initialSessionStartActivity(cmd.StartWork)
	if err := d.applySessionLifecycleTransitionWithActivity(
		ctx,
		req,
		cmd.ProjectID,
		cmd.SessionID,
		cmd.IssueID,
		daemonhandlers.CommandSessionStart,
		initialActivity,
		initialActivitySource,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session start transition: %v", err)), nil
	}
	if cmd.StartWork && sessionInitMarker.AbsolutePath != "" {
		if err := d.waitForSessionInitReady(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID, sessionInitMarker); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
	}
	if updateErr := issueClient.Update(ctx, cmd.IssueID, domain.StatusInProgress); updateErr != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("failed to update issue status to in_progress after session start",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"error", updateErr,
		)
	}

	worktreeLine := fmt.Sprintf("Worktree created: %s", worktree.Path)
	if reusedWorktree {
		worktreeLine = fmt.Sprintf("Worktree reused: %s", worktree.Path)
	}
	output := strings.Join([]string{
		fmt.Sprintf("Starting session for: %s - %s", task.ID, task.Title),
		fmt.Sprintf("Creating worktree from branch: %s", baseBranch),
		func() string {
			if strings.TrimSpace(baseBranchAncestorIssueID) == "" {
				return ""
			}
			return fmt.Sprintf("Base branch source ancestor: %s", baseBranchAncestorIssueID)
		}(),
		worktreeLine,
		worktreeSetupWarning,
		issueResourceStartOutput(resourcePrep),
		fmt.Sprintf("Creating tmux session: %s", cmd.SessionID),
		func() string {
			if cmd.StartWork {
				return "Launching AI session in tmux"
			}
			return "Skipping AI launch (tmux session only)"
		}(),
		sessionInitReadyOutput(sessionInitMarker),
		"",
		"✓ Session started successfully",
		fmt.Sprintf("  To attach: az attach %s", cmd.IssueID),
		fmt.Sprintf("  Or run:    tmux attach-session -t %s", cmd.SessionID),
		"",
	}, "\n")
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session start completed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"worktree", worktree.Path,
			"reused_worktree", reusedWorktree,
		)
	}
	return d.commandOutput(req, output), nil
}

func (d *Daemon) resolveSessionStartBaseBranch(
	ctx context.Context,
	projectID string,
	requestedBaseBranch string,
	issueClient *issues.Client,
	worktreeManager *git.WorktreeManager,
	task domain.Task,
) (baseBranch string, ancestorIssueID string) {
	baseBranch = strings.TrimSpace(requestedBaseBranch)
	if baseBranch == "" {
		baseBranch = strings.TrimSpace(d.baseBranchForProject(projectID))
	}
	if issueClient == nil || worktreeManager == nil || task.ParentID == nil {
		return baseBranch, ""
	}

	taskByIssue := map[string]domain.Task{
		strings.TrimSpace(task.ID.String()): task,
	}
	worktreesByIssue := map[string]domain.IssueWorktreeRef{}
	nextParentID := strings.TrimSpace(task.ParentID.String())
	visited := map[string]struct{}{}
	for nextParentID != "" {
		if _, seen := visited[nextParentID]; seen {
			break
		}
		visited[nextParentID] = struct{}{}

		if parentWorktree, err := worktreeManager.Get(ctx, nextParentID); err == nil {
			worktreesByIssue[nextParentID] = domain.IssueWorktreeRef{
				Branch: strings.TrimSpace(parentWorktree.Branch),
				Path:   strings.TrimSpace(parentWorktree.Path),
			}
		}

		parentTask, err := issueClient.GetWithRuntime(ctx, projectID, nextParentID)
		if err != nil {
			break
		}
		taskByIssue[nextParentID] = parentTask

		nextParentID = ""
		if parentTask.ParentID != nil {
			nextParentID = strings.TrimSpace(parentTask.ParentID.String())
		}
	}

	if target, ok := domain.ClosestAncestorWithWorktree(task.ID.String(), taskByIssue, worktreesByIssue); ok {
		return target.Branch, target.IssueID
	}

	return baseBranch, ""
}

func resolveSessionIssue(tasks []domain.Task, requestedIssueID string) (domain.Task, bool) {
	requestedKey := sessionKey(requestedIssueID)
	if requestedKey != "" {
		for _, task := range tasks {
			if sessionKey(task.ID.String()) == requestedKey {
				return task, true
			}
		}
	}
	return domain.Task{}, false
}

func (d *Daemon) handleSessionAttach(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session attach requested",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	attachTargetSource := d.sourceForSessionInvariant(sessionInvariantSessionAttachTarget)
	exists, err := d.sessionExistsForInvariant(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID, attachTargetSource)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if !exists {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found: %s (use 'az start %s' to create)", cmd.IssueID, cmd.IssueID)), nil
	}
	if err := d.applySessionLifecycleTransition(
		ctx,
		req,
		cmd.ProjectID,
		cmd.SessionID,
		cmd.IssueID,
		daemonhandlers.CommandSessionAttach,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session attach transition: %v", err)), nil
	}
	output := strings.Join([]string{
		fmt.Sprintf("Attaching to session: %s", cmd.SessionID),
		"(Press Ctrl+B then D to detach)",
		"",
	}, "\n")
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session attach completed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	return d.commandOutput(req, output), nil
}

func (d *Daemon) handleSessionPause(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session pause requested",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	exists, err := d.sessionLifecycleTargetExists(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if !exists {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found: %s (use 'az start %s' to create)", cmd.IssueID, cmd.IssueID)), nil
	}
	if !d.sessionLifecycleTransitionNeeded(cmd.ProjectID, cmd.SessionID, cmd.IssueID, daemonstate.SessionStatePaused) {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("daemon session pause unchanged",
				"project_id", cmd.ProjectID,
				"issue_id", cmd.IssueID,
				"session_id", cmd.SessionID,
			)
		}
		return d.commandOutput(req, fmt.Sprintf("Paused session: %s\n", cmd.IssueID)), nil
	}
	if err := d.applySessionLifecycleTransition(
		ctx,
		req,
		cmd.ProjectID,
		cmd.SessionID,
		cmd.IssueID,
		daemonhandlers.CommandSessionPause,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session pause transition: %v", err)), nil
	}
	d.refreshRuntimeForIssueMutationAsync(cmd.ProjectID, cmd.IssueID, daemonhandlers.CommandSessionPause)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session pause completed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	return d.commandOutput(req, fmt.Sprintf("Paused session: %s\n", cmd.IssueID)), nil
}

func (d *Daemon) handleSessionResume(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session resume requested",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	exists, err := d.sessionLifecycleTargetExists(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if !exists {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found: %s (use 'az start %s' to create)", cmd.IssueID, cmd.IssueID)), nil
	}
	if !d.sessionLifecycleTransitionNeeded(cmd.ProjectID, cmd.SessionID, cmd.IssueID, daemonstate.SessionStateAttached) {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("daemon session resume unchanged",
				"project_id", cmd.ProjectID,
				"issue_id", cmd.IssueID,
				"session_id", cmd.SessionID,
			)
		}
		return d.commandOutput(req, fmt.Sprintf("Resumed session: %s\n", cmd.IssueID)), nil
	}
	if err := d.applySessionLifecycleTransition(
		ctx,
		req,
		cmd.ProjectID,
		cmd.SessionID,
		cmd.IssueID,
		daemonhandlers.CommandSessionResume,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session resume transition: %v", err)), nil
	}
	d.refreshRuntimeForIssueMutationAsync(cmd.ProjectID, cmd.IssueID, daemonhandlers.CommandSessionResume)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session resume completed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	return d.commandOutput(req, fmt.Sprintf("Resumed session: %s\n", cmd.IssueID)), nil
}

func (d *Daemon) handleSessionStop(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if d.sessionLongRunning != nil {
		return d.sessionLongRunning.Execute(ctx, req, req.Command, func(execCtx context.Context) (protocol.ResponseEnvelope, error) {
			return d.handleSessionStopDirect(execCtx, req)
		})
	}
	return d.handleSessionStopDirect(ctx, req)
}

func (d *Daemon) handleSessionStopDirect(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	return d.handleSessionStopDirectWithOptions(ctx, req, sessionStopOptions{})
}

type sessionStopOptions struct {
	skipIssueResourceCleanup bool
}

func (d *Daemon) handleSessionStopDirectWithOptions(ctx context.Context, req protocol.RequestEnvelope, opts sessionStopOptions) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session stop requested",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	resourceCleanup := issueResourceLifecycleResult{}
	worktreePath, branch := d.issueWorktreeContext(ctx, cmd.ProjectID, cmd.IssueID)
	if !opts.skipIssueResourceCleanup && len(d.runtimeConfigForProject(cmd.ProjectID).IssueResources.CleanupCommands) > 0 {
		resourceCtx := d.issueResourceLifecycleContext(cmd.ProjectID, cmd.IssueID, cmd.SessionID, worktreePath, branch)
		var cleanupErr error
		resourceCleanup, cleanupErr = d.runIssueResourceCleanupCommands(ctx, cmd.ProjectID, resourceCtx)
		if cleanupErr != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("issue resource cleanup failed for %s: %v", cmd.IssueID, cleanupErr)), nil
		}
	}
	// Write-through stopped projection immediately so cache-first task/session
	// reads do not resurrect a just-stopped session while tmux/process stop
	// work is still in flight.
	clearStopPending := d.markSessionStopPending(cmd.ProjectID, cmd.IssueID)
	defer clearStopPending()
	if err := d.writeSessionStopProjection(cmd.ProjectID, cmd.SessionID, cmd.IssueID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session stop intent: %v", err)), nil
	}
	if err := d.applySessionLifecycleTransition(
		ctx,
		req,
		cmd.ProjectID,
		cmd.SessionID,
		cmd.IssueID,
		daemonhandlers.CommandSessionStop,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session stop transition: %v", err)), nil
	}
	if err := ctx.Err(); err != nil {
		return d.mutationFreshnessErrorResponse(req, err), nil
	}
	sessionNamesToKill, err := d.liveTmuxSessionNamesForIssue(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	for _, sessionName := range sessionNamesToKill {
		if strings.EqualFold(strings.TrimSpace(sessionName), strings.TrimSpace(cmd.SessionID)) {
			continue
		}
		if err := d.writeSessionStopProjection(cmd.ProjectID, sessionName, cmd.IssueID); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record live session stop intent: %v", err)), nil
		}
	}
	exists := len(sessionNamesToKill) > 0
	if exists {
		for _, sessionName := range sessionNamesToKill {
			if err := d.tmux.KillSession(ctx, sessionName); err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
			}
		}
	}
	if err := d.refreshStoppedSessionRuntimeState(ctx, cmd.ProjectID, cmd.IssueID, append([]string{cmd.SessionID}, sessionNamesToKill...)); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("daemon session stop post-kill issue refresh failed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"error", err,
		)
	}
	outputLines := []string{
		fmt.Sprintf("Killing session: %s", strings.Join(sessionNamesToKill, ", ")),
		fmt.Sprintf("✓ Session killed: %s", strings.Join(sessionNamesToKill, ", ")),
	}
	if !exists {
		outputLines[0] = fmt.Sprintf("Session not found in tmux: %s", cmd.IssueID)
		outputLines[1] = fmt.Sprintf("✓ Session marked stopped: %s", cmd.IssueID)
	}
	if line := issueResourceCleanupOutput(resourceCleanup); line != "" {
		outputLines = append([]string{line}, outputLines...)
	}
	output := strings.Join(append(outputLines,
		"  Note: Worktree is preserved. Use 'git worktree remove' to clean up.",
		"",
	), "\n")
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session stop completed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"tmux_session_existed", exists,
		)
	}
	return d.commandOutput(req, output), nil
}

func (d *Daemon) liveTmuxSessionNamesForIssue(ctx context.Context, projectID, issueID, canonicalSessionID string) ([]string, error) {
	if d == nil || d.tmux == nil {
		return []string{}, nil
	}
	typedIssueID, issueErr := naming.ParseIssueID(strings.TrimSpace(issueID))
	if issueErr != nil {
		return []string{}, nil
	}
	projectID = d.canonicalProjectID(projectID)
	namingScope := d.sessionNamingScope(projectID)
	canonicalSessionID = strings.TrimSpace(canonicalSessionID)

	liveSessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	liveSet := make(map[string]struct{}, len(liveSessions))
	names := make(map[string]struct{}, len(liveSessions))
	for _, sessionName := range liveSessions {
		name := strings.TrimSpace(sessionName)
		if name == "" {
			continue
		}
		liveSet[name] = struct{}{}
		if canonicalSessionID != "" && strings.EqualFold(name, canonicalSessionID) {
			names[name] = struct{}{}
			continue
		}
		projectedIssueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope)
		if ok && naming.IssueIDsEqual(projectedIssueID, typedIssueID.String()) {
			names[name] = struct{}{}
			continue
		}
		if sessionNameHasIssueSuffix(name, typedIssueID.String()) {
			names[name] = struct{}{}
		}
	}

	if store := d.sessionRuntimeStateStoreIfConfigured(projectID); store != nil {
		snapshotSessions, err := store.ListSessionStates(ctx, projectID)
		if err != nil {
			return nil, err
		}
		for _, session := range snapshotSessions {
			sessionID := strings.TrimSpace(session.ID)
			if sessionID == "" {
				continue
			}
			if _, live := liveSet[sessionID]; !live {
				continue
			}
			projectedIssueID := sessionProjectionIssueID(session, namingScope)
			if naming.IssueIDsEqual(projectedIssueID, typedIssueID.String()) {
				names[sessionID] = struct{}{}
			}
		}
	}

	resolved := make([]string, 0, len(names))
	for name := range names {
		resolved = append(resolved, name)
	}
	return resolved, nil
}

func sessionNameHasIssueSuffix(sessionName, issueID string) bool {
	sessionName = strings.TrimSpace(sessionName)
	issueID = strings.TrimSpace(issueID)
	if sessionName == "" || issueID == "" {
		return false
	}
	if naming.IssueIDsEqual(sessionName, issueID) {
		return true
	}
	parts := strings.SplitN(sessionName, "-", 2)
	if len(parts) != 2 || len(parts[0]) != 2 {
		return false
	}
	for _, r := range parts[0] {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return naming.IssueIDsEqual(parts[1], issueID)
}

func (d *Daemon) handleSessionResolveConflict(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if d.sessionLongRunning != nil {
		return d.sessionLongRunning.Execute(ctx, req, req.Command, func(execCtx context.Context) (protocol.ResponseEnvelope, error) {
			return d.handleSessionResolveConflictDirect(execCtx, req)
		})
	}
	return d.handleSessionResolveConflictDirect(ctx, req)
}

func (d *Daemon) handleSessionResolveConflictDirect(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var body protocol.SessionResolveConflictRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	projectID := protocol.TrimProjectID(body.ProjectID.String())
	if projectID == "" {
		projectID = req.Meta.ProjectID.String()
	}
	projectID = d.canonicalProjectID(projectID)
	issueID, err := naming.ParseIssueID(strings.TrimSpace(body.IssueID.String()))
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: project_id/issue_id"), nil
	}
	issueIDString := issueID.String()
	conflictFiles := normalizeConflictFiles(body.ConflictFiles)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session conflict resolution requested",
			"project_id", projectID,
			"issue_id", issueIDString,
			"worktree", strings.TrimSpace(body.Worktree),
			"conflict_file_count", len(conflictFiles),
		)
	}

	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, issueIDString)
	if errors.Is(err, domain.ErrNotFound) {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("issue not found: %s", issueIDString)), nil
	}
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}

	worktreePath, worktreeBranch, reusedWorktree, err := d.ensureConflictWorktree(ctx, projectID, issueIDString, task.Title, body.Worktree)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, issueIDString, worktreePath, worktreeBranch)

	canonicalSessionID := naming.CanonicalSessionIDForIssue(d.sessionNamingScope(projectID), issueID).String()
	sessionName, reusedSession, err := d.ensureConflictSession(ctx, projectID, issueIDString, canonicalSessionID, worktreePath)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if err := d.recordConflictSessionAttached(ctx, req, projectID, canonicalSessionID, issueIDString, reusedSession); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record conflict session projection: %v", err)), nil
	}

	reusedWindow, err := d.tmux.EnsureWindow(ctx, sessionName, sessionConflictWindowName, worktreePath)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		prompt = buildConflictResolutionPrompt(issueIDString, conflictFiles)
	}
	launchCommand := d.buildSessionLaunchCommand(projectID, issueIDString, canonicalSessionID, body.Yolo, body.ImagePaths, prompt)
	if err := d.tmux.SendKeys(ctx, sessionName+":"+sessionConflictWindowName, launchCommand); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}

	resp := d.successResponse(req)
	out := protocol.SessionResolveConflictResponseBody{
		ProjectID:     naming.ProjectID(projectID),
		IssueID:       issueID,
		SessionID:     naming.SessionID(canonicalSessionID),
		Worktree:      worktreePath,
		WindowName:    sessionConflictWindowName,
		ConflictFiles: conflictFiles,
		ReusedSession: reusedSession,
		ReusedWindow:  reusedWindow,
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal response body: %v", err)), nil
	}
	resp.Body = encoded
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session conflict resolution launched",
			"project_id", projectID,
			"issue_id", issueIDString,
			"session_id", canonicalSessionID,
			"tmux_session", sessionName,
			"window", sessionConflictWindowName,
			"worktree", worktreePath,
			"reused_session", reusedSession,
			"reused_window", reusedWindow,
			"reused_worktree", reusedWorktree,
		)
	}
	return resp, nil
}

func (d *Daemon) ensureConflictWorktree(ctx context.Context, projectID, issueID, issueTitle, requestedWorktree string) (path, branch string, reused bool, err error) {
	if worktree := strings.TrimSpace(requestedWorktree); worktree != "" {
		return filepath.Clean(worktree), "", true, nil
	}
	worktreeManager := d.worktreeManagerForProject(projectID)
	if worktreeManager == nil {
		return "", "", false, errors.New("worktree manager unavailable")
	}
	if worktree, getErr := worktreeManager.Get(ctx, issueID); getErr == nil {
		if err := d.ensureSessionStartWorktreeClean(ctx, worktreeManager, issueID, worktree.Path); err != nil {
			return "", "", false, err
		}
		return worktree.Path, worktree.Branch, true, nil
	}

	baseBranch := d.baseBranchForProject(projectID)
	worktree, createErr := worktreeManager.CreateWithTitle(ctx, issueID, issueTitle, baseBranch)
	if createErr != nil {
		if recoveredWorktree, recoverErr := worktreeManager.Get(ctx, issueID); recoverErr == nil {
			if err := d.ensureSessionStartWorktreeClean(ctx, worktreeManager, issueID, recoveredWorktree.Path); err != nil {
				return "", "", false, err
			}
			return recoveredWorktree.Path, recoveredWorktree.Branch, true, nil
		}
		return "", "", false, createErr
	}
	if err := d.ensureSessionStartWorktreeClean(ctx, worktreeManager, issueID, worktree.Path); err != nil {
		cleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, worktreeManager, issueID, worktree.Path, false)
		return "", "", false, fmt.Errorf("%w%s", err, cleanupNote)
	}
	if err := d.runWorktreeInitCommands(ctx, projectID, worktree.Path); err != nil {
		cleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, worktreeManager, issueID, worktree.Path, false)
		return "", "", false, fmt.Errorf("worktree init failed for %s: %w%s", issueID, err, cleanupNote)
	}
	return worktree.Path, worktree.Branch, false, nil
}

func (d *Daemon) ensureSessionStartWorktreeClean(ctx context.Context, worktreeManager *git.WorktreeManager, issueID, worktreePath string) error {
	if worktreeManager == nil {
		return errors.New("worktree manager unavailable")
	}
	status, err := worktreeManager.CleanStatus(ctx, worktreePath)
	if err != nil {
		return fmt.Errorf("pre-init worktree clean check failed for issue %s at %s: %w. Inspect with 'git -C %s status --porcelain' and repair or remove the worktree before starting the session", issueID, worktreePath, err, worktreePath)
	}
	if status == nil || !status.HasChanges {
		return nil
	}

	reasons := make([]string, 0, 2)
	if len(status.DeletedTrackedFiles) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d tracked deletion(s) from git ls-files -d", len(status.DeletedTrackedFiles)))
	}
	if strings.TrimSpace(status.PorcelainStatus) != "" {
		reasons = append(reasons, "dirty git status --porcelain output")
	}
	detail := strings.Join(reasons, "; ")
	if detail == "" {
		detail = "dirty worktree"
	}

	return fmt.Errorf("refusing to start session for issue %s: worktree %s is not clean before init (%s). Inspect with 'git -C %s status --porcelain' and 'git -C %s ls-files -d', then repair the checkout or remove the worktree and retry", issueID, worktreePath, detail, worktreePath, worktreePath)
}

func (d *Daemon) cleanupNewWorktreeAfterInitFailure(ctx context.Context, worktreeManager *git.WorktreeManager, issueID, worktreePath string, reusedWorktree bool) string {
	if reusedWorktree || worktreeManager == nil {
		return ""
	}
	if _, err := worktreeManager.DeleteWithOptions(ctx, issueID, git.WorktreeDeleteOptions{
		Force:         true,
		BranchCleanup: git.WorktreeBranchCleanupRequired,
	}); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("failed to cleanup worktree after init failure",
				"issue_id", issueID,
				"worktree", worktreePath,
				"error", err,
			)
		}
		return fmt.Sprintf(" (cleanup failed for worktree %s: %v)", worktreePath, err)
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("cleaned up worktree after init failure",
			"issue_id", issueID,
			"worktree", worktreePath,
		)
	}
	return fmt.Sprintf(" (cleaned up worktree %s)", worktreePath)
}

func (d *Daemon) ensureConflictSession(ctx context.Context, projectID, issueID, canonicalSessionID, worktreePath string) (sessionName string, reused bool, err error) {
	names, err := d.tmuxSessionNamesForIssue(ctx, projectID, issueID, canonicalSessionID, daemonInvariantSourceTmux)
	if err != nil {
		return "", false, err
	}
	for _, name := range names {
		if strings.EqualFold(strings.TrimSpace(name), canonicalSessionID) {
			return strings.TrimSpace(name), true, nil
		}
	}
	if len(names) > 0 {
		return strings.TrimSpace(names[0]), true, nil
	}
	if d.tmux == nil {
		return "", false, errors.New("tmux service unavailable")
	}
	if err := d.tmux.NewSession(ctx, canonicalSessionID, worktreePath); err != nil {
		return "", false, err
	}
	return canonicalSessionID, false, nil
}

func (d *Daemon) recordConflictSessionAttached(ctx context.Context, req protocol.RequestEnvelope, projectID, sessionID, issueID string, reusedSession bool) error {
	if !reusedSession {
		return d.applySessionLifecycleTransition(ctx, req, projectID, sessionID, issueID, daemonhandlers.CommandSessionStart)
	}
	err := d.applySessionLifecycleTransition(ctx, req, projectID, sessionID, issueID, daemonhandlers.CommandSessionAttach)
	if err == nil || !errors.Is(err, daemonstate.ErrInvalidTransition) {
		return err
	}
	if d.sessionStore == nil {
		return errors.New("session store unavailable")
	}
	event, forceErr := d.sessionStore.ForceUpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached)
	if forceErr != nil {
		return forceErr
	}
	d.runtimeProjectionStateWriter().PersistSessionProjectionAndPublish(ctx, projectID, req.Meta, event.Session)
	return nil
}

func normalizeConflictFiles(files []string) []string {
	seen := make(map[string]struct{}, len(files))
	out := make([]string, 0, len(files))
	for _, file := range files {
		trimmed := strings.TrimSpace(file)
		if trimmed == "" {
			continue
		}
		cleaned := filepath.Clean(trimmed)
		if _, exists := seen[cleaned]; exists {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

func (d *Daemon) mutationFreshnessErrorResponse(req protocol.RequestEnvelope, err error) protocol.ResponseEnvelope {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return d.errorResponse(req, protocol.ErrorCodeTimeout, fmt.Sprintf("refresh runtime state before mutation: %v", err))
	}
	return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("refresh runtime state before mutation: %v", err))
}

func isRuntimeMutationFreshnessTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "wait runtime reconcile") &&
		strings.Contains(message, "deadline exceeded")
}

func (d *Daemon) writeSessionStopProjection(projectID, sessionID, issueID string) error {
	if d.sessionRuntimeStateStoreIfConfigured(projectID) == nil {
		return nil
	}

	projectID = d.canonicalProjectID(projectID)
	sessionID = strings.TrimSpace(sessionID)
	issueID = strings.TrimSpace(issueID)
	if sessionID == "" {
		return errors.New("missing session_id for stop projection")
	}
	if issueID == "" {
		if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(sessionID, d.sessionNamingScope(projectID)); ok {
			issueID = parsedIssueID
		}
	}
	if issueID == "" {
		issueID = sessionID
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session := daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateStopped,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     time.Now().UTC(),
	}
	if err := d.sessionRuntimeStateStoreIfConfigured(projectID).UpsertSessionState(ctx, projectID, session); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("write-through stop session runtime state failed",
				"project_id", projectID,
				"session_id", sessionID,
				"issue_id", issueID,
				"error", err,
			)
		}
		return err
	}
	return nil
}

func (d *Daemon) handleSessionMessage(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session message request"), nil
	}
	if strings.TrimSpace(cmd.Message) == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: message"), nil
	}
	if len([]rune(cmd.Message)) > 4000 {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "message must be 4000 characters or fewer"), nil
	}
	if d.tmux == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "tmux client unavailable"), nil
	}
	exists, err := d.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("check session exists: %v", err)), nil
	}
	if !exists {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found in tmux: %s", cmd.IssueID)), nil
	}
	if err := d.tmux.PasteTextAndSubmit(ctx, cmd.SessionID, cmd.Message); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("send session message: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session message sent",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"message_bytes", len(cmd.Message),
		)
	}
	return d.commandOutput(req, fmt.Sprintf("Sent message to session: %s\n", cmd.IssueID)), nil
}

func (d *Daemon) handleSessionStatus(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, false)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	tmuxSessions, err := d.listTmuxSessionsLiveForProject(ctx, cmd.ProjectID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	issueClient := d.issueClientForProject(cmd.ProjectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	tasks, err := issueClient.List(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	taskMap := make(map[naming.IssueID]domain.Task, len(tasks))
	for _, task := range tasks {
		taskMap[task.ID] = task
	}
	if cmd.IssueID != "" {
		matching := make([]string, 0, 1)
		namingScope := d.sessionNamingScope(cmd.ProjectID)
		for _, name := range tmuxSessions {
			if issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope); ok && naming.IssueIDsEqual(issueID, cmd.IssueID) {
				matching = append(matching, name)
			}
		}
		if len(matching) == 0 {
			if output, ok := d.pendingSessionStartStatusOutput(ctx, cmd.ProjectID, cmd.IssueID); ok {
				return d.commandOutput(req, output), nil
			}
			if output, ok := d.staleSessionRuntimeStatusOutput(ctx, cmd.ProjectID, cmd.IssueID); ok {
				return d.commandOutput(req, output), nil
			}
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("no active session found for issue: %s", cmd.IssueID)), nil
		}
		tmuxSessions = matching
	}
	if len(tmuxSessions) == 0 {
		if output, ok := d.pendingSessionStartStatusOutput(ctx, cmd.ProjectID, ""); ok {
			return d.commandOutput(req, output), nil
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("daemon session status snapshot", "project_id", cmd.ProjectID, "issue_id", cmd.IssueID, "active_sessions", 0)
		}
		return d.commandOutput(req, "No active sessions\n"), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Active Sessions (%d):\n\n", len(tmuxSessions))
	activityByIssueKey := d.sessionDisplayActivityByIssueKey(ctx, cmd.ProjectID)
	progressByIssue := d.sessionStartProgressByIssue(ctx, cmd.ProjectID)
	sessionStartProgress := make([]taskGraphSessionStartProgress, 0, len(progressByIssue))
	b.WriteString("ISSUE ID\tSTATUS\tACTIVITY\tTITLE\n")
	b.WriteString("-------\t------\t--------\t-----\n")
	for _, name := range tmuxSessions {
		issueIDRaw := name
		if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(name, d.sessionNamingScope(cmd.ProjectID)); ok {
			issueIDRaw = parsedIssueID
		}
		issueID, parseErr := naming.ParseIssueID(issueIDRaw)
		task, ok := taskMap[issueID]
		if parseErr != nil {
			ok = false
		}
		status := "unknown"
		title := "(not in issues)"
		if ok {
			status = string(task.Status)
			title = task.Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
		}
		activity := "unknown"
		activitySource := ""
		issueKey := sessionKey(issueIDRaw)
		if display, ok := activityByIssueKey[issueKey]; ok && display.Activity != "" {
			activity = display.Activity
			activitySource = display.Source
		}
		if progress, found := progressByIssue[issueKey]; found {
			sessionStartProgress = append(sessionStartProgress, progress)
		} else if len(d.runtimeConfigForProject(cmd.ProjectID).SessionInitCommands) > 0 && activity == "busy" && activitySource == "session" {
			sessionStartProgress = append(sessionStartProgress, taskGraphSessionStartProgress{
				IssueID:        issueIDRaw,
				OperationState: string(protocol.OperationStateRunning),
				Phase:          "init_commands",
				Message:        "configured init commands likely running before agent hooks",
				Percent:        90,
			})
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", issueIDRaw, status, activity, title)
	}
	if output, ok := sessionStartStatusProgressSection(sessionStartProgress); ok {
		b.WriteString("\n")
		b.WriteString(output)
	}
	b.WriteString("\nUse 'az attach <issue-id>' to attach to a session\n")
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session status snapshot", "project_id", cmd.ProjectID, "issue_id", cmd.IssueID, "active_sessions", len(tmuxSessions))
	}
	return d.commandOutput(req, b.String()), nil
}

func (d *Daemon) pendingSessionStartStatusOutput(ctx context.Context, projectID, issueID string) (string, bool) {
	section, ok := d.sessionStartStatusSection(ctx, projectID, issueID)
	if !ok {
		return "", false
	}
	return strings.TrimRight(section, "\n") + "\n", true
}

func (d *Daemon) sessionStartStatusSection(ctx context.Context, projectID, issueID string) (string, bool) {
	progressByIssue := d.sessionStartProgressByIssue(ctx, projectID)
	if len(progressByIssue) == 0 {
		return "", false
	}
	var progress []taskGraphSessionStartProgress
	if strings.TrimSpace(issueID) != "" {
		item, found := progressByIssue[sessionKey(issueID)]
		if !found {
			return "", false
		}
		progress = append(progress, item)
	} else {
		for _, item := range progressByIssue {
			progress = append(progress, item)
		}
		sort.SliceStable(progress, func(i, j int) bool {
			if progress[i].IssueID != progress[j].IssueID {
				return progress[i].IssueID < progress[j].IssueID
			}
			return progress[i].OperationID < progress[j].OperationID
		})
	}
	return sessionStartStatusProgressSection(progress)
}

func sessionStartStatusProgressSection(progress []taskGraphSessionStartProgress) (string, bool) {
	if len(progress) == 0 {
		return "", false
	}
	var b strings.Builder
	b.WriteString("Session start progress:\n")
	for _, item := range progress {
		fmt.Fprintf(&b, "- %s: %s\n", item.IssueID, daemonSessionStartProgressSummary(item))
	}
	return b.String(), true
}

func daemonSessionStartProgressSummary(progress taskGraphSessionStartProgress) string {
	parts := make([]string, 0, 6)
	if state := strings.TrimSpace(progress.OperationState); state != "" {
		parts = append(parts, "state="+state)
	}
	if phase := strings.TrimSpace(progress.Phase); phase != "" {
		parts = append(parts, "phase="+phase)
	}
	if operationID := strings.TrimSpace(progress.OperationID); operationID != "" {
		parts = append(parts, "operation="+operationID)
	}
	if progress.ElapsedMS > 0 {
		parts = append(parts, fmt.Sprintf("elapsed=%s", (time.Duration(progress.ElapsedMS)*time.Millisecond).Round(time.Second)))
	}
	if progress.Percent > 0 {
		parts = append(parts, fmt.Sprintf("progress=%d%%", progress.Percent))
	}
	if message := strings.TrimSpace(progress.Message); message != "" {
		parts = append(parts, message)
	}
	if len(parts) == 0 {
		return "pending"
	}
	return strings.Join(parts, " ")
}

func (d *Daemon) sessionHookActivityByIssueKey(ctx context.Context, projectID string) map[string]sessionHookActivity {
	projectID = d.canonicalProjectID(projectID)
	namingScope := d.sessionNamingScope(projectID)
	sessions := []daemonstate.Session{}
	if store := d.sessionRuntimeStateStoreIfConfigured(projectID); store != nil {
		cachedSessions, err := store.ListSessionStates(ctx, projectID)
		if err == nil {
			sessions = cachedSessions
		} else if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load runtime session activity failed", "project_id", projectID, "error", err)
		}
	}
	if len(sessions) == 0 && d.sessionStore != nil {
		snapshot := d.sessionStore.ReadSnapshot(projectID)
		sessions = make([]daemonstate.Session, 0, len(snapshot.Sessions))
		for _, session := range snapshot.Sessions {
			sessions = append(sessions, session)
		}
	}
	return sessionHookActivityByIssueKeyFromSessions(sessions, namingScope)
}

func sessionHookActivityByIssueKeyFromSessions(sessions []daemonstate.Session, namingScope string) map[string]sessionHookActivity {
	activityByKey := make(map[string]sessionHookActivity)
	for _, session := range sessions {
		if !isAgentScopedSessionID(session.ID) {
			continue
		}
		observed := daemonstate.NormalizeSessionState(session.ObservedState)
		if strings.TrimSpace(string(observed)) == "" {
			observed = daemonstate.NormalizeSessionState(session.State)
		}
		if observed == daemonstate.SessionStateStopped {
			continue
		}
		key := sessionKey(sessionProjectionIssueID(session, namingScope))
		if key == "" {
			continue
		}
		activity := activityByKey[key]
		activity.Total++
		if daemonstate.NormalizeSessionState(session.State) == daemonstate.SessionStatePaused {
			activity.Paused++
		} else {
			activity.Active++
		}
		activityByKey[key] = activity
	}
	return activityByKey
}

func sessionActivityLabel(activity sessionHookActivity) (string, string) {
	if activity.Total == 0 {
		return "unknown", "none"
	}
	if activity.Active > 0 {
		return "busy", "hooks"
	}
	return "idle", "hooks"
}

func initialSessionStartActivity(startWork bool) (string, string) {
	if startWork {
		return "busy", "session"
	}
	return "no-agent", "session"
}

func normalizeSessionActivity(activity string) string {
	activity = strings.ToLower(strings.TrimSpace(activity))
	switch activity {
	case string(domain.SessionBusy),
		string(domain.SessionIdle),
		string(domain.SessionWaiting),
		string(domain.SessionPaused),
		string(domain.SessionDone),
		string(domain.SessionError),
		"unknown",
		"no-agent",
		"starting",
		"working",
		"ended":
		return activity
	default:
		return ""
	}
}

func normalizeSessionActivitySource(source, fallback string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	if source != "" {
		return source
	}
	return strings.ToLower(strings.TrimSpace(fallback))
}

func explicitSessionActivity(session daemonstate.Session) (sessionDisplayActivity, bool) {
	if isAgentScopedSessionID(session.ID) {
		return sessionDisplayActivity{}, false
	}
	state := daemonstate.NormalizeSessionState(session.State)
	observed := daemonstate.NormalizeSessionState(session.ObservedState)
	if strings.TrimSpace(string(observed)) == "" {
		observed = state
	}
	if state == daemonstate.SessionStateStopped || observed == daemonstate.SessionStateStopped {
		return sessionDisplayActivity{}, false
	}
	activity := normalizeSessionActivity(session.Activity)
	if activity == "" {
		return sessionDisplayActivity{}, false
	}
	return sessionDisplayActivity{
		Activity: activity,
		Source:   normalizeSessionActivitySource(session.ActivitySource, "session"),
	}, true
}

func sessionDisplayActivityByIssueKeyFromSessions(sessions []daemonstate.Session, namingScope string) map[string]sessionDisplayActivity {
	out := make(map[string]sessionDisplayActivity)
	for key, session := range sessionProjectionAggregateByIssueKey(sessions, namingScope) {
		display, ok := explicitSessionActivity(session)
		if !ok {
			continue
		}
		if key == "" {
			continue
		}
		out[key] = display
	}
	for key, hookActivity := range sessionHookActivityByIssueKeyFromSessions(sessions, namingScope) {
		activity, source := sessionActivityLabel(hookActivity)
		if activity == "unknown" {
			continue
		}
		out[key] = sessionDisplayActivity{Activity: activity, Source: source}
	}
	return out
}

func (d *Daemon) sessionDisplayActivityByIssueKey(ctx context.Context, projectID string) map[string]sessionDisplayActivity {
	projectID = d.canonicalProjectID(projectID)
	namingScope := d.sessionNamingScope(projectID)
	sessions := []daemonstate.Session{}
	if store := d.sessionRuntimeStateStoreIfConfigured(projectID); store != nil {
		cachedSessions, err := store.ListSessionStates(ctx, projectID)
		if err == nil {
			sessions = cachedSessions
		} else if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load runtime session activity failed", "project_id", projectID, "error", err)
		}
	}
	if len(sessions) == 0 && d.sessionStore != nil {
		snapshot := d.sessionStore.ReadSnapshot(projectID)
		sessions = make([]daemonstate.Session, 0, len(snapshot.Sessions))
		for _, session := range snapshot.Sessions {
			sessions = append(sessions, session)
		}
	}
	return sessionDisplayActivityByIssueKeyFromSessions(sessions, namingScope)
}

func unknownActivityAdvice(issueID string) string {
	return fmt.Sprintf("activity unknown: inspect hooks with az ai status --target=auto; run az ai install --target=auto only if hooks are missing, outdated, or not installed; use sparse pane capture only if status/watch looks stale, failed, or contradictory for %s", issueID)
}

func sessionActivityLabelForDisplay(activity sessionHookActivity, session daemonstate.Session) (string, string) {
	label, source := sessionActivityLabel(activity)
	if label != "unknown" || source != "none" {
		return label, source
	}
	if sessionWithinActivityStartupGrace(session, timeNow()) {
		return "starting", "startup-grace"
	}
	return label, source
}

func sessionWithinActivityStartupGrace(session daemonstate.Session, now time.Time) bool {
	if sessionActivityStartupGrace <= 0 {
		return false
	}
	start := session.StartedAt
	if start == nil || start.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	age := now.Sub(start.UTC())
	return age >= 0 && age <= sessionActivityStartupGrace
}

func (d *Daemon) staleSessionRuntimeStatusOutput(ctx context.Context, projectID, issueID string) (string, bool) {
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return "", false
	}
	projectID = d.canonicalProjectID(projectID)
	rows, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return "", false
	}
	session, found := nonAgentSessionProjectionByIssue(rows, d.sessionNamingScope(projectID), issueID)
	if !found {
		return "", false
	}
	observed := daemonstate.NormalizeSessionState(session.ObservedState)
	if strings.TrimSpace(string(observed)) == "" {
		observed = daemonstate.NormalizeSessionState(session.State)
	}
	desired := daemonstate.NormalizeSessionState(session.State)
	if observed != daemonstate.SessionStateStopped || desired == daemonstate.SessionStateStopped || strings.TrimSpace(session.ID) == "" {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Stale runtime session for %s\n\n", issueID)
	fmt.Fprintf(&b, "Runtime metadata still has desired state %q for session %q, but no backing tmux session exists.\n", session.State, session.ID)
	fmt.Fprintf(&b, "Observed state: %s\n", observed)
	fmt.Fprintf(&b, "\nRepair:\n")
	fmt.Fprintf(&b, "- clear stale runtime: az orchestrate close-session --issue %s\n", issueID)
	fmt.Fprintf(&b, "- restart worker if needed: az orchestrate start --root <root> --issue %s --json\n", issueID)
	return b.String(), true
}

func (d *Daemon) listTmuxSessionsLiveForProject(ctx context.Context, projectID string) ([]string, error) {
	projectID = d.canonicalProjectID(projectID)
	if d == nil || d.tmux == nil {
		return []string{}, nil
	}
	allSessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	namingScope := d.sessionNamingScope(projectID)
	sessions := make([]string, 0, len(allSessions))
	for _, name := range allSessions {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := naming.ParseIssueIDFromSessionName(name, namingScope); !ok {
			continue
		}
		sessions = append(sessions, name)
	}
	return sessions, nil
}

func (d *Daemon) handleSessionRecover(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd struct {
		ProjectID string `json:"project_id"`
		SessionID string `json:"session_id,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	cmd.ProjectID = protocol.TrimProjectID(cmd.ProjectID)
	if cmd.ProjectID == "" {
		cmd.ProjectID = req.Meta.ProjectID.String()
	}
	cmd.ProjectID = d.canonicalProjectID(cmd.ProjectID)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session recover requested", "project_id", cmd.ProjectID, "target_issue_id", cmd.SessionID)
	}
	targetIssueID := strings.TrimSpace(cmd.SessionID)
	if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(targetIssueID, d.sessionNamingScope(cmd.ProjectID)); ok {
		targetIssueID = parsedIssueID
	}

	result, err := d.reconcileTmuxAndDaemonSessions(ctx, cmd.ProjectID, targetIssueID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session recover completed",
			"project_id", cmd.ProjectID,
			"target_issue_id", targetIssueID,
			"recreated_tmux_sessions", result.RecreatedTmuxSessions,
			"aligned_daemon_sessions", result.AlignedDaemonSessions,
		)
	}
	resp := d.successResponse(req)
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal response body: %v", err)), nil
	}
	resp.Body = body
	resp.Revision = d.currentRevision(cmd.ProjectID)
	return resp, nil
}

func (d *Daemon) upsertSessionAndPublish(projectID, sessionID, issueID string, state daemonstate.SessionState) error {
	if d.sessionStore == nil {
		return errors.New("session store unavailable")
	}
	event, err := d.sessionStore.UpsertSession(projectID, sessionID, issueID, state)
	if err != nil {
		return err
	}
	d.runtimeProjectionStateWriter().PersistSessionProjectionAndPublish(context.Background(), projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, event.Session)
	return nil
}

func (d *Daemon) reconcileTmuxAndDaemonSessions(ctx context.Context, projectID, sessionID string) (sessionRecoveryResult, error) {
	result := sessionRecoveryResult{}
	if d == nil || d.sessionStore == nil || d.tmux == nil {
		return result, nil
	}
	projectID = d.canonicalProjectID(projectID)
	worktreeManager := d.worktreeManagerForProject(projectID)
	if worktreeManager == nil {
		return result, nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session reconcile started", "project_id", projectID, "target_issue_id", sessionID)
	}
	validIssueKeys, issueValidationEnabled, err := d.reconcileIssueKeyIndex(ctx, projectID)
	if err != nil {
		return result, err
	}
	isValidIssueKey := func(issueKey string) bool {
		if !issueValidationEnabled {
			return true
		}
		_, ok := validIssueKeys[issueKey]
		return ok
	}
	pruneInvalidSessionProjection := func(session daemonstate.Session, issueID string) {
		runtimeStore := d.sessionRuntimeStateStoreIfConfigured(projectID)
		if runtimeStore == nil {
			return
		}
		sessionIDToDelete := strings.TrimSpace(session.ID)
		if sessionIDToDelete == "" {
			sessionIDToDelete = naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID)
		}
		if sessionIDToDelete == "" {
			return
		}
		if err := runtimeStore.DeleteSessionState(ctx, projectID, sessionIDToDelete); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session reconciliation failed to prune invalid desired session",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", sessionIDToDelete,
				"error", err,
			)
			return
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("session reconciliation pruned invalid desired session",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", sessionIDToDelete,
			)
		}
	}
	pruneMissingWorktreeSessionProjection := func(session daemonstate.Session, issueID string, cause error) {
		runtimeStore := d.sessionRuntimeStateStoreIfConfigured(projectID)
		if runtimeStore == nil {
			return
		}
		sessionIDToDelete := strings.TrimSpace(session.ID)
		if sessionIDToDelete == "" {
			sessionIDToDelete = naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID)
		}
		if sessionIDToDelete == "" {
			return
		}
		if err := runtimeStore.DeleteSessionState(ctx, projectID, sessionIDToDelete); err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("session reconciliation failed to prune missing-worktree desired session",
					"project_id", projectID,
					"issue_id", issueID,
					"session_id", sessionIDToDelete,
					"cause", cause,
					"error", err,
				)
			}
			return
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("session reconciliation pruned missing-worktree desired session",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", sessionIDToDelete,
				"cause", cause,
			)
		}
	}

	source := d.sourceForSessionInvariant(sessionInvariantSessionReconcile)
	tmuxSessions := []string{}
	if usesTmuxSource(source) {
		tmuxSessions, err = d.tmux.ListSessions(ctx)
		if err != nil {
			return result, err
		}
	}
	tmuxSet := make(map[string]struct{}, len(tmuxSessions))
	tmuxNameByIssueKey := make(map[string]string, len(tmuxSessions))
	targetIssueKey := sessionKey(sessionID)
	namingScope := d.sessionNamingScope(projectID)
	for _, name := range tmuxSessions {
		issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope)
		if !ok {
			continue
		}
		key := sessionKey(issueID)
		if key == "" {
			continue
		}
		if targetIssueKey != "" && key != targetIssueKey {
			continue
		}
		tmuxSet[key] = struct{}{}
		tmuxNameByIssueKey[key] = name
	}

	snapshotSessions := []daemonstate.Session{}
	if usesProjectionSource(source) {
		snapshotSessions, err = d.sessionSnapshotForReconcile(ctx, projectID)
		if err != nil {
			return result, err
		}
	}
	for _, session := range snapshotSessions {
		issueID := sessionProjectionIssueID(session, namingScope)
		issueKey := sessionKey(issueID)
		if targetIssueKey != "" && issueKey != targetIssueKey {
			continue
		}
		if issueKey == "" || !isValidIssueKey(issueKey) {
			pruneInvalidSessionProjection(session, issueID)
			continue
		}
		if d.isSessionStopPending(projectID, issueID) {
			continue
		}
		if session.State == daemonstate.SessionStateStopped {
			continue
		}
		if _, ok := tmuxSet[issueKey]; ok {
			continue
		}
		wt, getErr := worktreeManager.Get(ctx, issueID)
		if getErr != nil {
			if issueValidationEnabled && errors.Is(getErr, git.ErrWorktreeNotFound) {
				pruneMissingWorktreeSessionProjection(session, issueID, getErr)
				continue
			}
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("session reconciliation skipped worktree restore",
					"project_id", projectID,
					"issue_id", issueID,
					"error", getErr,
				)
			}
			continue
		}
		canonicalSessionID := naming.CanonicalSessionID(namingScope, issueID)
		if newErr := d.tmux.NewSession(ctx, canonicalSessionID, wt.Path); newErr != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("session reconciliation failed to recreate tmux session",
					"project_id", projectID,
					"issue_id", issueID,
					"session_id", canonicalSessionID,
					"worktree", wt.Path,
					"error", newErr,
				)
			}
			continue
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("daemon session reconcile recreated tmux session",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", canonicalSessionID,
				"worktree", wt.Path,
			)
		}
		if sendErr := d.tmux.SendKeys(ctx, canonicalSessionID, d.buildSessionLaunchCommand(projectID, issueID, canonicalSessionID, false, nil, "")); sendErr != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session reconciliation failed to seed launch command",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", canonicalSessionID,
				"error", sendErr,
			)
		}
		reattached := session
		reattached.ID = canonicalSessionID
		reattached.IssueID = issueID
		reattached.ObservedState = daemonstate.SessionStateAttached
		reattached.UpdatedAt = time.Now().UTC()
		if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, reattached); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("persist recreated session observation failed",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", canonicalSessionID,
				"error", err,
			)
		}
		tmuxSet[issueKey] = struct{}{}
		tmuxNameByIssueKey[issueKey] = canonicalSessionID
		result.RecreatedTmuxSessions++
	}

	snapshotByIssueKey := sessionProjectionForReconcileByIssueKey(snapshotSessions, namingScope)

	for issueKey := range tmuxSet {
		sessionIDInTmux := tmuxNameByIssueKey[issueKey]
		session, ok := snapshotByIssueKey[issueKey]
		if !isValidIssueKey(issueKey) {
			continue
		}
		issueID, parsed := naming.ParseIssueIDFromSessionName(sessionIDInTmux, namingScope)
		if !parsed {
			issueID = sessionIDInTmux
		}
		if d.isSessionStopPending(projectID, issueID) {
			continue
		}
		if !ok {
			if err := d.upsertSessionAndPublish(projectID, sessionIDInTmux, issueID, daemonstate.SessionStateStarting); err == nil {
				if err := d.upsertSessionAndPublish(projectID, sessionIDInTmux, issueID, daemonstate.SessionStateAttached); err == nil {
					result.AlignedDaemonSessions++
					if d.cfg.Logger != nil {
						d.cfg.Logger.Info("daemon session reconcile aligned daemon state",
							"project_id", projectID,
							"issue_id", issueID,
							"session_id", sessionIDInTmux,
							"state", string(daemonstate.SessionStateAttached),
						)
					}
				} else if d.cfg.Logger != nil {
					d.cfg.Logger.Warn("session reconciliation failed to mark session attached",
						"project_id", projectID,
						"issue_id", issueID,
						"session_id", sessionIDInTmux,
						"error", err,
					)
				}
			} else if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("session reconciliation failed to mark session starting",
					"project_id", projectID,
					"issue_id", issueID,
					"session_id", sessionIDInTmux,
					"error", err,
				)
			}
			continue
		}

		canonicalSessionID := session.ID
		if canonicalSessionID == "" {
			canonicalSessionID = naming.CanonicalSessionID(namingScope, issueID)
		}

		if session.State == daemonstate.SessionStateStopped {
			if err := d.tmux.KillSession(ctx, sessionIDInTmux); err != nil {
				if d.cfg.Logger != nil {
					d.cfg.Logger.Warn("session reconciliation failed to kill stopped session",
						"project_id", projectID,
						"issue_id", issueID,
						"session_id", sessionIDInTmux,
						"error", err,
					)
				}
				continue
			}
			stopped := session
			stopped.ObservedState = daemonstate.SessionStateStopped
			stopped.UpdatedAt = time.Now().UTC()
			if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, stopped); err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Debug("persist stopped session observation failed",
					"project_id", projectID,
					"issue_id", issueID,
					"session_id", sessionIDInTmux,
					"error", err,
				)
			}
			continue
		}

		d.ensureSessionWorktreeProjection(ctx, projectID, issueID)

		switch session.State {
		case daemonstate.SessionStateStarting:
			if err := d.upsertSessionAndPublish(projectID, canonicalSessionID, issueID, daemonstate.SessionStateAttached); err == nil {
				result.AlignedDaemonSessions++
				if d.cfg.Logger != nil {
					d.cfg.Logger.Info("daemon session reconcile aligned daemon state",
						"project_id", projectID,
						"issue_id", issueID,
						"session_id", canonicalSessionID,
						"state", string(daemonstate.SessionStateAttached),
					)
				}
			} else if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("session reconciliation failed to transition session to attached",
					"project_id", projectID,
					"issue_id", issueID,
					"session_id", canonicalSessionID,
					"error", err,
				)
			}
		}
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session reconcile completed",
			"project_id", projectID,
			"target_issue_id", sessionID,
			"recreated_tmux_sessions", result.RecreatedTmuxSessions,
			"aligned_daemon_sessions", result.AlignedDaemonSessions,
		)
	}

	return result, nil
}

func (d *Daemon) reconcileIssueKeyIndex(ctx context.Context, projectID string) (map[string]struct{}, bool, error) {
	if d == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID)) == "" {
		return nil, false, nil
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, false, nil
	}
	tasks, err := issueClient.List(ctx)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session reconciliation issue validation unavailable",
				"project_id", projectID,
				"error", err,
			)
		}
		return nil, false, nil
	}
	index := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		key := sessionKey(task.ID.String())
		if key == "" {
			continue
		}
		index[key] = struct{}{}
	}
	return index, true, nil
}

func (d *Daemon) sessionSnapshotForReconcile(ctx context.Context, projectID string) ([]daemonstate.Session, error) {
	if d == nil {
		return nil, nil
	}
	projectID = d.canonicalProjectID(projectID)
	if d.sessionRuntimeStateStoreIfConfigured(projectID) == nil {
		return []daemonstate.Session{}, nil
	}
	return d.sessionProjectionSnapshot(ctx, projectID)
}

func (d *Daemon) ensureSessionWorktreeProjection(ctx context.Context, projectID, issueID string) {
	if d == nil || d.worktreeRuntimeStateStore(projectID) == nil {
		return
	}
	projectID = protocol.NormalizeProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return
	}
	if _, found, err := d.worktreeRuntimeStateStore(projectID).GetWorktreeStateByIssueID(ctx, projectID, issueID); err == nil && found {
		return
	}

	repoDir := strings.TrimSpace(d.resolveRepoDirForProjectLocked(projectID))
	if repoDir == "" {
		return
	}
	worktreePath := filepath.Join(filepath.Dir(repoDir), fmt.Sprintf("%s-%s", filepath.Base(repoDir), issueID))
	info, err := os.Stat(worktreePath)
	if err != nil || !info.IsDir() {
		return
	}

	branch := ""
	if d.git != nil {
		if currentBranch, branchErr := d.git.CurrentBranch(ctx, worktreePath); branchErr == nil {
			branch = strings.TrimSpace(currentBranch)
		}
	}
	rev := d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, issueID, worktreePath, branch)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info(
			"backfilled session worktree projection",
			"project_id", projectID,
			"issue_id", issueID,
			"worktree", worktreePath,
			"branch", branch,
			"revision", rev,
		)
	}
}

func (d *Daemon) enrichTasksWithSessionState(ctx context.Context, projectID string, tasks []domain.Task) []domain.Task {
	if len(tasks) == 0 || d.sessionStore == nil {
		return tasks
	}

	namingScope := d.sessionNamingScope(projectID)
	snapshot := d.sessionStore.ReadSnapshot(projectID)
	snapshotSessions := make([]daemonstate.Session, 0, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		snapshotSessions = append(snapshotSessions, session)
	}
	snapshotByKey := sessionProjectionAggregateByIssueKey(snapshotSessions, namingScope)
	var projectionSessions []daemonstate.Session
	projectionByKey := map[string]daemonstate.Session{}
	if d.sessionRuntimeStateStoreIfConfigured(projectID) != nil {
		cachedSessions, err := d.sessionRuntimeStateStoreIfConfigured(projectID).ListSessionStates(ctx, projectID)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("failed to load cached session runtime state while enriching tasks", "project_id", projectID, "error", err)
			}
		} else {
			projectionSessions = cachedSessions
			projectionByKey = sessionProjectionAggregateByIssueKey(projectionSessions, namingScope)
		}
	}
	sessionByKey := sessionProjectionForTaskDisplayByIssueKey(snapshotByKey, projectionByKey)
	activeIssueKeys := activeSessionIssueKeysFromProjection(projectionSessions, namingScope)
	countsByKey := sessionProjectionCountsByIssueKey(projectionSessions, namingScope)
	activityByKey := sessionDisplayActivityByIssueKeyFromSessions(projectionSessions, namingScope)
	if len(activeIssueKeys) == 0 {
		activeIssueKeys = activeSessionIssueKeysFromProjection(snapshotSessions, namingScope)
		countsByKey = sessionProjectionCountsByIssueKey(snapshotSessions, namingScope)
		activityByKey = sessionDisplayActivityByIssueKeyFromSessions(snapshotSessions, namingScope)
	}

	for i := range tasks {
		taskID := tasks[i].ID
		taskKey := sessionKey(taskID.String())
		if _, ok := activeIssueKeys[taskKey]; !ok {
			continue
		}

		state := domain.SessionBusy
		var startedAt *time.Time
		worktree := ""
		if tasks[i].Session != nil {
			worktree = strings.TrimSpace(tasks[i].Session.Worktree)
		}
		snapshotSession, snapshotOK := snapshotByKey[taskKey]
		projectionSession, projectionOK := projectionByKey[taskKey]
		session, ok := sessionByKey[taskKey]
		if ok {
			startedAt = sessionProjectionStartedAtForTaskDisplay(snapshotSession, snapshotOK, projectionSession, projectionOK)
			switch session.State {
			case daemonstate.SessionStatePaused:
				state = domain.SessionPaused
			case daemonstate.SessionStateStopped:
				continue
			default:
				state = domain.SessionBusy
			}
		}
		activity, activitySource := sessionActivityLabelForDisplay(sessionHookActivity{}, session)
		if display := activityByKey[taskKey]; display.Activity != "" {
			activity = display.Activity
			activitySource = display.Source
		}
		tasks[i].Session = &domain.Session{
			IssueID:           naming.IssueID(taskID),
			State:             state,
			Activity:          activity,
			ActivitySource:    activitySource,
			TotalCount:        countsByKey[taskKey].Total,
			ActiveCount:       countsByKey[taskKey].Active,
			PausedCount:       countsByKey[taskKey].Paused,
			TmuxAttached:      countsByKey[taskKey].TmuxAttachedCount > 0,
			TmuxAttachedCount: countsByKey[taskKey].TmuxAttachedCount,
			StartedAt:         startedAt,
			Worktree:          worktree,
		}
	}

	return tasks
}

func activeSessionIssueKeysFromProjection(sessions []daemonstate.Session, namingScope string) map[string]struct{} {
	active := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		if isAgentScopedSessionID(session.ID) {
			continue
		}
		observed := session.ObservedState
		if strings.TrimSpace(string(observed)) == "" {
			observed = session.State
		}
		if observed == daemonstate.SessionStateStopped {
			continue
		}
		key := sessionKey(sessionProjectionIssueID(session, namingScope))
		if key == "" {
			continue
		}
		active[key] = struct{}{}
	}
	return active
}

func (d *Daemon) sessionProjectionCountsForIssue(ctx context.Context, projectID, issueID string) sessionProjectionCounts {
	if d == nil || strings.TrimSpace(issueID) == "" {
		return sessionProjectionCounts{}
	}
	projectID = d.canonicalProjectID(projectID)
	namingScope := d.sessionNamingScope(projectID)
	if store := d.sessionRuntimeStateStoreIfConfigured(projectID); store != nil {
		sessions, err := store.ListSessionStates(ctx, projectID)
		if err == nil {
			return sessionProjectionCountsByIssueKey(sessions, namingScope)[sessionKey(issueID)]
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load runtime session counts failed", "project_id", projectID, "issue_id", issueID, "error", err)
		}
	}
	if d.sessionStore == nil {
		return sessionProjectionCounts{}
	}
	snapshot := d.sessionStore.ReadSnapshot(projectID)
	sessions := make([]daemonstate.Session, 0, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		sessions = append(sessions, session)
	}
	return sessionProjectionCountsByIssueKey(sessions, namingScope)[sessionKey(issueID)]
}

func (d *Daemon) listTmuxSessionsCacheFirst(ctx context.Context, projectID string) ([]string, error) {
	projectID = d.canonicalProjectID(projectID)

	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return []string{}, nil
	}
	cachedSessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return nil, err
	}
	cachedActive := d.activeSessionIDsFromProjection(projectID, cachedSessions)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Debug("using projection-backed session runtime state",
			"project_id", projectID,
			"cached_sessions", len(cachedActive),
		)
	}
	return cachedActive, nil
}

func (d *Daemon) activeSessionIDsFromProjection(projectID string, sessions []daemonstate.Session) []string {
	active := make([]string, 0, len(sessions))
	for _, session := range sessions {
		observed := session.ObservedState
		if strings.TrimSpace(string(observed)) == "" {
			observed = session.State
		}
		if observed == daemonstate.SessionStateStopped {
			continue
		}
		if d.isSessionStopPending(projectID, session.IssueID) {
			continue
		}
		if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(session.ID, d.sessionNamingScope(projectID)); ok {
			if d.isSessionStopPending(projectID, parsedIssueID) {
				continue
			}
		}
		if strings.TrimSpace(session.ID) == "" {
			continue
		}
		active = append(active, session.ID)
	}
	return active
}

func (d *Daemon) listProjectionSessionsOnly(ctx context.Context, projectID string) ([]string, error) {
	projectID = d.canonicalProjectID(projectID)
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return []string{}, nil
	}
	cachedSessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return d.activeSessionIDsFromProjection(projectID, cachedSessions), nil
}

func (d *Daemon) refreshSessionRuntimeState(ctx context.Context, projectID string) error {
	if d == nil || d.tmux == nil || d.sessionRuntimeStateStoreIfConfigured(projectID) == nil || d.sessionStore == nil {
		return nil
	}
	tmuxSessions, err := d.tmux.ListSessionInfos(ctx)
	if err != nil {
		return err
	}
	return d.persistTmuxSessionRuntimeState(ctx, projectID, tmuxSessions)
}

func (d *Daemon) refreshExistingSessionRuntimeState(ctx context.Context, projectID string) error {
	if d == nil || d.tmux == nil || d.sessionRuntimeStateStoreIfConfigured(projectID) == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	existingSessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return err
	}
	if len(existingSessions) == 0 {
		return nil
	}
	tmuxSessions, err := d.tmux.ListSessionInfos(ctx)
	if err != nil {
		return err
	}
	liveByID := make(map[string]tmux.SessionInfo, len(tmuxSessions))
	liveIssueKeys := make(map[string]struct{}, len(tmuxSessions))
	namingScope := d.sessionNamingScope(projectID)
	for _, info := range tmuxSessions {
		name := strings.TrimSpace(info.Name)
		if name != "" {
			liveByID[name] = info
		}
		if issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope); ok {
			liveIssueKeys[sessionKey(issueID)] = struct{}{}
		}
	}
	writer := d.runtimeProjectionStateWriter()
	for _, session := range existingSessions {
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			continue
		}
		if info, live := liveByID[sessionID]; live {
			session.ObservedState = daemonstate.SessionStateRunning
			session.TmuxAttachedCount = info.AttachedCount
			if (session.StartedAt == nil || session.StartedAt.IsZero()) && info.CreatedAt != nil && !info.CreatedAt.IsZero() {
				started := info.CreatedAt.UTC()
				session.StartedAt = &started
			}
		} else if isAgentScopedSessionID(sessionID) {
			issueKey := sessionKey(sessionProjectionIssueID(session, namingScope))
			if _, parentLive := liveIssueKeys[issueKey]; parentLive {
				session.ObservedState = daemonstate.NormalizeSessionState(session.State)
			} else {
				session.ObservedState = daemonstate.SessionStateStopped
			}
			session.TmuxAttachedCount = 0
		} else {
			session.ObservedState = daemonstate.SessionStateStopped
			session.TmuxAttachedCount = 0
		}
		session.UpdatedAt = time.Now().UTC()
		if writer != nil {
			if err := writer.PersistSessionProjection(ctx, projectID, session); err != nil {
				return err
			}
			continue
		}
		if err := store.UpsertSessionState(ctx, projectID, session); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) refreshIssueSessionRuntimeState(ctx context.Context, projectID string, issueIDs []string) error {
	if d == nil || d.tmux == nil || d.sessionRuntimeStateStoreIfConfigured(projectID) == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)
	issueSet := make(map[string]struct{}, len(issueIDs))
	for _, issueID := range issueIDs {
		issueID = strings.TrimSpace(issueID)
		if issueID != "" {
			issueSet[sessionKey(issueID)] = struct{}{}
		}
	}
	if len(issueSet) == 0 {
		return nil
	}
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	existingSessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return err
	}
	targetSessions := make([]daemonstate.Session, 0, len(issueSet))
	namingScope := d.sessionNamingScope(projectID)
	for _, session := range existingSessions {
		if _, ok := issueSet[sessionKey(sessionProjectionIssueID(session, namingScope))]; ok {
			targetSessions = append(targetSessions, session)
		}
	}
	if len(targetSessions) == 0 {
		return nil
	}
	tmuxSessions, err := d.tmux.ListSessionInfos(ctx)
	if err != nil {
		return err
	}
	liveByID := make(map[string]tmux.SessionInfo, len(tmuxSessions))
	liveIssueKeys := make(map[string]struct{}, len(tmuxSessions))
	for _, info := range tmuxSessions {
		name := strings.TrimSpace(info.Name)
		if name != "" {
			liveByID[name] = info
		}
		if issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope); ok {
			liveIssueKeys[sessionKey(issueID)] = struct{}{}
		}
	}
	writer := d.runtimeProjectionStateWriter()
	for _, session := range targetSessions {
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			continue
		}
		if info, live := liveByID[sessionID]; live {
			session.ObservedState = daemonstate.SessionStateRunning
			session.TmuxAttachedCount = info.AttachedCount
			if (session.StartedAt == nil || session.StartedAt.IsZero()) && info.CreatedAt != nil && !info.CreatedAt.IsZero() {
				started := info.CreatedAt.UTC()
				session.StartedAt = &started
			}
		} else if isAgentScopedSessionID(sessionID) {
			issueKey := sessionKey(sessionProjectionIssueID(session, namingScope))
			if _, parentLive := liveIssueKeys[issueKey]; parentLive {
				session.ObservedState = daemonstate.NormalizeSessionState(session.State)
			} else {
				session.ObservedState = daemonstate.SessionStateStopped
			}
			session.TmuxAttachedCount = 0
		} else {
			session.ObservedState = daemonstate.SessionStateStopped
			session.TmuxAttachedCount = 0
		}
		session.UpdatedAt = time.Now().UTC()
		if writer != nil {
			if err := writer.PersistSessionProjection(ctx, projectID, session); err != nil {
				return err
			}
			continue
		}
		if err := store.UpsertSessionState(ctx, projectID, session); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) refreshStoppedSessionRuntimeState(ctx context.Context, projectID, issueID string, sessionIDs []string) error {
	if d == nil || d.sessionRuntimeStateStoreIfConfigured(projectID) == nil || d.sessionStore == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	sessionIDSet := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
			sessionIDSet[sessionID] = struct{}{}
		}
	}
	if issueID == "" && len(sessionIDSet) == 0 {
		return nil
	}

	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	namingScope := d.sessionNamingScope(projectID)
	rows, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return err
	}
	matched := false
	for _, session := range rows {
		sessionID := strings.TrimSpace(session.ID)
		_, exactSessionMatch := sessionIDSet[sessionID]
		issueMatch := naming.IssueIDsEqual(sessionProjectionIssueID(session, namingScope), issueID)
		suffixMatch := sessionNameHasIssueSuffix(sessionID, issueID)
		if !exactSessionMatch && !issueMatch && !suffixMatch {
			continue
		}
		session.State = daemonstate.SessionStateStopped
		session.ObservedState = daemonstate.SessionStateStopped
		session.UpdatedAt = time.Now().UTC()
		if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, session); err != nil {
			return err
		}
		matched = true
	}
	if matched {
		return nil
	}

	fallbackSessionID := ""
	for sessionID := range sessionIDSet {
		fallbackSessionID = sessionID
		break
	}
	if fallbackSessionID == "" && issueID != "" {
		fallbackSessionID = naming.CanonicalSessionID(namingScope, issueID)
	}

	var session daemonstate.Session
	if issueID != "" {
		rows, err := store.ListSessionStates(ctx, projectID)
		if err != nil {
			return err
		}
		if existing, found := nonAgentSessionProjectionByIssue(rows, namingScope, issueID); found {
			session = existing
		}
	}
	if strings.TrimSpace(session.ID) == "" {
		session = daemonstate.Session{
			ID:      fallbackSessionID,
			IssueID: issueID,
		}
	}
	if strings.TrimSpace(session.ID) == "" {
		session.ID = fallbackSessionID
	}
	if strings.TrimSpace(session.IssueID) == "" {
		session.IssueID = issueID
	}
	session.State = daemonstate.SessionStateStopped
	session.ObservedState = daemonstate.SessionStateStopped
	session.UpdatedAt = time.Now().UTC()
	return d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, session)
}

func (d *Daemon) persistTmuxSessionRuntimeState(ctx context.Context, projectID string, tmuxSessions []tmux.SessionInfo) error {
	if d.sessionRuntimeStateStoreIfConfigured(projectID) == nil || d.sessionStore == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)

	namingScope := d.sessionNamingScope(projectID)
	existingSessions, err := d.sessionRuntimeStateStoreIfConfigured(projectID).ListSessionStates(ctx, projectID)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load cached session runtime-state snapshot failed", "project_id", projectID, "error", err)
		}
	}
	existingByIssueKey := sessionProjectionForTmuxHydrationByIssueKey(existingSessions, namingScope)
	liveSessionIDs := make(map[string]struct{}, len(tmuxSessions))
	liveIssueKeys := make(map[string]struct{}, len(tmuxSessions))
	for _, info := range tmuxSessions {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		liveSessionIDs[name] = struct{}{}
		issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope)
		if !ok {
			continue
		}
		issueKey := sessionKey(issueID)
		liveIssueKeys[issueKey] = struct{}{}
		row := daemonstate.Session{
			ID:                name,
			IssueID:           issueID,
			State:             daemonstate.SessionStateRunning,
			ObservedState:     daemonstate.SessionStateRunning,
			Activity:          "busy",
			ActivitySource:    "runtime",
			TmuxAttachedCount: info.AttachedCount,
			StartedAt:         info.CreatedAt,
			UpdatedAt:         time.Now().UTC(),
		}
		if existing, exists := existingByIssueKey[issueKey]; exists {
			row.State = existing.State
			row.ObservedState = daemonstate.SessionStateRunning
			if activity := normalizeSessionActivity(existing.Activity); activity != "" {
				row.Activity = activity
				row.ActivitySource = normalizeSessionActivitySource(existing.ActivitySource, "runtime")
			}
			if (row.StartedAt == nil || row.StartedAt.IsZero()) && existing.StartedAt != nil && !existing.StartedAt.IsZero() {
				started := existing.StartedAt.UTC()
				row.StartedAt = &started
			}
		}
		if row.StartedAt == nil || row.StartedAt.IsZero() {
			started := row.UpdatedAt.UTC()
			row.StartedAt = &started
		}
		if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, row); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("persist live tmux session runtime state failed",
				"project_id", projectID,
				"session_id", row.ID,
				"issue_id", row.IssueID,
				"error", err,
			)
		}
	}
	for _, session := range existingSessions {
		if _, live := liveSessionIDs[strings.TrimSpace(session.ID)]; live {
			continue
		}
		stopped := session
		if isAgentScopedSessionID(session.ID) {
			issueKey := sessionKey(sessionProjectionIssueID(session, namingScope))
			if _, parentLive := liveIssueKeys[issueKey]; parentLive {
				stopped.ObservedState = daemonstate.NormalizeSessionState(stopped.State)
			} else {
				stopped.ObservedState = daemonstate.SessionStateStopped
			}
		} else {
			stopped.ObservedState = daemonstate.SessionStateStopped
		}
		stopped.UpdatedAt = time.Now().UTC()
		if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, stopped); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("persist missing tmux session as stopped failed",
				"project_id", projectID,
				"session_id", stopped.ID,
				"issue_id", stopped.IssueID,
				"error", err,
			)
		}
	}
	return nil
}

func (d *Daemon) buildSessionLaunchCommand(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt string) string {
	return d.buildSessionLaunchCommandWithInitReadyPath(projectID, issueID, sessionID, yolo, imagePaths, initialPrompt, "")
}

func (d *Daemon) buildSessionLaunchCommandWithInitReadyPath(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt, initReadyPath string) string {
	projectCfg := d.runtimeConfigForProject(projectID)
	toolCommand := d.buildCLIToolCommand(projectID, issueID, sessionID, yolo, imagePaths, initialPrompt)
	commands := make([]string, 0, len(projectCfg.SessionInitCommands)+2)
	if trapCommand := sessionInitReadyTrapCommand(initReadyPath); trapCommand != "" {
		commands = append(commands, trapCommand)
	}
	for _, initCmd := range projectCfg.SessionInitCommands {
		trimmed := strings.TrimSpace(initCmd)
		if trimmed != "" {
			commands = append(commands, trimmed)
		}
	}
	if markerCommand := sessionInitReadyMarkerCommand(initReadyPath); markerCommand != "" {
		commands = append(commands, markerCommand)
	}
	commands = append(commands, toolCommand)

	shell := strings.TrimSpace(projectCfg.SessionShell)
	if shell == "" {
		shell = appconfig.DefaultSessionShell()
	}
	inner := strings.Join(commands, "; ")
	inner = inner + "; exec " + shell
	return fmt.Sprintf("%s -i -c %s", shell, singleQuoteForShell(inner))
}

func sessionInitReadyMarkerCommand(initReadyPath string) string {
	initReadyPath = strings.TrimSpace(initReadyPath)
	if initReadyPath == "" {
		return ""
	}
	return "printf %s ready > " + singleQuoteForShell(filepath.ToSlash(initReadyPath)) + " && __azedarach_session_init_ready=1"
}

func sessionInitReadyTrapCommand(initReadyPath string) string {
	initReadyPath = strings.TrimSpace(initReadyPath)
	if initReadyPath == "" {
		return ""
	}
	action := "if [ \"${__azedarach_session_init_ready:-0}\" != 1 ]; then printf %s ready > " + singleQuoteForShell(filepath.ToSlash(initReadyPath)) + "; fi"
	return "__azedarach_session_init_ready=0; trap " + singleQuoteForShell(action) + " EXIT"
}

const sessionSideEffectWindowName = "side-effects"
const sessionInitReadyPollInterval = 100 * time.Millisecond

func (d *Daemon) prepareSessionInitReadyMarker(projectID, worktreePath, issueID, sessionID string) (sessionInitReadyMarker, error) {
	commandCount := countNonEmptyStrings(d.runtimeConfigForProject(projectID).SessionInitCommands)
	if commandCount == 0 {
		return sessionInitReadyMarker{}, nil
	}
	worktreePath = strings.TrimSpace(worktreePath)
	if worktreePath == "" {
		return sessionInitReadyMarker{}, errors.New("missing worktree path")
	}
	relativePath := sessionInitReadyMarkerPath(issueID, sessionID)
	absolutePath := filepath.Join(worktreePath, relativePath)
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		return sessionInitReadyMarker{}, err
	}
	if err := os.Remove(absolutePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return sessionInitReadyMarker{}, err
	}
	return sessionInitReadyMarker{
		RelativePath: relativePath,
		AbsolutePath: absolutePath,
		CommandCount: commandCount,
	}, nil
}

func sessionInitReadyMarkerPath(issueID, sessionID string) string {
	return filepath.Join(
		".azedarach",
		"session-init-ready",
		safeSessionSideEffectPathSegment(issueID),
		safeSessionSideEffectPathSegment(sessionID),
		"ready",
	)
}

func countNonEmptyStrings(values []string) int {
	count := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}

func (d *Daemon) waitForSessionInitReady(ctx context.Context, projectID, issueID, sessionID string, marker sessionInitReadyMarker) error {
	if strings.TrimSpace(marker.AbsolutePath) == "" {
		return nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session start waiting for init commands",
			"project_id", projectID,
			"issue_id", issueID,
			"session_id", sessionID,
			"command_count", marker.CommandCount,
			"marker", marker.RelativePath,
		)
	}
	if sessionInitReady(marker.AbsolutePath) {
		return nil
	}
	ticker := time.NewTicker(sessionInitReadyPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("session init commands did not finish for %s before start context ended: %w", issueID, ctx.Err())
		case <-ticker.C:
			if sessionInitReady(marker.AbsolutePath) {
				if d.cfg.Logger != nil {
					d.cfg.Logger.Info("daemon session start init commands finished",
						"project_id", projectID,
						"issue_id", issueID,
						"session_id", sessionID,
						"command_count", marker.CommandCount,
					)
				}
				return nil
			}
		}
	}
}

func sessionInitReady(path string) bool {
	if strings.TrimSpace(path) == "" {
		return true
	}
	_, err := os.Stat(path)
	return err == nil
}

func (d *Daemon) startSessionSideEffectCommands(ctx context.Context, projectID, issueID, sessionID, worktreePath string) {
	projectCfg := d.runtimeConfigForProject(projectID)
	sideEffectCommand := buildSessionSideEffectWindowCommand(projectCfg, projectID, issueID, sessionID)
	if strings.TrimSpace(sideEffectCommand) == "" {
		return
	}
	if d.tmux == nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session side-effect commands skipped; tmux client unavailable",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", sessionID,
			)
		}
		return
	}
	if _, err := d.tmux.EnsureWindow(ctx, sessionID, sessionSideEffectWindowName, worktreePath); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session side-effect window setup failed",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", sessionID,
				"error", err,
			)
		}
		return
	}
	target := sessionID + ":" + sessionSideEffectWindowName
	if err := d.tmux.SendKeys(ctx, target, sideEffectCommand); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("session side-effect command dispatch failed",
			"project_id", projectID,
			"issue_id", issueID,
			"session_id", sessionID,
			"target", target,
			"error", err,
		)
	}
}

func buildSessionSideEffectWindowCommand(projectCfg daemonProjectRuntimeConfig, projectID, issueID, sessionID string) string {
	sideEffectCommands := buildSessionSideEffectCommands(projectCfg.SessionSideEffectCommands, issueID, sessionID)
	if len(sideEffectCommands) == 0 {
		return ""
	}
	commands := make([]string, 0, len(sideEffectCommands)+2)
	if contextExport := sessionLaunchContextExportCommand(projectID, issueID, sessionID); contextExport != "" {
		commands = append(commands, contextExport)
	}
	commands = append(commands, sideEffectCommands...)
	return strings.Join(commands, "; ")
}

type sessionContextEnvAssignment struct {
	Key   string
	Value string
}

func sessionLaunchContextEnvAssignments(projectID, issueID, sessionID string) []sessionContextEnvAssignment {
	return []sessionContextEnvAssignment{
		{Key: "AZEDARACH_PROJECT_ID", Value: strings.TrimSpace(projectID)},
		{Key: "AZEDARACH_ISSUE_ID", Value: strings.TrimSpace(issueID)},
		{Key: "AZEDARACH_SESSION_ID", Value: strings.TrimSpace(sessionID)},
	}
}

func sessionLaunchContextExportCommand(projectID, issueID, sessionID string) string {
	assignments := make([]string, 0, 3)
	for _, assignment := range sessionLaunchContextEnvAssignments(projectID, issueID, sessionID) {
		if assignment.Value == "" {
			continue
		}
		assignments = append(assignments, assignment.Key+"="+singleQuoteForShell(assignment.Value))
	}
	if len(assignments) == 0 {
		return ""
	}
	return "export " + strings.Join(assignments, " ")
}

func buildSessionSideEffectCommands(sideEffectCommands []string, issueID, sessionID string) []string {
	if len(sideEffectCommands) == 0 {
		return nil
	}
	logDir := sessionSideEffectLogDir(issueID, sessionID)
	commands := make([]string, 0, len(sideEffectCommands))
	index := 0
	for _, sideEffectCmd := range sideEffectCommands {
		trimmed := strings.TrimSpace(sideEffectCmd)
		if trimmed == "" {
			continue
		}
		index++
		logPath := filepath.ToSlash(filepath.Join(logDir, fmt.Sprintf("%03d.log", index)))
		commands = append(commands, buildSessionSideEffectCommand(index, trimmed, logDir, logPath))
	}
	return commands
}

func buildSessionSideEffectCommand(index int, command, logDir, logPath string) string {
	quotedLogDir := singleQuoteForShell(filepath.ToSlash(logDir))
	quotedLogPath := singleQuoteForShell(filepath.ToSlash(logPath))
	quotedCommand := singleQuoteForShell(command)
	return fmt.Sprintf(
		"mkdir -p %s && echo %s && { printf 'command: %%s\\n' %s; (%s); status=$?; printf 'exit status: %%s\\n' \"$status\"; } 2>&1 | tee -a %s",
		quotedLogDir,
		singleQuoteForShell(fmt.Sprintf("session side-effect[%d] log: %s", index, filepath.ToSlash(logPath))),
		quotedCommand,
		command,
		quotedLogPath,
	)
}

func sessionSideEffectLogDir(issueID, sessionID string) string {
	return filepath.Join(".azedarach", "session-side-effects", safeSessionSideEffectPathSegment(issueID), safeSessionSideEffectPathSegment(sessionID))
}

func safeSessionSideEffectPathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func (d *Daemon) issueResourceLifecycleContext(projectID, issueID, sessionID, worktreePath, branch string) issueResourceLifecycleContext {
	rootPath := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
	if rootPath == "" {
		rootPath = strings.TrimSpace(d.resolveRepoDirForProject(projectID))
	}
	if rootPath == "" {
		rootPath = strings.TrimSpace(d.cfg.RepoDir)
	}
	return issueResourceLifecycleContext{
		ProjectID:    d.canonicalProjectID(projectID),
		IssueID:      strings.TrimSpace(issueID),
		SessionID:    strings.TrimSpace(sessionID),
		WorktreePath: strings.TrimSpace(worktreePath),
		RootPath:     rootPath,
		Branch:       strings.TrimSpace(branch),
	}
}

func (d *Daemon) issueWorktreeContext(ctx context.Context, projectID, issueID string) (path, branch string) {
	if manager := d.worktreeManagerForProject(projectID); manager != nil {
		if worktree, err := manager.Get(ctx, issueID); err == nil {
			return strings.TrimSpace(worktree.Path), strings.TrimSpace(worktree.Branch)
		}
	}
	if store := d.worktreeRuntimeStateStore(projectID); store != nil {
		rows, err := store.ListWorktreeStates(ctx, d.canonicalProjectID(projectID))
		if err == nil {
			for _, row := range rows {
				if naming.IssueIDsEqual(row.IssueID, issueID) {
					return strings.TrimSpace(row.Path), strings.TrimSpace(row.Branch)
				}
			}
		}
	}
	return "", ""
}

func (d *Daemon) runIssueResourcePrepareCommands(ctx context.Context, projectID string, resourceCtx issueResourceLifecycleContext) (issueResourceLifecycleResult, error) {
	return d.runIssueResourceCommands(ctx, projectID, resourceCtx, d.runtimeConfigForProject(projectID).IssueResources.PrepareCommands)
}

func (d *Daemon) runIssueResourceFailedStartCleanupCommands(ctx context.Context, projectID string, resourceCtx issueResourceLifecycleContext) error {
	_, err := d.runIssueResourceCommands(ctx, projectID, resourceCtx, d.runtimeConfigForProject(projectID).IssueResources.FailedStartCleanupCommands)
	return err
}

func (d *Daemon) issueResourceFailedStartCleanupNote(ctx context.Context, projectID string, resourceCtx issueResourceLifecycleContext) string {
	if err := d.runIssueResourceFailedStartCleanupCommands(ctx, projectID, resourceCtx); err != nil {
		return fmt.Sprintf("; failed-start cleanup also failed: %v", err)
	}
	return ""
}

func (d *Daemon) issueResourceFailedStartRollbackNote(ctx context.Context, projectID, sessionID string, resourceCtx issueResourceLifecycleContext) string {
	note := d.issueResourceFailedStartCleanupNote(ctx, projectID, resourceCtx)
	if d == nil || d.tmux == nil {
		return note
	}
	if err := d.tmux.KillSession(ctx, sessionID); err != nil {
		return note + fmt.Sprintf("; failed-start tmux cleanup also failed: %v", err)
	}
	return note
}

func (d *Daemon) runIssueResourceCleanupCommands(ctx context.Context, projectID string, resourceCtx issueResourceLifecycleContext) (issueResourceLifecycleResult, error) {
	return d.runIssueResourceCommands(ctx, projectID, resourceCtx, d.runtimeConfigForProject(projectID).IssueResources.CleanupCommands)
}

func (d *Daemon) runIssueResourceReconcileCommand(ctx context.Context, projectID string, resourceCtx issueResourceLifecycleContext, desiredState string) (issueResourceLifecycleResult, error) {
	projectCfg := d.runtimeConfigForProject(projectID)
	command := strings.TrimSpace(projectCfg.IssueResources.ReconcileCommand)
	if command == "" {
		return issueResourceLifecycleResult{}, nil
	}
	resourceCtx.DesiredState = normalizeIssueResourceDesiredState(desiredState)
	return d.runIssueResourceCommands(ctx, projectID, resourceCtx, []string{command})
}

func (d *Daemon) runIssueResourceCommands(ctx context.Context, projectID string, resourceCtx issueResourceLifecycleContext, commands []string) (issueResourceLifecycleResult, error) {
	result := issueResourceLifecycleResult{}
	if len(commands) == 0 {
		return result, nil
	}
	projectCfg := d.runtimeConfigForProject(projectID)
	shell := strings.TrimSpace(projectCfg.SessionShell)
	if shell == "" {
		shell = appconfig.DefaultSessionShell()
	}
	env := d.issueResourceEnv(projectCfg.IssueResources, resourceCtx)
	for _, lifecycleCmd := range commands {
		trimmed := strings.TrimSpace(lifecycleCmd)
		if trimmed == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, shell, "-lc", trimmed)
		cmd.Dir = issueResourceCommandDir(resourceCtx)
		cmd.Env = append(os.Environ(), env...)
		output, err := cmd.CombinedOutput()
		result.Ran = append(result.Ran, trimmed)
		if err != nil {
			return result, fmt.Errorf("%s: %w (%s)", trimmed, err, strings.TrimSpace(string(output)))
		}
	}
	return result, nil
}

func (d *Daemon) exportIssueResourceSessionEnv(ctx context.Context, projectID string, resourceCtx issueResourceLifecycleContext) error {
	projectCfg := d.runtimeConfigForProject(projectID)
	if !issueResourcesConfigured(projectCfg.IssueResources) {
		return nil
	}
	assignments := d.issueResourceShellExports(projectCfg.IssueResources, resourceCtx)
	if len(assignments) == 0 {
		return nil
	}
	return d.tmux.SendKeys(ctx, resourceCtx.SessionID, "export "+strings.Join(assignments, " "))
}

func (d *Daemon) exportSessionContextEnv(ctx context.Context, projectID, issueID, sessionID string) error {
	for _, assignment := range sessionLaunchContextEnvAssignments(projectID, issueID, sessionID) {
		if assignment.Value == "" {
			continue
		}
		if err := d.tmux.SetEnvironment(ctx, sessionID, assignment.Key, assignment.Value); err != nil {
			return err
		}
	}
	if exportCommand := sessionLaunchContextExportCommand(projectID, issueID, sessionID); exportCommand != "" {
		if err := d.tmux.SendKeys(ctx, sessionID, exportCommand); err != nil {
			return err
		}
	}
	return nil
}

func issueResourcesConfigured(cfg appconfig.IssueResourcesConfig) bool {
	return len(cfg.Env) > 0 ||
		len(cfg.PrepareCommands) > 0 ||
		len(cfg.FailedStartCleanupCommands) > 0 ||
		len(cfg.CleanupCommands) > 0 ||
		strings.TrimSpace(cfg.ReconcileCommand) != ""
}

func (d *Daemon) issueResourceEnv(cfg appconfig.IssueResourcesConfig, resourceCtx issueResourceLifecycleContext) []string {
	values := issueResourceContextValues(resourceCtx)
	for key, value := range cfg.Env {
		key = strings.TrimSpace(key)
		if !validShellEnvName(key) || strings.HasPrefix(key, "AZEDARACH_") {
			continue
		}
		values[key] = value
	}
	for range 2 {
		for key, value := range values {
			values[key] = os.Expand(value, func(name string) string {
				return values[name]
			})
		}
	}
	for key, value := range issueResourceContextValues(resourceCtx) {
		values[key] = value
	}
	env := make([]string, 0, len(values))
	for key, value := range values {
		if validShellEnvName(key) {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func (d *Daemon) issueResourceShellExports(cfg appconfig.IssueResourcesConfig, resourceCtx issueResourceLifecycleContext) []string {
	env := d.issueResourceEnv(cfg, resourceCtx)
	assignments := make([]string, 0, len(env))
	for _, pair := range env {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || !validShellEnvName(key) {
			continue
		}
		assignments = append(assignments, key+"="+singleQuoteForShell(value))
	}
	return assignments
}

func issueResourceCommandDir(resourceCtx issueResourceLifecycleContext) string {
	if dir := strings.TrimSpace(resourceCtx.WorktreePath); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(resourceCtx.RootPath); dir != "" {
		return dir
	}
	return "."
}

func issueResourceContextValues(resourceCtx issueResourceLifecycleContext) map[string]string {
	values := map[string]string{
		"AZEDARACH_PROJECT_ID":    resourceCtx.ProjectID,
		"AZEDARACH_ISSUE_ID":      resourceCtx.IssueID,
		"AZEDARACH_SESSION_ID":    resourceCtx.SessionID,
		"AZEDARACH_WORKTREE_PATH": resourceCtx.WorktreePath,
		"AZEDARACH_ROOT_PATH":     resourceCtx.RootPath,
		"AZEDARACH_BRANCH":        resourceCtx.Branch,
	}
	if desired := normalizeIssueResourceDesiredState(resourceCtx.DesiredState); desired != "" {
		values["AZEDARACH_RESOURCE_DESIRED_STATE"] = desired
	}
	return values
}

func normalizeIssueResourceDesiredState(state string) string {
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "present", "absent":
		return strings.TrimSpace(strings.ToLower(state))
	default:
		return ""
	}
}

func validShellEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func issueResourceStartOutput(result issueResourceLifecycleResult) string {
	if len(result.Ran) == 0 {
		return ""
	}
	return fmt.Sprintf("Issue resources prepared: %d command(s)", len(result.Ran))
}

func sessionInitReadyOutput(marker sessionInitReadyMarker) string {
	if marker.CommandCount == 0 {
		return ""
	}
	return fmt.Sprintf("Session init commands finished: %d command(s)", marker.CommandCount)
}

func issueResourceCleanupOutput(result issueResourceLifecycleResult) string {
	if len(result.Ran) == 0 {
		return ""
	}
	return fmt.Sprintf("Issue resources cleaned: %d command(s)", len(result.Ran))
}

func (d *Daemon) runWorktreeInitCommands(ctx context.Context, projectID string, worktreePath string) error {
	commands := d.runtimeConfigForProject(projectID).WorktreeInitCommands
	if len(commands) == 0 {
		return nil
	}

	shell := strings.TrimSpace(d.runtimeConfigForProject(projectID).SessionShell)
	if shell == "" {
		shell = appconfig.DefaultSessionShell()
	}

	for _, initCmd := range commands {
		trimmed := strings.TrimSpace(initCmd)
		if trimmed == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, shell, "-lc", trimmed)
		cmd.Dir = worktreePath
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w (%s)", trimmed, err, strings.TrimSpace(string(output)))
		}
	}

	return nil
}

func (d *Daemon) buildCLIToolCommand(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt string) string {
	projectCfg := d.runtimeConfigForProject(projectID)
	tool := strings.TrimSpace(projectCfg.CLITool)
	if tool == "" {
		tool = "claude"
	}

	parts := []string{
		fmt.Sprintf(`AZEDARACH_ISSUE_ID="%s"`, escapeForShellDoubleQuotes(issueID)),
		tool,
	}

	if strings.EqualFold(tool, "codex") {
		// Codex hook wiring lives entirely in <repo>/.codex/hooks.json, written
		// by `az ai install --target=codex`. Launch-time `-c hooks.*` injection
		// was removed because it duplicated those entries — Codex merged the
		// override with the file config and every event fired twice (double
		// daemon notify, double hook-log row, double guard mutation, double
		// shell spawn). The single source of truth is now the install file.
		for _, imagePath := range imagePaths {
			trimmedPath := strings.TrimSpace(imagePath)
			if trimmedPath == "" {
				continue
			}
			parts = append(parts, fmt.Sprintf(`--image "%s"`, escapeForShellDoubleQuotes(trimmedPath)))
		}
	}
	if yolo || projectCfg.DangerouslySkipPermissions {
		switch strings.ToLower(tool) {
		case "codex":
			parts = append(parts, "--dangerously-bypass-approvals-and-sandbox")
		default:
			parts = append(parts, "--dangerously-skip-permissions")
		}
	}
	if initialPrompt != "" {
		escapedPrompt := escapeForShellDoubleQuotes(initialPrompt)
		switch strings.ToLower(tool) {
		case "opencode":
			parts = append(parts, fmt.Sprintf(`--prompt "%s"`, escapedPrompt))
		case "codex":
			parts = append(parts, "--", fmt.Sprintf(`"%s"`, escapedPrompt))
		default:
			parts = append(parts, fmt.Sprintf(`"%s"`, escapedPrompt))
		}
	}

	return strings.Join(parts, " ")
}

func escapeForShellDoubleQuotes(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	escaped = strings.ReplaceAll(escaped, "`", "\\`")
	escaped = strings.ReplaceAll(escaped, "$", `\$`)
	return strings.ReplaceAll(escaped, "!", `\!`)
}

func singleQuoteForShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func buildStartWorkPrompt(issueID, issueType, title string, orchestratedWorker bool, parentIssueID string) string {
	safeIssueType := sanitizePromptInline(issueType, 0)
	if safeIssueType == "" {
		safeIssueType = "task"
	}
	safeTitle := sanitizePromptInline(title, 160)
	if safeTitle == "" {
		safeTitle = issueID
	}
	base := fmt.Sprintf(
		"work on issue %s (%s): %s\n\nStart by running `az prime`. Then continue the task using the context it prints without waiting for further instruction.",
		issueID,
		safeIssueType,
		safeTitle,
	)
	if strings.EqualFold(safeIssueType, string(domain.TypeEpic)) {
		return base + "\n\nRole: orchestrator\n- Use `az orchestrate status --root <issue-id>` for readiness snapshots including active worker activity (`busy|idle|no-agent|unknown`).\n- Use `az orchestrate watch --root <issue-id> --since <seq> --jsonl` for continuous observe-only mailbox/runnable/activity updates; start it in another pane/session and leave it running while workers are active.\n- Do not use `--once` for orchestration monitoring; reserve it for diagnostic single polls.\n- Trust hook-backed `activity=busy|idle` for worker idleness checks and treat `activity=no-agent` as an intentional session-only shell. If activity is `unknown`, inspect hooks with `az ai status --target=auto`; run `az ai install --target=auto` only when hooks are missing, outdated, or not installed. Use direct tmux pane capture only when watch/status look stale, failed, or contradictory, or when you need a sparse progress spot-check. Do not poll tmux panes on a fixed interval.\n- Start runnable leaf workers manually with `az orchestrate start --root <issue-id> --limit 4`, then immediately ensure the continuous watch is running.\n- Send running-worker nudges with `az orchestrate message --root <issue-id> --issue <worker-issue> --body \"...\"`; workers reporting their own status should use `az mail send --parent <issue-id> --issue <worker-issue> --type worker-progress|worker-blocked|worker-integration-ready --body \"...\"`; bare `az mail send` is durable mailbox-only.\n- Treat blocked work as graph state from unresolved `blocks` dependencies or worker-blocked mailbox evidence, not as an issue status.\n- Treat `in_review` workers as ready for orchestrator validation; inspect evidence, run parent checks, then close accepted worker issues with `az issue close --id <issue-id>`.\n- Use `az orchestrate integrate --issue <issue-id>` for worker result inspection or repair guidance, not as the normal merge authority.\n- Use `az orchestrate close-session --issue <issue-id>` only for session cleanup repair when a worker must be stopped without closing the issue; daemon stop records stopped state before killing tmux so recovery cannot resurrect it.\n- Keep orchestration centralized in v1; do not auto-delegate sub-orchestrators.\n- Close only when `az orchestrate complete-check --root <issue-id>` passes."
	}
	if !orchestratedWorker {
		return base + "\n\nRole: contributor\n- Focus only on this issue scope unless the user explicitly expands it.\n- Keep issue status/notes current with evidence.\n- Use `in_progress` while actively working, `in_review` when complete and awaiting review/integration, and `closed` only after acceptance criteria and validation are done.\n- Represent blocked work with dependency edges and notes, not by using `in_review`."
	}
	mailboxGuidance := "- Check inbound orchestrator messages with `az mail list --parent <parent-issue> --since 0 --json` before declaring yourself blocked or idle; apply events for this issue and continue without waiting for a separate user prompt."
	if strings.TrimSpace(parentIssueID) != "" {
		mailboxGuidance = fmt.Sprintf("- Coordination mailbox parent: `%s`; check inbound orchestrator messages with `az mail list --parent %s --since 0 --json` before declaring yourself blocked or idle; apply events for this issue and continue without waiting for a separate user prompt.", parentIssueID, parentIssueID)
	}
	return base + "\n\nRole: worker\n- Focus only on this issue scope unless the user explicitly expands it.\n" + mailboxGuidance + "\n- Report coordination state with `az mail send --parent <parent-issue> --issue " + issueID + " --type worker-progress|worker-blocked|worker-integration-ready --body \"...\"`; do not use `az orchestrate message` for your own status because it is an orchestrator-to-worker live delivery command.\n- Report coordination state with mailbox event types: worker-progress, worker-blocked, and worker-integration-ready; worker-ready and worker-complete are accepted only as legacy aliases for worker-integration-ready.\n- Keep issue status/notes current with evidence for the orchestrator.\n- Use `in_progress` while actively working and `in_review` when complete and ready for orchestrator integration; the orchestrator closes accepted work.\n- Report blockers via dependency edges or worker-blocked mailbox events, not by setting `in_review`."
}

func buildConflictResolutionPrompt(issueID string, conflictFiles []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Resolve merge conflicts for issue %s.\n\n", issueID)
	b.WriteString("Start by running `az prime`. Inspect the conflicted files, resolve conflict markers, and run the focused checks needed to verify the merge.\n")
	if len(conflictFiles) > 0 {
		b.WriteString("\nConflicted files:\n")
		for _, file := range conflictFiles {
			fmt.Fprintf(&b, "- %s\n", sanitizePromptInline(file, 240))
		}
	}
	b.WriteString("\nDo not create a PR or push unless explicitly asked. Leave a concise summary of resolved files and validation results.")
	return b.String()
}

func sanitizePromptInline(value string, maxLength int) string {
	mapped := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	normalized := strings.Join(strings.Fields(mapped), " ")
	safe := strings.NewReplacer("<", "[", ">", "]").Replace(normalized)
	if maxLength <= 0 {
		return safe
	}
	runes := []rune(safe)
	if len(runes) <= maxLength {
		return safe
	}
	trimmed := strings.TrimSpace(string(runes[:maxLength-3]))
	return trimmed + "..."
}
