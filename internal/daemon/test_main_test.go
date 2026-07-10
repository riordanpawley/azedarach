package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestMain(m *testing.M) {
	if repoRoot, err := config.ResolveProjectRoot("."); err == nil {
		_ = os.Setenv("AZEDARACH_REFUSE_DB_PATH", filepath.Join(repoRoot, ".azedarach", "azedarach.db"))
	}
	_ = os.Setenv("AZEDARACH_REFUSE_REAL_TMUX_MUTATION", "1")
	_ = os.Unsetenv("AZEDARACH_ALLOW_REAL_TMUX_IN_TESTS")
	os.Exit(m.Run())
}
