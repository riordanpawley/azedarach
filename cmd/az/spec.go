package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const specDisabledMessage = "spec workflows are disabled for this project; re-enable with: az config set spec.enabled true"

type specReqListOptions struct {
	JSON   bool
	Issue  string
	Status string
	IDs    []string
	Query  string
	Match  string
	Limit  int
}

type specReqGetOptions struct {
	JSON bool
	ID   string
}

type specReqCreateOptions struct {
	JSON        bool
	ID          string
	Title       string
	Description string
	Issue       string
}

type specReqUpdateOptions struct {
	JSON        bool
	ID          string
	Title       string
	Description string
	Status      string
}

type specReqDeleteOptions struct {
	JSON    bool
	ID      string
	Confirm bool
}

type specLinkListOptions struct {
	JSON  bool
	Issue string
	Req   string
	IDs   []string
}

type specLinkAddOptions struct {
	JSON  bool
	Issue string
	Req   string
	Role  string
	Note  string
}

type specLinkRemoveOptions struct {
	JSON  bool
	Issue string
	Req   string
}

type specReadOptions struct {
	JSON  bool
	Issue string
	Req   string
}

type specPackOptions struct {
	JSON  bool
	Issue string
	Req   string
	Stage string
}

type specLintOptions struct {
	JSON   bool
	Strict bool
}

type specParityOptions struct {
	JSON      bool
	FailOnOut bool
}

type specGraphOptions struct {
	JSON     bool
	Issue    string
	MetaPath string
	Format   string
}

type specSliceGateOptions struct {
	JSON        bool
	Slice       string
	Issue       string
	Strict      bool
	SkipTests   bool
	TestCommand string
}

type specSliceGateResult struct {
	Slice                string   `json:"slice"`
	Issue                string   `json:"issue"`
	Requirements         int      `json:"requirements"`
	MissingImplementsReq []string `json:"missing_implements_req,omitempty"`
	MissingVerifiesReq   []string `json:"missing_verifies_req,omitempty"`
	LintOK               bool     `json:"lint_ok"`
	ParityOK             bool     `json:"parity_ok"`
	TestsOK              bool     `json:"tests_ok"`
	TestCommand          string   `json:"test_command,omitempty"`
	OK                   bool     `json:"ok"`
}

func runSpecCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		cli.PrintSpecUsage()
		return nil
	}
	if cfg == nil || !cfg.Spec.Enabled {
		return fmt.Errorf(specDisabledMessage)
	}

	switch args[0] {
	case "req":
		return runSpecReqCommand(cfg, args[1:])
	case "link":
		return runSpecLinkCommand(cfg, args[1:])
	case "read":
		return runSpecReadCommand(cfg, args[1:])
	case "pack":
		return runSpecPackCommand(cfg, args[1:])
	case "lint":
		return runSpecLintCommand(cfg, args[1:])
	case "parity":
		return runSpecParityCommand(cfg, args[1:])
	case "graph":
		return runSpecGraphCommand(cfg, args[1:])
	case "slice":
		return runSpecSliceCommand(cfg, args[1:])
	default:
		return fmt.Errorf("unknown spec command: %s", args[0])
	}
}

func runSpecReqCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		cli.PrintSpecReqUsage()
		return nil
	}

	switch args[0] {
	case "list":
		opts, err := parseSpecReqListArgs(args[1:])
		if err != nil {
			cli.PrintSpecReqUsage()
			return err
		}
		return runSpecReqListRPC(cfg, opts)
	case "get":
		opts, err := parseSpecReqGetArgs(args[1:])
		if err != nil {
			cli.PrintSpecReqUsage()
			return err
		}
		return runSpecReqGetRPC(cfg, opts)
	case "create":
		opts, err := parseSpecReqCreateArgs(args[1:])
		if err != nil {
			cli.PrintSpecReqUsage()
			return err
		}
		return runSpecReqCreateRPC(cfg, opts)
	case "update":
		opts, err := parseSpecReqUpdateArgs(args[1:])
		if err != nil {
			cli.PrintSpecReqUsage()
			return err
		}
		return runSpecReqUpdateRPC(cfg, opts)
	case "delete":
		opts, err := parseSpecReqDeleteArgs(args[1:])
		if err != nil {
			cli.PrintSpecReqUsage()
			return err
		}
		return runSpecReqDeleteRPC(cfg, opts)
	default:
		return fmt.Errorf("unknown spec req command: %s", args[0])
	}
}

func runSpecLinkCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		cli.PrintSpecLinkUsage()
		return nil
	}

	switch args[0] {
	case "list":
		opts, err := parseSpecLinkListArgs(args[1:])
		if err != nil {
			cli.PrintSpecLinkUsage()
			return err
		}
		return runSpecLinkListRPC(cfg, opts)
	case "add":
		opts, err := parseSpecLinkAddArgs(args[1:])
		if err != nil {
			cli.PrintSpecLinkUsage()
			return err
		}
		return runSpecLinkAddRPC(cfg, opts)
	case "remove":
		opts, err := parseSpecLinkRemoveArgs(args[1:])
		if err != nil {
			cli.PrintSpecLinkUsage()
			return err
		}
		return runSpecLinkRemoveRPC(cfg, opts)
	default:
		return fmt.Errorf("unknown spec link command: %s", args[0])
	}
}

