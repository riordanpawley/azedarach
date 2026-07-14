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
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/git"
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
	Lines      int      `json:"lines,omitempty"`
}

type rootedOrchestratorRecoveryContextKey struct{}

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
	Lines      int
}

type sessionRecoveryResult struct {
	RecreatedTmuxSessions  int                                   `json:"recreated_tmux_sessions"`
	AlignedDaemonSessions  int                                   `json:"aligned_daemon_sessions"`
	RepairedIssueLifecycle int                                   `json:"repaired_issue_lifecycle"`
	LifecycleDivergences   []protocol.RuntimeLifecycleDivergence `json:"lifecycle_divergences,omitempty"`
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

type sessionStartIssueLifecycleUpdater interface {
	Update(context.Context, string, domain.Status) error
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
	PaneScoped        int
	TmuxAttachedCount int
}

type sessionHookActivity struct {
	Total          int
	Active         int
	Paused         int
	Error          int
	LatestActiveAt time.Time
	LatestPausedAt time.Time
	LatestErrorAt  time.Time
}

type sessionDisplayActivity struct {
	Activity  string
	Source    string
	UpdatedAt time.Time
}

const (
	sessionInvariantSessionStartConflict   daemonInvariantID = daemonInvariantSessionStartConflict
	sessionInvariantSessionAttachTarget    daemonInvariantID = daemonInvariantSessionAttachTarget
	sessionInvariantSessionLifecycleTarget daemonInvariantID = daemonInvariantSessionLifecycleTarget
	sessionInvariantSessionStopTargets     daemonInvariantID = daemonInvariantSessionStopTargets
	sessionInvariantSessionReconcile       daemonInvariantID = daemonInvariantSessionReconcile

	sessionConflictWindowName         = "resolve-conflict"
	sessionActivityStartupGrace       = 45 * time.Second
	sessionRestartContinuePromptDelay = 500 * time.Millisecond
	sessionRestartContinuePrompt      = "Continue your prior task. Start by running `az prime` if you need to refresh issue context, then keep working from the existing conversation without waiting for further instruction."
	sessionCaptureDefaultLines        = 120
	sessionCaptureMaxLines            = 1000
	codexLaunchPromptArgMaxBytes      = 24 * 1024
	codexLaunchCommandArgMaxBytes     = 16 * 1024
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

func sessionStartLaunchFailureMessage(phase string, cmd resolvedSessionTarget, worktreePath, launchCommand string, startWork bool, err error, cleanupNote string) string {
	phase = strings.TrimSpace(phase)
	if phase == "" {
		phase = "session_start"
	}
	lines := []string{
		fmt.Sprintf("session start failed during %s", phase),
		fmt.Sprintf("issue_id=%s", cmd.IssueID),
		fmt.Sprintf("session_id=%s", cmd.SessionID),
		fmt.Sprintf("cwd=%s", strings.TrimSpace(worktreePath)),
		"command=" + sessionStartTmuxCommandLine(cmd.SessionID, worktreePath, launchCommand, startWork),
	}
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		lines = append(lines, "cause="+strings.TrimSpace(err.Error()))
	}
	if trimmedCleanup := strings.TrimSpace(cleanupNote); trimmedCleanup != "" {
		lines = append(lines, "cleanup="+trimmedCleanup)
	}
	lines = append(lines,
		fmt.Sprintf("Repair: Worktree exists but tmux session is not active; retry with `az session start %s`.", cmd.IssueID),
		fmt.Sprintf("Diagnostics: az session diagnose %s", cmd.IssueID),
	)
	return strings.Join(lines, "\n")
}

func sessionStartTmuxCommandLine(sessionID, worktreePath, launchCommand string, startWork bool) string {
	parts := []string{"tmux", "new-session", "-d", "-s", strings.TrimSpace(sessionID)}
	if trimmedWorktree := strings.TrimSpace(worktreePath); trimmedWorktree != "" {
		parts = append(parts, "-c", trimmedWorktree)
	}
	if startWork {
		if trimmedLaunch := strings.TrimSpace(launchCommand); trimmedLaunch != "" {
			parts = append(parts, compactSessionStartCommandDetail(trimmedLaunch, 500))
		}
	}
	return strings.Join(parts, " ")
}

func compactSessionStartCommandDetail(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return fmt.Sprintf("%s... [truncated, bytes=%d]", value[:limit], len(value))
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
		if session.Role == daemonstate.SessionRoleAdvisor {
			continue
		}
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
	intent := make([]daemonstate.Session, 0, len(sessions))
	for _, session := range sessions {
		if isAgentScopedSessionID(session.ID) || !lifecycleManagedSessionProjection(session) {
			continue
		}
		intent = append(intent, session)
	}
	byIssueKey := sessionProjectionAggregateByIssueKey(intent, namingScope)
	session, found := byIssueKey[sessionKey(issueID)]
	return session, found
}

func sessionProjectionCountsByIssueKey(sessions []daemonstate.Session, namingScope string) map[string]sessionProjectionCounts {
	byIssueKey := make(map[string]sessionProjectionCounts, len(sessions))
	liveAgentRowsByKey := make(map[string]int, len(sessions))
	for _, session := range sessions {
		if session.Role == daemonstate.SessionRoleAdvisor {
			continue
		}
		if !isAgentScopedSessionID(session.ID) {
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
		if key != "" {
			liveAgentRowsByKey[key]++
		}
	}
	for _, session := range sessions {
		if session.Role == daemonstate.SessionRoleAdvisor {
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
		if !isAgentScopedSessionID(session.ID) && liveAgentRowsByKey[key] > 0 {
			counts.TmuxAttachedCount += session.TmuxAttachedCount
			byIssueKey[key] = counts
			continue
		}
		counts.TmuxAttachedCount += session.TmuxAttachedCount
		counts.Total++
		if isAgentScopedSessionID(session.ID) {
			counts.PaneScoped++
		}
		switch sessionActivityState(session) {
		case domain.SessionError, domain.SessionPaused, domain.SessionIdle, domain.SessionWaiting:
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

func agentScopedSessionParentAndPane(sessionID string) (string, string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	parent, paneID, ok := strings.Cut(sessionID, ".pane-")
	if !ok || strings.TrimSpace(parent) == "" || strings.TrimSpace(paneID) == "" {
		return "", "", false
	}
	paneID = sanitizeAgentScopedPaneID(paneID)
	if paneID == "" {
		return "", "", false
	}
	return strings.TrimSpace(parent), paneID, true
}

func sanitizeAgentScopedPaneID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
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
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-_.")
}

func sessionProjectionForReconcileByIssueKey(sessions []daemonstate.Session, namingScope string) map[string]daemonstate.Session {
	implementationSessions := make([]daemonstate.Session, 0, len(sessions))
	for _, session := range sessions {
		if session.Role == daemonstate.SessionRoleAdvisor {
			continue
		}
		implementationSessions = append(implementationSessions, session)
	}
	return sessionProjectionAggregateByIssueKey(implementationSessions, namingScope)
}

func sessionProjectionCanRecreateTmuxSession(session daemonstate.Session) bool {
	if session.Role == daemonstate.SessionRoleAdvisor {
		return false
	}
	if isAgentScopedSessionID(session.ID) {
		return false
	}
	switch daemonstate.NormalizeSessionState(session.State) {
	case daemonstate.SessionStateStarting, daemonstate.SessionStateRunning, daemonstate.SessionStatePaused:
		return true
	default:
		return false
	}
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
		if daemonSessionProjectionStopped(session) {
			byIssueKey[key] = session
			continue
		}
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
		Lines:      cmd.Lines,
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
				continue
			}
			if sessionNameHasIssueSuffix(name, typedIssueID.String()) {
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

func (d *Daemon) issueSessionExistsForInvariant(ctx context.Context, projectID, issueID, canonicalSessionID string, source daemonInvariantSource) (bool, error) {
	names, err := d.tmuxSessionNamesForIssue(ctx, projectID, issueID, canonicalSessionID, source)
	if err != nil {
		return false, err
	}
	return len(names) > 0, nil
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

func (d *Daemon) handleSessionStartDirect(ctx context.Context, req protocol.RequestEnvelope) (resp protocol.ResponseEnvelope, err error) {
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
	exists, err := d.issueSessionExistsForInvariant(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID, startConflictSource)
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
	originalIssueStatus := task.Status
	if err := d.ensureSessionStartIssueLifecycle(ctx, issueClient, cmd.IssueID, originalIssueStatus); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("prepare issue lifecycle for session start: %v", err)), nil
	}
	var (
		worktreeManager             *git.WorktreeManager
		startedWorktreePath         string
		startedWorktreeReused       bool
		worktreeProjectionPersisted bool
	)
	rollbackIssueLifecycle := originalIssueStatus != domain.StatusInProgress
	defer func() {
		if rollbackIssueLifecycle && (err != nil || !resp.OK) {
			if worktreeProjectionPersisted && !startedWorktreeReused && startedWorktreePath != "" {
				d.runtimeProjectionStateWriter().DeleteWorktreeProjectionAndPublish(ctx, cmd.ProjectID, cmd.IssueID)
				_ = d.cleanupNewWorktreeAfterInitFailure(ctx, cmd.ProjectID, worktreeManager, cmd.IssueID, startedWorktreePath, startedWorktreeReused)
			}
			d.rollbackSessionStartIssueLifecycle(ctx, issueClient, cmd.IssueID, originalIssueStatus)
		}
	}()
	task.Status = domain.StatusInProgress
	worktreeManager = d.worktreeManagerForProject(cmd.ProjectID)
	if worktreeManager == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "worktree manager unavailable"), nil
	}
	baseBranch := strings.TrimSpace(cmd.BaseBranch)
	if baseBranch == "" {
		baseBranch = strings.TrimSpace(d.baseBranchForProject(cmd.ProjectID))
	}
	baseBranchAncestorIssueID := ""
	ensuredAncestors, err := ensureAncestorWorktrees(ctx, cmd.ProjectID, task, baseBranch, worktreeManager, func(ctx context.Context, issueID string) (domain.Task, error) {
		return issueClient.GetWithRuntime(ctx, cmd.ProjectID, issueID)
	}, d.runtimeProjectionStateWriter(), func(ctx context.Context, initCtx worktreeInitContext) error {
		if len(d.runtimeConfigForProject(cmd.ProjectID).WorktreeInitCommands) > 0 {
			reportSessionStartProgress(ctx, "worktree_preflight", fmt.Sprintf("running worktree init commands for %s", initCtx.IssueID), 20)
		}
		if err := d.runWorktreeSyncInitCommands(ctx, initCtx); err != nil {
			cleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, initCtx.ProjectID, worktreeManager, initCtx.IssueID, initCtx.WorktreePath, false)
			return fmt.Errorf("%w%s", err, cleanupNote)
		}
		d.startWorktreeAsyncInitCommands(initCtx)
		return nil
	})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	baseBranch = ensuredAncestors.BaseBranch
	baseBranchAncestorIssueID = ensuredAncestors.AncestorIssueID
	reportSessionStartProgress(ctx, "worktree_preflight", fmt.Sprintf("creating or reusing worktree from %s", baseBranch), 25)
	worktree, err := worktreeManager.CreateWithTitle(ctx, cmd.IssueID, task.Title, baseBranch)
	reusedWorktree := false
	worktreeCreateError := ""
	worktreeSetupWarning := ""
	if err != nil {
		worktreeCreateError = err.Error()
		if errors.Is(err, git.ErrWorktreeAlreadyExists) {
			recoveredWorktree, recoverErr := worktreeManager.Get(ctx, cmd.IssueID)
			if recoverErr != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("worktree already exists but could not be loaded: %v", recoverErr)), nil
			}
			worktree = recoveredWorktree
			reusedWorktree = true
			worktreeSetupWarning = fmt.Sprintf("Worktree setup warning: worktree already existed for %s; reused existing worktree at %s without running worktree init commands.", cmd.IssueID, worktree.Path)
		} else if materializedWorktree, recoverErr := worktreeManager.Get(ctx, cmd.IssueID); recoverErr == nil {
			cleanupNote := d.cleanupWorktreeAfterCreateFailure(ctx, cmd.ProjectID, worktreeManager, cmd.IssueID, materializedWorktree.Path)
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("git worktree create failed after materializing worktree",
					"project_id", cmd.ProjectID,
					"issue_id", cmd.IssueID,
					"session_id", cmd.SessionID,
					"worktree", materializedWorktree.Path,
					"branch", materializedWorktree.Branch,
					"worktree_create_error", worktreeCreateError,
				)
			}
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("worktree create failed for %s: %v%s", cmd.IssueID, err, cleanupNote)), nil
		} else {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("git worktree create failed",
					"project_id", cmd.ProjectID,
					"issue_id", cmd.IssueID,
					"session_id", cmd.SessionID,
					"worktree_create_error", worktreeCreateError,
					"materialized_worktree_lookup_error", recoverErr,
				)
			}
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
	}
	startedWorktreePath = worktree.Path
	startedWorktreeReused = reusedWorktree
	if !reusedWorktree {
		if len(d.runtimeConfigForProject(cmd.ProjectID).WorktreeInitCommands) > 0 {
			reportSessionStartProgress(ctx, "worktree_preflight", "running worktree init commands", 35)
		}
		if err := d.runWorktreeSyncInitCommands(ctx, worktreeInitContext{
			ProjectID:          normalizedProjectID(cmd.ProjectID),
			IssueID:            cmd.IssueID,
			WorktreePath:       worktree.Path,
			ParentIssueID:      baseBranchAncestorIssueID,
			ParentWorktreePath: ensuredAncestors.AncestorWorktreePath,
		}); err != nil {
			cleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, cmd.ProjectID, worktreeManager, cmd.IssueID, worktree.Path, reusedWorktree)
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("worktree init failed for %s: %v%s", cmd.IssueID, err, cleanupNote)), nil
		}
		d.startWorktreeAsyncInitCommands(worktreeInitContext{
			ProjectID:          normalizedProjectID(cmd.ProjectID),
			IssueID:            cmd.IssueID,
			WorktreePath:       worktree.Path,
			ParentIssueID:      baseBranchAncestorIssueID,
			ParentWorktreePath: ensuredAncestors.AncestorWorktreePath,
		})
	}
	reportSessionStartProgress(ctx, "issue_resources", "preparing issue resources", 50)
	resourceCtx := d.issueResourceLifecycleContext(cmd.ProjectID, cmd.IssueID, cmd.SessionID, worktree.Path, worktree.Branch)
	resourcePrep, err := d.runIssueResourcePrepareCommands(ctx, cmd.ProjectID, resourceCtx)
	if err != nil {
		cleanupNote := d.issueResourceFailedStartCleanupNote(ctx, cmd.ProjectID, resourceCtx)
		worktreeCleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, cmd.ProjectID, worktreeManager, cmd.IssueID, worktree.Path, reusedWorktree)
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("issue resource prepare failed for %s: %v%s%s", cmd.IssueID, err, cleanupNote, worktreeCleanupNote)), nil
	}
	if _, err := d.runIssueResourceReconcileCommand(ctx, cmd.ProjectID, resourceCtx, "present"); err != nil {
		cleanupNote := d.issueResourceFailedStartCleanupNote(ctx, cmd.ProjectID, resourceCtx)
		worktreeCleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, cmd.ProjectID, worktreeManager, cmd.IssueID, worktree.Path, reusedWorktree)
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("issue resource reconcile present failed for %s: %v%s%s", cmd.IssueID, err, cleanupNote, worktreeCleanupNote)), nil
	}
	imagePaths, imageErr := d.prepareSessionStartImagePaths(ctx, cmd.ProjectID, cmd.IssueID, worktree.Path, cmd.ImagePaths)
	if imageErr != nil {
		cleanupNote := d.issueResourceFailedStartCleanupNote(ctx, cmd.ProjectID, resourceCtx)
		worktreeCleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, cmd.ProjectID, worktreeManager, cmd.IssueID, worktree.Path, reusedWorktree)
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("prepare session images failed for %s: %v%s%s", cmd.IssueID, imageErr, cleanupNote, worktreeCleanupNote)), nil
	}
	cmd.ImagePaths = imagePaths
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
			"worktree_create_error", worktreeCreateError,
		)
		d.cfg.Logger.Info("daemon session start images prepared",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"image_count", len(cmd.ImagePaths),
			"image_paths", cmd.ImagePaths,
		)
	}
	projectionWriter := d.runtimeProjectionStateWriter()
	if err := projectionWriter.PersistWorktreeProjection(ctx, cmd.ProjectID, cmd.IssueID, worktree.Path, worktree.Branch); err != nil {
		cleanupNote := d.issueResourceFailedStartCleanupNote(ctx, cmd.ProjectID, resourceCtx)
		worktreeCleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, cmd.ProjectID, worktreeManager, cmd.IssueID, worktree.Path, reusedWorktree)
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("persist worktree runtime projection for %s: %v%s%s", cmd.IssueID, err, cleanupNote, worktreeCleanupNote)), nil
	}
	worktreeProjectionPersisted = true
	projectionWriter.PublishWorktreeProjectionEvent(ctx, cmd.ProjectID, cmd.IssueID, worktree.Path)
	sessionInitMarker := sessionInitReadyMarker{}
	if cmd.StartWork {
		var markerErr error
		sessionInitMarker, markerErr = d.prepareSessionInitReadyMarker(cmd.ProjectID, worktree.Path, cmd.IssueID, cmd.SessionID)
		if markerErr != nil {
			cleanupNote := d.issueResourceFailedStartRollbackNote(ctx, cmd.ProjectID, cmd.SessionID, resourceCtx)
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("prepare session init marker: %v%s", markerErr, cleanupNote)), nil
		}
	}
	postLaunchPrompt := ""
	launchCommand := ""
	initialPromptBytes := 0
	if cmd.StartWork {
		initialPrompt := strings.TrimSpace(cmd.Prompt)
		if initialPrompt == "" {
			parentIssueID := ""
			if task.ParentID != nil {
				parentIssueID = strings.TrimSpace(task.ParentID.String())
			}
			initialPrompt = buildStartWorkPrompt(cmd.IssueID, task.Type.String(), task.Title, parentIssueID != "", parentIssueID)
		}
		initialPromptBytes = len(initialPrompt)
		launchPrompt := initialPrompt
		if d.sessionLaunchNeedsPostLaunchPrompt(cmd.ProjectID, initialPrompt) {
			launchPrompt = ""
			postLaunchPrompt = initialPrompt
		}
		launchCommand = d.buildSessionLaunchCommandWithInitReadyPathAndEnv(cmd.ProjectID, cmd.IssueID, cmd.SessionID, cmd.Yolo, cmd.ImagePaths, launchPrompt, sessionInitMarker.RelativePath, d.sessionLaunchStartupExportCommands(d.runtimeConfigForProject(cmd.ProjectID), resourceCtx))
		if launchPrompt != "" && d.sessionLaunchCommandNeedsPostLaunchPrompt(cmd.ProjectID, launchCommand) {
			launchPrompt = ""
			postLaunchPrompt = initialPrompt
			launchCommand = d.buildSessionLaunchCommandWithInitReadyPathAndEnv(cmd.ProjectID, cmd.IssueID, cmd.SessionID, cmd.Yolo, cmd.ImagePaths, launchPrompt, sessionInitMarker.RelativePath, d.sessionLaunchStartupExportCommands(d.runtimeConfigForProject(cmd.ProjectID), resourceCtx))
		}
	}
	reportSessionStartProgress(ctx, "tmux_launch", "creating tmux session", 70)
	if cmd.StartWork {
		if err := d.tmux.NewSessionWithCommandAndEnvironment(ctx, cmd.SessionID, worktree.Path, launchCommand, d.sessionManagedEnvironment()); err != nil {
			cleanupNote := d.issueResourceFailedStartCleanupNote(ctx, cmd.ProjectID, resourceCtx)
			return d.errorResponse(req, protocol.ErrorCodeInternal, sessionStartLaunchFailureMessage("tmux_launch", cmd, worktree.Path, launchCommand, true, err, cleanupNote)), nil
		}
		if err := d.setSessionContextEnv(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID); err != nil {
			cleanupNote := d.issueResourceFailedStartRollbackNote(ctx, cmd.ProjectID, cmd.SessionID, resourceCtx)
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("set session context env: %v%s", err, cleanupNote)), nil
		}
		if err := d.setIssueResourceSessionEnv(ctx, cmd.ProjectID, resourceCtx); err != nil {
			cleanupNote := d.issueResourceFailedStartRollbackNote(ctx, cmd.ProjectID, cmd.SessionID, resourceCtx)
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("set issue resource env: %v%s", err, cleanupNote)), nil
		}
		d.startSessionAsyncInitCommands(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID, worktree.Path)
		if len(d.runtimeConfigForProject(cmd.ProjectID).SessionSyncInitCommands) > 0 {
			reportSessionStartProgress(ctx, "init_commands", "launch sent; configured init commands likely running before agent hooks", 90)
		} else {
			reportSessionStartProgress(ctx, "agent_launch", "launch sent; waiting for agent activity", 90)
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("daemon session start launch command installed",
				"project_id", cmd.ProjectID,
				"issue_id", cmd.IssueID,
				"session_id", cmd.SessionID,
				"prompt_bytes", initialPromptBytes,
			)
		}
	} else {
		if err := d.tmux.NewSessionWithEnvironment(ctx, cmd.SessionID, worktree.Path, d.sessionManagedEnvironment()); err != nil {
			cleanupNote := d.issueResourceFailedStartCleanupNote(ctx, cmd.ProjectID, resourceCtx)
			return d.errorResponse(req, protocol.ErrorCodeInternal, sessionStartLaunchFailureMessage("tmux_launch", cmd, worktree.Path, "", false, err, cleanupNote)), nil
		}
		if err := d.exportSessionContextEnv(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID); err != nil {
			cleanupNote := d.issueResourceFailedStartRollbackNote(ctx, cmd.ProjectID, cmd.SessionID, resourceCtx)
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("export session context env: %v%s", err, cleanupNote)), nil
		}
		if err := d.exportIssueResourceSessionEnv(ctx, cmd.ProjectID, resourceCtx); err != nil {
			cleanupNote := d.issueResourceFailedStartRollbackNote(ctx, cmd.ProjectID, cmd.SessionID, resourceCtx)
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("export issue resource env: %v%s", err, cleanupNote)), nil
		}
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
		cleanupNote := d.compensateSessionStartFailure(ctx, req, cmd.ProjectID, cmd.SessionID, cmd.IssueID, resourceCtx, initialActivity, initialActivitySource)
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session start transition: %v%s", err, cleanupNote)), nil
	}
	// The tmux session exists at this point. Record that physical fact through
	// the versioned observation authority; the lifecycle transition above owns
	// desired intent only and must not fabricate observed activity.
	if err := d.persistObservedRuntimeProjection(ctx, cmd.ProjectID, req.Meta, daemonstate.Session{
		ID: cmd.SessionID, ObservedState: daemonstate.SessionStateRunning,
		Activity: initialActivity, ActivitySource: initialActivitySource, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		cleanupNote := d.compensateSessionStartFailure(ctx, req, cmd.ProjectID, cmd.SessionID, cmd.IssueID, resourceCtx, initialActivity, initialActivitySource)
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session runtime observation: %v%s", err, cleanupNote)), nil
	}
	if task.Type == domain.TypeEpic {
		if err := d.acquireRootedOrchestratorLease(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID); err != nil {
			cleanupNote := d.compensateSessionStartFailure(ctx, req, cmd.ProjectID, cmd.SessionID, cmd.IssueID, resourceCtx, initialActivity, initialActivitySource)
			return d.errorResponse(req, protocol.ErrorCodeConflict, fmt.Sprintf("acquire rooted orchestrator lease: %v%s", err, cleanupNote)), nil
		}
	}
	rollbackIssueLifecycle = false
	if cmd.StartWork && sessionInitMarker.AbsolutePath != "" {
		if err := d.waitForSessionInitReady(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID, sessionInitMarker); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
	}
	if cmd.StartWork && strings.TrimSpace(postLaunchPrompt) != "" {
		reportSessionStartProgress(ctx, "agent_prompt", "agent launched; delivering initial prompt", 95)
		if err := d.waitBeforeSessionResume(ctx, sessionRestartContinuePromptDelay); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		if err := d.tmux.PasteTextAndSubmit(ctx, cmd.SessionID, postLaunchPrompt); err != nil {
			cleanupNote := d.issueResourceFailedStartRollbackNote(ctx, cmd.ProjectID, cmd.SessionID, resourceCtx)
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()+cleanupNote), nil
		}
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
			"worktree_create_error", worktreeCreateError,
		)
	}
	return d.commandOutput(req, output), nil
}

