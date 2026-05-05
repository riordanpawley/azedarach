package main

import (
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestRunSessionCommand_HelpArgsReturnUsage(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		args       []string
		namespaced bool
		want       string
	}{
		{name: "session start --help", command: "start", args: []string{"--help"}, namespaced: true, want: "usage: az session start <issue-id> [--wait]"},
		{name: "session start -h", command: "start", args: []string{"-h"}, namespaced: true, want: "usage: az session start <issue-id> [--wait]"},
		{name: "session attach --help", command: "attach", args: []string{"--help"}, namespaced: true, want: "usage: az session attach <issue-id>"},
		{name: "session stop --help", command: "stop", args: []string{"--help"}, namespaced: true, want: "usage: az session stop <issue-id> [--wait]"},
		{name: "session kill --help deprecated alias", command: "kill", args: []string{"--help"}, namespaced: true, want: "usage: az session kill <issue-id> [--wait] (deprecated alias for az session stop)"},
		{name: "session status --help", command: "status", args: []string{"--help"}, namespaced: true, want: "usage: az session status [issue-id]"},
		{name: "session resolve conflict --help", command: "resolve-conflict", args: []string{"--help"}, namespaced: true, want: "usage: az session resolve-conflict <issue-id> [--worktree <path>] [--file <path> ...] [--prompt <text>]"},
		{name: "start --help alias", command: "start", args: []string{"--help"}, namespaced: false, want: "usage: az start <issue-id> [--wait]"},
		{name: "attach --help alias", command: "attach", args: []string{"--help"}, namespaced: false, want: "usage: az attach <issue-id>"},
		{name: "stop --help alias", command: "stop", args: []string{"--help"}, namespaced: false, want: "usage: az stop <issue-id> [--wait]"},
		{name: "kill --help deprecated alias", command: "kill", args: []string{"--help"}, namespaced: false, want: "usage: az kill <issue-id> [--wait] (deprecated alias for az stop)"},
		{name: "status --help alias", command: "status", args: []string{"--help"}, namespaced: false, want: "usage: az status [issue-id]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runSessionCommand(config.DefaultConfig(), tt.command, tt.args, tt.namespaced)
			if err == nil {
				t.Fatalf("expected usage error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestParseSessionResolveConflictArgs(t *testing.T) {
	opts, err := parseSessionResolveConflictArgs([]string{"az-1", "--worktree", "/tmp/az-1", "--file", "a.go", "--file", "b.go", "--prompt", "fix it"})
	if err != nil {
		t.Fatalf("parseSessionResolveConflictArgs error = %v", err)
	}
	if opts.IssueID != "az-1" || opts.Worktree != "/tmp/az-1" || opts.Prompt != "fix it" {
		t.Fatalf("opts = %+v", opts)
	}
	if len(opts.ConflictFiles) != 2 || opts.ConflictFiles[0] != "a.go" || opts.ConflictFiles[1] != "b.go" {
		t.Fatalf("conflict files = %+v", opts.ConflictFiles)
	}
}
