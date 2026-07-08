package main

import (
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestRunBranchCommandSuggestsMergeForM2MTypo(t *testing.T) {
	err := runBranchCommand(config.DefaultConfig(), "m2m", nil)
	if err == nil {
		t.Fatal("expected error for unknown m2m command")
	}
	if !strings.Contains(err.Error(), "did you mean `az branch merge`?") {
		t.Fatalf("err = %q, want merge suggestion", err)
	}
}

func TestRunBranchCommandUnknownUsageListsMerge(t *testing.T) {
	err := runBranchCommand(config.DefaultConfig(), "wat", nil)
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
	if !strings.Contains(err.Error(), "usage: az branch <merge|agent-merge>") {
		t.Fatalf("err = %q, want usage with branch commands", err)
	}
}

func TestParseBranchAgentMergeArgs(t *testing.T) {
	opts, err := parseBranchAgentMergeArgs([]string{"--project", "azedarach", "az-1", "--target", "az-2"})
	if err != nil {
		t.Fatalf("parseBranchAgentMergeArgs error = %v", err)
	}
	if opts.Project != "azedarach" || opts.IssueID != "az-1" || opts.Target != "az-2" {
		t.Fatalf("opts = %+v, want project azedarach issue az-1 target az-2", opts)
	}
}

func TestParseBranchAgentMergeArgsRejectsMissingProjectValue(t *testing.T) {
	_, err := parseBranchAgentMergeArgs([]string{"--project", "--target", "az-2", "az-1"})
	if err == nil || !strings.Contains(err.Error(), "usage: az branch agent-merge [--project <project-id>] <issue-id>") {
		t.Fatalf("err = %v, want agent-merge usage error", err)
	}
}

func TestParseBranchMergeArgs(t *testing.T) {
	opts, err := parseBranchMergeArgs([]string{"az-1", "--allow-base-for-child"})
	if err != nil {
		t.Fatalf("parseBranchMergeArgs error = %v", err)
	}
	if opts.IssueID != "az-1" || !opts.AllowBaseForChild {
		t.Fatalf("opts = %+v, want issue az-1 with allow-base-for-child=true", opts)
	}

	opts, err = parseBranchMergeArgs([]string{"--project", "azedarach", "az-1"})
	if err != nil {
		t.Fatalf("parseBranchMergeArgs project error = %v", err)
	}
	if opts.IssueID != "az-1" || opts.Project != "azedarach" {
		t.Fatalf("opts = %+v, want project azedarach issue az-1", opts)
	}
}

func TestParseBranchMergeArgsRejectsUnknownFlag(t *testing.T) {
	_, err := parseBranchMergeArgs([]string{"--wat"})
	if err == nil || !strings.Contains(err.Error(), "usage: az branch merge [--project <project-id>] [issue-id]") || strings.Contains(err.Error(), "--allow-base-for-child") {
		t.Fatalf("err = %v, want merge usage error", err)
	}
}

func TestParseBranchMergeArgsRejectsMissingProjectValue(t *testing.T) {
	_, err := parseBranchMergeArgs([]string{"--project", "--allow-base-for-child", "az-1"})
	if err == nil || !strings.Contains(err.Error(), "usage: az branch merge [--project <project-id>] [issue-id]") {
		t.Fatalf("err = %v, want merge usage error", err)
	}
}
