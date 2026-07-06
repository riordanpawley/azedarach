package domain

import (
	"fmt"
	"testing"
)

func TestBuildIssueContextRiskIgnoresBroadSiblingRootWithoutOverlap(t *testing.T) {
	candidates := make([]IssueContextRiskEvidence, 0, 120)
	for i := 0; i < 120; i++ {
		candidates = append(candidates, IssueContextRiskEvidence{
			IssueID:      fmt.Sprintf("az-broad-%03d", i),
			Relationship: "sibling",
			Files:        []string{fmt.Sprintf("internal/other/file_%03d.go", i)},
		})
	}

	packet := BuildIssueContextRisk(IssueContextRiskInput{
		Target: IssueContextRiskEvidence{
			IssueID: "az-target",
			Files:   []string{"internal/domain/context.go"},
		},
		Candidates: candidates,
	})

	if packet.Level != IssueContextRiskNone {
		t.Fatalf("Level = %s, want %s", packet.Level, IssueContextRiskNone)
	}
	if len(packet.Clusters) != 0 {
		t.Fatalf("Clusters = %+v, want none", packet.Clusters)
	}
}

func TestBuildIssueContextRiskReportsNarrowRepeatedFileCluster(t *testing.T) {
	packet := BuildIssueContextRisk(IssueContextRiskInput{
		Target: IssueContextRiskEvidence{
			IssueID:   "az-target",
			Files:     []string{"internal/daemon/task_commands.go"},
			Invariant: "closeout checks use projection state",
		},
		Candidates: []IssueContextRiskEvidence{
			{
				IssueID:       "az-prev-1",
				Relationship:  "sibling",
				Files:         []string{"internal/daemon/task_commands.go"},
				EvidenceKinds: []string{"worker_evidence.v1"},
			},
			{
				IssueID:       "az-prev-2",
				Relationship:  "sibling",
				Files:         []string{"internal/daemon/task_commands.go"},
				RiskNotes:     []string{"same closeout failure repeated"},
				EvidenceKinds: []string{"risk.recorded"},
			},
		},
	})

	if packet.Level != IssueContextRiskHigh {
		t.Fatalf("Level = %s, want %s", packet.Level, IssueContextRiskHigh)
	}
	if packet.OverlapIssueCount != 2 {
		t.Fatalf("OverlapIssueCount = %d, want 2", packet.OverlapIssueCount)
	}
	if len(packet.Clusters) != 1 || packet.Clusters[0].Kind != "file" || packet.Clusters[0].Value != "internal/daemon/task_commands.go" {
		t.Fatalf("Clusters = %+v, want file cluster", packet.Clusters)
	}
	if len(packet.CloseoutPrompts) == 0 {
		t.Fatal("CloseoutPrompts empty, want bounded prompts")
	}
}

func TestIssueContextRiskRequiresStructuredCloseout(t *testing.T) {
	packet := BuildIssueContextRisk(IssueContextRiskInput{
		Target: IssueContextRiskEvidence{
			IssueID: "az-target",
			Files:   []string{"internal/daemon/task_commands.go"},
		},
		Candidates: []IssueContextRiskEvidence{
			{
				IssueID:       "az-prev-1",
				Relationship:  "sibling",
				Files:         []string{"internal/daemon/task_commands.go"},
				RiskNotes:     []string{"same failure recurred"},
				EvidenceKinds: []string{"risk.recorded"},
			},
			{
				IssueID:       "az-prev-2",
				Relationship:  "sibling",
				Files:         []string{"internal/daemon/task_commands.go"},
				EvidenceKinds: []string{"validation.failed"},
			},
		},
	})
	if packet.Level != IssueContextRiskHigh {
		t.Fatalf("Level = %s, want high", packet.Level)
	}
	if !IssueContextRiskRequiresStructuredCloseout(packet) {
		t.Fatal("IssueContextRiskRequiresStructuredCloseout = false, want true without target evidence")
	}

	packet.Evidence[0].Validation = "regression test covers repeated closeout failure"
	if IssueContextRiskRequiresStructuredCloseout(packet) {
		t.Fatal("IssueContextRiskRequiresStructuredCloseout = true, want false with target validation")
	}
}