func (d *Daemon) acquireRootedOrchestratorLease(ctx context.Context, projectID, issueID, sessionID string) error {
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil || d.tmux == nil {
		return nil
	}
	scope, err := domain.RootedOrchestrationScope(issueID)
	if err != nil {
		return err
	}
	identity, err := domain.NewOrchestratorIdentity(d.canonicalProjectID(projectID), scope)
	if err != nil {
		return err
	}
	_, err = daemonstate.NewOrchestratorLeaseAuthority(store).Acquire(ctx, identity, sessionID, d.tmux.HasSession)
	return err
}

func (d *Daemon) ensureSessionStartIssueLifecycle(ctx context.Context, issueClient sessionStartIssueLifecycleUpdater, issueID string, status domain.Status) error {
	if issueClient == nil {
		return errors.New("issue store unavailable")
	}
	if status == domain.StatusInProgress {
		return nil
	}
	if err := issueClient.Update(ctx, issueID, domain.StatusInProgress); err != nil {
		return err
	}
	return nil
}

func (d *Daemon) rollbackSessionStartIssueLifecycle(ctx context.Context, issueClient sessionStartIssueLifecycleUpdater, issueID string, previousStatus domain.Status) {
	if issueClient == nil || previousStatus == "" || previousStatus == domain.StatusInProgress {
		return
	}
	if err := issueClient.Update(ctx, issueID, previousStatus); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("rollback session start issue lifecycle failed",
			"issue_id", issueID,
			"status", previousStatus,
			"error", err,
		)
	}
}

func (d *Daemon) rollbackSessionStartLifecycle(projectID, sessionID, issueID string) {
	if d == nil || d.sessionStore == nil {
		return
	}
	if _, err := d.sessionStore.ForceUpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStopped); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("rollback session start lifecycle state failed",
			"project_id", projectID,
			"session_id", sessionID,
			"issue_id", issueID,
			"error", err,
		)
	}
}

