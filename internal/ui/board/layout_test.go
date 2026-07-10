package board

import "testing"

func TestVisibleColumnCount(t *testing.T) {
	tests := []struct {
		name         string
		totalColumns int
		boardWidth   int
		want         int
	}{
		{name: "none", totalColumns: 0, boardWidth: 80, want: 0},
		{name: "single column", totalColumns: 1, boardWidth: 20, want: 1},
		{name: "really narrow switches to single column", totalColumns: 4, boardWidth: 50, want: 1},
		{name: "breakpoint width switches to single column", totalColumns: 4, boardWidth: 72, want: 1},
		{name: "just above phone breakpoint still protects minimum", totalColumns: 4, boardWidth: 73, want: 1},
		{name: "just below two column minimum uses one", totalColumns: 4, boardWidth: 79, want: 1},
		{name: "two column minimum fits exactly", totalColumns: 4, boardWidth: 80, want: 2},
		{name: "medium pages three readable columns", totalColumns: 4, boardWidth: 120, want: 3},
		{name: "just below four column minimum pages three", totalColumns: 4, boardWidth: 159, want: 3},
		{name: "wide fits all four", totalColumns: 4, boardWidth: 160, want: 4},
		{name: "clamps to total", totalColumns: 3, boardWidth: 300, want: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := VisibleColumnCount(tc.totalColumns, tc.boardWidth); got != tc.want {
				t.Fatalf("VisibleColumnCount(%d, %d)=%d want=%d", tc.totalColumns, tc.boardWidth, got, tc.want)
			}
		})
	}
}

func TestColumnLayout(t *testing.T) {
	t.Run("centralizes medium viewport geometry", func(t *testing.T) {
		layout := NewColumnLayout(4, 120, 0)
		if layout.VisibleCount != 3 || layout.ColumnWidth != 40 {
			t.Fatalf("layout = %+v, want 3 visible columns at width 40", layout)
		}
		if start, end := layout.Range(); start != 0 || end != 3 {
			t.Fatalf("range = [%d,%d), want [0,3)", start, end)
		}
	})

	t.Run("reveals a column outside the current window", func(t *testing.T) {
		layout := NewColumnLayout(6, 120, 0).WithColumnVisible(4)
		if start, end := layout.Range(); start != 2 || end != 5 {
			t.Fatalf("range = [%d,%d), want [2,5)", start, end)
		}
	})

	t.Run("clamps stale viewport after column count shrinks", func(t *testing.T) {
		layout := NewColumnLayout(2, 120, 5)
		if start, end := layout.Range(); start != 0 || end != 2 {
			t.Fatalf("range = [%d,%d), want [0,2)", start, end)
		}
	})

	t.Run("maps remainder cells to the final visible column", func(t *testing.T) {
		layout := NewColumnLayout(4, 121, 1)
		column, ok := layout.ColumnAt(120)
		if !ok || column != 3 {
			t.Fatalf("ColumnAt(120) = (%d,%t), want (3,true)", column, ok)
		}
	})

	t.Run("rejects coordinates outside the board", func(t *testing.T) {
		layout := NewColumnLayout(4, 120, 0)
		for _, x := range []int{-1, 120} {
			if column, ok := layout.ColumnAt(x); ok {
				t.Fatalf("ColumnAt(%d) = (%d,true), want no column", x, column)
			}
		}
	})
}
