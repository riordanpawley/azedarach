package logging

const (
	DaemonLogFileName       = "azd.log"
	TUILogFileName          = "az-tui.log"
	CLILogFileName          = "az-cli.log"
	TmuxSelectorLogFileName = "az-tmux-selector.log"
)

const (
	DefaultMaxLogBytes = 16 * 1024 * 1024
	DefaultLogBackups  = 4
)