func runSpecReadCommand(cfg *config.Config, args []string) error {
	if len(args) > 0 && isHelpArg(args[0]) {
		cli.PrintSpecReadUsage()
		return nil
	}
	opts, err := parseSpecReadArgs(args)
	if err != nil {
		cli.PrintSpecReadUsage()
		return err
	}
	return runSpecReadRPC(cfg, opts)
}

func runSpecPackCommand(cfg *config.Config, args []string) error {
	if len(args) > 0 && isHelpArg(args[0]) {
		cli.PrintSpecPackUsage()
		return nil
	}
	opts, err := parseSpecPackArgs(args)
	if err != nil {
		cli.PrintSpecPackUsage()
		return err
	}
	return runSpecPackRPC(cfg, opts)
}

func runSpecLintCommand(cfg *config.Config, args []string) error {
	if len(args) > 0 && isHelpArg(args[0]) {
		cli.PrintSpecLintUsage()
		return nil
	}
	opts, err := parseSpecLintArgs(args)
	if err != nil {
		cli.PrintSpecLintUsage()
		return err
	}
	return runSpecLintRPC(cfg, opts)
}

func runSpecParityCommand(cfg *config.Config, args []string) error {
	if len(args) > 0 && isHelpArg(args[0]) {
		cli.PrintSpecParityUsage()
		return nil
	}
	opts, err := parseSpecParityArgs(args)
	if err != nil {
		cli.PrintSpecParityUsage()
		return err
	}
	return runSpecParityRPC(cfg, opts)
}

func runSpecGraphCommand(cfg *config.Config, args []string) error {
	if len(args) > 0 && isHelpArg(args[0]) {
		cli.PrintSpecGraphUsage()
		return nil
	}
	opts, err := parseSpecGraphArgs(args)
	if err != nil {
		cli.PrintSpecGraphUsage()
		return err
	}
	return runSpecGraphRPC(cfg, opts)
}

func runSpecSliceCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		cli.PrintSpecSliceUsage()
		return nil
	}
	switch args[0] {
	case "gate":
		opts, err := parseSpecSliceGateArgs(args[1:])
		if err != nil {
			cli.PrintSpecSliceUsage()
			return err
		}
		return runSpecSliceGateRPC(cfg, opts)
	default:
		return fmt.Errorf("unknown spec slice command: %s", args[0])
	}
}

func runSpecReqListRPC(cfg *config.Config, opts specReqListOptions) error {
	req := protocol.SpecRequirementListRequestBody{
		IssueID: naming.IssueID(opts.Issue),
		Status:  protocol.SpecRequirementStatus(opts.Status),
		IDs:     requirementIDsFromStrings(opts.IDs),
		Query:   opts.Query,
		Match:   opts.Match,
		Limit:   opts.Limit,
	}
	var out protocol.SpecRequirementListResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecRequirementList, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	for _, r := range out.Requirements {
		fmt.Printf("%s\t%s\t%s\n", r.ID, r.Status, r.Title)
	}
	return nil
}

func runSpecReqGetRPC(cfg *config.Config, opts specReqGetOptions) error {
	req := protocol.SpecRequirementGetRequestBody{ID: naming.RequirementID(opts.ID)}
	var out protocol.SpecRequirementGetResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecRequirementGet, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("%s\t%s\t%s\n", out.Requirement.ID, out.Requirement.Status, out.Requirement.Title)
	return nil
}

func runSpecReqCreateRPC(cfg *config.Config, opts specReqCreateOptions) error {
	req := protocol.SpecRequirementCreateRequestBody{
		ID:          naming.RequirementID(opts.ID),
		Title:       opts.Title,
		Description: opts.Description,
		IssueID:     naming.IssueID(opts.Issue),
	}
	var out protocol.SpecRequirementCreateResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecRequirementCreate, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Created requirement: %s\n", out.Requirement.ID)
	return nil
}

func runSpecReqUpdateRPC(cfg *config.Config, opts specReqUpdateOptions) error {
	req := protocol.SpecRequirementUpdateRequestBody{ID: naming.RequirementID(opts.ID)}
	if opts.Title != "" {
		v := opts.Title
		req.Title = &v
	}
	if opts.Description != "" {
		v := opts.Description
		req.Description = &v
	}
	if opts.Status != "" {
		v := protocol.SpecRequirementStatus(opts.Status)
		req.Status = &v
	}
	var out protocol.SpecRequirementUpdateResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecRequirementUpdate, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Updated requirement: %s\n", out.Requirement.ID)
	return nil
}

func runSpecReqDeleteRPC(cfg *config.Config, opts specReqDeleteOptions) error {
	req := protocol.SpecRequirementDeleteRequestBody{ID: naming.RequirementID(opts.ID), Confirm: opts.Confirm}
	var out protocol.SpecRequirementDeleteResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecRequirementDelete, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Deleted requirement: %s\n", out.ID)
	return nil
}

func runSpecLinkListRPC(cfg *config.Config, opts specLinkListOptions) error {
	req := protocol.SpecLinkListRequestBody{
		IssueID: naming.IssueID(opts.Issue),
		ReqID:   naming.RequirementID(opts.Req),
		IDs:     linkIDsFromStrings(opts.IDs),
	}
	var out protocol.SpecLinkListResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecLinkList, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	for _, l := range out.Links {
		fmt.Printf("%s\t%s\t%s\t%s\n", l.ID, l.IssueID, l.ReqID, l.Role)
	}
	return nil
}

