package issues

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if issueTestTemplate != nil {
		if err := issueTestTemplate.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "remove issue-store SQLite test template: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}
