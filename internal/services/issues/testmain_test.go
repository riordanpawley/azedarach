package issues

import (
	"fmt"
	"os"
	"testing"

	"github.com/riordanpawley/azedarach/internal/testisolation"
)

func TestMain(m *testing.M) {
	environment, err := testisolation.NewTemporary(".")
	if err != nil {
		panic(err)
	}
	restore, err := environment.Apply()
	if err != nil {
		panic(err)
	}
	// Issue-store tests intentionally exercise independent repository roots.
	// Keep each root's DB distinct while the refusal set protects originals.
	_ = os.Unsetenv("AZEDARACH_DB_PATH")
	code := m.Run()
	if issueTestTemplate != nil {
		if err := issueTestTemplate.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "remove issue-store SQLite test template: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	restore()
	if err := environment.Close(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "remove issue-store test isolation: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
