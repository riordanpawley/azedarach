package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type decisionListOpts struct {
	JSON     bool
	IDs      []string
	Statuses []string
	Issue    string
	Req      string
	Query    string
}

type decisionGetOpts struct {
	JSON        bool
	ID          string
	WithLinks   bool
}

type decisionCreateOpts struct {
	JSON         bool
	ID           string
	Title        string
	Context      string
	Decision     string
	Consequences string
	Status       string
	IssueLinks   []string
	ReqLinks     []string
}

type decisionUpdateOpts struct {
	JSON         bool
	ID           string
	Title        string
	Context      string
	Decision     string
	Consequences string
	Status       string
	titleSet     bool
	ctxSet       bool
	decisionSet  bool
	consSet      bool
}

type decisionDeleteOpts struct {
	JSON    bool
	ID      string
	Confirm bool
}

type decisionLinkListOpts struct {
	JSON       bool
	Decision   string
	TargetKind string
	TargetID   string
}

type decisionLinkAddOpts struct {
	JSON       bool
	Decision   string
	Issue      string
	Req        string
	Relation   string
	Note       string
}

type decisionLinkRemoveOpts struct {
	JSON     bool
	Decision string
	Issue    string
	Req      string
}

func runDecisionCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		printDecisionUsage()
		return nil
	}
	switch args[0] {
	case "list":
		opts, err := parseDecisionListArgs(args[1:])
		if err != nil {
			printDecisionUsage()
			return err
		}
		return runDecisionListRPC(cfg, opts)
	case "get":
		opts, err := parseDecisionGetArgs(args[1:])
		if err != nil {
			printDecisionUsage()
			return err
		}
		return runDecisionGetRPC(cfg, opts)
	case "create":
		opts, err := parseDecisionCreateArgs(args[1:])
		if err != nil {
			printDecisionUsage()
			return err
		}
		return runDecisionCreateRPC(cfg, opts)
	case "update":
		opts, err := parseDecisionUpdateArgs(args[1:])
		if err != nil {
			printDecisionUsage()
			return err
		}
		return runDecisionUpdateRPC(cfg, opts)
	case "delete":
		opts, err := parseDecisionDeleteArgs(args[1:])
		if err != nil {
			printDecisionUsage()
			return err
		}
		return runDecisionDeleteRPC(cfg, opts)
	case "link":
		return runDecisionLinkCommand(cfg, args[1:])
	default:
		return fmt.Errorf("unknown decision command: %s", args[0])
	}
}

func runDecisionLinkCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		printDecisionLinkUsage()
		return nil
	}
	switch args[0] {
	case "list":
		opts, err := parseDecisionLinkListArgs(args[1:])
		if err != nil {
			printDecisionLinkUsage()
			return err
		}
		return runDecisionLinkListRPC(cfg, opts)
	case "add":
		opts, err := parseDecisionLinkAddArgs(args[1:])
		if err != nil {
			printDecisionLinkUsage()
			return err
		}
		return runDecisionLinkAddRPC(cfg, opts)
	case "remove":
		opts, err := parseDecisionLinkRemoveArgs(args[1:])
		if err != nil {
			printDecisionLinkUsage()
			return err
		}
		return runDecisionLinkRemoveRPC(cfg, opts)
	default:
		return fmt.Errorf("unknown decision link command: %s", args[0])
	}
}

