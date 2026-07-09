package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

type DaemonWatchClientsOptions struct {
	JSON bool
	All  bool
}

func ParseDaemonWatchClientsArgs(args []string) (DaemonWatchClientsOptions, error) {
	opts := DaemonWatchClientsOptions{}
	fs := flag.NewFlagSet("daemon watch-clients", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	fs.BoolVar(&opts.All, "all", false, "include recently observed inactive watch clients")
	if err := fs.Parse(args); err != nil {
		return DaemonWatchClientsOptions{}, err
	}
	if fs.NArg() != 0 {
		return DaemonWatchClientsOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	return opts, nil
}

func DaemonWatchClientsCommand(deps *Dependencies, opts DaemonWatchClientsOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	result, err := deps.DaemonClient.DaemonWatchClients(ctx, protocol.DaemonWatchClientsCommandBody{IncludeExpired: opts.All})
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(result)
	}
	if len(result.Clients) == 0 {
		if opts.All {
			fmt.Println("No recent watch clients.")
		} else {
			fmt.Println("No active watch clients.")
		}
		return nil
	}
	fmt.Printf("Watch clients (active window: %ds)\n", result.ActiveWindowSeconds)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tPID\tPPID\tAGE\tIDLE\tPROJECT\tCOMMAND\tCWD")
	for _, client := range result.Clients {
		status := "active"
		if !client.Active {
			status = "recent"
		}
		if client.OrphanCandidate {
			status += ",orphan"
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\n",
			status,
			client.ClientPID,
			client.ClientPPID,
			formatWatchClientSeconds(client.AgeSeconds),
			formatWatchClientSeconds(client.IdleSeconds),
			client.ProjectID,
			strings.TrimSpace(client.CommandShape),
			strings.TrimSpace(client.ClientCWD),
		)
	}
	return w.Flush()
}

func formatWatchClientSeconds(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	return (time.Duration(seconds) * time.Second).String()
}
