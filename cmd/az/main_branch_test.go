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
	if !strings.Contains(err.Error(), "usage: az branch <merge>") {
		t.Fatalf("err = %q, want usage with merge command", err)
	}
}