func (d *Daemon) compensateSessionStartFailure(ctx context.Context, req protocol.RequestEnvelope, projectID, sessionID, issueID string, resourceCtx issueResourceLifecycleContext, liveActivity, liveActivitySource string) string {
	note := d.issueResourceFailedStartCleanupNote(ctx, projectID, resourceCtx)
	live := true
	if d != nil && d.tmux != nil {
		if err := d.tmux.KillSession(ctx, sessionID); err == nil {
			live = false
		} else {
			note += fmt.Sprintf("; failed-start tmux cleanup also failed: %v", err)
			if present, probeErr := d.tmux.HasSession(ctx, sessionID); probeErr == nil {
				live = present
			} else {
				note += fmt.Sprintf("; failed-start tmux reconciliation also failed: %v", probeErr)
			}
		}
	}
	desired, observed := daemonstate.SessionStateStopped, daemonstate.SessionStateStopped
	activity, source := "", ""
	if live {
		desired, observed = daemonstate.SessionStateRunning, daemonstate.SessionStateRunning
		activity, source = liveActivity, liveActivitySource
	}
	updatedAt := time.Now().UTC()
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	alignTransient := false
	if store != nil {
		rows, winner, err := store.ApplyWorkerSessionCompensation(ctx, projectID, sessionID, issueID, desired, observed, activity, source, updatedAt)
		if err != nil {
			note += fmt.Sprintf("; failed-start durable session compensation also failed: %v", err)
			if durable, found, loadErr := store.GetWorkerSessionStateByIssueID(ctx, projectID, issueID, sessionID); loadErr == nil && found {
				desired = durable.State
				alignTransient = true
			} else if loadErr != nil {
				note += fmt.Sprintf("; failed-start durable session reload also failed: %v", loadErr)
			}
		} else {
			desired = winner
			alignTransient = true
			writer := d.runtimeProjectionStateWriter()
			for _, row := range rows {
				writer.PublishSessionProjectionEvent(ctx, projectID, req.Meta, row)
			}
		}
	}
	if alignTransient && d != nil && d.sessionStore != nil {
		if _, err := d.sessionStore.ForceUpsertSession(projectID, sessionID, issueID, desired); err != nil {
			note += fmt.Sprintf("; failed-start transient session compensation also failed: %v", err)
		}
	}
	return note
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
	if !d.sessionLifecycleOrAgentActivityTransitionNeeded(cmd.ProjectID, cmd.SessionID, cmd.IssueID, daemonstate.SessionStatePaused) {
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
	if !d.sessionLifecycleOrAgentActivityTransitionNeeded(cmd.ProjectID, cmd.SessionID, cmd.IssueID, daemonstate.SessionStateAttached) {
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

func (d *Daemon) handleSessionRestartAll(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var body protocol.SessionRestartAllRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	projectID := protocol.TrimProjectID(body.ProjectID.String())
	if projectID == "" {
		projectID = req.Meta.ProjectID.String()
	}
	projectID = d.canonicalProjectID(projectID)
	if d.tmux == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "tmux client unavailable"), nil
	}

	projectIDs := d.sessionRestartAllProjectIDs(projectID)
	liveSessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	livePaneSessions, err := d.sessionRestartAllLivePaneSessions(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	typedProjectIDs := make([]naming.ProjectID, 0, len(projectIDs))
	for _, targetProjectID := range projectIDs {
		typedProjectIDs = append(typedProjectIDs, naming.ProjectID(targetProjectID))
	}
	result := protocol.SessionRestartAllResponseBody{
		ProjectID:  naming.ProjectID(projectID),
		ProjectIDs: typedProjectIDs,
		ForceBusy:  body.ForceBusy,
		Sessions:   make([]protocol.SessionRestartAllItem, 0, len(liveSessions)),
	}

	restartTargetsBySession := make(map[string]sessionRestartAllTarget, len(liveSessions))
	for _, targetProjectID := range projectIDs {
		activityByIssueKey := d.sessionRestartActivityByIssueKey(ctx, targetProjectID)
		projectedSessions := d.sessionRestartAllProjectedSessionIDs(ctx, targetProjectID)
		namingScope := d.sessionNamingScope(targetProjectID)
		for _, sessionID := range liveSessions {
			sessionID = strings.TrimSpace(sessionID)
			if sessionID == "" {
				continue
			}
			if !strings.HasPrefix(sessionID, naming.ProjectSessionPrefix(namingScope)+"-") {
				continue
			}
			issueID, ok := naming.ParseIssueIDFromSessionName(sessionID, namingScope)
			if !ok {
				continue
			}
			activity := "unknown"
			if display, found := activityByIssueKey[sessionKey(issueID)]; found && strings.TrimSpace(display.Activity) != "" {
				activity = display.Activity
			}
			target := sessionRestartAllTarget{
				ProjectID: targetProjectID,
				SessionID: sessionID,
				IssueID:   issueID,
				Activity:  activity,
			}
			if display, found := activityByIssueKey[sessionKey(issueID)]; found {
				target.ActivitySource = display.Source
			}
			if _, projected := projectedSessions[sessionID]; projected {
				target.Projected = true
			}
			if _, tmuxReady := livePaneSessions[sessionID]; tmuxReady {
				target.TmuxReady = true
			}
			target.ActiveIntent = sessionRestartActiveIntent(target.Activity)
			if existing, found := restartTargetsBySession[sessionID]; !found || sessionRestartAllTargetPreferred(target, existing) {
				restartTargetsBySession[sessionID] = target
			}
		}
	}
	restartTargets := make([]sessionRestartAllTarget, 0, len(restartTargetsBySession))
	for _, target := range restartTargetsBySession {
		restartTargets = append(restartTargets, target)
	}
	sort.Slice(restartTargets, func(i, j int) bool {
		if restartTargets[i].ProjectID != restartTargets[j].ProjectID {
			return restartTargets[i].ProjectID < restartTargets[j].ProjectID
		}
		return restartTargets[i].SessionID < restartTargets[j].SessionID
	})

	for _, target := range restartTargets {
		item := protocol.SessionRestartAllItem{
			ProjectID:      naming.ProjectID(target.ProjectID),
			SessionID:      naming.SessionID(target.SessionID),
			IssueID:        naming.IssueID(target.IssueID),
			Activity:       target.Activity,
			ActivitySource: target.ActivitySource,
			TmuxReady:      target.TmuxReady,
			ActiveIntent:   target.ActiveIntent,
		}
		if !item.TmuxReady {
			item.Skipped = true
			item.Reason = "no_tmux_pane"
			result.Skipped++
			result.Sessions = append(result.Sessions, item)
			continue
		}

		if err := d.tmux.SendKey(ctx, target.SessionID, "C-c"); err != nil {
			item.Error = err.Error()
			result.Failed++
			result.Sessions = append(result.Sessions, item)
			continue
		}
		if err := d.waitBeforeSessionResume(ctx, 250*time.Millisecond); err != nil {
			item.Error = err.Error()
			result.Failed++
			result.Sessions = append(result.Sessions, item)
			continue
		}
		if err := d.setSessionManagedPathEnv(ctx, target.SessionID); err != nil {
			item.Error = err.Error()
			result.Failed++
			result.Sessions = append(result.Sessions, item)
			continue
		}
		resumeCommand := d.buildSessionResumeCommand(target.ProjectID, target.IssueID, target.SessionID, body.Yolo, body.ImagePaths)
		if err := d.tmux.SendKeys(ctx, target.SessionID, resumeCommand); err != nil {
			item.Error = err.Error()
			result.Failed++
			result.Sessions = append(result.Sessions, item)
			continue
		}
		if item.ActiveIntent && d.sessionRestartNeedsPostLaunchPrompt(target.ProjectID) {
			if err := d.waitBeforeSessionResume(ctx, sessionRestartContinuePromptDelay); err != nil {
				item.Error = err.Error()
				result.Failed++
				result.Sessions = append(result.Sessions, item)
				continue
			}
			if err := d.tmux.PasteTextAndSubmit(ctx, target.SessionID, sessionRestartContinuePrompt); err != nil {
				item.Error = err.Error()
				result.Failed++
				result.Sessions = append(result.Sessions, item)
				continue
			}
		}
		item.Restarted = true
		result.Restarted++
		if target.IssueID != "" {
			d.persistRestartedSessionProjection(ctx, target.ProjectID, target.SessionID, target.IssueID)
		}
		result.Sessions = append(result.Sessions, item)
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session restart-all completed",
			"project_id", projectID,
			"force_busy", body.ForceBusy,
			"restarted", result.Restarted,
			"skipped", result.Skipped,
			"failed", result.Failed,
		)
	}
	resp := d.successResponse(req)
	encoded, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal response body: %v", err)), nil
	}
	resp.Body = encoded
	return resp, nil
}

type sessionRestartAllTarget struct {
	ProjectID      string
	SessionID      string
	IssueID        string
	Activity       string
	ActivitySource string
	Projected      bool
	TmuxReady      bool
	ActiveIntent   bool
}

func sessionRestartAllTargetPreferred(candidate, existing sessionRestartAllTarget) bool {
	if candidate.Projected != existing.Projected {
		return candidate.Projected
	}
	if candidate.ProjectID != existing.ProjectID {
		return candidate.ProjectID < existing.ProjectID
	}
	return candidate.SessionID < existing.SessionID
}

func (d *Daemon) sessionRestartAllLivePaneSessions(ctx context.Context) (map[string]struct{}, error) {
	if d == nil || d.tmux == nil {
		return nil, nil
	}
	panes, err := d.tmux.ListPaneInfos(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make(map[string]struct{}, len(panes))
	for _, pane := range panes {
		sessionName := strings.TrimSpace(pane.SessionName)
		if sessionName == "" {
			continue
		}
		sessions[sessionName] = struct{}{}
	}
	return sessions, nil
}

func sessionRestartActiveIntent(activity string) bool {
	switch strings.ToLower(strings.TrimSpace(activity)) {
	case "busy", "starting", "working":
		return true
	default:
		return false
	}
}

func (d *Daemon) sessionRestartAllProjectedSessionIDs(ctx context.Context, projectID string) map[string]struct{} {
	projectID = d.canonicalProjectID(projectID)
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return nil
	}
	sessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		if d != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load restart-all session projections failed", "project_id", projectID, "error", err)
		}
		return nil
	}
	ids := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			continue
		}
		ids[sessionID] = struct{}{}
	}
	return ids
}

func (d *Daemon) sessionRestartAllProjectIDs(requestProjectID string) []string {
	seen := map[string]struct{}{}
	var projectIDs []string
	addProjectID := func(projectID string) {
		projectID = d.canonicalProjectID(projectID)
		if strings.TrimSpace(projectID) == "" {
			return
		}
		if _, exists := seen[projectID]; exists {
			return
		}
		seen[projectID] = struct{}{}
		projectIDs = append(projectIDs, projectID)
	}
	addProjectID(requestProjectID)
	if repoDir := strings.TrimSpace(d.cfg.RepoDir); repoDir != "" {
		if baseProjectID, err := appconfig.ProjectIDForRoot(repoDir); err == nil {
			addProjectID(baseProjectID)
		}
	}
	var runtimeProjectIDs []string
	d.runtimeStoresMu.Lock()
	for projectID := range d.runtimeStoresByProject {
		runtimeProjectIDs = append(runtimeProjectIDs, projectID)
	}
	d.runtimeStoresMu.Unlock()
	for _, projectID := range runtimeProjectIDs {
		addProjectID(projectID)
	}
	if d.sessionStore != nil {
		for _, projectID := range d.sessionStore.ProjectIDs() {
			addProjectID(projectID)
		}
	}
	if registry, err := appconfig.LoadProjectsRegistry(); err == nil && registry != nil {
		for _, project := range registry.Projects {
			repoDir := strings.TrimSpace(project.Path)
			if repoDir != "" {
				if projectID, hashErr := appconfig.ProjectIDForRoot(repoDir); hashErr == nil {
					addProjectID(projectID)
					continue
				}
			}
			if strings.TrimSpace(project.Name) != "" {
				addProjectID(project.Name)
			}
		}
	}
	sort.Strings(projectIDs)
	return projectIDs
}

func waitBeforeSessionResume(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (d *Daemon) waitBeforeSessionResume(ctx context.Context, delay time.Duration) error {
	if d != nil && d.sessionResumeWait != nil {
		return d.sessionResumeWait(ctx, delay)
	}
	return waitBeforeSessionResume(ctx, delay)
}

func (d *Daemon) persistRestartedSessionProjection(ctx context.Context, projectID, sessionID, issueID string) {
	session := daemonstate.Session{
		ID:             sessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "session",
		UpdatedAt:      time.Now().UTC(),
	}
	if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, session); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("persist restarted session projection failed",
			"project_id", projectID,
			"session_id", sessionID,
			"issue_id", issueID,
			"error", err,
		)
	}
	if err := d.persistObservedRuntimeProjection(ctx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, session); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("persist restarted physical runtime observation failed", "project_id", projectID, "session_id", sessionID, "error", err)
	}
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

	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		prompt = buildConflictResolutionPrompt(issueIDString, conflictFiles)
	}
	launchPrompt := prompt
	postLaunchPrompt := ""
	if d.sessionLaunchNeedsPostLaunchPrompt(projectID, prompt) {
		launchPrompt = ""
		postLaunchPrompt = prompt
	}
	targetPane := sessionName + ":" + sessionConflictWindowName
	launchCommand := d.buildSessionLaunchCommand(projectID, issueIDString, canonicalSessionID, body.Yolo, body.ImagePaths, launchPrompt)
	if err := d.setSessionManagedPathEnv(ctx, sessionName); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("set conflict session PATH: %v", err)), nil
	}
	reusedWindow, err := d.tmux.EnsureWindowWithCommandAndEnvironment(ctx, sessionName, sessionConflictWindowName, worktreePath, launchCommand, d.sessionManagedEnvironment())
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("launch conflict resolver window: %v", err)), nil
	}
	if strings.TrimSpace(postLaunchPrompt) != "" {
		if err := d.waitBeforeSessionResume(ctx, sessionRestartContinuePromptDelay); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		if err := d.tmux.PasteTextAndSubmit(ctx, targetPane, postLaunchPrompt); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
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
		return worktree.Path, worktree.Branch, true, nil
	}

	baseBranch := d.baseBranchForProject(projectID)
	worktree, createErr := worktreeManager.CreateWithTitle(ctx, issueID, issueTitle, baseBranch)
	if createErr != nil {
		if errors.Is(createErr, git.ErrWorktreeAlreadyExists) {
			if recoveredWorktree, recoverErr := worktreeManager.Get(ctx, issueID); recoverErr == nil {
				return recoveredWorktree.Path, recoveredWorktree.Branch, true, nil
			}
			return "", "", false, createErr
		}
		if materializedWorktree, recoverErr := worktreeManager.Get(ctx, issueID); recoverErr == nil {
			cleanupNote := d.cleanupWorktreeAfterCreateFailure(ctx, projectID, worktreeManager, issueID, materializedWorktree.Path)
			return "", "", false, fmt.Errorf("worktree create failed for %s: %w%s", issueID, createErr, cleanupNote)
		}
		return "", "", false, createErr
	}
	if err := d.ensureSessionStartWorktreeClean(ctx, worktreeManager, issueID, worktree.Path); err != nil {
		cleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, projectID, worktreeManager, issueID, worktree.Path, false)
		return "", "", false, fmt.Errorf("%w%s", err, cleanupNote)
	}
	initCtx := worktreeInitContext{
		ProjectID:    normalizedProjectID(projectID),
		IssueID:      issueID,
		WorktreePath: worktree.Path,
	}
	if err := d.runWorktreeSyncInitCommands(ctx, initCtx); err != nil {
		cleanupNote := d.cleanupNewWorktreeAfterInitFailure(ctx, projectID, worktreeManager, issueID, worktree.Path, false)
		return "", "", false, fmt.Errorf("worktree init failed for %s: %w%s", issueID, err, cleanupNote)
	}
	d.startWorktreeAsyncInitCommands(initCtx)
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

func (d *Daemon) cleanupNewWorktreeAfterInitFailure(ctx context.Context, projectID string, worktreeManager *git.WorktreeManager, issueID, worktreePath string, reusedWorktree bool) string {
	if reusedWorktree || worktreeManager == nil {
		return ""
	}
	removedWorktree, err := worktreeManager.DeleteWithOptions(ctx, issueID, git.WorktreeDeleteOptions{
		Force:         true,
		BranchCleanup: git.WorktreeBranchCleanupRequired,
	})
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("failed to cleanup worktree after init failure",
				"issue_id", issueID,
				"worktree", worktreePath,
				"error", err,
			)
		}
		return fmt.Sprintf(" (cleanup failed for worktree %s: %v)", worktreePath, err)
	}
	if err := finalizeDeletedWorktree(ctx, projectID, issueID, worktreeManager, removedWorktree, d.runtimeProjectionStateWriter()); err != nil {
		return fmt.Sprintf(" (cleaned up worktree %s; %v)", worktreePath, err)
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("cleaned up worktree after init failure",
			"issue_id", issueID,
			"worktree", worktreePath,
		)
	}
	return fmt.Sprintf(" (cleaned up worktree %s)", worktreePath)
}

func (d *Daemon) cleanupWorktreeAfterCreateFailure(ctx context.Context, projectID string, worktreeManager *git.WorktreeManager, issueID, worktreePath string) string {
	if worktreeManager == nil {
		return ""
	}
	removedWorktree, err := worktreeManager.DeleteWithOptions(ctx, issueID, git.WorktreeDeleteOptions{
		Force:         true,
		BranchCleanup: git.WorktreeBranchCleanupRequired,
	})
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("failed to rollback worktree after create failure",
				"issue_id", issueID,
				"worktree", worktreePath,
				"error", err,
			)
		}
		return fmt.Sprintf(" (rollback failed for worktree %s: %v)", worktreePath, err)
	}
	if err := finalizeDeletedWorktree(ctx, projectID, issueID, worktreeManager, removedWorktree, d.runtimeProjectionStateWriter()); err != nil {
		return fmt.Sprintf(" (rolled back worktree %s; %v)", worktreePath, err)
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("rolled back worktree after create failure",
			"issue_id", issueID,
			"worktree", worktreePath,
		)
	}
	return fmt.Sprintf(" (rolled back worktree %s)", worktreePath)
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
	if err := d.tmux.NewSessionWithEnvironment(ctx, canonicalSessionID, worktreePath, d.sessionManagedEnvironment()); err != nil {
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
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	canonicalID := naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID)
	if existing, found, err := store.GetWorkerSessionStateByIssueID(context.Background(), projectID, issueID, canonicalID); err != nil {
		return fmt.Errorf("load logical session before stop projection: %w", err)
	} else if found && !isAgentScopedSessionID(existing.ID) {
		sessionID = existing.ID
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session := daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		Role:          daemonstate.SessionRoleWorker,
		ScopeKind:     daemonstate.SessionScopeIssue,
		ScopeID:       issueID,
		State:         daemonstate.SessionStateStopped,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     time.Now().UTC(),
	}
	if err := store.UpsertSessionState(ctx, projectID, session); err != nil {
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
	if _, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: session.ID, ObservedState: daemonstate.SessionStateStopped, UpdatedAt: session.UpdatedAt,
	}); err != nil {
		return err
	}
	rows, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return fmt.Errorf("list logical session observations before stop: %w", err)
	}
	for _, row := range rows {
		if strings.TrimSpace(row.IssueID) != issueID || !isAgentScopedSessionID(row.ID) {
			continue
		}
		row.State = daemonstate.SessionStateStopped
		row.ObservedState = daemonstate.SessionStateStopped
		row.TmuxAttachedCount = 0
		row.UpdatedAt = time.Now().UTC()
		if err := store.UpsertSessionState(ctx, projectID, row); err != nil {
			return fmt.Errorf("stop session observation %s: %w", row.ID, err)
		}
		if _, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
			ProjectID: projectID, SessionID: row.ID, ObservedState: daemonstate.SessionStateStopped, UpdatedAt: row.UpdatedAt,
		}); err != nil {
			return fmt.Errorf("stop physical session observation %s: %w", row.ID, err)
		}
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

