package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestTaskContextRiskDoesNotTreatBroadRootAsRiskWithoutOverlap(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-context-risk-broad"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Large root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	targetID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Target child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	now := time.Now().UTC()
	appendContextRiskEvidence(t, repoDir, parentID, targetID, 1, now, []string{"internal/daemon/context_risk_commands.go"}, "none")
	for i := 0; i < 100; i++ {
		childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
			Title:    fmt.Sprintf("Broad sibling %03d", i),
			Type:     domain.TypeTask,
			Status:   domain.StatusDone,
			ParentID: &parentID,
		})
		if err != nil {
			t.Fatalf("create broad sibling %d: %v", i, err)
		}
		appendContextRiskEvidence(t, repoDir, parentID, childID, int64(i+2), now, []string{fmt.Sprintf("internal/other/file_%03d.go", i)}, "none")
	}

	d := newContextRiskTestDaemon(projectID, repoDir, issuesClient)
	packet, err := d.taskContextRisk(ctx, projectID, targetID, repoDir, now.Add(-14*24*time.Hour))
	if err != nil {
		t.Fatalf("taskContextRisk error: %v", err)
	}
	if packet.Level != domain.IssueContextRiskNone {
		t.Fatalf("Level = %s, want %s; packet=%+v", packet.Level, domain.IssueContextRiskNone, packet)
	}
	if packet.CandidateCount != 100 || packet.OverlapIssueCount != 0 {
		t.Fatalf("counts = candidates %d overlaps %d, want 100/0", packet.CandidateCount, packet.OverlapIssueCount)
	}
}

func TestTaskContextRiskReportsRepeatedSiblingFileCluster(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-context-risk-cluster"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	targetID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Target child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	firstID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Prior child one", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create first sibling: %v", err)
	}
	secondID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Prior child two", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create second sibling: %v", err)
	}
	now := time.Now().UTC()
	sharedFile := "internal/daemon/task_commands.go"
	appendContextRiskEvidence(t, repoDir, parentID, targetID, 1, now, []string{sharedFile}, "none")
	appendContextRiskEvidence(t, repoDir, parentID, firstID, 2, now, []string{sharedFile}, "none")
	appendContextRiskEvidence(t, repoDir, parentID, secondID, 3, now, []string{sharedFile}, "same closeout failure repeated")
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, secondID, issues.IssueObservationEventParams{
		Type:       domain.IssueEventRiskRecorded,
		ObservedAt: now,
		Payload: map[string]any{
			"files_changed": []string{sharedFile},
			"summary":       "same closeout failure repeated",
			"invariant":     "closeout checks use projection state",
		},
	}); err != nil {
		t.Fatalf("append risk event: %v", err)
	}

	d := newContextRiskTestDaemon(projectID, repoDir, issuesClient)
	packet, err := d.taskContextRisk(ctx, projectID, targetID, repoDir, now.Add(-14*24*time.Hour))
	if err != nil {
		t.Fatalf("taskContextRisk error: %v", err)
	}
	if packet.Level != domain.IssueContextRiskHigh {
		t.Fatalf("Level = %s, want %s; packet=%+v", packet.Level, domain.IssueContextRiskHigh, packet)
	}
	if packet.OverlapIssueCount != 2 || len(packet.Clusters) != 1 || packet.Clusters[0].Value != sharedFile {
		t.Fatalf("clusters = %+v overlap=%d, want shared file cluster with two issues", packet.Clusters, packet.OverlapIssueCount)
	}
}

func TestIssueContextRiskObservationEvidenceMapsRelatedConsumersAudited(t *testing.T) {
	evidence := issueContextRiskObservationEvidence(domain.IssueObservationEvent{
		IssueID: naming.IssueID("az-target"),
		Type:    domain.IssueEventRiskRecorded,
		Payload: map[string]any{
			"related_consumers_audited": []any{"task close", "review handoff"},
		},
	})

	if got, want := strings.Join(evidence.RelatedConsumersAudited, ","), "task close,review handoff"; got != want {
		t.Fatalf("RelatedConsumersAudited = %q, want %q", got, want)
	}
}

