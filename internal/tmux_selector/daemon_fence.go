package tmuxselector

import (
	"os"
	"strings"

	"github.com/riordanpawley/azedarach/internal/config"
)

func validateSharedDaemonExecutable(socketPath string) error {
	executable, err := os.Executable()
	if err != nil || strings.TrimSpace(executable) == "" {
		return nil
	}
	return config.ValidateSharedDaemonExecutable(socketPath, executable)
}
