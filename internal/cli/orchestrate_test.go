package cli

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestParseOrchestrateStatusArgs(t *testing.T) {
	opts, err := ParseOrchestrateStatusArgs([]string{"--root", "az-123", "--since", "10", "--limit", "25", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateStatusArgs error = %v", err)
	}
	if opts.RootIssueID != "az-123" {
		t.Fatalf("RootIssueID = %q, want az-123", opts.RootIssueID)
	}
	if opts.SinceSeq != 10 {
		t.Fatalf("SinceSeq = %d, want 10", opts.SinceSeq)
	}
	if opts.Limit != 25 {
		t.Fatalf("Limit = %d, want 25", opts.Limit)
	}
	if !opts.JSON {
		t.Fatal("JSON = false, want true")
	}
}

func TestParseOrchestrateStatusArgs_RequiresRoot(t *testing.T) {
	if _, err := ParseOrchestrateStatusArgs([]string{"--since", "10"}); err == nil {
		t.Fatal("expected error for missing --root")
	}
}

func TestParseOrchestrateStartArgs_DefaultLimitAndIssues(t *testing.T) {
	opts, err := ParseOrchestrateStartArgs([]string{"--root", "az-123", "--issue", "az-3", "--issue", "az-2", "--issue", "az-3"})
	if err != nil {
		t.Fatalf("ParseOrchestrateStartArgs error = %v", err)
	}
	if opts.Limit != 4 {
		t.Fatalf("Limit = %d, want 4", opts.Limit)
	}
	if len(opts.IssueIDs) != 2 || opts.IssueIDs[0] != "az-2" || opts.IssueIDs[1] != "az-3" {
		t.Fatalf("IssueIDs = %+v, want [az-2 az-3]", opts.IssueIDs)
	}
}

func TestParseOrchestrateWatchArgs(t *testing.T) {
	opts, err := ParseOrchestrateWatchArgs([]string{"--root", "az-1", "--since", "12", "--once"})
	if err != nil {
		t.Fatalf("ParseOrchestrateWatchArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || opts.SinceSeq != 12 || !opts.Once {
		t.Fatalf("opts = %+v", opts)
	}
	if !opts.JSONL {
		t.Fatalf("JSONL = false, want true")
	}
}

func TestParseOrchestrateCompleteCheckArgs(t *testing.T) {
	opts, err := ParseOrchestrateCompleteCheckArgs([]string{"--root", "az-1", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateCompleteCheckArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestEvaluateOrchestrateCompleteCheck_Pass(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	tasks := []domain.Task{
		{ID: root, Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: child, ParentID: &root, Status: domain.StatusDone, Type: domain.TypeTask},
	}
	result, err := evaluateOrchestrateCompleteCheck("az-1", tasks)
	if err != nil {
		t.Fatalf("evaluateOrchestrateCompleteCheck error = %v", err)
	}
	if !result.Pass {
		t.Fatalf("Pass = false, reasons = %+v", result.Reasons)
	}
}

func TestEvaluateOrchestrateCompleteCheck_Failures(t *testing.T) {
	root := naming.IssueID("az-1")
	leaf1 := naming.IssueID("az-2")
	leaf2 := naming.IssueID("az-3")
	tasks := []domain.Task{
		{ID: root, Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: leaf1, ParentID: &root, Status: domain.StatusOpen, Type: domain.TypeTask},
		{ID: leaf2, ParentID: &root, Status: domain.StatusDone, Type: domain.TypeTask, HasTmuxSession: true},
	}
	result, err := evaluateOrchestrateCompleteCheck("az-1", tasks)
	if err != nil {
		t.Fatalf("evaluateOrchestrateCompleteCheck error = %v", err)
	}
	if result.Pass {
		t.Fatalf("Pass = true, want false")
	}
	if len(result.Reasons) < 2 {
		t.Fatalf("reasons = %+v, want multiple blockers", result.Reasons)
	}
}

func TestNextMailboxSeq(t *testing.T) {
	events := []protocol.MailEvent{
		{Seq: 11},
		{Seq: 18},
		{Seq: 14},
	}
	if got := nextMailboxSeq(events, 10); got != 18 {
		t.Fatalf("nextMailboxSeq = %d, want 18", got)
	}
	if got := nextMailboxSeq(nil, 10); got != 10 {
		t.Fatalf("nextMailboxSeq(nil) = %d, want 10", got)
	}
}
