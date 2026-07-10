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