func TestTaskContextRiskMarksRelatedConsumersAuditedFromObservation(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-context-risk-related-consumers"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	targetID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Target", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	now := time.Now().UTC()
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, targetID, issues.IssueObservationEventParams{
		Type:       domain.IssueEventRiskRecorded,
		ObservedAt: now,
		Payload: map[string]any{
			"related_consumers_audited": []string{"daemon closeout gate", "CLI summary output"},
			"summary":                   "structured closeout evidence",
		},
	}); err != nil {
		t.Fatalf("append risk event: %v", err)
	}

	d := newContextRiskTestDaemon(projectID, repoDir, issuesClient)
	packet, err := d.taskContextRisk(ctx, projectID, targetID, repoDir, now.Add(-14*24*time.Hour))
	if err != nil {
		t.Fatalf("taskContextRisk error: %v", err)
	}
	if !containsContextRiskField(packet.HandoffFields.Present, "related_consumers_audited") {
		t.Fatalf("Present = %v, want related_consumers_audited", packet.HandoffFields.Present)
	}
	if containsContextRiskField(packet.HandoffFields.Missing, "related_consumers_audited") {
		t.Fatalf("Missing = %v, do not want related_consumers_audited", packet.HandoffFields.Missing)
	}
}

func TestTaskContextRiskCompactResponseBoundsReturnedEvidence(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-context-risk-compact"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	targetID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Target child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	now := time.Now().UTC()
	sharedFile := "internal/daemon/task_commands.go"
	appendContextRiskEvidence(t, repoDir, parentID, targetID, 1, now, []string{sharedFile}, "none")
	for i := 0; i < 6; i++ {
		childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
			Title:    fmt.Sprintf("Prior child %d", i),
			Type:     domain.TypeTask,
			Status:   domain.StatusDone,
			ParentID: &parentID,
		})
		if err != nil {
			t.Fatalf("create prior child %d: %v", i, err)
		}
		appendContextRiskEvidence(t, repoDir, parentID, childID, int64(i+2), now.Add(time.Duration(i)*time.Minute), []string{sharedFile}, "same failure repeated")
	}

	d := newContextRiskTestDaemon(projectID, repoDir, issuesClient)
	body, err := json.Marshal(map[string]any{
		"task_id":  targetID,
		"repo_dir": repoDir,
		"since":    now.Add(-14 * 24 * time.Hour),
		"compact":  true,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := d.handleTaskContextRisk(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-context-risk-compact",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.context_risk",
		Body:            body,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("handleTaskContextRisk error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response = %+v, want OK", resp)
	}
	var packet domain.IssueContextRiskPacket
	if err := json.Unmarshal(resp.Body, &packet); err != nil {
		t.Fatalf("unmarshal packet: %v", err)
	}
	if len(packet.Evidence) != 3 || !packet.EvidenceTruncated {
		t.Fatalf("evidence len=%d truncated=%t, want compact truncated packet", len(packet.Evidence), packet.EvidenceTruncated)
	}
	if len(packet.RelatedIssueIDs) != 6 {
		t.Fatalf("related ids = %+v, want all six overlap issue ids", packet.RelatedIssueIDs)
	}
	if len(packet.Evidence) == 0 || packet.Evidence[0].IssueID != targetID {
		t.Fatalf("compact evidence = %+v, want target evidence preserved first", packet.Evidence)
	}
}

func TestTaskContextRiskReadsTopLevelRelatedMailboxEvidence(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-context-risk-top-level"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	targetID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Target root", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	relatedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Related root", Type: domain.TypeTask, Status: domain.StatusDone})
	if err != nil {
		t.Fatalf("create related: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, targetID, relatedID, string(domain.DependencyRelatedTo)); err != nil {
		t.Fatalf("add related dependency: %v", err)
	}
	now := time.Now().UTC()
	sharedFile := "internal/cli/commands.go"
	appendContextRiskEvidence(t, repoDir, targetID, targetID, 1, now, []string{sharedFile}, "none")
	appendContextRiskEvidence(t, repoDir, relatedID, relatedID, 1, now, []string{sharedFile}, "none")

	d := newContextRiskTestDaemon(projectID, repoDir, issuesClient)
	packet, err := d.taskContextRisk(ctx, projectID, targetID, repoDir, now.Add(-14*24*time.Hour))
	if err != nil {
		t.Fatalf("taskContextRisk error: %v", err)
	}
	if packet.ParentIssueID != "" {
		t.Fatalf("ParentIssueID = %q, want empty for top-level target", packet.ParentIssueID)
	}
	if packet.Level == domain.IssueContextRiskNone || packet.OverlapIssueCount != 1 {
		t.Fatalf("packet = %+v, want related top-level overlap from mailbox evidence", packet)
	}
	if len(packet.Clusters) != 1 || packet.Clusters[0].Issues[0] != relatedID {
		t.Fatalf("clusters = %+v, want related issue cluster", packet.Clusters)
	}
}

func TestTaskUpdateStatusRejectsInReviewForHighContextRiskWithoutCloseoutEvidence(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-context-risk-review-block"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	targetID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Target child", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	firstID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Prior child one", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create first sibling: %v", err)
	}
	secondID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Prior child two", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create second sibling: %v", err)
	}
	now := time.Now().UTC()
	sharedFile := "internal/daemon/task_commands.go"
	for _, issueID := range []string{targetID, firstID, secondID} {
		payload := map[string]any{"files_changed": []string{sharedFile}}
		if issueID != targetID {
			payload["summary"] = "same failure repeated"
		}
		eventType := domain.IssueEventEvidenceSubmitted
		if issueID != targetID {
			eventType = domain.IssueEventRiskRecorded
		}
		if _, err := issuesClient.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
			Type:       eventType,
			ObservedAt: now,
			Payload:    payload,
		}); err != nil {
			t.Fatalf("append observation for %s: %v", issueID, err)
		}
	}

	d := newContextRiskTestDaemon(projectID, repoDir, issuesClient)
	body, err := json.Marshal(map[string]any{"task_id": targetID, "status": domain.StatusInReview})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-review-risk",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.update_status",
		Body:            body,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error.Message, "context risk is high") {
		t.Fatalf("response = %+v, want context risk rejection", resp)
	}
}

