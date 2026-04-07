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
		{name: "session kill --help", command: "kill", args: []string{"--help"}, namespaced: true, want: "usage: az session kill <issue-id> [--wait]"},
		{name: "session status --help", command: "status", args: []string{"--help"}, namespaced: true, want: "usage: az session status [issue-id]"},
		{name: "start --help alias", command: "start", args: []string{"--help"}, namespaced: false, want: "usage: az start <issue-id> [--wait]"},
		{name: "attach --help alias", command: "attach", args: []string{"--help"}, namespaced: false, want: "usage: az attach <issue-id>"},
		{name: "kill --help alias", command: "kill", args: []string{"--help"}, namespaced: false, want: "usage: az kill <issue-id> [--wait]"},
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

