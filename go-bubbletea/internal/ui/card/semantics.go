package card

import (
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/workflows/session"
)

type Badge string

const (
	BadgeTaskOpen       Badge = "task:open"
	BadgeTaskInProgress Badge = "task:in_progress"
	BadgeTaskBlocked    Badge = "task:blocked"
	BadgeTaskDone       Badge = "task:done"

	BadgeSessionBusy    Badge = "session:busy"
	BadgeSessionWaiting Badge = "session:waiting"
	BadgeSessionPaused  Badge = "session:paused"
	BadgeSessionError   Badge = "session:error"
	BadgeSessionDone    Badge = "session:done"

	BadgePROpen   Badge = "pr:open"
	BadgePRDraft  Badge = "pr:draft"
	BadgePRMerged Badge = "pr:merged"
	BadgePRClosed Badge = "pr:closed"

	BadgeDevOn  Badge = "dev:on"
	BadgeDevOff Badge = "dev:off"
)

type PRState string

const (
	PRStateOpen   PRState = "open"
	PRStateDraft  PRState = "draft"
	PRStateMerged PRState = "merged"
	PRStateClosed PRState = "closed"
)

type Model struct {
	TaskStatus      domain.Status
	SessionState    session.State
	PullRequest     PRState
	DevServerActive bool
}

func DeriveBadges(model Model) []Badge {
	out := make([]Badge, 0, 4)

	if taskBadge, ok := taskBadge(model.TaskStatus); ok {
		out = append(out, taskBadge)
	}
	if sessionBadge, ok := sessionBadge(model.SessionState); ok {
		out = append(out, sessionBadge)
	}
	if prBadge, ok := prBadge(model.PullRequest); ok {
		out = append(out, prBadge)
	}
	if model.DevServerActive {
		out = append(out, BadgeDevOn)
	} else {
		out = append(out, BadgeDevOff)
	}

	return out
}

func taskBadge(status domain.Status) (Badge, bool) {
	switch status {
	case domain.StatusOpen:
		return BadgeTaskOpen, true
	case domain.StatusInProgress:
		return BadgeTaskInProgress, true
	case domain.StatusBlocked:
		return BadgeTaskBlocked, true
	case domain.StatusDone:
		return BadgeTaskDone, true
	default:
		return "", false
	}
}

func sessionBadge(state session.State) (Badge, bool) {
	switch state {
	case session.StateBusy:
		return BadgeSessionBusy, true
	case session.StateWaiting:
		return BadgeSessionWaiting, true
	case session.StatePaused:
		return BadgeSessionPaused, true
	case session.StateError:
		return BadgeSessionError, true
	case session.StateDone:
		return BadgeSessionDone, true
	default:
		return "", false
	}
}

func prBadge(state PRState) (Badge, bool) {
	switch state {
	case PRStateOpen:
		return BadgePROpen, true
	case PRStateDraft:
		return BadgePRDraft, true
	case PRStateMerged:
		return BadgePRMerged, true
	case PRStateClosed:
		return BadgePRClosed, true
	default:
		return "", false
	}
}
