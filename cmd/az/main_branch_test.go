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
	opts, err := parseBranchAgentMergeArgs([]string{"az-1", "--target", "az-2"})
	if err != nil {
		t.Fatalf("parseBranchAgentMergeArgs error = %v", err)
	}
	if opts.IssueID != "az-1" || opts.Target != "az-2" {
		t.Fatalf("opts = %+v, want issue az-1 target az-2", opts)
	}
}