func runSpecLinkAddRPC(cfg *config.Config, opts specLinkAddOptions) error {
	req := protocol.SpecLinkAddRequestBody{
		IssueID: naming.IssueID(opts.Issue),
		ReqID:   naming.RequirementID(opts.Req),
		Role:    protocol.SpecLinkRole(opts.Role),
		Note:    opts.Note,
	}
	var out protocol.SpecLinkAddResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecLinkAdd, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Linked issue %s -> %s\n", out.Link.IssueID, out.Link.ReqID)
	return nil
}

func runSpecLinkRemoveRPC(cfg *config.Config, opts specLinkRemoveOptions) error {
	req := protocol.SpecLinkRemoveRequestBody{
		IssueID: naming.IssueID(opts.Issue),
		ReqID:   naming.RequirementID(opts.Req),
	}
	var out protocol.SpecLinkRemoveResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecLinkRemove, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Removed link %s -> %s\n", out.IssueID, out.ReqID)
	return nil
}

func runSpecReadRPC(cfg *config.Config, opts specReadOptions) error {
	req := protocol.SpecReadRequestBody{
		IssueID: naming.IssueID(opts.Issue),
		ReqID:   naming.RequirementID(opts.Req),
	}
	var out protocol.SpecReadResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecRead, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Requirements: %d\nLinks: %d\n", len(out.Requirements), len(out.Links))
	return nil
}

func runSpecPackRPC(cfg *config.Config, opts specPackOptions) error {
	req := protocol.SpecPackRequestBody{
		IssueID: naming.IssueID(opts.Issue),
		ReqID:   naming.RequirementID(opts.Req),
		Stage:   protocol.SpecPackStage(opts.Stage),
	}
	var out protocol.SpecPackResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecPack, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Stage: %s\n", out.Stage)
	if out.IssueID != "" {
		fmt.Printf("Issue: %s\n", out.IssueID)
	}
	fmt.Printf("Requirements: %d\nLinks: %d\n", len(out.Requirements), len(out.Links))
	if len(out.Requirements) > 0 {
		fmt.Println("")
		fmt.Println("Requirements:")
		for _, r := range out.Requirements {
			fmt.Printf("- %s [%s] %s\n", r.ID, r.Status, r.Title)
			if strings.TrimSpace(r.Description) != "" {
				fmt.Printf("  %s\n", strings.TrimSpace(r.Description))
			}
		}
	}
	if len(out.Guidance) > 0 {
		fmt.Println("")
		fmt.Println("Guidance:")
		for _, item := range out.Guidance {
			fmt.Printf("- %s\n", item)
		}
	}
	if len(out.Gates) > 0 {
		fmt.Println("")
		fmt.Println("Gates:")
		for _, gate := range out.Gates {
			fmt.Printf("- %s\n", gate)
		}
	}
	return nil
}

func runSpecLintRPC(cfg *config.Config, opts specLintOptions) error {
	req := protocol.SpecLintRequestBody{Strict: opts.Strict}
	var out protocol.SpecLintResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecLint, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Lint OK: %t (diagnostics=%d)\n", out.OK, len(out.Diagnostics))
	return nil
}

func runSpecParityRPC(cfg *config.Config, opts specParityOptions) error {
	req := protocol.SpecParityRequestBody{FailOnOut: opts.FailOnOut}
	var out protocol.SpecParityResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecParity, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Parity OK: %t (findings=%d)\n", out.OK, len(out.Findings))
	return nil
}

type specSliceMetaFile struct {
	Requirements map[string]specSliceRequirementMeta `json:"requirements"`
}

type specSliceRequirementMeta struct {
	Slice     string   `json:"slice"`
	DependsOn []string `json:"depends_on"`
}

type specSliceNode struct {
	ID           string   `json:"id"`
	Requirements []string `json:"requirements"`
	DependsOn    []string `json:"depends_on"`
}

type specSliceGraph struct {
	Nodes             []specSliceNode `json:"nodes"`
	TopologicalOrder  []string        `json:"topological_order"`
	CriticalPath      []string        `json:"critical_path"`
	CriticalPathDepth int             `json:"critical_path_depth"`
}

