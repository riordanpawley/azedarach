package domain

import "testing"

func TestManagedAgentRuntimeIdentitySameIncarnationRejectsReusedPane(t *testing.T) {
	current := ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "%12", PanePID: 410, AgentIncarnation: "thread-new"}
	if err := current.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for name, candidate := range map[string]ManagedAgentRuntimeIdentity{
		"stale pid":          {LogicalPaneID: "agent", TmuxPaneID: "%12", PanePID: 309, AgentIncarnation: "thread-new"},
		"stale agent":        {LogicalPaneID: "agent", TmuxPaneID: "%12", PanePID: 410, AgentIncarnation: "thread-old"},
		"wrong logical pane": {LogicalPaneID: "reviewer", TmuxPaneID: "%12", PanePID: 410, AgentIncarnation: "thread-new"},
	} {
		t.Run(name, func(t *testing.T) {
			if current.SameIncarnation(candidate) {
				t.Fatal("stale/reused identity matched current incarnation")
			}
		})
	}
}
