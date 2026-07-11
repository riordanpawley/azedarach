package state

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	// ErrSessionNotFound indicates session state was requested for missing session ID.
	ErrSessionNotFound = errors.New("session not found")
	// ErrInvalidTransition indicates requested session transition is not allowed.
	ErrInvalidTransition = errors.New("invalid session transition")
)

// SessionState represents daemon-authoritative lifecycle state.
type SessionState string

type SessionRole string
type SessionScopeKind string

const (
	SessionRoleWorker       SessionRole = "worker"
	SessionRoleOrchestrator SessionRole = "orchestrator"
	SessionRoleAdvisor      SessionRole = "advisor"

	SessionScopeIssue         SessionScopeKind = "issue"
	SessionScopeOrchestration SessionScopeKind = "orchestration"
	SessionScopeInteraction   SessionScopeKind = "interaction"
)

const (
	SessionStateStarting SessionState = "starting"
	SessionStateRunning  SessionState = "running"
	SessionStateStopping SessionState = "stopping"
	// SessionStateAttached is a deprecated compatibility alias. Use
	// SessionStateRunning for persisted/protocol state.
	SessionStateAttached SessionState = SessionStateRunning
	SessionStatePaused   SessionState = "paused"
	SessionStateStopped  SessionState = "stopped"
)

// Session contains authoritative session state.
type Session struct {
	ID                string
	IssueID           string
	Role              SessionRole
	ScopeKind         SessionScopeKind
	ScopeID           string
	State             SessionState
	ObservedState     SessionState
	Activity          string
	ActivitySource    string
	TmuxAttachedCount int
	StartedAt         *time.Time
	UpdatedAt         time.Time
}

func NormalizeSessionMetadata(session Session) Session {
	if session.Role == "" {
		session.Role = SessionRoleWorker
	}
	if session.ScopeKind == "" {
		session.ScopeKind = SessionScopeIssue
	}
	if strings.TrimSpace(session.ScopeID) == "" && session.ScopeKind == SessionScopeIssue {
		session.ScopeID = strings.TrimSpace(session.IssueID)
	}
	return session
}

// Snapshot is a read model for frontend/client attach flows.
type Snapshot struct {
	ProjectID string
	Revision  uint64
	Sessions  map[string]Session
}

// SessionEvent is emitted for each committed transition.
type SessionEvent struct {
	ProjectID string
	Revision  uint64
	Type      string
	Session   Session
}

// Store is an in-memory authoritative lifecycle state store.
type Store struct {
	mu       sync.RWMutex
	projects map[string]*projectState
	nowFn    func() time.Time
}

type projectState struct {
	revision uint64
	sessions map[string]Session
}

// NewStore returns an empty authoritative store.
func NewStore() *Store {
	return &Store{
		projects: make(map[string]*projectState),
		nowFn:    time.Now,
	}
}

// ReadSnapshot returns project state at current revision.
func (s *Store) ReadSnapshot(projectID string) Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ps := s.ensureProjectLocked(projectID)
	out := Snapshot{
		ProjectID: projectID,
		Revision:  ps.revision,
		Sessions:  make(map[string]Session, len(ps.sessions)),
	}
	for id, session := range ps.sessions {
		session = NormalizeSessionMetadata(session)
		session.State = NormalizeSessionState(session.State)
		session.ObservedState = NormalizeSessionState(session.ObservedState)
		out.Sessions[id] = session
	}
	return out
}

// CurrentRevision returns the current project revision watermark.
func (s *Store) CurrentRevision(projectID string) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ps := s.ensureProjectLocked(projectID)
	return ps.revision
}

// UpsertSession creates or updates session state and increments revision.
func (s *Store) UpsertSession(projectID, sessionID, issueID string, state SessionState) (SessionEvent, error) {
	return s.upsertSession(projectID, sessionID, issueID, state, true)
}

// ForceUpsertSession creates or updates session state without enforcing lifecycle transition validity.
func (s *Store) ForceUpsertSession(projectID, sessionID, issueID string, state SessionState) (SessionEvent, error) {
	return s.upsertSession(projectID, sessionID, issueID, state, false)
}