func TestCloseTaskRejectsHighContextRiskWithoutCloseoutEvidence(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-context-risk-close-block"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	targetID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Target child", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	firstPriorID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Prior child one", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create first prior sibling: %v", err)
	}
	secondPriorID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Prior child two", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create second prior sibling: %v", err)
	}
	now := time.Now().UTC()
	sharedFile := "internal/daemon/task_commands.go"
	for _, issueID := range []string{targetID, firstPriorID, secondPriorID} {
		payload := map[string]any{"files_changed": []string{sharedFile}}
		eventType := domain.IssueEventEvidenceSubmitted
		if issueID != targetID {
			payload["summary"] = "same failure repeated"
			eventType = domain.IssueEventRiskRecorded
		}
		if _, err := issuesClient.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
			Type:       eventType,
			ObservedAt: now,
			Payload:    payload,
		}); err != nil {
			t.Fatalf("append observation for %s: %v", issueID, err)
		}
	}

	d := newContextRiskTestDaemon(projectID, repoDir, issuesClient)
	for _, tc := range []struct {
		name    string
		outcome string
	}{
		{name: "completed"},
		{name: "cancelled", outcome: string(domain.IssueCloseCancelled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := d.closeTask(ctx, projectID, taskCloseRequest{TaskID: targetID, CloseOutcome: tc.outcome}, protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       naming.RequestID("req-close-risk-" + tc.name),
				Kind:            protocol.EnvelopeKindCommand,
				Command:         "task.close",
				Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			})
			if err == nil || !strings.Contains(err.Error(), "context risk is high") {
				t.Fatalf("closeTask error = %v, want context risk rejection", err)
			}
			if result.ContextRisk == nil || !domain.IssueContextRiskRequiresStructuredCloseout(*result.ContextRisk) {
				t.Fatalf("result.ContextRisk = %+v, want blocking context risk packet", result.ContextRisk)
			}
		})
	}
}

func newContextRiskTestDaemon(projectID, repoDir string, issuesClient *issues.Client) *Daemon {
	return &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}
}

func appendContextRiskEvidence(t *testing.T, repoDir, parentID, issueID string, seq int64, createdAt time.Time, files []string, risk string) {
	t.Helper()
	if risk == "" {
		risk = "none"
	}
	body := fmt.Sprintf(`{
		"schema": "worker_evidence.v1",
		"summary": "Ready for integration.",
		"commands_run": ["go test ./internal/daemon"],
		"key_assertions": ["context risk fixture"],
		"files_changed": %s,
		"review": {"status": "clean", "findings": []},
		"risks": [%q]
	}`, mustMarshalContextRiskFiles(t, files), risk)
	if err := appendMailboxEvent(repoDir, daemonMailEvent{
		Seq:         seq,
		ParentIssue: parentID,
		IssueID:     issueID,
		Type:        "worker-integration-ready",
		Body:        body,
		CreatedAt:   createdAt,
	}); err != nil {
		t.Fatalf("append mailbox event: %v", err)
	}
}

func mustMarshalContextRiskFiles(t *testing.T, files []string) string {
	t.Helper()
	data := "["
	for i, file := range files {
		if i > 0 {
			data += ","
		}
		data += fmt.Sprintf("%q", file)
	}
	data += "]"
	return data
}

func containsContextRiskField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}
