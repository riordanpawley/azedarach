package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/testutil/issuestest"
)

func TestMain(m *testing.M) {
	if dir, err := os.MkdirTemp("", "azedarach-daemon-user-db-"); err == nil {
		defer os.RemoveAll(dir)
		_ = os.Setenv("AZEDARACH_USER_DB_PATH", filepath.Join(dir, "azedarach.db"))
	}
	_ = os.Setenv("AZEDARACH_DISABLE_USER_DB", "1")
	if repoRoot, err := config.ResolveProjectRoot("."); err == nil {
		_ = os.Setenv("AZEDARACH_REFUSE_DB_PATH", filepath.Join(repoRoot, ".azedarach", "azedarach.db"))
	}
	_ = os.Setenv("AZEDARACH_REFUSE_REAL_TMUX_MUTATION", "1")
	_ = os.Unsetenv("AZEDARACH_ALLOW_REAL_TMUX_IN_TESTS")
	code := m.Run()
	if err := issuestest.CloseTemplate(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "remove daemon SQLite test template: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
