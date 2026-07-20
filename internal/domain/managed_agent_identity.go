package domain

import (
	"fmt"
	"strings"
)

// ManagedAgentPaneID is the stable logical identity of an agent pane within a
// managed session. It is deliberately independent of tmux pane allocation.
type ManagedAgentPaneID string

// ManagedAgentRuntimeIdentity binds a logical managed pane to one physical
// tmux process and one agent process incarnation.
type ManagedAgentRuntimeIdentity struct {
	LogicalPaneID    ManagedAgentPaneID `json:"logical_pane_id"`
	TmuxPaneID       string             `json:"tmux_pane_id"`
	PanePID          int                `json:"pane_pid"`
	AgentIncarnation string             `json:"agent_incarnation"`
	AgentThreadID    string             `json:"agent_thread_id,omitempty"`
}

func (i ManagedAgentRuntimeIdentity) Validate() error {
	if strings.TrimSpace(string(i.LogicalPaneID)) == "" {
		return fmt.Errorf("managed agent identity: missing logical pane id")
	}
	if strings.TrimSpace(i.TmuxPaneID) == "" {
		return fmt.Errorf("managed agent identity: missing tmux pane id")
	}
	if i.PanePID <= 0 {
		return fmt.Errorf("managed agent identity: invalid pane pid %d", i.PanePID)
	}
	if strings.TrimSpace(i.AgentIncarnation) == "" {
		return fmt.Errorf("managed agent identity: missing agent incarnation")
	}
	return nil
}

// SameIncarnation reports whether an observation belongs to the exact current
// physical pane and agent process, not merely a reused tmux pane ID.
func (i ManagedAgentRuntimeIdentity) SameIncarnation(other ManagedAgentRuntimeIdentity) bool {
	return i.LogicalPaneID == other.LogicalPaneID &&
		strings.TrimSpace(i.TmuxPaneID) == strings.TrimSpace(other.TmuxPaneID) &&
		i.PanePID == other.PanePID &&
		strings.TrimSpace(i.AgentIncarnation) == strings.TrimSpace(other.AgentIncarnation) &&
		strings.TrimSpace(i.AgentThreadID) == strings.TrimSpace(other.AgentThreadID)
}
