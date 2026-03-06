package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
)

const (
	configCommandName         = "config"
	configShowCommandName     = "config.show"
	configValidateCommandName = "config.validate"
	configUsage               = "Usage: az config <validate|show> [--json]"

	configInvalidArgumentCode = "invalid_argument"
	configInternalErrorCode   = "internal_error"

	configValidateFailureCode = "invalid_config"

	configInvalidArgRemediation      = "Run az config <validate|show> [--json]"
	configShowInternalRemediation    = "Retry az config show [--json] and inspect stderr diagnostics"
	configValidateFailureRemediation = "Fix configuration errors and rerun az config validate [--json]"

	configExitCodeInvalidUsage = 2

	configSubcommandShow     = "show"
	configSubcommandValidate = "validate"
)

var (
	configCommandPath         = []string{"az", "config"}
	configShowCommandPath     = []string{"az", "config", "show"}
	configValidateCommandPath = []string{"az", "config", "validate"}
)

type configParsedArgs struct {
	Subcommand string
	JSONMode   bool
}

type configValidateResult struct {
	Valid bool `json:"valid"`
}

func handleConfigCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()
	parsedArgs, parseErr := parseConfigArgs(args)
	if parseErr != nil {
		return handleConfigInvalidUsage(
			parseErr,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	switch parsedArgs.Subcommand {
	case configSubcommandShow:
		return runConfigShow(
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	case configSubcommandValidate:
		return runConfigValidate(
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	default:
		return handleConfigInvalidUsage(
			fmt.Errorf("unsupported subcommand: %s", parsedArgs.Subcommand),
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}
}

func parseConfigArgs(args []string) (configParsedArgs, error) {
	parsed := configParsedArgs{}
	positionals := make([]string, 0, 2)
	unknownFlags := make([]string, 0)

	for _, arg := range args {
		switch {
		case arg == "--json":
			parsed.JSONMode = true
		case strings.HasPrefix(arg, "-"):
			unknownFlags = append(unknownFlags, arg)
		default:
			positionals = append(positionals, arg)
		}
	}

	if len(unknownFlags) > 0 {
		return parsed, fmt.Errorf("unknown flag(s): %s", strings.Join(unknownFlags, " "))
	}
	if len(positionals) == 0 {
		return parsed, fmt.Errorf("missing required subcommand")
	}

	parsed.Subcommand = positionals[0]
	if parsed.Subcommand != configSubcommandShow && parsed.Subcommand != configSubcommandValidate {
		return parsed, fmt.Errorf("unsupported subcommand: %s", parsed.Subcommand)
	}
	if len(positionals) > 1 {
		return parsed, fmt.Errorf("unsupported positional args: %s", strings.Join(positionals[1:], " "))
	}

	return parsed, nil
}

func runConfigShow(
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	cfg, err := loadConfig()
	if err != nil {
		return handleConfigShowInternalError(
			fmt.Errorf("failed to load config: %w", err),
			jsonMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	if jsonMode {
		envelope := NewH2SuccessEnvelope(
			configShowCommandName,
			configShowCommandPath,
			project,
			meta,
			cfg,
		)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	if err := writeJSON(stdout, cfg); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func runConfigValidate(
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	cfg, err := loadConfig()
	if err != nil {
		return handleConfigValidateFailure(
			fmt.Errorf("config validation failed: %w", err),
			jsonMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	if err := validateLoadedConfig(cfg); err != nil {
		return handleConfigValidateFailure(
			err,
			jsonMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	result := configValidateResult{Valid: true}
	if jsonMode {
		envelope := NewH2SuccessEnvelope(
			configValidateCommandName,
			configValidateCommandPath,
			project,
			meta,
			result,
		)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintln(stdout, "valid")
	return 0
}

func validateLoadedConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("config must not be nil")
	}
	if cfg.Session.TimeoutMs <= 0 {
		return fmt.Errorf("session.timeoutMs must be > 0")
	}
	if cfg.Linear.SyncInterval <= 0 {
		return fmt.Errorf("linear.syncInterval must be > 0")
	}
	if cfg.Network.CheckInterval <= 0 {
		return fmt.Errorf("network.checkInterval must be > 0")
	}
	if cfg.Network.OfflineTimeout <= 0 {
		return fmt.Errorf("network.offlineTimeout must be > 0")
	}
	if cfg.Network.RetryAttempts < 0 {
		return fmt.Errorf("network.retryAttempts must be >= 0")
	}
	if cfg.DevServer.BasePort <= 0 {
		return fmt.Errorf("devServer.basePort must be > 0")
	}
	if cfg.DevServer.MaxPort <= 0 {
		return fmt.Errorf("devServer.maxPort must be > 0")
	}
	if cfg.DevServer.MaxPort < cfg.DevServer.BasePort {
		return fmt.Errorf("devServer.maxPort must be >= devServer.basePort")
	}
	if cfg.Worktree.KeepDays < 0 {
		return fmt.Errorf("worktree.keepDays must be >= 0")
	}
	if cfg.IssueTracker.Local.Backups.IntervalMinutes <= 0 {
		return fmt.Errorf("issueTracker.local.backups.intervalMinutes must be > 0")
	}
	if cfg.IssueTracker.Local.Backups.WriteCooldownSeconds <= 0 {
		return fmt.Errorf("issueTracker.local.backups.writeCooldownSeconds must be > 0")
	}
	if cfg.IssueTracker.Local.Backups.MaxBackups <= 0 {
		return fmt.Errorf("issueTracker.local.backups.maxBackups must be > 0")
	}
	if strings.TrimSpace(cfg.IssueTracker.Local.Backups.Directory) == "" {
		return fmt.Errorf("issueTracker.local.backups.directory must not be empty")
	}

	return nil
}

func handleConfigInvalidUsage(
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			configInvalidArgumentCode,
			parseErr.Error(),
			configInvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(configCommandName, configCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return configExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, configUsage)
	return configExitCodeInvalidUsage
}

func handleConfigShowInternalError(
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			configInternalErrorCode,
			commandErr.Error(),
			configShowInternalRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(configShowCommandName, configShowCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 1
	}

	fmt.Fprintf(stderr, "Error: %v\n", commandErr)
	return 1
}

func handleConfigValidateFailure(
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			configValidateFailureCode,
			commandErr.Error(),
			configValidateFailureRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(
			configValidateCommandName,
			configValidateCommandPath,
			project,
			meta,
			errorPayload,
		)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 1
	}

	fmt.Fprintf(stderr, "Error: %v\n", commandErr)
	return 1
}
