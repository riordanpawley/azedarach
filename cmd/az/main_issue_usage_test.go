package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestIssueCreateUsageExplainsImplIsNotParentage(t *testing.T) {
	var out bytes.Buffer
	printIssueCreateUsage(&out)
	text := out.String()

	for _, want := range []string{
		"Usage: az ticket create [--project <project-id>] [--parent <ticket-id>] [--impl <implementation> ...]",
		"`az issue create \"Child task\"` auto-parents to AZEDARACH_ISSUE_ID when set",
		"use `--parent <issue-id>` for another parent/root",
		"--impl only assigns implementation/spec variant metadata; it is not parent/root selection.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("create usage missing %q in %q", want, text)
		}
	}
}

func TestIssueSplitUsageExplainsParentFlagOwnsParentage(t *testing.T) {
	var out bytes.Buffer
	printIssueSplitUsage(&out)
	text := out.String()

	for _, want := range []string{
		"Usage: az ticket split --intent-key <unique-invocation-key> [--project <project-id>] [--parent <ticket-id>] [--impl <implementation> ...]",
		"use --parent or AZEDARACH_ISSUE_ID for parentage",
		"--impl only assigns implementation/spec variant metadata",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("split usage missing %q in %q", want, text)
		}
	}
}
