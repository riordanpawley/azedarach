package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

type OrchestratorSessionOptions struct {
	Project     string
	RootIssueID string
	JSON        bool
}

func ParseOrchestratorSessionArgs(command string, args []string) (OrchestratorSessionOptions, error) {
	opts := OrchestratorSessionOptions{}
	fs := flag.NewFlagSet("orchestrator-session "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id; omit for environment/project scope")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return OrchestratorSessionOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestratorSessionOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.RootIssueID = strings.TrimSpace(opts.RootIssueID)
	return opts, nil
}

func OrchestratorSessionCommand(deps *Dependencies, command string, opts OrchestratorSessionOptions) error {
	restore, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restore()
	scope, err := resolveCLIOrchestrationScope(opts.RootIssueID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionStartCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	request := protocol.OrchestratorSessionRequest{Scope: scope}
	var result protocol.OrchestratorSessionResult
	switch command {
	case "start":
		result, err = deps.DaemonClient.StartOrchestratorSession(ctx, request)
	case "attach":
		result, err = deps.DaemonClient.AttachOrchestratorSession(ctx, request)
	case "stop":
		result, err = deps.DaemonClient.StopOrchestratorSession(ctx, request)
	case "status":
		result, err = deps.DaemonClient.OrchestratorSessionStatus(ctx, request)
	default:
		return fmt.Errorf("unknown orchestrator-session command: %s", command)
	}
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(result)
	}
	registry, _ := config.LoadProjectsRegistry()
	projectName := config.ProjectDisplayName(registry, deps.ProjectID, deps.RepoDir)
	fmt.Printf("Project: %s", projectName)
	if strings.TrimSpace(deps.ProjectID) != "" && deps.ProjectID != projectName {
		fmt.Printf(" (%s)", deps.ProjectID)
	}
	fmt.Println()
	fmt.Printf("Orchestrator session: %s\n", result.SessionID)
	fmt.Printf("Scope: %s", result.Scope.Kind)
	if result.Scope.RootIssueID != "" {
		fmt.Printf(":%s", result.Scope.RootIssueID)
	}
	fmt.Println()
	if result.Disposition != "" {
		fmt.Printf("Disposition: %s\n", result.Disposition)
	}
	if result.Lifecycle != "" {
		fmt.Printf("State: %s\n", result.Lifecycle)
	}
	fmt.Printf("Live: %t\n", result.Live)
	if result.Forced {
		fmt.Println("Forced cleanup: true")
	}
	return nil
}
