package protocol

import "testing"

func TestProjectIDNormalization(t *testing.T) {
	t.Run("trim only", func(t *testing.T) {
		cases := []struct {
			input string
			want  string
		}{
			{input: "bmd", want: "bmd"},
			{input: " bmd ", want: "bmd"},
			{input: "\tbmd\n", want: "bmd"},
			{input: "", want: ""},
			{input: "   ", want: ""},
		}
		for _, tc := range cases {
			if got := TrimProjectID(tc.input); got != tc.want {
				t.Fatalf("TrimProjectID(%q) = %q, want %q", tc.input, got, tc.want)
			}
		}
	})

	t.Run("normalize fallback", func(t *testing.T) {
		cases := []struct {
			input string
			want  string
		}{
			{input: "bmd", want: "bmd"},
			{input: " bmd ", want: "bmd"},
			{input: "\tbmd\n", want: "bmd"},
			{input: "", want: DefaultProjectID},
			{input: "   ", want: DefaultProjectID},
		}
		for _, tc := range cases {
			if got := NormalizeProjectID(tc.input); got != tc.want {
				t.Fatalf("NormalizeProjectID(%q) = %q, want %q", tc.input, got, tc.want)
			}
		}
	})
}