func (d *Daemon) handleSessionCapture(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session capture request"), nil
	}
	lines := normalizeSessionCaptureLines(cmd.Lines)
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
	output, err := d.tmux.CapturePane(ctx, cmd.SessionID, lines)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("capture session pane: %v", err)), nil
	}
	body, err := json.Marshal(protocol.SessionCaptureResponseBody{
		ProjectID:  naming.ProjectID(cmd.ProjectID),
		IssueID:    naming.IssueID(cmd.IssueID),
		SessionID:  naming.SessionID(cmd.SessionID),
		Lines:      lines,
		Output:     output,
		CapturedAt: time.Now().UTC(),
	})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal session capture response: %v", err)), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session pane captured",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"lines", lines,
			"bytes", len(output),
		)
	}
	return resp, nil
}

func normalizeSessionCaptureLines(lines int) int {
	if lines <= 0 {
		return sessionCaptureDefaultLines
	}
	if lines > sessionCaptureMaxLines {
		return sessionCaptureMaxLines
	}
	return lines
}

func (d *Daemon) handleSessionStatus(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, false)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if err, unhealthy := d.projectIssueStoreHealthError(cmd.ProjectID); unhealthy {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, err.Error()), nil
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
		err = d.recordProjectIssueStoreFailure(cmd.ProjectID, err)
		return d.errorResponse(req, projectIssueStoreHealthErrorCode(err), err.Error()), nil
	}
	d.clearProjectIssueStoreHealth(cmd.ProjectID)
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
		} else if len(d.runtimeConfigForProject(cmd.ProjectID).SessionSyncInitCommands) > 0 && activity == "busy" && activitySource == "session" {
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
		state := daemonstate.NormalizeSessionState(session.State)
		observed := daemonstate.NormalizeSessionState(session.ObservedState)
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
		activity := activityByKey[key]
		activity.Total++
		updatedAt := session.UpdatedAt.UTC()
		switch sessionActivityState(session) {
		case domain.SessionError:
			activity.Error++
			if activity.LatestErrorAt.IsZero() || updatedAt.After(activity.LatestErrorAt) {
				activity.LatestErrorAt = updatedAt
			}
		case domain.SessionPaused, domain.SessionIdle, domain.SessionWaiting:
			activity.Paused++
			if activity.LatestPausedAt.IsZero() || updatedAt.After(activity.LatestPausedAt) {
				activity.LatestPausedAt = updatedAt
			}
		case domain.SessionBusy:
			activity.Active++
			if activity.LatestActiveAt.IsZero() || updatedAt.After(activity.LatestActiveAt) {
				activity.LatestActiveAt = updatedAt
			}
		}
		activityByKey[key] = activity
	}
	return activityByKey
}

func sessionActivityState(session daemonstate.Session) domain.SessionState {
	switch normalizeSessionActivity(session.Activity) {
	case string(domain.SessionBusy), "starting", "working":
		return domain.SessionBusy
	case string(domain.SessionError):
		return domain.SessionError
	case string(domain.SessionIdle), string(domain.SessionPaused), string(domain.SessionWaiting):
		return domain.SessionPaused
	}
	switch daemonstate.NormalizeSessionState(session.State) {
	case daemonstate.SessionStatePaused:
		return domain.SessionPaused
	case daemonstate.SessionStateStarting:
		return domain.SessionBusy
	default:
		// Pane observations prove tmux liveness, not agent activity. Older hook
		// writes left live pane rows without activity, so treat those as idle
		// unless a hook explicitly recorded busy.
		if isAgentScopedSessionID(session.ID) && strings.TrimSpace(session.Activity) == "" {
			return domain.SessionPaused
		}
		return domain.SessionBusy
	}
}

func sessionActivityLabel(activity sessionHookActivity) (string, string) {
	if activity.Total == 0 {
		return "unknown", "none"
	}
	if activity.Active > 0 {
		return "busy", "hooks"
	}
	if activity.Error > 0 {
		return "error", "hooks"
	}
	return "idle", "hooks"
}

