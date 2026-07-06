package daemon

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("AZEDARACH_REFUSE_REAL_TMUX_MUTATION", "1")
	_ = os.Unsetenv("AZEDARACH_ALLOW_REAL_TMUX_IN_TESTS")
	os.Exit(m.Run())
}
