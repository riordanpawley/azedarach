package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestTaskContextRiskDoesNotTreatBroadRootAsRiskWithoutOverlap(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-context-risk-broad"
	repoDir := t.TempDir()
	issuesClient := issues.NewClient(repoDir, slog.Default())
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
	issuesClient := issues.NewClient(repoDir, slog.Default())
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

func TestTaskContextRiskReadsTopLevelRelatedMailboxEvidence(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-context-risk-top-level"
	repoDir := t.TempDir()
	issuesClient := issues.NewClient(repoDir, slog.Default())
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