func (s *Store) upsertSession(projectID, sessionID, issueID string, state SessionState, validate bool) (SessionEvent, error) {
	state = NormalizeSessionState(state)
	if sessionID == "" {
		return SessionEvent{}, fmt.Errorf("%w: missing session id", ErrInvalidTransition)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ps := s.ensureProjectLocked(projectID)
	existing, ok := ps.sessions[sessionID]
	if validate && ok {
		existing.State = NormalizeSessionState(existing.State)
		existing.ObservedState = NormalizeSessionState(existing.ObservedState)
		if !isValidTransition(existing.State, state) {
			return SessionEvent{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, existing.State, state)
		}
	}
	ps.revision++
	now := s.nowFn().UTC()
	var startedAt *time.Time
	observedState := state
	if ok && existing.StartedAt != nil && !existing.StartedAt.IsZero() {
		resetStartTimestamp := existing.State == SessionStateStopped && state == SessionStateStarting
		if !resetStartTimestamp {
			preserved := existing.StartedAt.UTC()
			startedAt = &preserved
		}
	}
	if startedAt == nil && state != SessionStateStopped {
		start := now
		startedAt = &start
	}
	if ok && strings.TrimSpace(string(existing.ObservedState)) != "" {
		observedState = NormalizeSessionState(existing.ObservedState)
	}
	if state == SessionStateStopped {
		observedState = SessionStateStopped
	}
	next := Session{
		ID:             sessionID,
		IssueID:        issueID,
		Role:           existing.Role,
		ScopeKind:      existing.ScopeKind,
		ScopeID:        existing.ScopeID,
		State:          state,
		ObservedState:  observedState,
		Activity:       strings.TrimSpace(existing.Activity),
		ActivitySource: strings.TrimSpace(existing.ActivitySource),
		StartedAt:      startedAt,
		UpdatedAt:      now,
	}
	next = NormalizeSessionMetadata(next)
	ps.sessions[sessionID] = next
	return SessionEvent{
		ProjectID: projectID,
		Revision:  ps.revision,
		Type:      "session.updated",
		Session:   next,
	}, nil
}

// NormalizeSessionState maps historical lifecycle literals onto the canonical
// vocabulary used by current runtime projections.
func NormalizeSessionState(state SessionState) SessionState {
	switch strings.TrimSpace(string(state)) {
	case "attached", string(SessionStateRunning):
		return SessionStateRunning
	case string(SessionStateStarting):
		return SessionStateStarting
	case string(SessionStatePaused):
		return SessionStatePaused
	case string(SessionStateStopping):
		return SessionStateStopping
	case string(SessionStateStopped):
		return SessionStateStopped
	default:
		return state
	}
}

// Session returns a single session state.
func (s *Store) Session(projectID, sessionID string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ps := s.ensureProjectLocked(projectID)
	session, ok := ps.sessions[sessionID]
	if !ok {
		return Session{}, ErrSessionNotFound
	}
	return session, nil
}

// SessionByIssueID returns the most recent session whose issue ID matches the given issue.
func (s *Store) SessionByIssueID(projectID, issueID string) (Session, bool) {
	projectID = strings.TrimSpace(projectID)
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return Session{}, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	ps := s.ensureProjectLocked(projectID)
	var newest Session
	found := false
	for _, session := range ps.sessions {
		session.State = NormalizeSessionState(session.State)
		session.ObservedState = NormalizeSessionState(session.ObservedState)
		if strings.TrimSpace(session.IssueID) == issueID {
			if !found || session.UpdatedAt.After(newest.UpdatedAt) {
				newest = session
				found = true
			}
		}
	}
	return newest, found
}

// ReplaceProjectSessions atomically replaces the in-memory session cache for a project.
// Intended for durable->cache hydration before invariant reads.
func (s *Store) ReplaceProjectSessions(projectID string, sessions []Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ps := s.ensureProjectLocked(projectID)
	next := make(map[string]Session, len(sessions))
	for _, session := range sessions {
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			continue
		}
		session.State = NormalizeSessionState(session.State)
		session.ObservedState = NormalizeSessionState(session.ObservedState)
		next[sessionID] = session
	}
	ps.sessions = next
}

// ProjectIDs returns the known project IDs currently present in the store.
func (s *Store) ProjectIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]struct{}, len(s.projects))
	ids := make([]string, 0, len(s.projects))
	for projectID := range s.projects {
		normalized := strings.TrimSpace(projectID)
		if normalized == "" {
			normalized = "default"
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		ids = append(ids, normalized)
	}
	slices.Sort(ids)
	return ids
}

func (s *Store) ensureProjectLocked(projectID string) *projectState {
	ps, ok := s.projects[projectID]
	if !ok {
		ps = &projectState{
			sessions: make(map[string]Session),
		}
		s.projects[projectID] = ps
	}
	return ps
}

func isValidTransition(from, to SessionState) bool {
	from = NormalizeSessionState(from)
	to = NormalizeSessionState(to)
	if from == to {
		return true
	}
	switch from {
	case SessionStateStarting:
		return slices.Contains([]SessionState{SessionStateRunning, SessionStateStopped}, to)
	case SessionStateRunning:
		return slices.Contains([]SessionState{SessionStatePaused, SessionStateStopped}, to)
	case SessionStatePaused:
		return slices.Contains([]SessionState{SessionStateRunning, SessionStateStopping, SessionStateStopped}, to)
	case SessionStateStopping:
		return slices.Contains([]SessionState{SessionStateStopped}, to)
	case SessionStateStopped:
		return slices.Contains([]SessionState{SessionStateStarting}, to)
	default:
		return false
	}
}
