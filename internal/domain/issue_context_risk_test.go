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

func TestSummarizeIssueContextRiskBoundsEvidenceAndSignals(t *testing.T) {
	candidates := make([]IssueContextRiskEvidence, 0, 8)
	for i := 0; i < 8; i++ {
		candidates = append(candidates, IssueContextRiskEvidence{
			IssueID:      fmt.Sprintf("az-prev-%d", i),
			Relationship: "sibling",
			Files:        []string{"internal/daemon/task_commands.go", fmt.Sprintf("internal/other/%d.go", i)},
			Symbols:      []string{"closeTask"},
			RiskNotes:    []string{fmt.Sprintf("risk note %d", i)},
		})
	}
	packet := BuildIssueContextRisk(IssueContextRiskInput{
		Target: IssueContextRiskEvidence{
			IssueID: "az-target",
			Files:   []string{"internal/daemon/task_commands.go"},
			Symbols: []string{"closeTask"},
		},
		Candidates: candidates,
	})

	summary := SummarizeIssueContextRisk(packet)
	if len(summary.Signals) > 5 {
		t.Fatalf("signals = %d, want bounded to 5", len(summary.Signals))
	}
	if len(summary.EvidenceSnippets) != 3 {
		t.Fatalf("evidence snippets = %d, want 3", len(summary.EvidenceSnippets))
	}
	if len(summary.RelatedIssueIDs) != 8 {
		t.Fatalf("related issue ids = %+v, want all 8 related ids", summary.RelatedIssueIDs)
	}
}

func TestCompactIssueContextRiskKeepsTargetEvidenceForCloseoutGate(t *testing.T) {
	packet := BuildIssueContextRisk(IssueContextRiskInput{
		Target: IssueContextRiskEvidence{
			IssueID: "az-target",
			Files:   []string{"internal/daemon/task_commands.go"},
		},
		Candidates: []IssueContextRiskEvidence{
			{IssueID: "az-prev-1", Files: []string{"internal/daemon/task_commands.go"}, RiskNotes: []string{"same failure"}},
			{IssueID: "az-prev-2", Files: []string{"internal/daemon/task_commands.go"}, RiskNotes: []string{"same failure"}},
			{IssueID: "az-prev-3", Files: []string{"internal/daemon/task_commands.go"}, RiskNotes: []string{"same failure"}},
			{IssueID: "az-prev-4", Files: []string{"internal/daemon/task_commands.go"}, RiskNotes: []string{"same failure"}},
		},
	})

	compact := CompactIssueContextRisk(packet)
	if len(compact.Evidence) != 3 {
		t.Fatalf("compact evidence = %d, want 3", len(compact.Evidence))
	}
	if compact.Evidence[0].IssueID != "az-target" {
		t.Fatalf("first compact evidence = %+v, want target evidence preserved", compact.Evidence[0])
	}
	if !compact.EvidenceTruncated {
		t.Fatal("EvidenceTruncated = false, want true")
	}
	if !IssueContextRiskRequiresStructuredCloseout(compact) {
		t.Fatal("compact packet no longer blocks high risk without target evidence")
	}
}
