package daemon

import (
	"fmt"
	"os"
	"testing"

	"github.com/riordanpawley/azedarach/internal/testisolation"
	"github.com/riordanpawley/azedarach/internal/testutil/issuestest"
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
	// Daemon tests intentionally exercise independent repository roots. Keep
	// each root's DB distinct while the refusal set protects originals.
	_ = os.Unsetenv("AZEDARACH_DB_PATH")
	_ = os.Setenv("AZEDARACH_DISABLE_USER_DB", "1")
	_ = os.Setenv("AZEDARACH_REFUSE_REAL_TMUX_MUTATION", "1")
	_ = os.Unsetenv("AZEDARACH_ALLOW_REAL_TMUX_IN_TESTS")
	code := m.Run()
	if err := issuestest.CloseTemplate(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "remove daemon SQLite test template: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	restore()
	if err := environment.Close(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "remove daemon test isolation: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
