package statusbar

import (
	"fmt"
	"testing"

	"github.com/riordanpawley/azedarach/internal/app"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// TestDemo_VisualOutput is not a real test, but demonstrates the visual output
// Run with: go test -v -run TestDemo_VisualOutput
func TestDemo_VisualOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping visual demo in short mode")
	}

	style := styles.New()
	width := 80

	modes := []app.Mode{
		app.ModeNormal,
		app.ModeSelect,
		app.ModeSearch,
		app.ModeGoto,
		app.ModeAction,
	}

	fmt.Println("\n=== StatusBar Visual Demo ===")
	fmt.Println()

	for _, mode := range modes {
		sb := New(mode, width, style)
		rendered := sb.Render()

		fmt.Printf("Mode: %s\n", mode)
		fmt.Printf("Rendered (with ANSI): %s\n", rendered)
		fmt.Printf("Hints: %s\n\n", GetHints(mode))
	}
}