func runSpecSliceGateRPC(cfg *config.Config, opts specSliceGateOptions) error {
	readReq := protocol.SpecReadRequestBody{IssueID: naming.IssueID(opts.Issue)}
	var read protocol.SpecReadResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecRead, readReq, &read); err != nil {
		return err
	}

	implementsByReq := map[naming.RequirementID]bool{}
	verifiesByReq := map[naming.RequirementID]bool{}
	for _, link := range read.Links {
		switch link.Role {
		case protocol.SpecLinkRoleImplements:
			implementsByReq[link.ReqID] = true
		case protocol.SpecLinkRoleVerifies:
			verifiesByReq[link.ReqID] = true
		}
	}

	missingImplements := make([]string, 0)
	missingVerifies := make([]string, 0)
	for _, req := range read.Requirements {
		if !implementsByReq[req.ID] {
			missingImplements = append(missingImplements, req.ID.String())
		}
		if !verifiesByReq[req.ID] {
			missingVerifies = append(missingVerifies, req.ID.String())
		}
	}

	var lint protocol.SpecLintResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecLint, protocol.SpecLintRequestBody{Strict: opts.Strict}, &lint); err != nil {
		return err
	}

	var parity protocol.SpecParityResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecParity, protocol.SpecParityRequestBody{}, &parity); err != nil {
		return err
	}

	testsOK := true
	if !opts.SkipTests {
		testCtx, endSpan := latencytrace.StartSpan(context.Background(), "dependency", "spec_test_command",
			"dependency.name", "sh",
			"dependency.operation", "test_command",
		)
		cmd := exec.CommandContext(testCtx, "sh", "-lc", opts.TestCommand)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		endSpan(err)
		if err != nil {
			testsOK = false
		}
	}

	result := specSliceGateResult{
		Slice:                opts.Slice,
		Issue:                opts.Issue,
		Requirements:         len(read.Requirements),
		MissingImplementsReq: missingImplements,
		MissingVerifiesReq:   missingVerifies,
		LintOK:               lint.OK,
		ParityOK:             parity.OK,
		TestsOK:              testsOK,
		TestCommand:          opts.TestCommand,
	}
	result.OK = result.LintOK && result.ParityOK && result.TestsOK && len(result.MissingImplementsReq) == 0 && len(result.MissingVerifiesReq) == 0

	if opts.JSON {
		if err := printJSON(result); err != nil {
			return err
		}
	} else {
		fmt.Printf("Spec slice gate: %s (issue=%s)\n", result.Slice, result.Issue)
		fmt.Printf("Requirements: %d\n", result.Requirements)
		fmt.Printf("Missing implements links: %d\n", len(result.MissingImplementsReq))
		fmt.Printf("Missing verifies links: %d\n", len(result.MissingVerifiesReq))
		fmt.Printf("Lint OK: %t\n", result.LintOK)
		fmt.Printf("Parity OK: %t\n", result.ParityOK)
		if opts.SkipTests {
			fmt.Println("Tests: skipped")
		} else {
			fmt.Printf("Tests OK: %t (%s)\n", result.TestsOK, opts.TestCommand)
		}
		fmt.Printf("Gate OK: %t\n", result.OK)
	}

	if !result.OK {
		return fmt.Errorf("spec slice gate failed")
	}
	return nil
}

func runSpecGraphRPC(cfg *config.Config, opts specGraphOptions) error {
	var reqs protocol.SpecRequirementListResponseBody
	if err := runSpecRPC(cfg, protocol.CommandSpecRequirementList, protocol.SpecRequirementListRequestBody{
		IssueID: naming.IssueID(opts.Issue),
	}, &reqs); err != nil {
		return err
	}
	metadata, err := loadSpecSliceMeta(opts.MetaPath)
	if err != nil {
		return err
	}
	graph, err := buildSpecSliceGraph(reqs.Requirements, metadata)
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(graph)
	}
	switch opts.Format {
	case "dot":
		fmt.Print(renderSpecSliceGraphDOT(graph))
	default:
		fmt.Print(renderSpecSliceGraphText(graph))
	}
	return nil
}

