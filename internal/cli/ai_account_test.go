package cli

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestParseAIAccountArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want AIAccountOptions
	}{
		{
			name: "backup",
			args: []string{"backup", "--force", "--json", "claude", "work@example.com"},
			want: AIAccountOptions{Action: AIAccountBackup, Provider: protocol.AIAccountProviderClaude, Name: "work@example.com", Force: true, JSON: true},
		},
		{
			name: "filtered list",
			args: []string{"list", "--provider", "codex", "--json"},
			want: AIAccountOptions{Action: AIAccountList, Provider: protocol.AIAccountProviderCodex, JSON: true},
		},
		{
			name: "delete",
			args: []string{"delete", "--confirm", "codex", "personal"},
			want: AIAccountOptions{Action: AIAccountDelete, Provider: protocol.AIAccountProviderCodex, Name: "personal", Confirm: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAIAccountArgs(tt.args)
			if err != nil {
				t.Fatalf("ParseAIAccountArgs: %v", err)
			}
			if got != tt.want {
				t.Fatalf("options = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseAIAccountArgsRejectsUnsafeShapes(t *testing.T) {
	for _, args := range [][]string{
		{"delete", "codex", "personal"},
		{"backup", "gemini", "personal"},
		{"list", "unexpected"},
		{"bogus"},
		{"activate", "--force", "claude", "personal"},
		{"status", "--confirm"},
	} {
		if _, err := ParseAIAccountArgs(args); err == nil {
			t.Fatalf("ParseAIAccountArgs(%v) succeeded, want error", args)
		}
	}
}
