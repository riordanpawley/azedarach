package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

type AIAccountAction string

const (
	AIAccountBackup   AIAccountAction = "backup"
	AIAccountList     AIAccountAction = "list"
	AIAccountStatus   AIAccountAction = "status"
	AIAccountActivate AIAccountAction = "activate"
	AIAccountDelete   AIAccountAction = "delete"
)

type AIAccountOptions struct {
	Action       AIAccountAction
	Provider     protocol.AIAccountProvider
	Name         string
	Force        bool
	Confirm      bool
	JSON         bool
	ReloadDaemon bool
}

func ParseAIAccountArgs(args []string) (AIAccountOptions, error) {
	if len(args) == 0 {
		return AIAccountOptions{}, fmt.Errorf("missing AI account command")
	}
	opts := AIAccountOptions{Action: AIAccountAction(args[0])}
	fs := flag.NewFlagSet("ai account "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	provider := ""
	fs.StringVar(&provider, "provider", "", "provider filter (claude or codex)")
	fs.BoolVar(&opts.Force, "force", false, "replace an existing profile")
	fs.BoolVar(&opts.Confirm, "confirm", false, "confirm destructive deletion")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	fs.BoolVar(&opts.ReloadDaemon, "reload-daemon", false, "reload persistent Codex daemons after activation")
	if err := fs.Parse(args[1:]); err != nil {
		return AIAccountOptions{}, err
	}

	positionals := fs.Args()
	switch opts.Action {
	case AIAccountBackup, AIAccountActivate, AIAccountDelete:
		if provider != "" || len(positionals) != 2 {
			return AIAccountOptions{}, fmt.Errorf("usage: az ai account %s [flags] <provider> <profile>", opts.Action)
		}
		opts.Provider = protocol.AIAccountProvider(strings.ToLower(positionals[0]))
		opts.Name = positionals[1]
	case AIAccountList, AIAccountStatus:
		if len(positionals) != 0 {
			return AIAccountOptions{}, fmt.Errorf("usage: az ai account %s [--provider claude|codex] [--json]", opts.Action)
		}
		opts.Provider = protocol.AIAccountProvider(strings.ToLower(provider))
	default:
		return AIAccountOptions{}, fmt.Errorf("unknown AI account command: %s", opts.Action)
	}
	if opts.Action != AIAccountBackup && opts.Force {
		return AIAccountOptions{}, fmt.Errorf("--force is only valid with AI account backup")
	}
	if opts.Action != AIAccountDelete && opts.Confirm {
		return AIAccountOptions{}, fmt.Errorf("--confirm is only valid with AI account delete")
	}
	if opts.Action != AIAccountActivate && opts.ReloadDaemon {
		return AIAccountOptions{}, fmt.Errorf("--reload-daemon is only valid with AI account activate")
	}
	if opts.ReloadDaemon && opts.Provider != protocol.AIAccountProviderCodex {
		return AIAccountOptions{}, fmt.Errorf("--reload-daemon requires the codex provider")
	}
	if opts.Provider != "" && !opts.Provider.Valid() {
		return AIAccountOptions{}, fmt.Errorf("unsupported AI account provider %q (want claude or codex)", opts.Provider)
	}
	if opts.Action == AIAccountDelete && !opts.Confirm {
		return AIAccountOptions{}, fmt.Errorf("AI account delete requires --confirm")
	}
	return opts, nil
}

func PrintAIAccountUsage() {
	fmt.Println("Usage: az ai account backup [--force] [--json] <provider> <profile>")
	fmt.Println("       az ai account list [--provider claude|codex] [--json]")
	fmt.Println("       az ai account status [--provider claude|codex] [--json]")
	fmt.Println("       az ai account activate [--reload-daemon] [--json] <provider> <profile>")
	fmt.Println("       az ai account delete --confirm [--json] <provider> <profile>")
	fmt.Println("Manage local Claude and Codex credential profiles through the Azedarach daemon.")
}

func AIAccountCommand(deps *Dependencies, opts AIAccountOptions) error {
	if deps == nil || deps.DaemonClient == nil {
		return fmt.Errorf("daemon client unavailable")
	}
	ctx, cancel := context.WithTimeout(commandTraceContext(deps), daemonCommandTimeout)
	defer cancel()

	switch opts.Action {
	case AIAccountBackup:
		result, err := deps.DaemonClient.BackupAIAccount(ctx, protocol.AIAccountBackupRequestBody{Provider: opts.Provider, Name: opts.Name, Force: opts.Force})
		if err != nil {
			return err
		}
		if opts.JSON {
			return printJSON(result)
		}
		fmt.Printf("Saved %s account profile %q.\n", result.Profile.Provider, result.Profile.Name)
	case AIAccountList:
		result, err := deps.DaemonClient.ListAIAccounts(ctx, protocol.AIAccountListRequestBody{Provider: opts.Provider})
		if err != nil {
			return err
		}
		if opts.JSON {
			return printJSON(result)
		}
		if len(result.Profiles) == 0 {
			fmt.Println("No AI account profiles saved.")
			return nil
		}
		for _, profile := range result.Profiles {
			active := ""
			if profile.Active {
				active = " (active)"
			}
			system := ""
			if profile.System {
				system = " [system]"
			}
			fmt.Printf("%s/%s%s%s\n", profile.Provider, profile.Name, active, system)
		}
	case AIAccountStatus:
		result, err := deps.DaemonClient.StatusAIAccounts(ctx, protocol.AIAccountStatusRequestBody{Provider: opts.Provider})
		if err != nil {
			return err
		}
		if opts.JSON {
			return printJSON(result)
		}
		for _, status := range result.Providers {
			summary := "not authenticated"
			if status.Authenticated {
				summary = "authenticated (no matching saved profile)"
			}
			if status.ActiveProfile != "" {
				summary = status.ActiveProfile + " (active)"
			}
			fmt.Printf("%s: %s\n", status.Provider, summary)
		}
	case AIAccountActivate:
		result, err := deps.DaemonClient.ActivateAIAccount(ctx, protocol.AIAccountActivateRequestBody{Provider: opts.Provider, Name: opts.Name, ReloadCodexDaemon: opts.ReloadDaemon})
		if err != nil {
			return err
		}
		if opts.JSON {
			return printJSON(result)
		}
		fmt.Printf("Activated %s account profile %q.\n", result.Profile.Provider, result.Profile.Name)
		if result.SafetyBackupProfile != "" {
			fmt.Printf("Preserved previous credentials as %s.\n", result.SafetyBackupProfile)
		}
		if result.OutgoingResnapshotted != "" {
			fmt.Printf("Re-snapshotted outgoing profile %q.\n", result.OutgoingResnapshotted)
		}
		if result.FreshLivePreserved {
			fmt.Println("Preserved newer live Codex tokens for the same account.")
		}
		if reload := result.CodexDaemonReload; reload != nil {
			if reload.InspectionFailed {
				fmt.Println("Could not inspect Codex daemon processes; restart existing Codex sessions manually.")
			} else if opts.ReloadDaemon && !reload.Supported {
				fmt.Println("Codex daemon reload is not supported on this platform; restart it manually.")
			} else if !opts.ReloadDaemon && len(reload.DetectedPIDs) > 0 {
				fmt.Printf("Detected %d scoped Codex daemon process(es); re-run with --reload-daemon to switch them.\n", len(reload.DetectedPIDs))
			} else if !opts.ReloadDaemon && reload.NativeDaemon {
				fmt.Println("Detected the native Codex app-server daemon; re-run with --reload-daemon to switch it.")
			}
			if reload.NativeRestarted {
				fmt.Println("Restarted the native Codex app-server daemon.")
			}
			if len(reload.ReloadedPIDs) > 0 {
				fmt.Printf("Reloaded %d scoped Codex daemon process(es).\n", len(reload.ReloadedPIDs))
			}
			if len(reload.FailedPIDs) > 0 {
				fmt.Printf("Could not reload %d scoped Codex daemon process(es); restart them manually.\n", len(reload.FailedPIDs))
			}
			if reload.UnattributedCount > 0 {
				fmt.Printf("Left %d unattributable Codex daemon process(es) running for safety.\n", reload.UnattributedCount)
			}
		}
		if result.RestartExistingProcesses {
			if result.CodexDaemonReload != nil && result.CodexDaemonReload.NativeRestarted {
				fmt.Println("Restart any legacy standalone Codex processes still using the previous account.")
			} else {
				qualifier := ""
				if result.CodexDaemonReload != nil && len(result.CodexDaemonReload.ReloadedPIDs) > 0 {
					qualifier = "other "
				}
				fmt.Printf("Restart any %sexisting %s processes to use the activated profile.\n", qualifier, result.Profile.Provider)
			}
		}
	case AIAccountDelete:
		result, err := deps.DaemonClient.DeleteAIAccount(ctx, protocol.AIAccountDeleteRequestBody{Provider: opts.Provider, Name: opts.Name, Confirm: opts.Confirm})
		if err != nil {
			return err
		}
		if opts.JSON {
			return printJSON(result)
		}
		fmt.Printf("Deleted %s account profile %q.\n", result.Provider, result.Name)
	}
	return nil
}
