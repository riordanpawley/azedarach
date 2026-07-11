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
	opts, err := parseBranchMergeArgs([]string{"--source", "az-1", "--target", "az-parent"})
	if err != nil {
		t.Fatalf("parseBranchMergeArgs error = %v", err)
	}
	if opts.IssueID != "az-1" || opts.Target != "az-parent" {
		t.Fatalf("opts = %+v, want source az-1 and target az-parent", opts)
	}

	opts, err = parseBranchMergeArgs([]string{"--project", "azedarach", "--source", "az-1", "--target", "base"})
	if err != nil {
		t.Fatalf("parseBranchMergeArgs project error = %v", err)
	}
	if opts.IssueID != "az-1" || opts.Project != "azedarach" {
		t.Fatalf("opts = %+v, want project azedarach issue az-1", opts)
	}
}

func TestParseBranchMergeArgsRejectsUnknownFlag(t *testing.T) {
	_, err := parseBranchMergeArgs([]string{"--wat"})
	if err == nil || !strings.Contains(err.Error(), "usage: az branch merge [--project <project-id>] --source <issue-id> --target base|<issue-id>") || strings.Contains(err.Error(), "--allow-base-for-child") {
		t.Fatalf("err = %v, want merge usage error", err)
	}
}

func TestParseBranchMergeArgsRejectsMissingProjectValue(t *testing.T) {
	_, err := parseBranchMergeArgs([]string{"--project", "--source", "az-1", "--target", "base"})
	if err == nil || !strings.Contains(err.Error(), "usage: az branch merge [--project <project-id>] --source <issue-id> --target base|<issue-id>") {
		t.Fatalf("err = %v, want merge usage error", err)
	}
}

func TestParseBranchMergeArgsRejectsAmbiguousImplicitTarget(t *testing.T) {
	for _, args := range [][]string{{"az-source"}, {"--source", "az-source"}, {"--target", "az-target"}, {"--source", "one", "--source", "two", "--target", "target"}, {"--source", "source", "--target", "one", "--target", "two"}} {
		if _, err := parseBranchMergeArgs(args); err == nil {
			t.Fatalf("parseBranchMergeArgs(%v) error = nil, want explicit source/target refusal", args)
		}
	}
}
