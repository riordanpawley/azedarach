package statusbar_test

import (
	"fmt"

	"github.com/riordanpawley/azedarach/internal/app"
	"github.com/riordanpawley/azedarach/internal/ui/statusbar"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// Example demonstrates how to use the StatusBar
func Example() {
	style := styles.New()

	// Create a status bar in normal mode
	sb := statusbar.New(app.ModeNormal, 80, style)

	// Render it (output will include ANSI codes for styling)
	rendered := sb.Render()

	// For this example, we just verify it's not empty
	fmt.Println(len(rendered) > 0)
	// Output: true
}

// ExampleGetHints shows how to get hints for different modes
func ExampleGetHints() {
	normalHints := statusbar.GetHints(app.ModeNormal)
	fmt.Println(normalHints)
	// Output: h/l: columns  j/k: tasks  Space: action  ?: help  q: quit
}