func sessionHookActivityUpdatedAt(activity sessionHookActivity) time.Time {
	if activity.Total == 0 {
		return time.Time{}
	}
	if activity.Active > 0 {
		return activity.LatestActiveAt
	}
	if activity.Error > 0 {
		return activity.LatestErrorAt
	}
	return activity.LatestPausedAt
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

type sessionDisplayActivityOptions struct {
	includeSessionSourcedBusy bool
}

func explicitSessionActivity(session daemonstate.Session, opts sessionDisplayActivityOptions) (sessionDisplayActivity, bool) {
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
	source := normalizeSessionActivitySource(session.ActivitySource, "session")
	if activity == "busy" && source == "session" && !opts.includeSessionSourcedBusy {
		return sessionDisplayActivity{}, false
	}
	return sessionDisplayActivity{Activity: activity, Source: source, UpdatedAt: session.UpdatedAt.UTC()}, true
}

func sessionDisplayActivityByIssueKeyFromSessions(sessions []daemonstate.Session, namingScope string) map[string]sessionDisplayActivity {
	return sessionDisplayActivityByIssueKeyFromSessionsWithOptions(sessions, namingScope, sessionDisplayActivityOptions{})
}

func sessionDisplayActivityByIssueKeyFromSessionsWithOptions(sessions []daemonstate.Session, namingScope string, opts sessionDisplayActivityOptions) map[string]sessionDisplayActivity {
	out := make(map[string]sessionDisplayActivity)
	for key, session := range sessionProjectionAggregateByIssueKey(sessions, namingScope) {
		display, ok := explicitSessionActivity(session, opts)
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
		hookUpdatedAt := sessionHookActivityUpdatedAt(hookActivity)
		if existing, found := out[key]; found {
			if hookUpdatedAt.IsZero() && !existing.UpdatedAt.IsZero() {
				continue
			}
			if !hookUpdatedAt.IsZero() && existing.UpdatedAt.After(hookUpdatedAt) {
				continue
			}
		}
		out[key] = sessionDisplayActivity{Activity: activity, Source: source, UpdatedAt: hookUpdatedAt}
	}
	return out
}

func (d *Daemon) sessionDisplayActivityByIssueKey(ctx context.Context, projectID string) map[string]sessionDisplayActivity {
	return d.sessionActivityByIssueKey(ctx, projectID, sessionDisplayActivityOptions{})
}

func (d *Daemon) sessionRestartActivityByIssueKey(ctx context.Context, projectID string) map[string]sessionDisplayActivity {
	return d.sessionActivityByIssueKey(ctx, projectID, sessionDisplayActivityOptions{includeSessionSourcedBusy: true})
}

func (d *Daemon) sessionActivityByIssueKey(ctx context.Context, projectID string, opts sessionDisplayActivityOptions) map[string]sessionDisplayActivity {
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
	activity := sessionDisplayActivityByIssueKeyFromSessionsWithOptions(sessions, namingScope, opts)
	return d.applyTerminalFailureProbes(ctx, projectID, sessions, namingScope, activity)
}

func unknownActivityAdvice(issueID string) string {
	return fmt.Sprintf("activity unknown: inspect hooks with az ai status --target=auto; run az ai install --target=auto only if hooks are missing, outdated, or not installed; use `az orchestrate capture --issue %s` only if status/watch looks stale, failed, or contradictory", issueID)
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
	if strings.TrimSpace(sessionID) == "" {
		return d.reconcileTmuxAndDaemonSessionsForIssues(ctx, projectID, nil)
	}
	return d.reconcileTmuxAndDaemonSessionsForIssues(ctx, projectID, []string{sessionID})
}

func (d *Daemon) reconcileTmuxAndDaemonSessionsForIssues(ctx context.Context, projectID string, sessionIDs []string) (sessionRecoveryResult, error) {
	result := sessionRecoveryResult{}
	if d == nil || d.sessionStore == nil || d.tmux == nil {
		return result, nil
	}
	projectID = d.canonicalProjectID(projectID)
	targetIssueKeys := runtimeReconcileTargetIssueKeySet(sessionIDs)
	targeted := len(targetIssueKeys) > 0
	targetIssueID := strings.Join(normalizeRuntimeReconcileIssueIDs(sessionIDs), ",")
	matchesTargetIssue := func(issueKey string) bool {
		if !targeted {
			return true
		}
		_, ok := targetIssueKeys[issueKey]
		return ok
	}
	worktreeManager := d.worktreeManagerForProject(projectID)
	if worktreeManager == nil {
		return result, nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session reconcile started", "project_id", projectID, "target_issue_id", targetIssueID)
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
		session.ID = sessionIDToDelete
		if err := runtimeStore.DeleteSessionIntentState(ctx, projectID, session); err != nil && d.cfg.Logger != nil {
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
		session.ID = sessionIDToDelete
		if err := runtimeStore.DeleteSessionIntentState(ctx, projectID, session); err != nil {
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
	var err error
	tmuxSessions := []string{}
	if usesTmuxSource(source) {
		tmuxSessions, err = d.tmux.ListSessions(ctx)
		if err != nil {
			return result, err
		}
	}
	tmuxSet := make(map[string]struct{}, len(tmuxSessions))
	tmuxNameByIssueKey := make(map[string]string, len(tmuxSessions))
	namingScope := d.sessionNamingScope(projectID)
	snapshotSessions := []daemonstate.Session{}
	if usesProjectionSource(source) {
		snapshotSessions, err = d.sessionSnapshotForReconcile(ctx, projectID)
		if err != nil {
			return result, err
		}
	}
	liveTmuxNames := make(map[string]struct{}, len(tmuxSessions))
	for _, name := range tmuxSessions {
		liveTmuxNames[strings.TrimSpace(name)] = struct{}{}
	}
	for _, session := range snapshotSessions {
		if suppress, _ := ctx.Value(rootedOrchestratorRecoveryContextKey{}).(bool); suppress {
			break
		}
		if !orchestratorSessionProjection(session) {
			continue
		}
		isProjectScope := strings.TrimSpace(session.ScopeID) == string(domain.OrchestrationScopeProject)
		if isProjectScope && len(targetIssueKeys) != 0 {
			continue
		}
		if !isProjectScope && !matchesTargetIssue(sessionKey(session.ScopeID)) {
			continue
		}
		var scope domain.OrchestrationScope
		if isProjectScope {
			scope = domain.ProjectOrchestrationScope()
		} else {
			var scopeErr error
			scope, scopeErr = domain.RootedOrchestrationScope(session.ScopeID)
			if scopeErr != nil {
				return result, fmt.Errorf("recover rooted orchestrator %s: %w", session.ScopeID, scopeErr)
			}
		}
		canonicalSessionID := d.orchestratorSessionID(projectID, scope)
		runtimeSessionID := strings.TrimSpace(session.ID)
		if runtimeSessionID == "" {
			runtimeSessionID = canonicalSessionID
		}
		_, canonicalLive := liveTmuxNames[strings.TrimSpace(canonicalSessionID)]
		_, projectedLive := liveTmuxNames[runtimeSessionID]
		if daemonstate.NormalizeSessionState(session.State) == daemonstate.SessionStateStopped {
			if _, err := d.pauseEndedOrchestratorSession(ctx, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, projectID, runtimeSessionID); err != nil {
				return result, fmt.Errorf("align stopped orchestrator lease %s: %w", session.ScopeID, err)
			}
			if canonicalLive || projectedLive {
				cleanupSessionID := runtimeSessionID
				if !projectedLive && canonicalLive {
					cleanupSessionID = canonicalSessionID
				}
				if err := d.tmux.KillSession(ctx, cleanupSessionID); err != nil {
					return result, fmt.Errorf("clean stopped orchestrator %s: %w", session.ScopeID, err)
				}
				if err := d.persistStoppedOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, projectID, scope, cleanupSessionID, daemonstate.SessionStateStopped); err != nil {
					return result, err
				}
				result.AlignedDaemonSessions++
			}
			continue
		}
		if !sessionProjectionCanRecreateTmuxSession(session) {
			continue
		}
		if canonicalLive || projectedLive {
			continue
		}
		if daemonstate.NormalizeSessionState(session.ObservedState) == daemonstate.SessionStateStopped {
			paused, pauseErr := d.pauseEndedOrchestratorSession(ctx, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, projectID, session.ID)
			if pauseErr != nil {
				return result, fmt.Errorf("repair ended orchestrator %s: %w", session.ScopeID, pauseErr)
			}
			if paused {
				continue
			}
		}
		body, marshalErr := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
		if marshalErr != nil {
			return result, fmt.Errorf("encode rooted orchestrator recovery: %w", marshalErr)
		}
		recoveryCtx := context.WithValue(ctx, rootedOrchestratorRecoveryContextKey{}, true)
		response, recoverErr := d.handleOrchestratorSession(recoveryCtx, protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
		if recoverErr != nil {
			return result, fmt.Errorf("recover rooted orchestrator %s: %w", session.ScopeID, recoverErr)
		}
		if response.Error != nil {
			return result, fmt.Errorf("recover rooted orchestrator %s: %s", session.ScopeID, response.Error.Message)
		}
		if strings.TrimSpace(session.ID) != canonicalSessionID {
			if err := d.sessionRuntimeStateStoreIfConfigured(projectID).DeleteSessionIntentState(ctx, projectID, session); err != nil {
				return result, fmt.Errorf("remove noncanonical orchestrator projection %s: %w", session.ID, err)
			}
		}
		result.RecreatedTmuxSessions++
	}
	excludedTmuxNames := make(map[string]struct{})
	for _, session := range snapshotSessions {
		if !lifecycleManagedSessionProjection(session) {
			excludedTmuxNames[strings.TrimSpace(session.ID)] = struct{}{}
		}
	}
	for _, name := range tmuxSessions {
		if _, excluded := excludedTmuxNames[strings.TrimSpace(name)]; excluded {
			continue
		}
		issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope)
		if !ok {
			continue
		}
		key := sessionKey(issueID)
		if key == "" {
			continue
		}
		if !matchesTargetIssue(key) {
			continue
		}
		tmuxSet[key] = struct{}{}
		tmuxNameByIssueKey[key] = name
	}

	validationIssueIDs := reconcileSessionValidationIssueIDs(targetIssueKeys, tmuxSet, snapshotSessions, namingScope)
	if len(targetIssueKeys) == 0 && strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID)) != "" {
		if issueClient := d.issueClientForProject(projectID); issueClient != nil {
			divergentIssueIDs, divergenceErr := issueClient.ListActiveRuntimeDivergenceIssueIDs(ctx)
			if divergenceErr != nil {
				return result, fmt.Errorf("list unresolved lifecycle runtime divergences: %w", divergenceErr)
			}
			validationIssueIDs = normalizeRuntimeReconcileIssueIDs(append(validationIssueIDs, divergentIssueIDs...))
		}
	}
	issuesByKey, issueValidationEnabled, err := d.reconcileIssueKeyIndex(ctx, projectID, validationIssueIDs)
	if err != nil {
		return result, err
	}
	isValidIssueKey := func(issueKey string) bool {
		if !issueValidationEnabled {
			return true
		}
		_, ok := issuesByKey[issueKey]
		return ok
	}
	var lifecycleInvariantErrs []error
	lifecycleSource := sourceForInvariant(daemonInvariantSessionIssueLifecycle)
	if !usesProjectionSource(lifecycleSource) || !usesTmuxSource(lifecycleSource) {
		return result, fmt.Errorf("invariant %s requires hybrid source, got %s", daemonInvariantSessionIssueLifecycle, lifecycleSource)
	}
	enforceManagedRuntimeLifecycle := func(issueKey string) bool {
		if !issueValidationEnabled {
			return true
		}
		task, ok := issuesByKey[issueKey]
		if !ok {
			return true
		}
		issueClient := d.issueClientForProject(projectID)
		action, invariantErr := domain.EvaluateManagedRuntimeLifecycle(task.State, true)
		if action == domain.ManagedRuntimeLifecycleReject {
			if recordErr := issueClient.RecordRuntimeDivergence(ctx, task.ID.String(), invariantErr.Error()); recordErr != nil {
				lifecycleInvariantErrs = append(lifecycleInvariantErrs, fmt.Errorf("persist runtime divergence for issue %s: %w", task.ID, recordErr))
			}
			lifecycleInvariantErrs = append(lifecycleInvariantErrs, fmt.Errorf("managed session lifecycle invariant for issue %s: %w", task.ID, invariantErr))
			result.LifecycleDivergences = append(result.LifecycleDivergences, protocol.RuntimeLifecycleDivergence{IssueID: task.ID.String(), Reason: invariantErr.Error()})
			return false
		}
		if action != domain.ManagedRuntimeLifecycleRepairWorking {
			if clearErr := issueClient.ClearRuntimeDivergence(ctx, task.ID.String()); clearErr != nil {
				lifecycleInvariantErrs = append(lifecycleInvariantErrs, fmt.Errorf("clear runtime divergence for issue %s: %w", task.ID, clearErr))
				return false
			}
			return true
		}
		repaired, updateErr := issueClient.RepairReadyIdleEngagement(ctx, task.ID.String())
		if updateErr != nil {
			lifecycleInvariantErrs = append(lifecycleInvariantErrs, fmt.Errorf("repair live managed session lifecycle for issue %s: %w", task.ID, updateErr))
			return false
		}
		if !repaired {
			refreshed, refreshErr := issueClient.GetWithRuntime(ctx, projectID, task.ID.String())
			if refreshErr != nil {
				lifecycleInvariantErrs = append(lifecycleInvariantErrs, fmt.Errorf("refresh live managed session lifecycle for issue %s: %w", task.ID, refreshErr))
				return false
			}
			postAction, postErr := domain.EvaluateManagedRuntimeLifecycle(refreshed.State, true)
			if postAction != domain.ManagedRuntimeLifecycleNoop {
				lifecycleInvariantErrs = append(lifecycleInvariantErrs, fmt.Errorf("managed session lifecycle invariant for issue %s after concurrent transition: action=%s: %w", task.ID, postAction, postErr))
				return false
			}
			issuesByKey[issueKey] = refreshed
			if clearErr := issueClient.ClearRuntimeDivergence(ctx, task.ID.String()); clearErr != nil {
				lifecycleInvariantErrs = append(lifecycleInvariantErrs, fmt.Errorf("clear converged runtime divergence for issue %s: %w", task.ID, clearErr))
				return false
			}
			return true
		}
		result.RepairedIssueLifecycle++
		if repairedTask, getErr := issueClient.GetWithRuntime(ctx, projectID, task.ID.String()); getErr == nil {
			issuesByKey[issueKey] = repairedTask
			if d.hub != nil {
				revision := d.nextRevision(projectID)
				body, _ := json.Marshal(taskEventBodyFromTask(projectID, repairedTask))
				d.hub.Publish(protocol.EventEnvelope{ProtocolVersion: protocol.CurrentVersion, ProjectID: naming.ProjectID(projectID), Revision: revision, Event: protocol.EventTaskUpdated, Kind: protocol.EnvelopeKindEvent, EmittedAt: time.Now().UTC(), Body: body})
			}
		}
		if clearErr := issueClient.ClearRuntimeDivergence(ctx, task.ID.String()); clearErr != nil {
			lifecycleInvariantErrs = append(lifecycleInvariantErrs, fmt.Errorf("clear repaired runtime divergence for issue %s: %w", task.ID, clearErr))
			return false
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("session reconciliation repaired issue engagement", "project_id", projectID, "issue_id", task.ID.String(), "engagement", string(domain.IssueEngagementWorking))
		}
		return true
	}
	if issueValidationEnabled {
		runtimeIntent := make(map[string]struct{}, len(tmuxSet)+len(snapshotSessions))
		for issueKey := range tmuxSet {
			runtimeIntent[issueKey] = struct{}{}
		}
		for _, session := range snapshotSessions {
			if !lifecycleManagedSessionProjection(session) || !sessionProjectionCanRecreateTmuxSession(session) {
				continue
			}
			issueID := sessionProjectionIssueID(session, namingScope)
			if d.isSessionStopPending(projectID, issueID) {
				continue
			}
			runtimeIntent[sessionKey(issueID)] = struct{}{}
		}
		issueClient := d.issueClientForProject(projectID)
		for issueKey, task := range issuesByKey {
			if _, present := runtimeIntent[issueKey]; present {
				continue
			}
			if action, invariantErr := domain.EvaluateManagedRuntimeLifecycle(task.State, false); action != domain.ManagedRuntimeLifecycleNoop || invariantErr != nil {
				lifecycleInvariantErrs = append(lifecycleInvariantErrs, fmt.Errorf("evaluate absent managed runtime for issue %s: action=%s: %w", task.ID, action, invariantErr))
				continue
			}
			if clearErr := issueClient.ClearRuntimeDivergence(ctx, task.ID.String()); clearErr != nil {
				lifecycleInvariantErrs = append(lifecycleInvariantErrs, fmt.Errorf("resolve absent runtime divergence for issue %s: %w", task.ID, clearErr))
			}
		}
		for issueKey := range tmuxSet {
			if !enforceManagedRuntimeLifecycle(issueKey) {
				delete(tmuxSet, issueKey)
			}
		}
	}
	for _, session := range snapshotSessions {
		if !lifecycleManagedSessionProjection(session) {
			continue
		}
		issueID := sessionProjectionIssueID(session, namingScope)
		issueKey := sessionKey(issueID)
		if !matchesTargetIssue(issueKey) {
			continue
		}
		if issueKey == "" || !isValidIssueKey(issueKey) {
			pruneInvalidSessionProjection(session, issueID)
			continue
		}
		if d.isSessionStopPending(projectID, issueID) {
			continue
		}
		if !sessionProjectionCanRecreateTmuxSession(session) {
			continue
		}
		if _, ok := tmuxSet[issueKey]; ok {
			continue
		}
		// Recreating tmux makes the runtime live, so gate the prospective
		// runtime through the same lifecycle invariant first.
		if !enforceManagedRuntimeLifecycle(issueKey) {
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
		if newErr := d.tmux.NewSessionWithEnvironment(ctx, canonicalSessionID, wt.Path, d.sessionManagedEnvironment()); newErr != nil {
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
		if envErr := d.setSessionContextEnv(ctx, projectID, issueID, canonicalSessionID); envErr != nil {
			_ = d.tmux.KillSession(ctx, canonicalSessionID)
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("session reconciliation failed to install session environment",
					"project_id", projectID,
					"issue_id", issueID,
					"session_id", canonicalSessionID,
					"error", envErr,
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
		if err := d.persistObservedRuntimeProjection(ctx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, reattached); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("persist recreated physical session observation failed", "project_id", projectID, "session_id", canonicalSessionID, "error", err)
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
			if err := d.persistObservedRuntimeProjection(ctx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, stopped); err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Debug("persist killed physical session observation failed", "project_id", projectID, "session_id", stopped.ID, "error", err)
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
			"target_issue_id", targetIssueID,
			"recreated_tmux_sessions", result.RecreatedTmuxSessions,
			"aligned_daemon_sessions", result.AlignedDaemonSessions,
			"repaired_issue_lifecycle", result.RepairedIssueLifecycle,
		)
	}
	return result, errors.Join(lifecycleInvariantErrs...)
}

func runtimeReconcileTargetIssueKeySet(issueIDs []string) map[string]struct{} {
	keys := make(map[string]struct{}, len(issueIDs))
	for _, issueID := range normalizeRuntimeReconcileIssueIDs(issueIDs) {
		key := sessionKey(issueID)
		if key != "" {
			keys[key] = struct{}{}
		}
	}
	return keys
}

func reconcileSessionValidationIssueIDs(targetIssueKeys map[string]struct{}, tmuxSet map[string]struct{}, snapshotSessions []daemonstate.Session, namingScope string) []string {
	ids := make([]string, 0, len(tmuxSet)+len(snapshotSessions))
	targeted := len(targetIssueKeys) > 0
	matchesTargetIssue := func(issueKey string) bool {
		if !targeted {
			return true
		}
		_, ok := targetIssueKeys[issueKey]
		return ok
	}
	for issueKey := range targetIssueKeys {
		ids = append(ids, issueKey)
	}
	for issueKey := range tmuxSet {
		if !matchesTargetIssue(issueKey) {
			continue
		}
		ids = append(ids, issueKey)
	}
	for _, session := range snapshotSessions {
		if !lifecycleManagedSessionProjection(session) {
			continue
		}
		issueID := sessionProjectionIssueID(session, namingScope)
		issueKey := sessionKey(issueID)
		if issueKey == "" {
			continue
		}
		if !matchesTargetIssue(issueKey) {
			continue
		}
		ids = append(ids, issueKey)
	}
	return normalizeRuntimeReconcileIssueIDs(ids)
}

func lifecycleManagedSessionProjection(session daemonstate.Session) bool {
	switch session.Role {
	case "", daemonstate.SessionRoleWorker:
		return session.ScopeKind == "" || session.ScopeKind == daemonstate.SessionScopeIssue
	default:
		return false
	}
}

func orchestratorSessionProjection(session daemonstate.Session) bool {
	if session.Role != daemonstate.SessionRoleOrchestrator || session.ScopeKind != daemonstate.SessionScopeOrchestration || strings.TrimSpace(session.ScopeID) == "" {
		return false
	}
	if strings.TrimSpace(session.ScopeID) == string(domain.OrchestrationScopeProject) {
		return strings.TrimSpace(session.IssueID) == ""
	}
	return strings.TrimSpace(session.IssueID) == strings.TrimSpace(session.ScopeID)
}

func (d *Daemon) reconcileIssueKeyIndex(ctx context.Context, projectID string, issueIDs []string) (map[string]domain.Task, bool, error) {
	if d == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID)) == "" {
		return nil, false, nil
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, true, fmt.Errorf("configured project %s issue projection unavailable", projectID)
	}
	issueIDs = normalizeRuntimeReconcileIssueIDs(issueIDs)
	var (
		tasks []domain.Task
		err   error
	)
	tasks, err = issueClient.GetManyWithRuntime(ctx, projectID, issueIDs)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session reconciliation issue validation unavailable",
				"project_id", projectID,
				"issue_count", len(issueIDs),
				"error", err,
			)
		}
		return nil, true, fmt.Errorf("refresh configured project %s issue projection: %w", projectID, err)
	}
	index := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		key := sessionKey(task.ID.String())
		if key == "" {
			continue
		}
		index[key] = task
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
	return d.sessionRuntimeStateStoreIfConfigured(projectID).ListSessionIntentStates(ctx, projectID)
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
	if len(tasks) == 0 {
		return tasks
	}
	defer func() {
		issueClient := d.issueClientForProject(projectID)
		if issueClient == nil {
			return
		}
		waiting, err := issueClient.UnresolvedInteractionIssueIDs(ctx)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("refresh interaction projection while enriching tasks failed", "project_id", projectID, "error", err)
			}
			waiting = map[string]struct{}{}
		}
		investigationAcceptances, err := issueClient.InvestigationAcceptances(ctx, tasks)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("refresh investigation acceptance projection while enriching tasks failed", "project_id", projectID, "error", err)
			}
			// Fail closed for human-facing investigations without discarding
			// independently refreshed interaction authority.
			investigationAcceptances = make(map[string]domain.InvestigationAcceptance)
			for _, task := range tasks {
				if task.Type == domain.TypeInvestigation {
					investigationAcceptances[task.ID.String()] = domain.EvaluateInvestigationAcceptance(task, nil)
				}
			}
		}
		for i := range tasks {
			_, decisionWaiting := waiting[tasks[i].ID.String()]
			base := tasks[i].IssueFacts()
			acceptance, hasAcceptance := investigationAcceptances[tasks[i].ID.String()]
			var acceptancePtr *domain.InvestigationAcceptance
			if hasAcceptance {
				acceptancePtr = &acceptance
			}
			tasks[i].Facts = domain.DeriveIssueFacts(domain.IssueFactsInput{Status: tasks[i].Status, Priority: tasks[i].Priority, Type: tasks[i].Type, State: tasks[i].State, Session: tasks[i].Session, HasTmuxSession: tasks[i].HasTmuxSession, OperationBlockers: base.OperationBlockers, DecisionWaiting: decisionWaiting, DecisionWaitReason: "unresolved interaction request requires human decision", InvestigationAcceptance: acceptancePtr})
		}
	}()
	if d.sessionStore == nil {
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
	activityByKey = d.applyTerminalFailureProbes(ctx, projectID, projectionSessions, namingScope, activityByKey)
	if len(projectionSessions) == 0 {
		activityByKey = d.applyTerminalFailureProbes(ctx, projectID, snapshotSessions, namingScope, activityByKey)
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
		counts := countsByKey[taskKey]
		if counts.PaneScoped > 0 {
			if counts.Active > 0 {
				state = domain.SessionBusy
			} else {
				state = domain.SessionPaused
			}
		}
		activity, activitySource := sessionActivityLabelForDisplay(sessionHookActivity{}, session)
		if display := activityByKey[taskKey]; display.Activity != "" {
			activity = display.Activity
			activitySource = display.Source
		}
		tasks[i].Session = &domain.Session{
			IssueID:           naming.IssueID(taskID),
			Role:              string(session.Role),
			ScopeKind:         string(session.ScopeKind),
			ScopeID:           session.ScopeID,
			State:             state,
			Activity:          activity,
			ActivitySource:    activitySource,
			TotalCount:        counts.Total,
			ActiveCount:       counts.Active,
			PausedCount:       counts.Paused,
			TmuxAttached:      counts.TmuxAttachedCount > 0,
			TmuxAttachedCount: counts.TmuxAttachedCount,
			StartedAt:         startedAt,
			UpdatedAt:         session.UpdatedAt,
			Worktree:          worktree,
		}
	}

	return tasks
}

