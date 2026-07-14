package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type decisionListOpts struct {
	JSON  bool
	IDs   []string
	Issue string
	Req   string
	Query string
}

type decisionGetOpts struct {
	JSON      bool
	ID        string
	WithLinks bool
}

type decisionRecordOpts struct {
	JSON         bool
	Title        string
	Rationale    string
	Context      string
	Consequences string
	Issues       []string
	Reqs         []string
}

type decisionUpdateOpts struct {
	JSON         bool
	ID           string
	Title        string
	Rationale    string
	Context      string
	Consequences string
	titleSet     bool
	ratSet       bool
	ctxSet       bool
	consSet      bool
}

type decisionDeleteOpts struct {
	JSON    bool
	ID      string
	Confirm bool
}

type decisionRevisitOpts struct {
	JSON      bool
	Old       string
	Title     string
	Rationale string
	Context   string
	New       string
	Note      string
}

type decisionSyncOpts struct {
	JSON       bool
	Check      bool
	ProjectDir string
}

type decisionImportOpts struct {
	JSON       bool
	Check      bool
	Force      bool
	ProjectDir string
}

type decisionLinkListOpts struct {
	JSON       bool
	Decision   string
	TargetKind string
	TargetID   string
}

type decisionLinkAddOpts struct {
	JSON     bool
	Decision string
	Issue    string
	Req      string
	OtherDec string
	Relation string
	Note     string
}