func runDecisionListRPC(cfg *config.Config, opts decisionListOpts) error {
	req := protocol.DecisionListRequestBody{
		IDs:           opts.IDs,
		IssueID:       naming.IssueID(opts.Issue),
		RequirementID: naming.RequirementID(opts.Req),
		Query:         opts.Query,
	}
	for _, s := range opts.Statuses {
		req.Statuses = append(req.Statuses, protocol.DecisionStatus(s))
	}
	var out protocol.DecisionListResponseBody
	if err := runDecisionRPC(cfg, protocol.CommandDecisionList, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	for _, d := range out.Decisions {
		fmt.Printf("%s\t%s\t%s\n", d.ID, d.Status, d.Title)
	}
	return nil
}

func runDecisionGetRPC(cfg *config.Config, opts decisionGetOpts) error {
	req := protocol.DecisionGetRequestBody{ID: opts.ID, IncludeLinks: opts.WithLinks}
	var out protocol.DecisionGetResponseBody
	if err := runDecisionRPC(cfg, protocol.CommandDecisionGet, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("%s\t%s\t%s\n", out.Decision.ID, out.Decision.Status, out.Decision.Title)
	if out.Decision.Context != "" {
		fmt.Printf("\nContext:\n%s\n", out.Decision.Context)
	}
	if out.Decision.Decision != "" {
		fmt.Printf("\nDecision:\n%s\n", out.Decision.Decision)
	}
	if out.Decision.Consequences != "" {
		fmt.Printf("\nConsequences:\n%s\n", out.Decision.Consequences)
	}
	if opts.WithLinks && len(out.Links) > 0 {
		fmt.Println("\nLinks:")
		for _, l := range out.Links {
			note := ""
			if l.Note != "" {
				note = " — " + l.Note
			}
			fmt.Printf("  %s %s %s%s\n", l.Relation, l.TargetKind, l.TargetID, note)
		}
	}
	return nil
}

func runDecisionCreateRPC(cfg *config.Config, opts decisionCreateOpts) error {
	req := protocol.DecisionCreateRequestBody{
		ID:           opts.ID,
		Title:        opts.Title,
		Context:      opts.Context,
		Decision:     opts.Decision,
		Consequences: opts.Consequences,
		Status:       protocol.DecisionStatus(opts.Status),
	}
	var out protocol.DecisionCreateResponseBody
	if err := runDecisionRPC(cfg, protocol.CommandDecisionCreate, req, &out); err != nil {
		return err
	}
	for _, issueID := range opts.IssueLinks {
		linkReq := protocol.DecisionLinkAddRequestBody{
			DecisionID: out.Decision.ID,
			TargetKind: protocol.DecisionTargetIssue,
			TargetID:   issueID,
		}
		var linkOut protocol.DecisionLinkAddResponseBody
		if err := runDecisionRPC(cfg, protocol.CommandDecisionLinkAdd, linkReq, &linkOut); err != nil {
			return fmt.Errorf("create succeeded but linking issue %s failed: %w", issueID, err)
		}
	}
	for _, reqID := range opts.ReqLinks {
		linkReq := protocol.DecisionLinkAddRequestBody{
			DecisionID: out.Decision.ID,
			TargetKind: protocol.DecisionTargetRequirement,
			TargetID:   reqID,
		}
		var linkOut protocol.DecisionLinkAddResponseBody
		if err := runDecisionRPC(cfg, protocol.CommandDecisionLinkAdd, linkReq, &linkOut); err != nil {
			return fmt.Errorf("create succeeded but linking requirement %s failed: %w", reqID, err)
		}
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Created decision: %s\n", out.Decision.ID)
	return nil
}

func runDecisionUpdateRPC(cfg *config.Config, opts decisionUpdateOpts) error {
	req := protocol.DecisionUpdateRequestBody{ID: opts.ID}
	if opts.titleSet {
		v := opts.Title
		req.Title = &v
	}
	if opts.ctxSet {
		v := opts.Context
		req.Context = &v
	}
	if opts.decisionSet {
		v := opts.Decision
		req.Decision = &v
	}
	if opts.consSet {
		v := opts.Consequences
		req.Consequences = &v
	}
	if opts.Status != "" {
		v := protocol.DecisionStatus(opts.Status)
		req.Status = &v
	}
	var out protocol.DecisionUpdateResponseBody
	if err := runDecisionRPC(cfg, protocol.CommandDecisionUpdate, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Updated decision: %s\n", out.Decision.ID)
	return nil
}

func runDecisionDeleteRPC(cfg *config.Config, opts decisionDeleteOpts) error {
	req := protocol.DecisionDeleteRequestBody{ID: opts.ID, Confirm: opts.Confirm}
	var out protocol.DecisionDeleteResponseBody
	if err := runDecisionRPC(cfg, protocol.CommandDecisionDelete, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Deleted decision: %s\n", out.ID)
	return nil
}

func runDecisionLinkListRPC(cfg *config.Config, opts decisionLinkListOpts) error {
	req := protocol.DecisionLinkListRequestBody{
		DecisionID: opts.Decision,
		TargetKind: protocol.DecisionTargetKind(opts.TargetKind),
		TargetID:   opts.TargetID,
	}
	var out protocol.DecisionLinkListResponseBody
	if err := runDecisionRPC(cfg, protocol.CommandDecisionLinkList, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	for _, l := range out.Links {
		note := ""
		if l.Note != "" {
			note = "\t" + l.Note
		}
		fmt.Printf("%s\t%s\t%s\t%s%s\n", l.DecisionID, l.Relation, l.TargetKind, l.TargetID, note)
	}
	return nil
}

func runDecisionLinkAddRPC(cfg *config.Config, opts decisionLinkAddOpts) error {
	kind, target, err := resolveDecisionTarget(opts.Issue, opts.Req)
	if err != nil {
		return err
	}
	req := protocol.DecisionLinkAddRequestBody{
		DecisionID: opts.Decision,
		TargetKind: kind,
		TargetID:   target,
		Relation:   protocol.DecisionRelation(opts.Relation),
		Note:       opts.Note,
	}
	var out protocol.DecisionLinkAddResponseBody
	if err := runDecisionRPC(cfg, protocol.CommandDecisionLinkAdd, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Linked decision %s -> %s %s\n", out.Link.DecisionID, out.Link.TargetKind, out.Link.TargetID)
	return nil
}

func runDecisionLinkRemoveRPC(cfg *config.Config, opts decisionLinkRemoveOpts) error {
	kind, target, err := resolveDecisionTarget(opts.Issue, opts.Req)
	if err != nil {
		return err
	}
	req := protocol.DecisionLinkRemoveRequestBody{
		DecisionID: opts.Decision,
		TargetKind: kind,
		TargetID:   target,
	}
	var out protocol.DecisionLinkRemoveResponseBody
	if err := runDecisionRPC(cfg, protocol.CommandDecisionLinkRemove, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Removed link %s -> %s %s\n", out.DecisionID, out.TargetKind, out.TargetID)
	return nil
}

func resolveDecisionTarget(issue, req string) (protocol.DecisionTargetKind, string, error) {
	issue = strings.TrimSpace(issue)
	req = strings.TrimSpace(req)
	if issue != "" && req != "" {
		return "", "", fmt.Errorf("provide only one of --issue or --req")
	}
	if issue != "" {
		return protocol.DecisionTargetIssue, issue, nil
	}
	if req != "" {
		return protocol.DecisionTargetRequirement, req, nil
	}
	return "", "", fmt.Errorf("either --issue or --req is required")
}

func runDecisionRPC(cfg *config.Config, command string, body any, out any) error {
	return runCommand(cfg, func(deps *cli.Dependencies) error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal %s request: %w", command, err)
		}
		resp, err := deps.DaemonClient.Command(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID(fmt.Sprintf("%s-%d", command, time.Now().UnixNano())),
			Kind:            protocol.EnvelopeKindCommand,
			Command:         command,
			SentAt:          time.Now().UTC(),
			Body:            payload,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(deps.ProjectID)},
		})
		if err != nil {
			return err
		}
		if !resp.OK {
			if resp.Error == nil {
				return fmt.Errorf("%s failed", command)
			}
			return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
		}
		if out == nil || len(resp.Body) == 0 {
			return nil
		}
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return fmt.Errorf("decode %s response: %w", command, err)
		}
		return nil
	})
}

func parseDecisionListArgs(args []string) (decisionListOpts, error) {
	opts := decisionListOpts{}
	fs := flag.NewFlagSet("decision list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Issue, "issue", "", "filter by linked issue id")
	fs.StringVar(&opts.Req, "req", "", "filter by linked requirement id")
	fs.StringVar(&opts.Query, "query", "", "free-text search across id/title/context/decision")
	fs.Func("id", "restrict to a specific decision id (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty decision id")
		}
		opts.IDs = appendUniqueOrdered(opts.IDs, trimmed)
		return nil
	})
	fs.Func("status", "restrict to a specific status (repeatable)", func(v string) error {
		status, err := parseDecisionStatusFlag(v)
		if err != nil {
			return err
		}
		opts.Statuses = appendUniqueOrdered(opts.Statuses, status)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return decisionListOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionListOpts{}, fmt.Errorf("usage: az decision list [--json] [--issue <id>] [--req <id>] [--status <s> ...] [--id <id> ...] [--query <text>]")
	}
	return opts, nil
}

func parseDecisionGetArgs(args []string) (decisionGetOpts, error) {
	opts := decisionGetOpts{}
	fs := flag.NewFlagSet("decision get", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ID, "id", "", "decision id")
	fs.BoolVar(&opts.WithLinks, "with-links", false, "include linked targets in output")
	if err := fs.Parse(args); err != nil {
		return decisionGetOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionGetOpts{}, fmt.Errorf("usage: az decision get --id <id> [--with-links] [--json]")
	}
	if strings.TrimSpace(opts.ID) == "" {
		return decisionGetOpts{}, fmt.Errorf("missing required flag: --id")
	}
	return opts, nil
}

func parseDecisionCreateArgs(args []string) (decisionCreateOpts, error) {
	opts := decisionCreateOpts{}
	fs := flag.NewFlagSet("decision create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ID, "id", "", "decision id (kebab-case slug)")
	fs.StringVar(&opts.Title, "title", "", "decision title")
	fs.StringVar(&opts.Context, "context", "", "context/forces narrative")
	fs.StringVar(&opts.Decision, "decision", "", "the chosen option and rationale")
	fs.StringVar(&opts.Consequences, "consequences", "", "consequences/trade-offs")
	fs.StringVar(&opts.Status, "status", "", "status: proposed|accepted|rejected|deprecated|superseded")
	fs.Func("issue", "link decision to issue (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty issue id")
		}
		opts.IssueLinks = appendUniqueOrdered(opts.IssueLinks, trimmed)
		return nil
	})
	fs.Func("req", "link decision to requirement (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty requirement id")
		}
		opts.ReqLinks = appendUniqueOrdered(opts.ReqLinks, trimmed)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return decisionCreateOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionCreateOpts{}, fmt.Errorf("usage: az decision create --id <slug> --title <text> [--context ...] [--decision ...] [--consequences ...] [--status ...] [--issue <id> ...] [--req <id> ...] [--json]")
	}
	if strings.TrimSpace(opts.ID) == "" {
		return decisionCreateOpts{}, fmt.Errorf("missing required flag: --id")
	}
	if strings.TrimSpace(opts.Title) == "" {
		return decisionCreateOpts{}, fmt.Errorf("missing required flag: --title")
	}
	if opts.Status != "" {
		status, err := parseDecisionStatusFlag(opts.Status)
		if err != nil {
			return decisionCreateOpts{}, err
		}
		opts.Status = status
	}
	return opts, nil
}

