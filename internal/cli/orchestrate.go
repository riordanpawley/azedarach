package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

type OrchestrateStatusOptions struct {
	Project     string
	RootIssueID string
	SinceSeq    int64
	Limit       int
	JSON        bool
}

type orchestrateStatusResult struct {
	RootIssueID   string                 `json:"root_issue_id"`
	Runnable      []string               `json:"runnable"`
	Blocked       map[string]string      `json:"blocked"`
	MailboxEvents []protocol.MailEvent   `json:"mailbox_events"`
	Advice        map[string]interface{} `json:"advice,omitempty"`
}

func ParseOrchestrateStatusArgs(args []string) (OrchestrateStatusOptions, error) {
	opts := OrchestrateStatusOptions{Limit: 50}
	fs := flag.NewFlagSet("orchestrate status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.Int64Var(&opts.SinceSeq, "since", 0, "mailbox sequence lower bound")
	fs.IntVar(&opts.Limit, "limit", 50, "maximum mailbox events to include")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return OrchestrateStatusOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateStatusOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.RootIssueID) == "" {
		return OrchestrateStatusOptions{}, fmt.Errorf("missing required flag: --root")
	}
	if opts.Limit < 1 {
		return OrchestrateStatusOptions{}, fmt.Errorf("limit must be >= 1")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func OrchestrateStatusCommand(deps *Dependencies, opts OrchestrateStatusOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	tasks, err := deps.DaemonClient.ListTasks(ctx)
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	ready, err := computeRunnableLeaves(opts.RootIssueID, tasks)
	if err != nil {
		return err
	}

	events, err := deps.DaemonClient.MailList(ctx, protocol.MailListCommandBody{
		RepoDir:     deps.RepoDir,
		ParentIssue: opts.RootIssueID,
		SinceSeq:    opts.SinceSeq,
		Limit:       opts.Limit,
	})
	if err != nil {
		return err
	}

	result := orchestrateStatusResult{
		RootIssueID:   ready.RootIssueID,
		Runnable:      ready.Runnable,
		Blocked:       ready.Blocked,
		MailboxEvents: events,
		Advice: map[string]interface{}{
			"watch": fmt.Sprintf("az mail watch --parent %s --since %d --jsonl", ready.RootIssueID, nextMailboxSeq(events, opts.SinceSeq)),
		},
	}
	if opts.JSON {
		return printJSON(result)
	}

	fmt.Printf("Root issue: %s\n", result.RootIssueID)
	fmt.Println("Runnable leaves:")
	if len(result.Runnable) == 0 {
		fmt.Println("- (none)")
	} else {
		for _, id := range result.Runnable {
			fmt.Printf("- %s\n", id)
		}
	}
	if len(result.Blocked) > 0 {
		fmt.Println("Blocked leaves:")
		ids := make([]string, 0, len(result.Blocked))
		for id := range result.Blocked {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			reason := result.Blocked[id]
			fmt.Printf("- %s: %s\n", id, reason)
		}
	}
	fmt.Printf("Mailbox events (latest %d, since seq>%d): %d\n", opts.Limit, opts.SinceSeq, len(result.MailboxEvents))
	for _, evt := range result.MailboxEvents {
		fmt.Printf("- seq=%d issue=%s type=%s from=%s to=%s\n", evt.Seq, strings.TrimSpace(evt.IssueID.String()), evt.Type, strings.TrimSpace(evt.From), strings.TrimSpace(evt.To))
	}
	fmt.Println("Next watch command:")
	fmt.Printf("- %s\n", result.Advice["watch"])
	return nil
}

func nextMailboxSeq(events []protocol.MailEvent, since int64) int64 {
	maxSeq := since
	for _, evt := range events {
		if evt.Seq > maxSeq {
			maxSeq = evt.Seq
		}
	}
	return maxSeq
}
