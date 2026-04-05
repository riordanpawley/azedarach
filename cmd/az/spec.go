package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const specDisabledMessage = "spec workflows are disabled for this project; re-enable with: az config set spec.enabled true"

type specReqListOptions struct {
	JSON   bool
	Issue  string
	Status string
	IDs    []string
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

type specLintOptions struct {
	JSON   bool
	Strict bool
}

type specParityOptions struct {
	JSON      bool
	FailOnOut bool
}

type specSyncOptions struct {
	Target string
	Check  bool
	JSON   bool
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
	case "lint":
		return runSpecLintCommand(cfg, args[1:])
	case "parity":
		return runSpecParityCommand(cfg, args[1:])
	case "sync":
		return runSpecSyncCommand(args[1:])
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

func runSpecSyncCommand(args []string) error {
	if len(args) > 0 && isHelpArg(args[0]) {
		cli.PrintSpecSyncUsage()
		return nil
	}
	opts, err := parseSpecSyncArgs(args)
	if err != nil {
		cli.PrintSpecSyncUsage()
		return err
	}

	repoDir, err := specRepoDir()
	if err != nil {
		return err
	}
	result, runErr := cli.RunSpecMarkdownSync(repoDir, opts.Check)
	if result.Target != "" {
		if opts.JSON {
			data, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal spec sync result: %w", err)
			}
			fmt.Println(string(data))
		} else {
			printSpecSyncResult(result)
		}
	}
	return runErr
}

func runSpecReqListRPC(cfg *config.Config, opts specReqListOptions) error {
	req := protocol.SpecRequirementListRequestBody{
		IssueID: opts.Issue,
		Status:  protocol.SpecRequirementStatus(opts.Status),
		IDs:     opts.IDs,
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
	req := protocol.SpecRequirementGetRequestBody{ID: opts.ID}
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
		ID:          opts.ID,
		Title:       opts.Title,
		Description: opts.Description,
		IssueID:     opts.Issue,
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
	req := protocol.SpecRequirementUpdateRequestBody{ID: opts.ID}
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
	req := protocol.SpecRequirementDeleteRequestBody{ID: opts.ID, Confirm: opts.Confirm}
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
	req := protocol.SpecLinkListRequestBody{IssueID: opts.Issue, ReqID: opts.Req, IDs: opts.IDs}
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
		IssueID: opts.Issue,
		ReqID:   opts.Req,
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
	req := protocol.SpecLinkRemoveRequestBody{IssueID: opts.Issue, ReqID: opts.Req}
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
	req := protocol.SpecReadRequestBody{IssueID: opts.Issue, ReqID: opts.Req}
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
			Meta:            protocol.Metadata{ProjectID: deps.ProjectID},
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
	addSelectorFlags(fs, &opts.IDs, "req")
	if err := fs.Parse(args); err != nil {
		return specReqListOptions{}, err
	}
	if fs.NArg() != 0 {
		return specReqListOptions{}, fmt.Errorf("usage: az spec req list [--json] [--issue <issue-id>] [--status <open|accepted|superseded>] [--id <req-id> ...] [--ids a,b,c]")
	}
	var err error
	if opts.Issue, err = normalizeOptionalIdentifier("issue-id", opts.Issue); err != nil {
		return specReqListOptions{}, err
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

func parseSpecSyncArgs(args []string) (specSyncOptions, error) {
	opts := specSyncOptions{}
	fs := flag.NewFlagSet("spec sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Target, "target", "", "sync target")
	fs.BoolVar(&opts.Check, "check", false, "check mode")
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	if err := fs.Parse(args); err != nil {
		return specSyncOptions{}, err
	}
	if fs.NArg() != 0 || strings.TrimSpace(opts.Target) != "md" {
		return specSyncOptions{}, fmt.Errorf("usage: az spec sync --target md [--check] [--json]")
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

func parseLinkRole(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "implements", "verifies", "relates":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("invalid link role %q; expected implements|verifies|relates", value)
	}
}

func specNotImplementedError(parts ...string) error {
	return fmt.Errorf("az spec %s is not implemented in Go runtime yet", strings.Join(parts, " "))
}

func specRepoDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	repoDir, err := config.ResolveProjectRoot(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	return repoDir, nil
}

func printSpecSyncResult(result cli.SpecMarkdownSyncResult) {
	fmt.Printf("Spec markdown sync target: %s\n", result.Target)
	fmt.Printf("Mode: %s\n", result.Mode)
	fmt.Printf("Changed: %t\n", result.Changed)
	for _, file := range result.Files {
		fmt.Printf("- %s: %s\n", file.Path, file.Status)
	}
}