func activeSessionIssueKeysFromProjection(sessions []daemonstate.Session, namingScope string) map[string]struct{} {
	active := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		if !lifecycleManagedSessionProjection(session) {
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
		state := session.State
		observed := session.ObservedState
		if strings.TrimSpace(string(observed)) == "" {
			observed = state
		}
		if state == daemonstate.SessionStateStopped || observed == daemonstate.SessionStateStopped {
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
	tmuxPanes, err := d.tmux.ListPaneInfos(ctx)
	if err != nil {
		return err
	}
	return d.persistTmuxSessionRuntimeState(ctx, projectID, tmuxSessions, tmuxPanes)
}

type tmuxRuntimeLiveness struct {
	sessionByID    map[string]tmux.SessionInfo
	sessionIDs     map[string]struct{}
	panesBySession map[string]map[string]struct{}
}

func newTmuxRuntimeLiveness(tmuxSessions []tmux.SessionInfo, tmuxPanes []tmux.PaneInfo) tmuxRuntimeLiveness {
	live := tmuxRuntimeLiveness{
		sessionByID:    make(map[string]tmux.SessionInfo, len(tmuxSessions)),
		sessionIDs:     make(map[string]struct{}, len(tmuxSessions)),
		panesBySession: make(map[string]map[string]struct{}, len(tmuxPanes)),
	}
	for _, info := range tmuxSessions {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		live.sessionByID[name] = info
		live.sessionIDs[name] = struct{}{}
	}
	for _, pane := range tmuxPanes {
		sessionName := strings.TrimSpace(pane.SessionName)
		paneID := sanitizeRuntimePaneID(pane.PaneID)
		if sessionName == "" || paneID == "" {
			continue
		}
		if live.panesBySession[sessionName] == nil {
			live.panesBySession[sessionName] = map[string]struct{}{}
		}
		live.panesBySession[sessionName][paneID] = struct{}{}
	}
	return live
}

func (live tmuxRuntimeLiveness) sessionLive(sessionID string) bool {
	_, ok := live.sessionIDs[strings.TrimSpace(sessionID)]
	return ok
}

func (live tmuxRuntimeLiveness) paneLive(sessionID string) bool {
	parent, paneID, ok := agentScopedSessionParentAndPane(sessionID)
	if !ok {
		return false
	}
	panes := live.panesBySession[parent]
	if len(panes) == 0 {
		return false
	}
	_, ok = panes[sanitizeRuntimePaneID(paneID)]
	return ok
}

func sanitizeRuntimePaneID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
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
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-_.")
}

func applyObservedRuntimeLiveness(session *daemonstate.Session, info tmux.SessionInfo, infoLive bool, live tmuxRuntimeLiveness) {
	if infoLive {
		session.ObservedState = daemonstate.SessionStateRunning
		session.TmuxAttachedCount = info.AttachedCount
		if (session.StartedAt == nil || session.StartedAt.IsZero()) && info.CreatedAt != nil && !info.CreatedAt.IsZero() {
			started := info.CreatedAt.UTC()
			session.StartedAt = &started
		}
		return
	}
	if isAgentScopedSessionID(session.ID) {
		parent, _, ok := agentScopedSessionParentAndPane(session.ID)
		switch {
		case !ok || !live.sessionLive(parent):
			session.ObservedState = daemonstate.SessionStateStopped
		case live.paneLive(session.ID):
			session.ObservedState = daemonstate.NormalizeSessionState(session.State)
		default:
			session.ObservedState = daemonstate.SessionStateStopped
		}
		session.TmuxAttachedCount = 0
		return
	}
	session.ObservedState = daemonstate.SessionStateStopped
	session.TmuxAttachedCount = 0
}

func sessionRuntimeProjectionChanged(before, after daemonstate.Session) bool {
	if strings.TrimSpace(before.ID) != strings.TrimSpace(after.ID) {
		return true
	}
	if strings.TrimSpace(before.IssueID) != strings.TrimSpace(after.IssueID) {
		return true
	}
	if daemonstate.NormalizeSessionState(before.State) != daemonstate.NormalizeSessionState(after.State) {
		return true
	}
	if daemonstate.NormalizeSessionState(before.ObservedState) != daemonstate.NormalizeSessionState(after.ObservedState) {
		return true
	}
	if normalizeSessionActivity(before.Activity) != normalizeSessionActivity(after.Activity) {
		return true
	}
	if normalizeSessionActivitySource(before.ActivitySource, "") != normalizeSessionActivitySource(after.ActivitySource, "") {
		return true
	}
	if before.TmuxAttachedCount != after.TmuxAttachedCount {
		return true
	}
	return !runtimeProjectionTimesEqual(before.StartedAt, after.StartedAt)
}

func runtimeProjectionTimesEqual(left, right *time.Time) bool {
	if left == nil || left.IsZero() {
		return right == nil || right.IsZero()
	}
	if right == nil || right.IsZero() {
		return false
	}
	return left.UTC().Equal(right.UTC())
}

func (d *Daemon) refreshExistingSessionRuntimeState(ctx context.Context, projectID string) error {
	if d == nil || d.tmux == nil || d.sessionRuntimeStateStoreIfConfigured(projectID) == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)
	startedAt := time.Now()
	scanned := 0
	changed := 0
	skippedUnchanged := 0
	defer func() {
		latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "session_runtime.refresh_existing_projection", startedAt, "project_id", projectID, "session_count", scanned, "changed_count", changed, "skipped_unchanged_count", skippedUnchanged)
	}()
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
	tmuxPanes, err := d.tmux.ListPaneInfos(ctx)
	if err != nil {
		return err
	}
	live := newTmuxRuntimeLiveness(tmuxSessions, tmuxPanes)
	persistCtx := contextWithRuntimeProjectionWriterOperation(ctx, "session.refresh_existing.persist")
	for _, session := range existingSessions {
		scanned++
		before := session
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			continue
		}
		info, infoLive := live.sessionByID[sessionID]
		applyObservedRuntimeLiveness(&session, info, infoLive, live)
		if !sessionRuntimeProjectionChanged(before, session) {
			skippedUnchanged++
			continue
		}
		session.UpdatedAt = time.Now().UTC()
		if err := d.persistObservedRuntimeProjection(persistCtx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, session); err != nil {
			return err
		}
		changed++
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
	tmuxPanes, err := d.tmux.ListPaneInfos(ctx)
	if err != nil {
		return err
	}
	live := newTmuxRuntimeLiveness(tmuxSessions, tmuxPanes)
	persistCtx := contextWithRuntimeProjectionWriterOperation(ctx, "session.refresh_issue.persist")
	for _, session := range targetSessions {
		before := session
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			continue
		}
		info, infoLive := live.sessionByID[sessionID]
		applyObservedRuntimeLiveness(&session, info, infoLive, live)
		if !sessionRuntimeProjectionChanged(before, session) {
			continue
		}
		session.UpdatedAt = time.Now().UTC()
		if err := d.persistObservedRuntimeProjection(persistCtx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, session); err != nil {
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
		if err := d.persistObservedRuntimeProjection(ctx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, session); err != nil {
			return err
		}
		matched = true
	}
	if matched && issueID == "" {
		return nil
	}

	if matched {
		canonicalStopped := daemonstate.Session{
			ID:            naming.CanonicalSessionID(namingScope, issueID),
			IssueID:       issueID,
			State:         daemonstate.SessionStateStopped,
			ObservedState: daemonstate.SessionStateStopped,
			UpdatedAt:     time.Now().UTC(),
		}
		if canonicalStopped.ID == "" {
			return nil
		}
		if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, canonicalStopped); err != nil {
			return err
		}
		return d.persistObservedRuntimeProjection(ctx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, canonicalStopped)
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
	if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, session); err != nil {
		return err
	}
	return d.persistObservedRuntimeProjection(ctx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, session)
}

func (d *Daemon) persistTmuxSessionRuntimeState(ctx context.Context, projectID string, tmuxSessions []tmux.SessionInfo, tmuxPanes []tmux.PaneInfo) error {
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
	existingBySessionID := make(map[string]daemonstate.Session, len(existingSessions))
	for _, session := range existingSessions {
		sessionID := strings.TrimSpace(session.ID)
		if sessionID != "" {
			existingBySessionID[sessionID] = session
		}
	}
	live := newTmuxRuntimeLiveness(tmuxSessions, tmuxPanes)
	persistCtx := contextWithRuntimeProjectionWriterOperation(ctx, "session.tmux_snapshot.persist")
	for _, info := range tmuxSessions {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope)
		if !ok {
			continue
		}
		issueKey := sessionKey(issueID)
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
		if existing, exists := existingBySessionID[name]; exists && !sessionRuntimeProjectionChanged(existing, row) {
			continue
		}
		if err := d.runtimeProjectionStateWriter().PersistSessionProjection(persistCtx, projectID, row); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("persist live tmux session runtime state failed",
				"project_id", projectID,
				"session_id", row.ID,
				"issue_id", row.IssueID,
				"error", err,
			)
		}
		if err := d.persistObservedRuntimeProjection(persistCtx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, row); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("persist live tmux physical observation failed", "project_id", projectID, "session_id", row.ID, "error", err)
		}
	}
	for _, session := range existingSessions {
		if _, liveSession := live.sessionIDs[strings.TrimSpace(session.ID)]; liveSession {
			continue
		}
		stopped := session
		applyObservedRuntimeLiveness(&stopped, tmux.SessionInfo{}, false, live)
		if !sessionRuntimeProjectionChanged(session, stopped) {
			continue
		}
		stopped.UpdatedAt = time.Now().UTC()
		if err := d.runtimeProjectionStateWriter().PersistSessionProjection(persistCtx, projectID, stopped); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("persist missing tmux session as stopped failed",
				"project_id", projectID,
				"session_id", stopped.ID,
				"issue_id", stopped.IssueID,
				"error", err,
			)
		}
		if err := d.persistObservedRuntimeProjection(persistCtx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, stopped); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("persist stopped tmux physical observation failed", "project_id", projectID, "session_id", stopped.ID, "error", err)
		}
	}
	return nil
}

func (d *Daemon) buildSessionLaunchCommand(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt string) string {
	return d.buildSessionLaunchCommandWithInitReadyPath(projectID, issueID, sessionID, yolo, imagePaths, initialPrompt, "")
}

func (d *Daemon) buildSessionResumeCommand(projectID, issueID, sessionID string, yolo bool, imagePaths []string) string {
	projectCfg := d.runtimeConfigForProject(projectID)
	tool := strings.TrimSpace(projectCfg.CLITool)
	if tool == "" {
		tool = "claude"
	}
	if strings.EqualFold(tool, "claude") {
		return d.withSessionManagedPathCommand(d.buildClaudeContinueCommand(projectID, issueID, yolo, sessionRestartContinuePrompt))
	}
	if !strings.EqualFold(tool, "codex") {
		return d.buildSessionLaunchCommand(projectID, issueID, sessionID, yolo, imagePaths, sessionRestartContinuePrompt)
	}

	parts := []string{
		fmt.Sprintf(`AZEDARACH_ISSUE_ID="%s"`, escapeForShellDoubleQuotes(issueID)),
		tool,
		"resume",
	}
	if projectCfg.CodexAppServer {
		parts = append(parts, "--remote", "unix://")
	}
	for _, imagePath := range imagePaths {
		trimmedPath := strings.TrimSpace(imagePath)
		if trimmedPath == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf(`--image "%s"`, escapeForShellDoubleQuotes(trimmedPath)))
	}
	if yolo || projectCfg.DangerouslySkipPermissions {
		parts = append(parts, "--dangerously-bypass-approvals-and-sandbox")
	}
	// Azedarach tracks tmux session IDs, not Codex conversation UUIDs.
	// Codex's cwd filter makes --last target this worktree's latest session.
	parts = append(parts, "--last")
	command := strings.Join(parts, " ")
	if projectCfg.CodexAppServer {
		command = codexAppServerSupervisedCommand(tool, command, command)
	}
	return d.withSessionManagedPathCommand(command)
}

func (d *Daemon) sessionRestartNeedsPostLaunchPrompt(projectID string) bool {
	tool := strings.TrimSpace(d.runtimeConfigForProject(projectID).CLITool)
	return strings.EqualFold(tool, "codex")
}

func (d *Daemon) sessionLaunchNeedsPostLaunchPrompt(projectID, prompt string) bool {
	tool := strings.TrimSpace(d.runtimeConfigForProject(projectID).CLITool)
	return strings.EqualFold(tool, "codex") && !codexLaunchPromptArgAllowed(prompt)
}

func (d *Daemon) sessionLaunchCommandNeedsPostLaunchPrompt(projectID, launchCommand string) bool {
	tool := strings.TrimSpace(d.runtimeConfigForProject(projectID).CLITool)
	return strings.EqualFold(tool, "codex") && len(launchCommand) > codexLaunchCommandArgMaxBytes
}

func codexLaunchPromptArgAllowed(prompt string) bool {
	return len(prompt) <= codexLaunchPromptArgMaxBytes
}

func (d *Daemon) buildClaudeContinueCommand(projectID, issueID string, yolo bool, prompt string) string {
	projectCfg := d.runtimeConfigForProject(projectID)
	tool := strings.TrimSpace(projectCfg.CLITool)
	if tool == "" {
		tool = "claude"
	}
	parts := []string{
		fmt.Sprintf(`AZEDARACH_ISSUE_ID="%s"`, escapeForShellDoubleQuotes(issueID)),
		tool,
		"--continue",
	}
	if yolo || projectCfg.DangerouslySkipPermissions {
		parts = append(parts, "--dangerously-skip-permissions")
	}
	if strings.TrimSpace(prompt) != "" {
		parts = append(parts, fmt.Sprintf(`"%s"`, escapeForShellDoubleQuotes(prompt)))
	}
	return strings.Join(parts, " ")
}

func (d *Daemon) buildSessionLaunchCommandWithInitReadyPath(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt, initReadyPath string) string {
	return d.buildSessionLaunchCommandWithInitReadyPathAndEnv(projectID, issueID, sessionID, yolo, imagePaths, initialPrompt, initReadyPath, nil)
}