func parseDecisionUpdateArgs(args []string) (decisionUpdateOpts, error) {
	opts := decisionUpdateOpts{}
	fs := flag.NewFlagSet("decision update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ID, "id", "", "decision id")
	fs.Func("title", "new title", func(v string) error {
		opts.Title = v
		opts.titleSet = true
		return nil
	})
	fs.Func("context", "new context", func(v string) error {
		opts.Context = v
		opts.ctxSet = true
		return nil
	})
	fs.Func("decision", "new decision text", func(v string) error {
		opts.Decision = v
		opts.decisionSet = true
		return nil
	})
	fs.Func("consequences", "new consequences", func(v string) error {
		opts.Consequences = v
		opts.consSet = true
		return nil
	})
	fs.StringVar(&opts.Status, "status", "", "new status")
	if err := fs.Parse(args); err != nil {
		return decisionUpdateOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionUpdateOpts{}, fmt.Errorf("usage: az decision update --id <id> [--title ...] [--context ...] [--decision ...] [--consequences ...] [--status ...] [--json]")
	}
	if strings.TrimSpace(opts.ID) == "" {
		return decisionUpdateOpts{}, fmt.Errorf("missing required flag: --id")
	}
	if !opts.titleSet && !opts.ctxSet && !opts.decisionSet && !opts.consSet && opts.Status == "" {
		return decisionUpdateOpts{}, fmt.Errorf("no update fields provided")
	}
	if opts.Status != "" {
		status, err := parseDecisionStatusFlag(opts.Status)
		if err != nil {
			return decisionUpdateOpts{}, err
		}
		opts.Status = status
	}
	return opts, nil
}

func parseDecisionDeleteArgs(args []string) (decisionDeleteOpts, error) {
	opts := decisionDeleteOpts{}
	fs := flag.NewFlagSet("decision delete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ID, "id", "", "decision id")
	fs.BoolVar(&opts.Confirm, "confirm", false, "confirm delete")
	if err := fs.Parse(args); err != nil {
		return decisionDeleteOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionDeleteOpts{}, fmt.Errorf("usage: az decision delete --id <id> --confirm [--json]")
	}
	if strings.TrimSpace(opts.ID) == "" {
		return decisionDeleteOpts{}, fmt.Errorf("missing required flag: --id")
	}
	if !opts.Confirm {
		return decisionDeleteOpts{}, fmt.Errorf("missing required flag: --confirm")
	}
	return opts, nil
}

func parseDecisionLinkListArgs(args []string) (decisionLinkListOpts, error) {
	opts := decisionLinkListOpts{}
	fs := flag.NewFlagSet("decision link list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Decision, "id", "", "decision id filter")
	fs.StringVar(&opts.TargetKind, "kind", "", "target kind filter (issue|requirement)")
	fs.StringVar(&opts.TargetID, "target", "", "target id filter")
	if err := fs.Parse(args); err != nil {
		return decisionLinkListOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionLinkListOpts{}, fmt.Errorf("usage: az decision link list [--json] [--id <decision-id>] [--kind <issue|requirement>] [--target <id>]")
	}
	if opts.TargetKind != "" {
		switch opts.TargetKind {
		case "issue", "requirement":
		default:
			return decisionLinkListOpts{}, fmt.Errorf("invalid kind %q; expected issue|requirement", opts.TargetKind)
		}
	}
	return opts, nil
}

func parseDecisionLinkAddArgs(args []string) (decisionLinkAddOpts, error) {
	opts := decisionLinkAddOpts{}
	fs := flag.NewFlagSet("decision link add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Decision, "id", "", "decision id")
	fs.StringVar(&opts.Issue, "issue", "", "issue id (mutually exclusive with --req)")
	fs.StringVar(&opts.Req, "req", "", "requirement id (mutually exclusive with --issue)")
	fs.StringVar(&opts.Relation, "relation", "", "relation: relates|implements|supersedes|superseded-by")
	fs.StringVar(&opts.Note, "note", "", "free-text note")
	if err := fs.Parse(args); err != nil {
		return decisionLinkAddOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionLinkAddOpts{}, fmt.Errorf("usage: az decision link add --id <decision-id> (--issue <id> | --req <id>) [--relation ...] [--note ...] [--json]")
	}
	if strings.TrimSpace(opts.Decision) == "" {
		return decisionLinkAddOpts{}, fmt.Errorf("missing required flag: --id")
	}
	if opts.Relation != "" {
		relation, err := parseDecisionRelationFlag(opts.Relation)
		if err != nil {
			return decisionLinkAddOpts{}, err
		}
		opts.Relation = relation
	}
	return opts, nil
}

func parseDecisionLinkRemoveArgs(args []string) (decisionLinkRemoveOpts, error) {
	opts := decisionLinkRemoveOpts{}
	fs := flag.NewFlagSet("decision link remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Decision, "id", "", "decision id")
	fs.StringVar(&opts.Issue, "issue", "", "issue id (mutually exclusive with --req)")
	fs.StringVar(&opts.Req, "req", "", "requirement id (mutually exclusive with --issue)")
	if err := fs.Parse(args); err != nil {
		return decisionLinkRemoveOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionLinkRemoveOpts{}, fmt.Errorf("usage: az decision link remove --id <decision-id> (--issue <id> | --req <id>) [--json]")
	}
	if strings.TrimSpace(opts.Decision) == "" {
		return decisionLinkRemoveOpts{}, fmt.Errorf("missing required flag: --id")
	}
	return opts, nil
}

func parseDecisionStatusFlag(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "proposed", "accepted", "rejected", "deprecated", "superseded":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("invalid status %q; expected proposed|accepted|rejected|deprecated|superseded", value)
	}
}

func parseDecisionRelationFlag(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "relates", "implements", "supersedes", "superseded-by":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("invalid relation %q; expected relates|implements|supersedes|superseded-by", value)
	}
}

func printDecisionUsage() {
	fmt.Println("Usage: az decision <list|get|create|update|delete|link> [arguments]")
	fmt.Println("  list    List decisions (optionally filtered by linked issue/requirement)")
	fmt.Println("  get     Show a single decision (use --with-links to include linked targets)")
	fmt.Println("  create  Create a decision record (optionally link to issues/requirements)")
	fmt.Println("  update  Update fields on an existing decision")
	fmt.Println("  delete  Soft-delete a decision (requires --confirm)")
	fmt.Println("  link    Manage decision-to-issue/requirement links")
	fmt.Println("")
	fmt.Println("Grammar:")
	fmt.Println("  az decision list [--json] [--issue <id>] [--req <id>] [--status <s> ...] [--id <id> ...] [--query <text>]")
	fmt.Println("  az decision get --id <id> [--with-links] [--json]")
	fmt.Println("  az decision create --id <slug> --title <text> [--context ...] [--decision ...] [--consequences ...] [--status ...] [--issue <id> ...] [--req <id> ...] [--json]")
	fmt.Println("  az decision update --id <id> [--title ...] [--context ...] [--decision ...] [--consequences ...] [--status ...] [--json]")
	fmt.Println("  az decision delete --id <id> --confirm [--json]")
	fmt.Println("  az decision link list|add|remove ...")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  az decision create --id use-sqlite --title \"Use SQLite for store\" --context \"Need durable local store\" --decision \"SQLite\" --status accepted --issue cgn")
	fmt.Println("  az decision list --issue cgn")
	fmt.Println("  az decision get --id use-sqlite --with-links")
	fmt.Println("  az decision link add --id use-sqlite --req cgn-req-1 --relation implements")
}

func printDecisionLinkUsage() {
	fmt.Println("Usage: az decision link <list|add|remove> [arguments]")
	fmt.Println("")
	fmt.Println("Grammar:")
	fmt.Println("  az decision link list [--json] [--id <decision-id>] [--kind <issue|requirement>] [--target <id>]")
	fmt.Println("  az decision link add --id <decision-id> (--issue <id> | --req <id>) [--relation <relates|implements|supersedes|superseded-by>] [--note <text>] [--json]")
	fmt.Println("  az decision link remove --id <decision-id> (--issue <id> | --req <id>) [--json]")
}
