package cli

import "testing"

func TestBuiltInDecisionCommandsForHook(t *testing.T) {
	cases := []struct {
		hook     string
		expected []string
	}{
		{"pre-commit", []string{"az decision sync"}},
		{"post-merge", []string{"az decision import"}},
		{"post-checkout", []string{"az decision import"}},
		{"post-rewrite", []string{"az decision import"}},
		{"post-commit", nil},
		{"unknown-hook", nil},
		{"", nil},
	}
	for _, tc := range cases {
		t.Run(tc.hook, func(t *testing.T) {
			got := builtInDecisionCommandsForHook(tc.hook)
			if !equalStringSlice(got, tc.expected) {
				t.Errorf("builtInDecisionCommandsForHook(%q) = %v, want %v", tc.hook, got, tc.expected)
			}
		})
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