func (d *Daemon) buildSessionLaunchCommandWithInitReadyPathAndEnv(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt, initReadyPath string, startupEnvCommands []string) string {
	projectCfg := d.runtimeConfigForProject(projectID)
	toolCommand := d.buildCLIToolCommand(projectID, issueID, sessionID, yolo, imagePaths, initialPrompt)
	commands := make([]string, 0, len(startupEnvCommands)+len(projectCfg.SessionSyncInitCommands)+3)
	if pathExport := d.sessionManagedPathExportCommand(); pathExport != "" {
		commands = append(commands, pathExport)
	}
	for _, envCommand := range startupEnvCommands {
		trimmed := strings.TrimSpace(envCommand)
		if trimmed != "" {
			commands = append(commands, trimmed)
		}
	}
	if trapCommand := sessionInitReadyTrapCommand(initReadyPath); trapCommand != "" {
		commands = append(commands, trapCommand)
	}
	for _, initCmd := range projectCfg.SessionSyncInitCommands {
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
	inner = inner + "; " + sessionAgentProcessExitCommand(projectCfg.CLITool) + "; exec " + shell
	return fmt.Sprintf("%s -i -c %s", shell, singleQuoteForShell(inner))
}

func sessionAgentProcessExitCommand(tool string) string {
	agent := strings.ToLower(strings.TrimSpace(tool))
	switch agent {
	case "codex", "opencode":
	default:
		agent = "claude"
	}
	return `__azedarach_agent_exit_status=$?; printf '{"exit_status":%s}\n' "$__azedarach_agent_exit_status" | az ai hook run --agent=` + agent + ` session_end >/dev/null 2>&1 || true`
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

const sessionAsyncInitWindowName = "async-init"
const sessionInitReadyPollInterval = 100 * time.Millisecond

func (d *Daemon) prepareSessionInitReadyMarker(projectID, worktreePath, issueID, sessionID string) (sessionInitReadyMarker, error) {
	commandCount := countNonEmptyStrings(d.runtimeConfigForProject(projectID).SessionSyncInitCommands)
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
		safeSessionAsyncInitPathSegment(issueID),
		safeSessionAsyncInitPathSegment(sessionID),
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

func (d *Daemon) startSessionAsyncInitCommands(ctx context.Context, projectID, issueID, sessionID, worktreePath string) {
	projectCfg := d.runtimeConfigForProject(projectID)
	asyncInitCommand := buildSessionAsyncInitWindowCommand(projectCfg, projectID, issueID, sessionID)
	if strings.TrimSpace(asyncInitCommand) == "" {
		return
	}
	if d.tmux == nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session async init commands skipped; tmux client unavailable",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", sessionID,
			)
		}
		return
	}
	if _, err := d.tmux.EnsureWindow(ctx, sessionID, sessionAsyncInitWindowName, worktreePath); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session async init window setup failed",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", sessionID,
				"error", err,
			)
		}
		return
	}
	target := sessionID + ":" + sessionAsyncInitWindowName
	if err := d.tmux.SendKeys(ctx, target, asyncInitCommand); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("session async init command dispatch failed",
			"project_id", projectID,
			"issue_id", issueID,
			"session_id", sessionID,
			"target", target,
			"error", err,
		)
	}
}

func buildSessionAsyncInitWindowCommand(projectCfg daemonProjectRuntimeConfig, projectID, issueID, sessionID string) string {
	asyncInitCommands := buildSessionAsyncInitCommands(projectCfg.SessionAsyncInitCommands, issueID, sessionID)
	if len(asyncInitCommands) == 0 {
		return ""
	}
	commands := make([]string, 0, len(asyncInitCommands)+2)
	if contextExport := sessionLaunchContextExportCommand(projectID, issueID, sessionID); contextExport != "" {
		commands = append(commands, contextExport)
	}
	commands = append(commands, asyncInitCommands...)
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

func (d *Daemon) sessionLaunchStartupExportCommands(projectCfg daemonProjectRuntimeConfig, resourceCtx issueResourceLifecycleContext) []string {
	commands := make([]string, 0, 1)
	assignments := issueResourceShellExports(projectCfg.IssueResources, resourceCtx)
	if len(assignments) > 0 {
		commands = append(commands, "export "+strings.Join(assignments, " "))
	}
	return commands
}

func buildSessionAsyncInitCommands(asyncInitCommands []string, issueID, sessionID string) []string {
	if len(asyncInitCommands) == 0 {
		return nil
	}
	logDir := sessionAsyncInitLogDir(issueID, sessionID)
	commands := make([]string, 0, len(asyncInitCommands))
	index := 0
	for _, asyncInitCmd := range asyncInitCommands {
		trimmed := strings.TrimSpace(asyncInitCmd)
		if trimmed == "" {
			continue
		}
		index++
		logPath := filepath.ToSlash(filepath.Join(logDir, fmt.Sprintf("%03d.log", index)))
		commands = append(commands, buildSessionAsyncInitCommand(index, trimmed, logDir, logPath))
	}
	return commands
}

func buildSessionAsyncInitCommand(index int, command, logDir, logPath string) string {
	quotedLogDir := singleQuoteForShell(filepath.ToSlash(logDir))
	quotedLogPath := singleQuoteForShell(filepath.ToSlash(logPath))
	quotedCommand := singleQuoteForShell(command)
	return fmt.Sprintf(
		"mkdir -p %s && echo %s && { printf 'command: %%s\\n' %s; (%s); status=$?; printf 'exit status: %%s\\n' \"$status\"; } 2>&1 | tee -a %s",
		quotedLogDir,
		singleQuoteForShell(fmt.Sprintf("session async-init[%d] log: %s", index, filepath.ToSlash(logPath))),
		quotedCommand,
		command,
		quotedLogPath,
	)
}

func sessionAsyncInitLogDir(issueID, sessionID string) string {
	return filepath.Join(".azedarach", "session-async-init", safeSessionAsyncInitPathSegment(issueID), safeSessionAsyncInitPathSegment(sessionID))
}

func safeSessionAsyncInitPathSegment(value string) string {
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

func (d *Daemon) prepareSessionStartImagePaths(ctx context.Context, projectID, issueID, worktreePath string, explicitPaths []string) ([]string, error) {
	explicitPaths = dedupeSessionStartImagePaths(explicitPaths)
	rootPath := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
	if rootPath == "" {
		rootPath = strings.TrimSpace(d.resolveRepoDirForProject(projectID))
	}
	if rootPath == "" {
		rootPath = strings.TrimSpace(d.cfg.RepoDir)
	}
	worktreePath = strings.TrimSpace(worktreePath)
	if rootPath == "" || worktreePath == "" {
		return explicitPaths, nil
	}

	service := attachment.NewUnifiedService(filepath.Join(rootPath, ".azedarach"), d.cfg.Logger)
	attachments, err := service.List(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("list issue attachments: %w", err)
	}
	materializedPaths := make([]string, 0, len(attachments))
	issueImageSourcePaths := map[string]struct{}{}
	for _, att := range attachments {
		if !attachment.IsImage(att) {
			continue
		}
		sourcePath := strings.TrimSpace(att.Path)
		if sourcePath == "" {
			sourcePath = filepath.Join(rootPath, filepath.FromSlash(strings.TrimSpace(att.Relative)))
		}
		if sourcePath != "" {
			issueImageSourcePaths[filepath.Clean(sourcePath)] = struct{}{}
		}
		destPath, err := sessionAttachmentWorktreePath(worktreePath, att)
		if err != nil {
			return nil, err
		}
		if err := copySessionAttachmentToWorktree(sourcePath, destPath); err != nil {
			return nil, err
		}
		materializedPaths = append(materializedPaths, destPath)
	}

	paths := make([]string, 0, len(explicitPaths)+len(materializedPaths))
	for _, path := range explicitPaths {
		if _, isIssueAttachmentSource := issueImageSourcePaths[filepath.Clean(path)]; isIssueAttachmentSource {
			continue
		}
		paths = append(paths, path)
	}
	paths = append(paths, materializedPaths...)
	return dedupeSessionStartImagePaths(paths), nil
}

func sessionAttachmentWorktreePath(worktreePath string, att attachment.Attachment) (string, error) {
	rel := filepath.ToSlash(strings.TrimSpace(att.Relative))
	if rel == "" {
		rel = filepath.ToSlash(filepath.Join(".azedarach", "attachments", strings.TrimSpace(att.Filename)))
	}
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid attachment relative path %q", att.Relative)
	}
	dest := filepath.Clean(filepath.Join(worktreePath, filepath.FromSlash(rel)))
	root := filepath.Clean(worktreePath)
	if dest != root && !strings.HasPrefix(dest, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("attachment path escapes worktree: %s", rel)
	}
	return dest, nil
}

func copySessionAttachmentToWorktree(sourcePath, destPath string) error {
	sourcePath = strings.TrimSpace(sourcePath)
	destPath = strings.TrimSpace(destPath)
	if sourcePath == "" || destPath == "" {
		return fmt.Errorf("missing attachment source or destination path")
	}
	if filepath.Clean(sourcePath) == filepath.Clean(destPath) {
		if _, err := os.Stat(sourcePath); err != nil {
			return fmt.Errorf("stat attachment %s: %w", sourcePath, err)
		}
		return nil
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read attachment %s: %w", sourcePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create worktree attachment dir: %w", err)
	}
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return fmt.Errorf("write worktree attachment %s: %w", destPath, err)
	}
	return nil
}

