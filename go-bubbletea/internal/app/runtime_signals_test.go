package app

import "testing"

func TestParseDiffStatTotals(t *testing.T) {
	t.Run("insertions and deletions", func(t *testing.T) {
		additions, deletions := parseDiffStatTotals(" 3 files changed, 27 insertions(+), 14 deletions(-)")
		if additions != 27 || deletions != 14 {
			t.Fatalf("parseDiffStatTotals(...) = (%d,%d), want (27,14)", additions, deletions)
		}
	})

	t.Run("insertions only", func(t *testing.T) {
		additions, deletions := parseDiffStatTotals(" 1 file changed, 5 insertions(+)")
		if additions != 5 || deletions != 0 {
			t.Fatalf("parseDiffStatTotals(...) = (%d,%d), want (5,0)", additions, deletions)
		}
	})

	t.Run("empty diff", func(t *testing.T) {
		additions, deletions := parseDiffStatTotals("")
		if additions != 0 || deletions != 0 {
			t.Fatalf("parseDiffStatTotals(empty) = (%d,%d), want (0,0)", additions, deletions)
		}
	})

	t.Run("multiple shortstat lines are aggregated", func(t *testing.T) {
		diffStat := " 1 file changed, 3 insertions(+), 1 deletion(-)\n 2 files changed, 4 insertions(+), 2 deletions(-)"
		additions, deletions := parseDiffStatTotals(diffStat)
		if additions != 7 || deletions != 3 {
			t.Fatalf("parseDiffStatTotals(multi-line) = (%d,%d), want (7,3)", additions, deletions)
		}
	})
}