type decisionLinkRemoveOpts struct {
	JSON     bool
	Decision string
	Issue    string
	Req      string
	OtherDec string
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
	case "record":
		opts, err := parseDecisionRecordArgs(args[1:])
		if err != nil {
			printDecisionUsage()
			return err
		}
		return runDecisionRecordRPC(cfg, opts)
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
	case "revisit":
		opts, err := parseDecisionRevisitArgs(args[1:])
		if err != nil {
			printDecisionUsage()
			return err
		}
		return runDecisionRevisitRPC(cfg, opts)
	case "sync":
		opts, err := parseDecisionSyncArgs(args[1:])
		if err != nil {
			printDecisionUsage()
			return err
		}
		return runDecisionSyncRPC(cfg, opts)
	case "import":
		opts, err := parseDecisionImportArgs(args[1:])
		if err != nil {
			printDecisionUsage()
			return err
		}
		return runDecisionImportRPC(cfg, opts)
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
	var out protocol.DecisionListResponseBody
	if err := runDecisionRPC(cfg, "", protocol.CommandDecisionList, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	for _, d := range out.Decisions {
		fmt.Printf("%s\t%s\n", d.ID, d.Title)
	}
	return nil
}

func runDecisionGetRPC(cfg *config.Config, opts decisionGetOpts) error {
	req := protocol.DecisionGetRequestBody{ID: opts.ID, IncludeLinks: opts.WithLinks}
	var out protocol.DecisionGetResponseBody
	if err := runDecisionRPC(cfg, "", protocol.CommandDecisionGet, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("%s\t%s\n", out.Decision.ID, out.Decision.Title)
	if out.Decision.Rationale != "" {
		fmt.Printf("\nRationale:\n%s\n", out.Decision.Rationale)
	}
	if out.Decision.Context != "" {
		fmt.Printf("\nContext:\n%s\n", out.Decision.Context)
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

func runDecisionRecordRPC(cfg *config.Config, opts decisionRecordOpts) error {
	req := protocol.DecisionRecordRequestBody{
		Title:        opts.Title,
		Rationale:    opts.Rationale,
		Context:      opts.Context,
		Consequences: opts.Consequences,
	}
	var out protocol.DecisionRecordResponseBody
	if err := runDecisionRPC(cfg, "", protocol.CommandDecisionRecord, req, &out); err != nil {
		return err
	}
	for _, issueID := range opts.Issues {
		linkReq := protocol.DecisionLinkAddRequestBody{
			DecisionID: out.Decision.ID,
			TargetKind: protocol.DecisionTargetIssue,
			TargetID:   issueID,
		}
		var linkOut protocol.DecisionLinkAddResponseBody
		if err := runDecisionRPC(cfg, "", protocol.CommandDecisionLinkAdd, linkReq, &linkOut); err != nil {
			return fmt.Errorf("recorded %s but linking issue %s failed: %w", out.Decision.ID, issueID, err)
		}
	}
	for _, reqID := range opts.Reqs {
		linkReq := protocol.DecisionLinkAddRequestBody{
			DecisionID: out.Decision.ID,
			TargetKind: protocol.DecisionTargetRequirement,
			TargetID:   reqID,
		}
		var linkOut protocol.DecisionLinkAddResponseBody
		if err := runDecisionRPC(cfg, "", protocol.CommandDecisionLinkAdd, linkReq, &linkOut); err != nil {
			return fmt.Errorf("recorded %s but linking requirement %s failed: %w", out.Decision.ID, reqID, err)
		}
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Recorded decision: %s\n", out.Decision.ID)
	return nil
}

func runDecisionUpdateRPC(cfg *config.Config, opts decisionUpdateOpts) error {
	req := protocol.DecisionUpdateRequestBody{ID: opts.ID}
	if opts.titleSet {
		v := opts.Title
		req.Title = &v
	}
	if opts.ratSet {
		v := opts.Rationale
		req.Rationale = &v
	}
	if opts.ctxSet {
		v := opts.Context
		req.Context = &v
	}
	if opts.consSet {
		v := opts.Consequences
		req.Consequences = &v
	}
	var out protocol.DecisionUpdateResponseBody
	if err := runDecisionRPC(cfg, "", protocol.CommandDecisionUpdate, req, &out); err != nil {
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
	if err := runDecisionRPC(cfg, "", protocol.CommandDecisionDelete, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Deleted decision: %s\n", out.ID)
	return nil
}

// runDecisionRevisitRPC handles two flows:
//   - --new <id>:  link an existing decision as the revision of the old one.
//   - --title + --rationale: create a NEW decision in the same call and link it.
func runDecisionRevisitRPC(cfg *config.Config, opts decisionRevisitOpts) error {
	newID := strings.TrimSpace(opts.New)
	if newID == "" {
		req := protocol.DecisionRecordRequestBody{
			Title:     opts.Title,
			Rationale: opts.Rationale,
			Context:   opts.Context,
		}
		var out protocol.DecisionRecordResponseBody
		if err := runDecisionRPC(cfg, "", protocol.CommandDecisionRecord, req, &out); err != nil {
			return err
		}
		newID = out.Decision.ID
	}
	linkReq := protocol.DecisionLinkAddRequestBody{
		DecisionID: newID,
		TargetKind: protocol.DecisionTargetDecision,
		TargetID:   opts.Old,
		Relation:   protocol.DecisionRelationRevises,
		Note:       opts.Note,
	}
	var linkOut protocol.DecisionLinkAddResponseBody
	if err := runDecisionRPC(cfg, "", protocol.CommandDecisionLinkAdd, linkReq, &linkOut); err != nil {
		return fmt.Errorf("recorded %s but linking revises %s failed: %w", newID, opts.Old, err)
	}
	if opts.JSON {
		return printJSON(linkOut)
	}
	fmt.Printf("%s revises %s\n", newID, opts.Old)
	return nil
}

func runDecisionSyncRPC(cfg *config.Config, opts decisionSyncOpts) error {
	repoDir, err := decisionRequestRepoDir(opts.ProjectDir)
	if err != nil {
		return err
	}
	req := protocol.DecisionSyncMDRequestBody{Check: opts.Check, RepoDir: repoDir}
	var out protocol.DecisionSyncMDResponseBody
	if err := runDecisionRPC(cfg, opts.ProjectDir, protocol.CommandDecisionSyncMD, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	switch {
	case opts.Check && out.Changed:
		fmt.Printf("Drift detected (%d files would change):\n", len(out.Files))
		for _, f := range out.Files {
			fmt.Printf("  %s\n", f)
		}
	case opts.Check:
		fmt.Println("No drift; markdown files are in sync.")
	case out.Changed:
		fmt.Printf("Wrote %d file(s):\n", len(out.Files))
		for _, f := range out.Files {
			fmt.Printf("  %s\n", f)
		}
	default:
		fmt.Println("No changes; markdown files are already in sync.")
	}
	return nil
}

func parseDecisionSyncArgs(args []string) (decisionSyncOpts, error) {
	opts := decisionSyncOpts{}
	fs := flag.NewFlagSet("decision sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.BoolVar(&opts.Check, "check", false, "report drift without writing files")
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project/worktree directory for markdown files")
	if err := fs.Parse(args); err != nil {
		return decisionSyncOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionSyncOpts{}, fmt.Errorf("usage: az decision sync [--check] [--project-dir <dir>] [--json]")
	}
	return opts, nil
}

func runDecisionImportRPC(cfg *config.Config, opts decisionImportOpts) error {
	repoDir, err := decisionRequestRepoDir(opts.ProjectDir)
	if err != nil {
		return err
	}
	req := protocol.DecisionImportMDRequestBody{Check: opts.Check, Force: opts.Force, RepoDir: repoDir}
	var out protocol.DecisionImportMDResponseBody
	if err := runDecisionRPC(cfg, opts.ProjectDir, protocol.CommandDecisionImportMD, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	if len(out.Files) == 0 {
		fmt.Println("No decision markdown files found.")
		return nil
	}
	for _, file := range out.Files {
		header := file.Path
		if file.DecisionID != "" {
			header = fmt.Sprintf("%s [%s]", file.Path, file.DecisionID)
		}
		if file.ParseError != "" {
			fmt.Printf("%s: parse error: %s\n", header, file.ParseError)
			continue
		}
		if file.NewRecord {
			fmt.Printf("%s: NEW\n", header)
		}
		for _, c := range file.Changes {
			fmt.Printf("  + %s: %q -> %q\n", c.Field, c.OldValue, c.NewValue)
		}
		for _, c := range file.Conflicts {
			fmt.Printf("  ! conflict on %s: sqlite=%q markdown=%q\n", c.Field, c.SQLiteValue, c.MarkdownValue)
		}
		switch {
		case file.ApplyError != "":
			fmt.Printf("  ✗ apply failed: %s\n", file.ApplyError)
		case file.Skipped:
			fmt.Printf("  skipped (use --force to override)\n")
		case file.Imported:
			fmt.Printf("  imported\n")
		case len(file.Changes) == 0 && len(file.Conflicts) == 0:
			fmt.Printf("  in sync\n")
		}
	}
	switch {
	case opts.Check && out.Conflicts > 0:
		fmt.Printf("\n%d file(s) with conflicts. Run with --force to apply markdown values, or edit md/SQLite to align.\n", out.Conflicts)
	case opts.Check:
		fmt.Printf("\n%d file(s) would be imported.\n", out.Imported)
	default:
		fmt.Printf("\nImported %d file(s); %d had conflicts.\n", out.Imported, out.Conflicts)
	}
	return nil
}

func parseDecisionImportArgs(args []string) (decisionImportOpts, error) {
	opts := decisionImportOpts{}
	fs := flag.NewFlagSet("decision import", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.BoolVar(&opts.Check, "check", false, "report plan without applying")
	fs.BoolVar(&opts.Force, "force", false, "apply even when fields conflict (markdown wins)")
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project/worktree directory for markdown files")
	if err := fs.Parse(args); err != nil {
		return decisionImportOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionImportOpts{}, fmt.Errorf("usage: az decision import [--check] [--force] [--project-dir <dir>] [--json]")
	}
	return opts, nil
}

func runDecisionLinkListRPC(cfg *config.Config, opts decisionLinkListOpts) error {
	req := protocol.DecisionLinkListRequestBody{
		DecisionID: opts.Decision,
		TargetKind: protocol.DecisionTargetKind(opts.TargetKind),
		TargetID:   opts.TargetID,
	}
	var out protocol.DecisionLinkListResponseBody
	if err := runDecisionRPC(cfg, "", protocol.CommandDecisionLinkList, req, &out); err != nil {
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
	kind, target, err := resolveDecisionLinkTarget(opts.Issue, opts.Req, opts.OtherDec)
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
	if err := runDecisionRPC(cfg, "", protocol.CommandDecisionLinkAdd, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Linked %s -> %s %s\n", out.Link.DecisionID, out.Link.TargetKind, out.Link.TargetID)
	return nil
}

func runDecisionLinkRemoveRPC(cfg *config.Config, opts decisionLinkRemoveOpts) error {
	kind, target, err := resolveDecisionLinkTarget(opts.Issue, opts.Req, opts.OtherDec)
	if err != nil {
		return err
	}
	req := protocol.DecisionLinkRemoveRequestBody{
		DecisionID: opts.Decision,
		TargetKind: kind,
		TargetID:   target,
	}
	var out protocol.DecisionLinkRemoveResponseBody
	if err := runDecisionRPC(cfg, "", protocol.CommandDecisionLinkRemove, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Removed link %s -> %s %s\n", out.DecisionID, out.TargetKind, out.TargetID)
	return nil
}

func resolveDecisionLinkTarget(issue, req, otherDec string) (protocol.DecisionTargetKind, string, error) {
	issue = strings.TrimSpace(issue)
	req = strings.TrimSpace(req)
	otherDec = strings.TrimSpace(otherDec)
	count := 0
	for _, v := range []string{issue, req, otherDec} {
		if v != "" {
			count++
		}
	}
	if count == 0 {
		return "", "", fmt.Errorf("one of --issue, --req, or --decision is required")
	}
	if count > 1 {
		return "", "", fmt.Errorf("only one of --issue, --req, or --decision may be set")
	}
	switch {
	case issue != "":
		return protocol.DecisionTargetIssue, issue, nil
	case req != "":
		return protocol.DecisionTargetRequirement, req, nil
	default:
		return protocol.DecisionTargetDecision, otherDec, nil
	}
}

func runDecisionRPC(cfg *config.Config, projectDir, command string, body any, out any) error {
	run := runCommand
	if strings.TrimSpace(projectDir) != "" {
		run = func(cfg *config.Config, fn func(*cli.Dependencies) error) error {
			return runCommandAtRepoDir(cfg, projectDir, fn)
		}
	}
	return run(cfg, func(deps *cli.Dependencies) error {
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

func decisionRequestRepoDir(projectDir string) (string, error) {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve decision markdown working directory: %w", err)
		}
		projectDir = cwd
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("resolve decision markdown project dir: %w", err)
	}
	return abs, nil
}

func parseDecisionListArgs(args []string) (decisionListOpts, error) {
	opts := decisionListOpts{}
	fs := flag.NewFlagSet("decision list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Issue, "issue", "", "filter by linked issue id")
	fs.StringVar(&opts.Req, "req", "", "filter by linked requirement id")
	fs.StringVar(&opts.Query, "query", "", "free-text search across id/title/rationale/context")
	fs.Func("id", "restrict to a specific decision id (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty decision id")
		}
		opts.IDs = appendUniqueOrdered(opts.IDs, trimmed)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return decisionListOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionListOpts{}, fmt.Errorf("usage: az decision list [--json] [--issue <id>] [--req <id>] [--id <id> ...] [--query <text>]")
	}
	return opts, nil
}

func parseDecisionGetArgs(args []string) (decisionGetOpts, error) {
	opts := decisionGetOpts{}
	fs := flag.NewFlagSet("decision get", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ID, "id", "", "decision id (e.g. dec-1)")
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

func parseDecisionRecordArgs(args []string) (decisionRecordOpts, error) {
	opts := decisionRecordOpts{}
	fs := flag.NewFlagSet("decision record", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Title, "title", "", "one-line summary of what was decided")
	fs.StringVar(&opts.Rationale, "rationale", "", "why this was chosen (required)")
	fs.StringVar(&opts.Context, "context", "", "situational backdrop (optional)")
	fs.StringVar(&opts.Consequences, "consequences", "", "downstream effects / trade-offs (optional)")
	fs.Func("issue", "link to issue (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty issue id")
		}
		opts.Issues = appendUniqueOrdered(opts.Issues, trimmed)
		return nil
	})
	fs.Func("req", "link to requirement (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty requirement id")
		}
		opts.Reqs = appendUniqueOrdered(opts.Reqs, trimmed)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return decisionRecordOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionRecordOpts{}, fmt.Errorf("usage: az decision record --title <text> --rationale <text> [--context <text>] [--consequences <text>] [--issue <id> ...] [--req <id> ...] [--json]")
	}
	if strings.TrimSpace(opts.Title) == "" {
		return decisionRecordOpts{}, fmt.Errorf("missing required flag: --title")
	}
	if strings.TrimSpace(opts.Rationale) == "" {
		return decisionRecordOpts{}, fmt.Errorf("missing required flag: --rationale")
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
	fs.Func("rationale", "new rationale", func(v string) error {
		opts.Rationale = v
		opts.ratSet = true
		return nil
	})
	fs.Func("context", "new context", func(v string) error {
		opts.Context = v
		opts.ctxSet = true
		return nil
	})
	fs.Func("consequences", "new consequences", func(v string) error {
		opts.Consequences = v
		opts.consSet = true
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return decisionUpdateOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionUpdateOpts{}, fmt.Errorf("usage: az decision update --id <id> [--title ...] [--rationale ...] [--context ...] [--consequences ...] [--json]")
	}
	if strings.TrimSpace(opts.ID) == "" {
		return decisionUpdateOpts{}, fmt.Errorf("missing required flag: --id")
	}
	if !opts.titleSet && !opts.ratSet && !opts.ctxSet && !opts.consSet {
		return decisionUpdateOpts{}, fmt.Errorf("no update fields provided")
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

func parseDecisionRevisitArgs(args []string) (decisionRevisitOpts, error) {
	opts := decisionRevisitOpts{}
	fs := flag.NewFlagSet("decision revisit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Old, "id", "", "decision id being revised")
	fs.StringVar(&opts.New, "new", "", "id of an already-recorded decision that revises the old one")
	fs.StringVar(&opts.Title, "title", "", "title for a freshly recorded replacement (alternative to --new)")
	fs.StringVar(&opts.Rationale, "rationale", "", "rationale for the replacement decision")
	fs.StringVar(&opts.Context, "context", "", "context for the replacement decision")
	fs.StringVar(&opts.Note, "note", "", "note attached to the revises link")
	if err := fs.Parse(args); err != nil {
		return decisionRevisitOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionRevisitOpts{}, fmt.Errorf("usage: az decision revisit --id <old-id> (--new <existing-id> | --title <text> --rationale <text> [--context <text>]) [--note <text>] [--json]")
	}
	if strings.TrimSpace(opts.Old) == "" {
		return decisionRevisitOpts{}, fmt.Errorf("missing required flag: --id")
	}
	if strings.TrimSpace(opts.New) == "" {
		if strings.TrimSpace(opts.Title) == "" || strings.TrimSpace(opts.Rationale) == "" {
			return decisionRevisitOpts{}, fmt.Errorf("either --new <id> or --title and --rationale must be provided")
		}
	}
	return opts, nil
}

func parseDecisionLinkListArgs(args []string) (decisionLinkListOpts, error) {
	opts := decisionLinkListOpts{}
	fs := flag.NewFlagSet("decision link list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Decision, "id", "", "decision id filter")
	fs.StringVar(&opts.TargetKind, "kind", "", "target kind filter (issue|requirement|decision)")
	fs.StringVar(&opts.TargetID, "target", "", "target id filter")
	if err := fs.Parse(args); err != nil {
		return decisionLinkListOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionLinkListOpts{}, fmt.Errorf("usage: az decision link list [--json] [--id <decision-id>] [--kind <issue|requirement|decision>] [--target <id>]")
	}
	if opts.TargetKind != "" {
		switch opts.TargetKind {
		case "issue", "requirement", "decision":
		default:
			return decisionLinkListOpts{}, fmt.Errorf("invalid kind %q; expected issue|requirement|decision", opts.TargetKind)
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
	fs.StringVar(&opts.Issue, "issue", "", "issue id (mutually exclusive with --req/--decision)")
	fs.StringVar(&opts.Req, "req", "", "requirement id (mutually exclusive with --issue/--decision)")
	fs.StringVar(&opts.OtherDec, "decision", "", "other decision id (mutually exclusive with --issue/--req)")
	fs.StringVar(&opts.Relation, "relation", "", "relation: applies-to|revises|informs (default applies-to)")
	fs.StringVar(&opts.Note, "note", "", "free-text note")
	if err := fs.Parse(args); err != nil {
		return decisionLinkAddOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionLinkAddOpts{}, fmt.Errorf("usage: az decision link add --id <decision-id> (--issue <id> | --req <id> | --decision <id>) [--relation ...] [--note ...] [--json]")
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
	fs.StringVar(&opts.Issue, "issue", "", "issue id (mutually exclusive with --req/--decision)")
	fs.StringVar(&opts.Req, "req", "", "requirement id (mutually exclusive with --issue/--decision)")
	fs.StringVar(&opts.OtherDec, "decision", "", "other decision id (mutually exclusive with --issue/--req)")
	if err := fs.Parse(args); err != nil {
		return decisionLinkRemoveOpts{}, err
	}
	if fs.NArg() != 0 {
		return decisionLinkRemoveOpts{}, fmt.Errorf("usage: az decision link remove --id <decision-id> (--issue <id> | --req <id> | --decision <id>) [--json]")
	}
	if strings.TrimSpace(opts.Decision) == "" {
		return decisionLinkRemoveOpts{}, fmt.Errorf("missing required flag: --id")
	}
	return opts, nil
}

func parseDecisionRelationFlag(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "applies-to", "revises", "informs":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("invalid relation %q; expected applies-to|revises|informs", value)
	}
}

func printDecisionUsage() {
	fmt.Println("Usage: az decision <list|get|record|update|delete|revisit|sync|import|link> [arguments]")
	fmt.Println("  list     List recorded decisions (optionally filtered by linked issue/requirement)")
	fmt.Println("  get      Show a single decision (use --with-links for links)")
	fmt.Println("  record   Record a new decision (id auto-allocated as dec-N)")
	fmt.Println("  update   Update fields on an existing decision")
	fmt.Println("  delete   Soft-delete a decision (requires --confirm)")
	fmt.Println("  revisit  Replace an older decision with a new one (creates the revises link)")
	fmt.Println("  sync     Explicitly reconcile docs/decisions with the store, including renames/deletes (use --check for drift)")
	fmt.Println("  import   Read docs/decisions/*.md into the store without overwriting conflicts unless --force is used")
	fmt.Println("  link     Manage decision-to-issue/requirement/decision links")
	fmt.Println("")
	fmt.Println("Grammar:")
	fmt.Println("  az decision list [--json] [--issue <id>] [--req <id>] [--id <id> ...] [--query <text>]")
	fmt.Println("  az decision get --id <id> [--with-links] [--json]")
	fmt.Println("  az decision record --title <text> --rationale <text> [--context <text>] [--consequences <text>] [--issue <id> ...] [--req <id> ...] [--json]")
	fmt.Println("  az decision update --id <id> [--title ...] [--rationale ...] [--context ...] [--consequences ...] [--json]")
	fmt.Println("  az decision delete --id <id> --confirm [--json]")
	fmt.Println("  az decision revisit --id <old-id> (--new <existing-id> | --title <text> --rationale <text>) [--context ...] [--note ...] [--json]")
	fmt.Println("  az decision sync [--check] [--project-dir <dir>] [--json]")
	fmt.Println("  az decision import [--check] [--force] [--project-dir <dir>] [--json]")
	fmt.Println("  az decision link list|add|remove ...")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  az decision record --title \"Use SQLite for the store\" --rationale \"Existing schema; new datastore not worth the operational cost\" --issue cgn")
	fmt.Println("  az decision list --issue cgn")
	fmt.Println("  az decision get --id dec-1 --with-links")
	fmt.Println("  az decision revisit --id dec-1 --title \"Move to Postgres\" --rationale \"Multi-process write contention; sqlite no longer fits\"")
	fmt.Println("  az decision import --check && az decision sync --check")
	fmt.Println("  az decision sync && git add docs/decisions")
}

func printDecisionLinkUsage() {
	fmt.Println("Usage: az decision link <list|add|remove> [arguments]")
	fmt.Println("")
	fmt.Println("Grammar:")
	fmt.Println("  az decision link list [--json] [--id <decision-id>] [--kind <issue|requirement|decision>] [--target <id>]")
	fmt.Println("  az decision link add --id <decision-id> (--issue <id> | --req <id> | --decision <id>) [--relation <applies-to|revises|informs>] [--note <text>] [--json]")
	fmt.Println("  az decision link remove --id <decision-id> (--issue <id> | --req <id> | --decision <id>) [--json]")
}