func runSpecRPC(cfg *config.Config, command string, body any, out any) error {
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

func parseSpecReqListArgs(args []string) (specReqListOptions, error) {
	opts := specReqListOptions{}
	fs := flag.NewFlagSet("spec req list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Issue, "issue", "", "issue id filter")
	fs.StringVar(&opts.Status, "status", "", "requirement status filter")
	fs.StringVar(&opts.Query, "query", "", "content query over requirement id, external code, title, and description")
	fs.StringVar(&opts.Match, "match", "all", "query match mode: all or any")
	fs.IntVar(&opts.Limit, "limit", 0, "maximum requirements to return")
	addSelectorFlags(fs, &opts.IDs, "req")
	if err := fs.Parse(args); err != nil {
		return specReqListOptions{}, err
	}
	if fs.NArg() != 0 {
		return specReqListOptions{}, fmt.Errorf("usage: az spec req list [--json] [--issue <issue-id>] [--status <open|accepted|superseded>] [--query <text>] [--match <all|any>] [--limit <n>] [--id <req-id> ...] [--ids a,b,c]")
	}
	var err error
	if opts.Issue, err = normalizeOptionalIdentifier("issue-id", opts.Issue); err != nil {
		return specReqListOptions{}, err
	}
	opts.Query = strings.TrimSpace(opts.Query)
	opts.Match = strings.TrimSpace(strings.ToLower(opts.Match))
	if opts.Match == "" {
		opts.Match = "all"
	}
	if opts.Match != "all" && opts.Match != "any" {
		return specReqListOptions{}, fmt.Errorf("invalid match %q; expected all|any", opts.Match)
	}
	if opts.Limit < 0 {
		return specReqListOptions{}, fmt.Errorf("limit must be non-negative")
	}
	if opts.Status != "" {
		if opts.Status, err = parseRequirementStatus(opts.Status); err != nil {
			return specReqListOptions{}, err
		}
	}
	return opts, nil
}

func parseSpecReqGetArgs(args []string) (specReqGetOptions, error) {
	opts := specReqGetOptions{}
	fs := flag.NewFlagSet("spec req get", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ID, "id", "", "requirement id")
	if err := fs.Parse(args); err != nil {
		return specReqGetOptions{}, err
	}
	if fs.NArg() != 0 {
		return specReqGetOptions{}, fmt.Errorf("usage: az spec req get --id <req-id> [--json]")
	}
	if strings.TrimSpace(opts.ID) == "" {
		return specReqGetOptions{}, fmt.Errorf("missing required flag: --id")
	}
	return opts, nil
}

func parseSpecReqCreateArgs(args []string) (specReqCreateOptions, error) {
	opts := specReqCreateOptions{}
	fs := flag.NewFlagSet("spec req create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ID, "id", "", "requirement id")
	fs.StringVar(&opts.Title, "title", "", "requirement title")
	fs.StringVar(&opts.Description, "description", "", "requirement description")
	fs.StringVar(&opts.Issue, "issue", "", "issue id")
	if err := fs.Parse(args); err != nil {
		return specReqCreateOptions{}, err
	}
	if fs.NArg() != 0 {
		return specReqCreateOptions{}, fmt.Errorf("usage: az spec req create --id <req-id> --title <text> [--description <text>] [--issue <issue-id>] [--json]")
	}
	if strings.TrimSpace(opts.ID) == "" {
		return specReqCreateOptions{}, fmt.Errorf("missing required flag: --id")
	}
	if strings.TrimSpace(opts.Title) == "" {
		return specReqCreateOptions{}, fmt.Errorf("missing required flag: --title")
	}
	var err error
	if opts.Issue, err = normalizeOptionalIdentifier("issue-id", opts.Issue); err != nil {
		return specReqCreateOptions{}, err
	}
	return opts, nil
}

func parseSpecReqUpdateArgs(args []string) (specReqUpdateOptions, error) {
	opts := specReqUpdateOptions{}
	fs := flag.NewFlagSet("spec req update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ID, "id", "", "requirement id")
	fs.StringVar(&opts.Title, "title", "", "requirement title")
	fs.StringVar(&opts.Description, "description", "", "requirement description")
	fs.StringVar(&opts.Status, "status", "", "requirement status")
	if err := fs.Parse(args); err != nil {
		return specReqUpdateOptions{}, err
	}
	if fs.NArg() != 0 {
		return specReqUpdateOptions{}, fmt.Errorf("usage: az spec req update --id <req-id> [--title <text>] [--description <text>] [--status <open|accepted|superseded>] [--json]")
	}
	if strings.TrimSpace(opts.ID) == "" {
		return specReqUpdateOptions{}, fmt.Errorf("missing required flag: --id")
	}
	if opts.Title == "" && opts.Description == "" && opts.Status == "" {
		return specReqUpdateOptions{}, fmt.Errorf("no update fields provided")
	}
	if opts.Status != "" {
		var err error
		opts.Status, err = parseRequirementStatus(opts.Status)
		if err != nil {
			return specReqUpdateOptions{}, err
		}
	}
	return opts, nil
}

func parseSpecReqDeleteArgs(args []string) (specReqDeleteOptions, error) {
	opts := specReqDeleteOptions{}
	fs := flag.NewFlagSet("spec req delete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ID, "id", "", "requirement id")
	fs.BoolVar(&opts.Confirm, "confirm", false, "confirm delete")
	if err := fs.Parse(args); err != nil {
		return specReqDeleteOptions{}, err
	}
	if fs.NArg() != 0 {
		return specReqDeleteOptions{}, fmt.Errorf("usage: az spec req delete --id <req-id> --confirm [--json]")
	}
	if strings.TrimSpace(opts.ID) == "" {
		return specReqDeleteOptions{}, fmt.Errorf("missing required flag: --id")
	}
	if !opts.Confirm {
		return specReqDeleteOptions{}, fmt.Errorf("missing required flag: --confirm")
	}
	return opts, nil
}

func parseSpecLinkListArgs(args []string) (specLinkListOptions, error) {
	opts := specLinkListOptions{}
	fs := flag.NewFlagSet("spec link list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Issue, "issue", "", "issue id filter")
	fs.StringVar(&opts.Req, "req", "", "requirement id filter")
	addSelectorFlags(fs, &opts.IDs, "link")
	if err := fs.Parse(args); err != nil {
		return specLinkListOptions{}, err
	}
	if fs.NArg() != 0 {
		return specLinkListOptions{}, fmt.Errorf("usage: az spec link list [--json] [--issue <issue-id>] [--req <req-id>] [--id <link-id> ...] [--ids a,b,c]")
	}
	var err error
	if opts.Issue, err = normalizeOptionalIdentifier("issue-id", opts.Issue); err != nil {
		return specLinkListOptions{}, err
	}
	if opts.Req, err = normalizeOptionalIdentifier("req-id", opts.Req); err != nil {
		return specLinkListOptions{}, err
	}
	return opts, nil
}

func parseSpecLinkAddArgs(args []string) (specLinkAddOptions, error) {
	opts := specLinkAddOptions{Role: "implements"}
	fs := flag.NewFlagSet("spec link add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Issue, "issue", "", "issue id")
	fs.StringVar(&opts.Req, "req", "", "requirement id")
	fs.StringVar(&opts.Role, "role", opts.Role, "link role")
	fs.StringVar(&opts.Note, "note", "", "link note")
	if err := fs.Parse(args); err != nil {
		return specLinkAddOptions{}, err
	}
	if fs.NArg() != 0 {
		return specLinkAddOptions{}, fmt.Errorf("usage: az spec link add --issue <issue-id> --req <req-id> [--role <implements|verifies|relates>] [--note <text>] [--json]")
	}
	if strings.TrimSpace(opts.Issue) == "" {
		return specLinkAddOptions{}, fmt.Errorf("missing required flag: --issue")
	}
	if strings.TrimSpace(opts.Req) == "" {
		return specLinkAddOptions{}, fmt.Errorf("missing required flag: --req")
	}
	var err error
	if opts.Role, err = parseLinkRole(opts.Role); err != nil {
		return specLinkAddOptions{}, err
	}
	return opts, nil
}

func parseSpecLinkRemoveArgs(args []string) (specLinkRemoveOptions, error) {
	opts := specLinkRemoveOptions{}
	fs := flag.NewFlagSet("spec link remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Issue, "issue", "", "issue id")
	fs.StringVar(&opts.Req, "req", "", "requirement id")
	if err := fs.Parse(args); err != nil {
		return specLinkRemoveOptions{}, err
	}
	if fs.NArg() != 0 {
		return specLinkRemoveOptions{}, fmt.Errorf("usage: az spec link remove --issue <issue-id> --req <req-id> [--json]")
	}
	if strings.TrimSpace(opts.Issue) == "" {
		return specLinkRemoveOptions{}, fmt.Errorf("missing required flag: --issue")
	}
	if strings.TrimSpace(opts.Req) == "" {
		return specLinkRemoveOptions{}, fmt.Errorf("missing required flag: --req")
	}
	return opts, nil
}

func parseSpecReadArgs(args []string) (specReadOptions, error) {
	opts := specReadOptions{}
	fs := flag.NewFlagSet("spec read", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Issue, "issue", "", "issue id filter")
	fs.StringVar(&opts.Req, "req", "", "requirement id filter")
	if err := fs.Parse(args); err != nil {
		return specReadOptions{}, err
	}
	if fs.NArg() != 0 {
		return specReadOptions{}, fmt.Errorf("usage: az spec read [--json] [--issue <issue-id>] [--req <req-id>]")
	}
	var err error
	if opts.Issue, err = normalizeOptionalIdentifier("issue-id", opts.Issue); err != nil {
		return specReadOptions{}, err
	}
	if opts.Req, err = normalizeOptionalIdentifier("req-id", opts.Req); err != nil {
		return specReadOptions{}, err
	}
	return opts, nil
}

func parseSpecPackArgs(args []string) (specPackOptions, error) {
	opts := specPackOptions{Stage: string(protocol.SpecPackStageBrownfield)}
	fs := flag.NewFlagSet("spec pack", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Issue, "issue", "", "issue id filter")
	fs.StringVar(&opts.Req, "req", "", "requirement id filter")
	fs.StringVar(&opts.Stage, "stage", opts.Stage, "source reconciliation stage")
	if err := fs.Parse(args); err != nil {
		return specPackOptions{}, err
	}
	if fs.NArg() != 0 {
		return specPackOptions{}, fmt.Errorf("usage: az spec pack [--json] (--issue <issue-id> | --req <req-id>) [--stage <greenfield|brownfield|repair|verify>]")
	}
	var err error
	if opts.Issue, err = normalizeOptionalIdentifier("issue-id", opts.Issue); err != nil {
		return specPackOptions{}, err
	}
	if opts.Req, err = normalizeOptionalIdentifier("req-id", opts.Req); err != nil {
		return specPackOptions{}, err
	}
	if opts.Issue == "" && opts.Req == "" {
		return specPackOptions{}, fmt.Errorf("missing required flag: --issue or --req")
	}
	if opts.Stage, err = parseSpecPackStage(opts.Stage); err != nil {
		return specPackOptions{}, err
	}
	return opts, nil
}

func parseSpecLintArgs(args []string) (specLintOptions, error) {
	opts := specLintOptions{}
	fs := flag.NewFlagSet("spec lint", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.BoolVar(&opts.Strict, "strict", false, "strict mode")
	if err := fs.Parse(args); err != nil {
		return specLintOptions{}, err
	}
	if fs.NArg() != 0 {
		return specLintOptions{}, fmt.Errorf("usage: az spec lint [--json] [--strict]")
	}
	return opts, nil
}

func parseSpecParityArgs(args []string) (specParityOptions, error) {
	opts := specParityOptions{}
	fs := flag.NewFlagSet("spec parity", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.BoolVar(&opts.FailOnOut, "fail-on-out", false, "fail on drift")
	if err := fs.Parse(args); err != nil {
		return specParityOptions{}, err
	}
	if fs.NArg() != 0 {
		return specParityOptions{}, fmt.Errorf("usage: az spec parity [--json] [--fail-on-out]")
	}
	return opts, nil
}

func parseSpecSliceGateArgs(args []string) (specSliceGateOptions, error) {
	opts := specSliceGateOptions{TestCommand: "go test ./..."}
	fs := flag.NewFlagSet("spec slice gate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Slice, "slice", "", "slice id")
	fs.StringVar(&opts.Issue, "issue", "", "issue id (defaults to --slice)")
	fs.BoolVar(&opts.Strict, "strict", false, "strict lint mode")
	fs.BoolVar(&opts.SkipTests, "skip-tests", false, "skip test command")
	fs.StringVar(&opts.TestCommand, "test-command", opts.TestCommand, "shell command for test gate")
	if err := fs.Parse(args); err != nil {
		return specSliceGateOptions{}, err
	}
	if fs.NArg() != 0 {
		return specSliceGateOptions{}, fmt.Errorf("usage: az spec slice gate --slice <slice-id> [--issue <issue-id>] [--strict] [--skip-tests] [--test-command <cmd>] [--json]")
	}
	if strings.TrimSpace(opts.Slice) == "" {
		return specSliceGateOptions{}, fmt.Errorf("missing required flag: --slice")
	}
	var err error
	if opts.Issue, err = normalizeOptionalIdentifier("issue-id", opts.Issue); err != nil {
		return specSliceGateOptions{}, err
	}
	if opts.Issue == "" {
		opts.Issue = strings.TrimSpace(opts.Slice)
	}
	opts.TestCommand = strings.TrimSpace(opts.TestCommand)
	if !opts.SkipTests && opts.TestCommand == "" {
		return specSliceGateOptions{}, fmt.Errorf("test command must be non-empty when tests are enabled")
	}
	return opts, nil
}

func parseSpecGraphArgs(args []string) (specGraphOptions, error) {
	opts := specGraphOptions{
		MetaPath: ".azedarach/spec/slices.json",
		Format:   "text",
	}
	fs := flag.NewFlagSet("spec graph", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Issue, "issue", "", "issue id filter")
	fs.StringVar(&opts.MetaPath, "meta", opts.MetaPath, "slice metadata json path")
	fs.StringVar(&opts.Format, "format", opts.Format, "output format: text|dot")
	if err := fs.Parse(args); err != nil {
		return specGraphOptions{}, err
	}
	if fs.NArg() != 0 {
		return specGraphOptions{}, fmt.Errorf("usage: az spec graph [--json] --issue <issue-id> [--meta <path>] [--format <text|dot>]")
	}
	if strings.TrimSpace(opts.Issue) == "" {
		return specGraphOptions{}, fmt.Errorf("missing required flag: --issue")
	}
	var err error
	if opts.Issue, err = normalizeOptionalIdentifier("issue-id", opts.Issue); err != nil {
		return specGraphOptions{}, err
	}
	opts.MetaPath = strings.TrimSpace(opts.MetaPath)
	if opts.MetaPath == "" {
		return specGraphOptions{}, fmt.Errorf("meta path must be non-empty")
	}
	switch strings.TrimSpace(opts.Format) {
	case "text", "dot":
		opts.Format = strings.TrimSpace(opts.Format)
	default:
		return specGraphOptions{}, fmt.Errorf("invalid format %q; expected text|dot", opts.Format)
	}
	return opts, nil
}

func addSelectorFlags(fs *flag.FlagSet, ids *[]string, kind string) {
	fs.Func("id", fmt.Sprintf("restrict to a specific %s id (repeatable)", kind), func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty %s id", kind)
		}
		*ids = appendUniqueOrdered(*ids, trimmed)
		return nil
	})
	fs.Func("ids", fmt.Sprintf("comma-separated %s ids", kind), func(v string) error {
		for _, token := range strings.Split(v, ",") {
			trimmed := strings.TrimSpace(token)
			if trimmed == "" {
				continue
			}
			*ids = appendUniqueOrdered(*ids, trimmed)
		}
		return nil
	})
}

func appendUniqueOrdered(ids []string, values ...string) []string {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	return ids
}

func normalizeOptionalIdentifier(name, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if trimmed == "" {
		return "", fmt.Errorf("%s must be non-empty", name)
	}
	return trimmed, nil
}

func parseRequirementStatus(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "open", "accepted", "superseded":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("invalid requirement status %q; expected open|accepted|superseded", value)
	}
}

func parseSpecPackStage(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "greenfield", "brownfield", "repair", "verify":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("invalid stage %q; expected greenfield|brownfield|repair|verify", value)
	}
}

func parseLinkRole(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "implements", "verifies", "relates":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("invalid link role %q; expected implements|verifies|relates", value)
	}
}

func requirementIDsFromStrings(ids []string) []naming.RequirementID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]naming.RequirementID, 0, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		out = append(out, naming.RequirementID(trimmed))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func linkIDsFromStrings(ids []string) []naming.SpecLinkID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]naming.SpecLinkID, 0, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		out = append(out, naming.SpecLinkID(trimmed))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func loadSpecSliceMeta(path string) (specSliceMetaFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return specSliceMetaFile{}, fmt.Errorf("read slice metadata %q: %w", path, err)
	}
	var out specSliceMetaFile
	if err := json.Unmarshal(data, &out); err != nil {
		return specSliceMetaFile{}, fmt.Errorf("decode slice metadata %q: %w", path, err)
	}
	if len(out.Requirements) == 0 {
		return specSliceMetaFile{}, fmt.Errorf("slice metadata %q has no requirements", path)
	}
	return out, nil
}

func buildSpecSliceGraph(reqs []protocol.SpecRequirement, meta specSliceMetaFile) (specSliceGraph, error) {
	sliceReqs := map[string][]string{}
	deps := map[string]map[string]struct{}{}
	for _, req := range reqs {
		m, ok := meta.Requirements[req.ID.String()]
		if !ok || strings.TrimSpace(m.Slice) == "" {
			continue
		}
		sliceID := strings.TrimSpace(m.Slice)
		sliceReqs[sliceID] = append(sliceReqs[sliceID], req.ID.String())
		if _, ok := deps[sliceID]; !ok {
			deps[sliceID] = map[string]struct{}{}
		}
		for _, dep := range m.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" || dep == sliceID {
				continue
			}
			deps[sliceID][dep] = struct{}{}
			if _, ok := deps[dep]; !ok {
				deps[dep] = map[string]struct{}{}
			}
		}
	}
	if len(sliceReqs) == 0 {
		return specSliceGraph{}, fmt.Errorf("no slice metadata matched requirements for selected issue")
	}
	order, err := topoSortSlices(deps)
	if err != nil {
		return specSliceGraph{}, err
	}
	nodes := make([]specSliceNode, 0, len(order))
	for _, id := range order {
		reqIDs := append([]string(nil), sliceReqs[id]...)
		sort.Strings(reqIDs)
		depIDs := setToSortedList(deps[id])
		nodes = append(nodes, specSliceNode{ID: id, Requirements: reqIDs, DependsOn: depIDs})
	}
	path := criticalPath(order, deps)
	return specSliceGraph{
		Nodes:             nodes,
		TopologicalOrder:  order,
		CriticalPath:      path,
		CriticalPathDepth: len(path),
	}, nil
}

func topoSortSlices(deps map[string]map[string]struct{}) ([]string, error) {
	incoming := map[string]int{}
	reverse := map[string][]string{}
	for node := range deps {
		incoming[node] = 0
	}
	for node, nodeDeps := range deps {
		for dep := range nodeDeps {
			incoming[node]++
			reverse[dep] = append(reverse[dep], node)
		}
	}
	ready := make([]string, 0)
	for node, count := range incoming {
		if count == 0 {
			ready = append(ready, node)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(incoming))
	for len(ready) > 0 {
		node := ready[0]
		ready = ready[1:]
		order = append(order, node)
		downstream := reverse[node]
		sort.Strings(downstream)
		for _, child := range downstream {
			incoming[child]--
			if incoming[child] == 0 {
				ready = append(ready, child)
			}
		}
		sort.Strings(ready)
	}
	if len(order) != len(incoming) {
		return nil, fmt.Errorf("slice dependency graph contains a cycle")
	}
	return order, nil
}

func criticalPath(order []string, deps map[string]map[string]struct{}) []string {
	bestDepth := map[string]int{}
	parent := map[string]string{}
	maxNode := ""
	for _, node := range order {
		bestDepth[node] = 1
		for dep := range deps[node] {
			if bestDepth[dep]+1 > bestDepth[node] {
				bestDepth[node] = bestDepth[dep] + 1
				parent[node] = dep
			}
		}
		if maxNode == "" || bestDepth[node] > bestDepth[maxNode] {
			maxNode = node
		}
	}
	if maxNode == "" {
		return nil
	}
	reversed := []string{maxNode}
	for parent[reversed[len(reversed)-1]] != "" {
		reversed = append(reversed, parent[reversed[len(reversed)-1]])
	}
	path := make([]string, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		path = append(path, reversed[i])
	}
	return path
}

func setToSortedList(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for v := range values {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func renderSpecSliceGraphText(graph specSliceGraph) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Slices: %d\n", len(graph.Nodes)))
	b.WriteString(fmt.Sprintf("Critical path depth: %d\n", graph.CriticalPathDepth))
	if len(graph.CriticalPath) > 0 {
		b.WriteString("Critical path: " + strings.Join(graph.CriticalPath, " -> ") + "\n")
	}
	b.WriteString("\nTopological order:\n")
	for i, id := range graph.TopologicalOrder {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, id))
	}
	b.WriteString("\nSlices:\n")
	for _, node := range graph.Nodes {
		b.WriteString(fmt.Sprintf("- %s\n", node.ID))
		if len(node.DependsOn) == 0 {
			b.WriteString("  depends_on: (none)\n")
		} else {
			b.WriteString("  depends_on: " + strings.Join(node.DependsOn, ", ") + "\n")
		}
		if len(node.Requirements) == 0 {
			b.WriteString("  requirements: (none)\n")
		} else {
			b.WriteString("  requirements: " + strings.Join(node.Requirements, ", ") + "\n")
		}
	}
	return b.String()
}

func renderSpecSliceGraphDOT(graph specSliceGraph) string {
	var b strings.Builder
	b.WriteString("digraph spec_slices {\n")
	b.WriteString("  rankdir=LR;\n")
	for _, node := range graph.Nodes {
		b.WriteString(fmt.Sprintf("  \"%s\";\n", node.ID))
	}
	for _, node := range graph.Nodes {
		for _, dep := range node.DependsOn {
			b.WriteString(fmt.Sprintf("  \"%s\" -> \"%s\";\n", dep, node.ID))
		}
	}
	b.WriteString("}\n")
	return b.String()
}
