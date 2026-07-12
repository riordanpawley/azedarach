package state

import (
	"fmt"
	"strings"
)

// ValidateSessionProduct validates the local, typed session state product.
// Relational stores additionally verify that issue-scoped identities exist.
func ValidateSessionProduct(session Session) error {
	session = NormalizeSessionMetadata(session)
	issueID := strings.TrimSpace(session.IssueID)
	scopeID := strings.TrimSpace(session.ScopeID)
	switch session.Role {
	case SessionRoleWorker:
		if session.ScopeKind != SessionScopeIssue || issueID == "" || scopeID != issueID {
			return fmt.Errorf("worker session requires matching issue scope")
		}
	case SessionRoleAdvisor:
		if session.ScopeKind != SessionScopeInteraction || scopeID == "" {
			return fmt.Errorf("advisor session requires interaction scope")
		}
	case SessionRoleOrchestrator:
		if session.ScopeKind != SessionScopeOrchestration || scopeID == "" {
			return fmt.Errorf("orchestrator session requires orchestration scope")
		}
		if scopeID == "project" {
			if issueID != "" {
				return fmt.Errorf("project orchestrator must not have issue identity")
			}
		} else if issueID != scopeID {
			return fmt.Errorf("rooted orchestrator requires matching root issue identity")
		}
	default:
		return fmt.Errorf("invalid session role %q", session.Role)
	}
	if !validSessionProductState(session.State, false) {
		return fmt.Errorf("invalid desired session state %q", session.State)
	}
	if !validSessionProductState(session.ObservedState, true) {
		return fmt.Errorf("invalid observed session state %q", session.ObservedState)
	}
	if session.TmuxAttachedCount < 0 {
		return fmt.Errorf("tmux attachment count cannot be negative")
	}
	if (session.State == SessionStateStopped || session.ObservedState == SessionStateStopped) && session.TmuxAttachedCount != 0 {
		return fmt.Errorf("stopped session cannot be attached")
	}
	return nil
}

func validSessionProductState(state SessionState, allowEmpty bool) bool {
	if allowEmpty && strings.TrimSpace(string(state)) == "" {
		return true
	}
	switch NormalizeSessionState(state) {
	case SessionStateStarting, SessionStateRunning, SessionStateStopping, SessionStatePaused, SessionStateStopped:
		return true
	default:
		return false
	}
}
