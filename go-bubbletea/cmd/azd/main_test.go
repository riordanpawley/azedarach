package main

import "testing"

func TestIsScopedDaemonMode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "worktree", in: "worktree", want: true},
		{name: "scoped", in: "scoped", want: true},
		{name: "local", in: "local", want: true},
		{name: "whitespace and case", in: "  WorkTree  ", want: true},
		{name: "empty", in: "", want: false},
		{name: "global", in: "global", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isScopedDaemonMode(tt.in); got != tt.want {
				t.Fatalf("isScopedDaemonMode(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
