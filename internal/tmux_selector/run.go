package tmuxselector

import (
	"log/slog"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func Run(cfg *config.Config) error {
	_ = cfg
	logger := slog.Default()
	tmuxClient := tmux.NewClient(&tmux.ExecRunner{}, logger)
	model := New(
		NewDefaultGlobalInventoryLoader(tmuxClient, logger),
		WithSwitcher(tmuxClient),
	)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