func dedupeSessionStartImagePaths(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		key := filepath.Clean(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
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
	return d.runIssueResourceCommandsWithDir(ctx, projectID, resourceCtx, []string{command}, issueResourceReconcileCommandDir(resourceCtx))
}

func (d *Daemon) runIssueResourceCommands(ctx context.Context, projectID string, resourceCtx issueResourceLifecycleContext, commands []string) (issueResourceLifecycleResult, error) {
	return d.runIssueResourceCommandsWithDir(ctx, projectID, resourceCtx, commands, issueResourceCommandDir(resourceCtx))
}

func (d *Daemon) runIssueResourceCommandsWithDir(ctx context.Context, projectID string, resourceCtx issueResourceLifecycleContext, commands []string, commandDir string) (issueResourceLifecycleResult, error) {
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
		commandCtx, endSpan := latencytrace.StartSpan(ctx, "dependency", "issue_resource_command",
			"dependency.name", filepath.Base(shell),
			"dependency.operation", "run",
			"project_id", projectID,
			"arg_count", 2,
		)
		output, err := d.runSessionShell(commandCtx, shell, commandDir, trimmed, append(os.Environ(), env...))
		endSpan(err)
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
	assignments := issueResourceShellExports(projectCfg.IssueResources, resourceCtx)
	if len(assignments) == 0 {
		return nil
	}
	return d.tmux.SendKeys(ctx, resourceCtx.SessionID, "export "+strings.Join(assignments, " "))
}

func (d *Daemon) setIssueResourceSessionEnv(ctx context.Context, projectID string, resourceCtx issueResourceLifecycleContext) error {
	projectCfg := d.runtimeConfigForProject(projectID)
	if !issueResourcesConfigured(projectCfg.IssueResources) {
		return nil
	}
	for _, pair := range d.issueResourceEnv(projectCfg.IssueResources, resourceCtx) {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || !validShellEnvName(key) {
			continue
		}
		if err := d.tmux.SetEnvironment(ctx, resourceCtx.SessionID, key, value); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) exportSessionContextEnv(ctx context.Context, projectID, issueID, sessionID string) error {
	if err := d.setSessionContextEnv(ctx, projectID, issueID, sessionID); err != nil {
		return err
	}
	exportCommands := make([]string, 0, 2)
	if pathExport := d.sessionManagedPathExportCommand(); pathExport != "" {
		exportCommands = append(exportCommands, pathExport)
	}
	if contextExport := sessionLaunchContextExportCommand(projectID, issueID, sessionID); contextExport != "" {
		exportCommands = append(exportCommands, contextExport)
	}
	if len(exportCommands) > 0 {
		if err := d.tmux.SendKeys(ctx, sessionID, strings.Join(exportCommands, "; ")); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) setSessionContextEnv(ctx context.Context, projectID, issueID, sessionID string) error {
	if err := d.setSessionManagedPathEnv(ctx, sessionID); err != nil {
		return err
	}
	for _, assignment := range sessionLaunchContextEnvAssignments(projectID, issueID, sessionID) {
		if assignment.Value == "" {
			continue
		}
		if err := d.tmux.SetEnvironment(ctx, sessionID, assignment.Key, assignment.Value); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) setSessionManagedPathEnv(ctx context.Context, sessionID string) error {
	if managedPath := d.sessionManagedPathValue(); managedPath != "" {
		return d.tmux.SetEnvironment(ctx, sessionID, "PATH", managedPath)
	}
	return nil
}

func (d *Daemon) sessionManagedEnvironment() map[string]string {
	if managedPath := d.sessionManagedPathValue(); managedPath != "" {
		return map[string]string{"PATH": managedPath}
	}
	return nil
}

func (d *Daemon) withSessionManagedPathCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if pathExport := d.sessionManagedPathExportCommand(); pathExport != "" {
		return pathExport + "; " + command
	}
	return command
}

func (d *Daemon) sessionManagedPathExportCommand() string {
	binDir := strings.TrimSpace(d.cfg.ManagedGenerationBinDir)
	if binDir == "" || d.cfg.ScopedRuntime {
		return ""
	}
	return "export PATH=" + singleQuoteForShell(binDir) + ":\"$PATH\""
}

func (d *Daemon) sessionManagedPathValue() string {
	binDir := strings.TrimSpace(d.cfg.ManagedGenerationBinDir)
	if binDir == "" || d.cfg.ScopedRuntime {
		return ""
	}
	return appconfig.PrependPathEntry(os.Getenv("PATH"), binDir)
}

func issueResourcesConfigured(cfg appconfig.IssueResourcesConfig) bool {
	return len(cfg.Env) > 0 ||
		len(cfg.PrepareCommands) > 0 ||
		len(cfg.FailedStartCleanupCommands) > 0 ||
		len(cfg.CleanupCommands) > 0 ||
		strings.TrimSpace(cfg.ReconcileCommand) != ""
}

func (d *Daemon) issueResourceEnv(cfg appconfig.IssueResourcesConfig, resourceCtx issueResourceLifecycleContext) []string {
	return issueResourceEnv(cfg, resourceCtx)
}

func issueResourceEnv(cfg appconfig.IssueResourcesConfig, resourceCtx issueResourceLifecycleContext) []string {
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

func issueResourceShellExports(cfg appconfig.IssueResourcesConfig, resourceCtx issueResourceLifecycleContext) []string {
	env := issueResourceEnv(cfg, resourceCtx)
	assignments := make([]string, 0, len(env))
	for _, pair := range env {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || !validShellEnvName(key) {
			continue
		}
		assignments = append(assignments, key+"="+singleQuoteForShell(value))
	}
	sort.Strings(assignments)
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

func issueResourceReconcileCommandDir(resourceCtx issueResourceLifecycleContext) string {
	if dir := strings.TrimSpace(resourceCtx.RootPath); dir != "" {
		return dir
	}
	return issueResourceCommandDir(resourceCtx)
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

const worktreeAsyncInitTimeout = 30 * time.Minute

func (d *Daemon) runWorktreeInitCommands(ctx context.Context, projectID string, worktreePath string) error {
	return d.runWorktreeSyncInitCommands(ctx, worktreeInitContext{
		ProjectID:    normalizedProjectID(projectID),
		WorktreePath: worktreePath,
	})
}

func (d *Daemon) runWorktreeSyncInitCommands(ctx context.Context, initCtx worktreeInitContext) error {
	projectCfg := d.runtimeConfigForProject(initCtx.ProjectID)
	return d.runWorktreeInitCommandList(ctx, initCtx, "sync", projectCfg.WorktreeInitCommands)
}

func (d *Daemon) startWorktreeAsyncInitCommands(initCtx worktreeInitContext) {
	go func(initCtx worktreeInitContext) {
		projectCfg := d.runtimeConfigForProject(initCtx.ProjectID)
		commands := append([]string(nil), projectCfg.WorktreeAsyncInitCommands...)
		if len(commands) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), worktreeAsyncInitTimeout)
		defer cancel()
		if err := d.runWorktreeInitCommandList(ctx, initCtx, "async", commands); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("worktree async init failed",
				"project_id", initCtx.ProjectID,
				"issue_id", initCtx.IssueID,
				"worktree", initCtx.WorktreePath,
				"error", err,
			)
		}
	}(initCtx)
}

func (d *Daemon) runWorktreeInitCommandList(ctx context.Context, initCtx worktreeInitContext, phase string, commands []string) error {
	if len(commands) == 0 {
		return nil
	}

	projectID := normalizedProjectID(initCtx.ProjectID)
	if strings.TrimSpace(initCtx.ProjectRoot) == "" {
		initCtx.ProjectRoot = d.resolveRepoDirForProject(projectID)
	}
	projectCfg := d.runtimeConfigForProject(projectID)
	shell := strings.TrimSpace(projectCfg.SessionShell)
	if shell == "" {
		shell = appconfig.DefaultSessionShell()
	}

	for _, initCmd := range commands {
		trimmed := strings.TrimSpace(initCmd)
		if trimmed == "" {
			continue
		}
		commandCtx, endSpan := latencytrace.StartSpan(ctx, "dependency", "worktree_init_command",
			"dependency.name", filepath.Base(shell),
			"dependency.operation", "run",
			"project_id", projectID,
			"arg_count", 2,
		)
		output, err := d.runSessionShell(commandCtx, shell, initCtx.WorktreePath, trimmed, worktreeInitCommandEnv(initCtx, phase))
		endSpan(err)
		if err != nil {
			return fmt.Errorf("%s: %w (%s)", trimmed, err, strings.TrimSpace(string(output)))
		}
	}

	return nil
}

func (d *Daemon) runSessionShell(ctx context.Context, shell, workdir, command string, env []string) ([]byte, error) {
	if d != nil && d.sessionShellRun != nil {
		return d.sessionShellRun(ctx, shell, workdir, command, env)
	}
	cmd := exec.CommandContext(ctx, shell, "-lc", command)
	cmd.Dir = workdir
	cmd.Env = env
	return cmd.CombinedOutput()
}

func worktreeInitCommandEnv(initCtx worktreeInitContext, phase string) []string {
	env := os.Environ()
	env = append(env,
		"AZEDARACH_PROJECT_ID="+normalizedProjectID(initCtx.ProjectID),
		"AZEDARACH_PROJECT_ROOT="+strings.TrimSpace(initCtx.ProjectRoot),
		"AZEDARACH_ISSUE_ID="+strings.TrimSpace(initCtx.IssueID),
		"AZEDARACH_WORKTREE_PATH="+strings.TrimSpace(initCtx.WorktreePath),
		"AZEDARACH_PARENT_ISSUE_ID="+strings.TrimSpace(initCtx.ParentIssueID),
		"AZEDARACH_PARENT_WORKTREE_PATH="+strings.TrimSpace(initCtx.ParentWorktreePath),
		"AZEDARACH_WORKTREE_INIT_PHASE="+strings.TrimSpace(phase),
	)
	return env
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
		if projectCfg.CodexAppServer {
			parts = append(parts, "--remote", "unix://")
		}
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
		promptAssignment := initialPromptShellAssignment(initialPrompt)
		promptArg := `"$` + initialPromptShellVariable + `"`
		switch strings.ToLower(tool) {
		case "codex":
			if !codexLaunchPromptArgAllowed(initialPrompt) {
				return strings.Join(parts, " ")
			}
			parts = append(parts, "--", promptArg)
		case "opencode":
			parts = append(parts, "--prompt", promptArg)
		default:
			parts = append(parts, promptArg)
		}
		command := promptAssignment + "; " + strings.Join(parts, " ")
		if strings.EqualFold(tool, "codex") && projectCfg.CodexAppServer {
			resume := d.codexAppServerResumeCommand(projectID, issueID, yolo)
			return codexAppServerSupervisedCommand(tool, command, resume)
		}
		return command
	}

	command := strings.Join(parts, " ")
	if strings.EqualFold(tool, "codex") && projectCfg.CodexAppServer {
		resume := d.codexAppServerResumeCommand(projectID, issueID, yolo)
		return codexAppServerSupervisedCommand(tool, command, resume)
	}
	return command
}

func (d *Daemon) codexAppServerResumeCommand(projectID, issueID string, yolo bool) string {
	projectCfg := d.runtimeConfigForProject(projectID)
	parts := []string{
		fmt.Sprintf(`AZEDARACH_ISSUE_ID="%s"`, escapeForShellDoubleQuotes(issueID)),
		strings.TrimSpace(projectCfg.CLITool),
		"resume",
		"--remote",
		"unix://",
	}
	if yolo || projectCfg.DangerouslySkipPermissions {
		parts = append(parts, "--dangerously-bypass-approvals-and-sandbox")
	}
	parts = append(parts, "--last")
	return strings.Join(parts, " ")
}

func codexAppServerSupervisedCommand(tool, firstCommand, resumeCommand string) string {
	startDaemon := tool + " app-server daemon start >/dev/null"
	steps := []string{
		"__az_codex_remote_started=$(date +%s)",
		"if [ \"$__az_codex_remote_first\" -eq 1 ]; then __az_codex_remote_first=0; " + firstCommand + "; else " + resumeCommand + "; fi",
		"__az_codex_remote_status=$?",
		"[ \"$__az_codex_remote_status\" -eq 0 ] && break",
		"__az_codex_remote_elapsed=$(($(date +%s)-__az_codex_remote_started))",
		"if [ \"$__az_codex_remote_elapsed\" -lt 5 ]; then __az_codex_remote_failures=$((__az_codex_remote_failures+1)); else __az_codex_remote_failures=1; fi",
		"[ \"$__az_codex_remote_failures\" -ge 3 ] && exit \"$__az_codex_remote_status\"",
		startDaemon + " || exit $?",
		"sleep 1",
	}
	return startDaemon + " || exit $?; __az_codex_remote_first=1; __az_codex_remote_failures=0; while :; do " + strings.Join(steps, "; ") + "; done"
}

const initialPromptShellVariable = "__AZEDARACH_INITIAL_PROMPT"

func initialPromptShellAssignment(prompt string) string {
	return initialPromptShellVariable + "=$(printf '%b' " + singleQuoteForShell(shellPrintfPercentBBytes(prompt)) + ")"
}

func shellPrintfPercentBBytes(value string) string {
	var b strings.Builder
	b.Grow(len(value) * 5)
	for i := 0; i < len(value); i++ {
		fmt.Fprintf(&b, `\0%03o`, value[i])
	}
	return b.String()
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
		base += "\n\nReview closeout contract: resolve accepted review tickets with `az orchestrate review accept --root <root-issue> --issue <review-issue>` so trusted acceptance and authoritative terminal close finish before dependent results are used or presented. Return unresolved findings with `az orchestrate review return --root <root-issue> --issue <review-issue> --finding \"...\"`. Reuse the reported `--intent-key` when retrying the same decision. Pending, returned, and human-gated reviews remain non-terminal."
		base += "\nThis review contract supersedes generic manual-close wording below for review decisions; `az issue close` remains only a repair/manual-close path, not reviewer acceptance authority."
		return base + "\n\nRole: orchestrator\n- Use `az orchestrate status --root <issue-id>` for readiness snapshots including active worker activity (`busy|idle|waiting|no-agent|unknown`).\n- Use `az orchestrate watch --root <issue-id> --since <seq> --jsonl` for continuous observe-only mailbox/runnable/activity updates; start it in another pane/session and leave it running while workers are active. Remain in this active turn/loop and continuously consume its events; starting sessions and a background watch is not a completed handoff to the human.\n- Do not use `--once` for orchestration monitoring; reserve it for diagnostic single polls.\n- Trust hook-backed `activity=busy|idle|waiting` for worker idleness checks and treat `activity=no-agent` as an intentional session-only shell. If activity is `unknown`, inspect hooks with `az ai status --target=auto`; run `az ai install --target=auto` only when hooks are missing, outdated, or not installed. Use `az orchestrate capture --issue <worker-issue>` only when watch/status look stale, failed, or contradictory, or when you need a sparse progress spot-check. Do not poll panes on a fixed interval.\n- Start this root's direct runnable leaf workers manually with `az orchestrate start --root <issue-id> --limit 4`, then immediately ensure the continuous watch is running. Queued reviews do not block unrelated starts when managed agent capacity remains.\n- Nested epic/root rule: if a runnable child is itself an epic/root that should self-orchestrate, start that child's own orchestrator session with `az orchestrator-session start --root <child-root>` and let that session run `az prime` and manage `az orchestrate start --root <child-root>`; supervise that nested orchestrator as a direct child while it owns its descendant workers, and do not launch or take over those descendants unless the user explicitly asks to flatten orchestration.\n- Send running-worker nudges with `az orchestrate message --root <issue-id> --issue <worker-issue> --body \"...\"`; workers reporting to this active parent orchestrator/watch should use `az mail send --parent <issue-id> --issue <worker-issue> --type worker-progress|worker-blocked|worker-integration-ready --body \"...\"`; non-orchestrated progress, follow-ups, validation, risks, or completion evidence should use `az issue record` instead. Bare `az mail send` is durable mailbox-only.\n- React to progress, blocked, and integration-ready evidence; review and integrate accepted children/epics, advance newly unblocked work, and repeat status/start/watch/review while graph work remains.\n- Worker integration evidence should be a structured JSON `worker_evidence.v1` packet with `summary`, `commands_run`, `key_assertions`, `files_changed`, `review.status`, `review.findings`, and `risks`; use `az issue record --type evidence.submitted --data '<json>'` when mailbox delivery is irrelevant, and use `worker-integration-ready` mail only for active parent coordination. Omit `artifact_links` unless needed, and when present use objects like `[{\"label\":\"CI\",\"url\":\"https://example.test/run\"}]`, not a string array. Run `az evidence validate --body '<json>'` before recording or sending, `az evidence validate --fix --body '<json>'` for repairable schema aliases, or `az evidence validate --template` for the canonical template.\n- Delegate every non-trivial diff inspection to a fresh ephemeral review subagent; use bounded parallel delegates for distinct review-ready issues. Give each delegate the issue acceptance context, worker evidence, context risk, exact worktree, and diff base. Delegates are read-only: they must not edit, mutate issue/session state, send findings, accept/return reviews, integrate, or close. Require a structured clean-or-findings verdict with commands and risks. Validate the packet and spot-check high-risk or contradictory results; keep durable review-return, review-accept, integration, and close authority in this orchestrator.\n- Treat blocked work as graph state from unresolved `blocks` dependencies or active worker-blocked mailbox evidence, not as an issue status.\n- Treat `in_review` workers as ready for orchestrator validation; inspect evidence, delegate diff review, run parent checks, then close accepted worker issues with `az issue close --id <issue-id>`.\n- Parent/tracker completion includes child lifecycle cleanup: close accepted completed children with `az issue close --id <child-issue>`, and leave any child `open` or `in_progress` only with an explicit blocker, dependency, or remaining-scope rationale.\n- Use `az orchestrate integrate --issue <issue-id>` for worker result inspection or repair guidance, not as the normal merge authority.\n- Use `az orchestrate close-session --issue <issue-id>` only for exceptional session cleanup when a worker must be stopped without closing the issue; never use it for ordinary review handoff.\n- Keep orchestration authority centralized inside each root session while delegating read-only review inspection; delegate explicit nested epic/root issues to their own orchestrator sessions rather than flattening their children into this session.\n- Continue the parent loop until `az orchestrate complete-check --root <issue-id>` and final validation pass; only then set the root `in_review` and hand it to the human while keeping its tmux session/worktree alive. Close/integrate the root only after explicit human acceptance."
	}
	if !orchestratedWorker {
		return base + "\n\nRole: contributor\n- Focus only on this issue scope unless the user explicitly expands it.\n- Keep issue status current; record progress, follow-ups, validation, blockers, review facts, risks, and closeout evidence with `az issue record`; keep notes as terse human audit scratchpad only.\n- Use `in_progress` while actively working and `in_review` when complete and awaiting review/integration. Review handoff is non-terminal: preserve the tmux session and worktree; do not stop or close them while waiting for human feedback.\n- Use `closed` only after explicit acceptance/integration and `cancelled` only for a terminal non-integrated outcome.\n- Represent blocked work with dependency edges and issue record evidence, not by using `in_review`."
	}
	mailboxGuidance := "- Check inbound orchestrator messages with `az mail list --parent <parent-issue> --since 0 --json` before declaring yourself blocked or idle; apply events for this issue and continue without waiting for a separate user prompt."
	if strings.TrimSpace(parentIssueID) != "" {
		mailboxGuidance = fmt.Sprintf("- Coordination mailbox parent: `%s`; check inbound orchestrator messages with `az mail list --parent %s --since 0 --json` before declaring yourself blocked or idle; apply events for this issue and continue without waiting for a separate user prompt.", parentIssueID, parentIssueID)
	}
	reportParentID := "<parent-issue>"
	if strings.TrimSpace(parentIssueID) != "" {
		reportParentID = parentIssueID
	}
	return base + "\n\nRole: worker\n- Focus only on this issue scope unless the user explicitly expands it.\n" + mailboxGuidance + "\n- Report coordination state to an active parent orchestrator/watch with `az mail send --parent " + reportParentID + " --issue " + issueID + " --type worker-progress|worker-blocked|worker-integration-ready --body \"...\"`; do not use `az orchestrate message` for your own status because it is an orchestrator-to-worker live delivery command.\n- Use mailbox event types only for active coordination: worker-progress, worker-blocked, and worker-integration-ready; worker-ready and worker-complete are accepted only as legacy aliases for worker-integration-ready. For non-orchestrated progress, follow-ups, validation, risks, blockers, or closeout evidence, use `az issue record` instead.\n- Evidence bodies should be JSON `worker_evidence.v1` packets with `summary`, `commands_run`, `key_assertions`, `files_changed`, `review.status`, `review.findings`, and `risks`; use `\"none\"` entries when a required list has no findings or risks. Omit `artifact_links` unless links are needed; when present, encode it as objects like `[{\"label\":\"CI\",\"url\":\"https://example.test/run\"}]`, not a string array. Preflight with `az evidence validate --body '<json>'`; use `az issue record " + issueID + " --type evidence.submitted --data '<json>'` when mailbox delivery is irrelevant.\n- Before handing off, run the relevant validation/review checks, build the final `worker_evidence.v1` packet from actual results, run `az evidence validate --body '<json>'`, record or send that exact JSON packet, then set/leave the issue `in_review`. Review handoff is non-terminal: preserve this tmux session and worktree for orchestrator feedback; do not stop or close them yourself.\n- Keep issue status current; report progress, blockers, risks, and readiness through issue records or active-coordination mailbox evidence, with notes as terse human audit scratchpad only.\n- Use `in_progress` while actively working and `in_review` when complete and ready for orchestrator integration; the orchestrator closes accepted work.\n- Report blockers via dependency edges, issue record evidence, or active worker-blocked mailbox events, not by setting `in_review`."
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
