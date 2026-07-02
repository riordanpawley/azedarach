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
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type learnAddOpts struct {
	JSON     bool
	Issue    string
	Req      string
	Session  string
	Summary  string
	Evidence string
	Tags     []string
	Files    []string
}

type learnRecallOpts struct {
	JSON            bool
	Query           string
	Issue           string
	Req             string
	Statuses        []string
	Tags            []string
	Files           []string
	Limit           int
	IncludeEvidence bool
}

type learnShowOpts struct {
	JSON bool
	ID   string
}

type learnReviewOpts struct {
	JSON   bool
	ID     string
	Status string
	Note   string
	Limit  int
}

type learnPromoteOpts struct {
	JSON           bool
	ID             string
	Target         string
	TargetID       string
	Note           string
	TargetHash     string
	TargetMetadata map[string]string
}

type learnRelateOpts struct {
	JSON             bool
	Type             string
	SourceLearningID string
	TargetLearningID string
	Note             string
	ScopeIssue       string
	ScopeReq         string
	ScopeSession     string
	ScopeTags        []string
	ScopeFiles       []string
}

func runLearnCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		printLearnUsage()
		return nil
	}
	if len(args) > 1 && isHelpArg(args[1]) {
		printLearnUsage()
		return nil
	}
	switch args[0] {
	case "add":
		opts, err := parseLearnAddArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnAddRPC(cfg, opts)
	case "recall":
		opts, err := parseLearnRecallArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnRecallRPC(cfg, opts)
	case "show":
		opts, err := parseLearnShowArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnShowRPC(cfg, opts)
	case "review":
		opts, err := parseLearnReviewArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnReviewRPC(cfg, opts)
	case "promote":
		opts, err := parseLearnPromoteArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnPromoteRPC(cfg, opts)
	case "relate":
		opts, err := parseLearnRelateArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnRelateRPC(cfg, opts)
	default:
		return fmt.Errorf("unknown learn command: %s", args[0])
	}
}

func runLearnAddRPC(cfg *config.Config, opts learnAddOpts) error {
	req := protocol.LearnAddRequestBody{
		IssueID:   naming.IssueID(opts.Issue),
		ReqID:     naming.RequirementID(opts.Req),
		SessionID: naming.SessionID(opts.Session),
		Summary:   opts.Summary,
		Evidence:  opts.Evidence,
		Tags:      opts.Tags,
		Files:     opts.Files,
	}
	var out protocol.LearnAddResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnAdd, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Recorded learning: %s\n", out.Learning.ID)
	return nil
}

func runLearnRecallRPC(cfg *config.Config, opts learnRecallOpts) error {
	statuses := make([]protocol.LearningStatus, 0, len(opts.Statuses))
	for _, status := range opts.Statuses {
		statuses = append(statuses, protocol.LearningStatus(status))
	}
	req := protocol.LearnRecallRequestBody{
		Query:           opts.Query,
		IssueID:         naming.IssueID(opts.Issue),
		ReqID:           naming.RequirementID(opts.Req),
		Statuses:        statuses,
		Tags:            opts.Tags,
		Files:           opts.Files,
		Limit:           opts.Limit,
		IncludeEvidence: opts.IncludeEvidence,
	}
	var out protocol.LearnRecallResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnRecall, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	printLearnings(out.Learnings, opts.IncludeEvidence)
	return nil
}

func runLearnShowRPC(cfg *config.Config, opts learnShowOpts) error {
	var out protocol.LearnShowResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnShow, protocol.LearnShowRequestBody{ID: opts.ID}, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	printLearnings([]protocol.Learning{out.Learning}, true)
	return nil
}

func runLearnReviewRPC(cfg *config.Config, opts learnReviewOpts) error {
	req := protocol.LearnReviewRequestBody{ID: opts.ID, Status: protocol.LearningStatus(opts.Status), Note: opts.Note, Limit: opts.Limit}
	var out protocol.LearnReviewResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnReview, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	if out.Updated != nil {
		fmt.Printf("Updated learning: %s [%s]\n", out.Updated.ID, out.Updated.Status)
		return nil
	}
	printLearnings(out.Learnings, false)
	return nil
}

func runLearnPromoteRPC(cfg *config.Config, opts learnPromoteOpts) error {
	req := protocol.LearnPromoteRequestBody{
		ID:             opts.ID,
		Target:         protocol.LearningPromotionTarget(opts.Target),
		TargetID:       opts.TargetID,
		Note:           opts.Note,
		TargetHash:     opts.TargetHash,
		TargetMetadata: opts.TargetMetadata,
	}
	var out protocol.LearnPromoteResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnPromote, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Promoted learning: %s -> %s:%s\n%s\n", out.Learning.ID, out.Learning.Target, out.Learning.TargetID, out.Guidance)
	return nil
}

func runLearnRelateRPC(cfg *config.Config, opts learnRelateOpts) error {
	req := protocol.LearnRelateRequestBody{
		Type:             protocol.LearningRelationType(opts.Type),
		SourceLearningID: opts.SourceLearningID,
		TargetLearningID: opts.TargetLearningID,
		Note:             opts.Note,
		ScopeIssueID:     naming.IssueID(opts.ScopeIssue),
		ScopeReqID:       naming.RequirementID(opts.ScopeReq),
		ScopeSessionID:   naming.SessionID(opts.ScopeSession),
		ScopeTags:        opts.ScopeTags,
		ScopeFiles:       opts.ScopeFiles,
	}
	var out protocol.LearnRelateResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnRelate, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	printLearningRelation(out.Relation)
	return nil
}

func printLearnings(learnings []protocol.Learning, includeEvidence bool) {
	if len(learnings) == 0 {
		fmt.Println("No learnings found.")
		return
	}
	for _, learning := range learnings {
		scope := learningScope(learning)
		if scope != "" {
			scope = " " + scope
		}
		fmt.Printf("%s [%s]%s %s\n", learning.ID, learning.Status, scope, learning.Summary)
		if len(learning.Tags) > 0 {
			fmt.Printf("  tags: %s\n", strings.Join(learning.Tags, ", "))
		}
		if includeEvidence && strings.TrimSpace(learning.Evidence) != "" {
			fmt.Printf("  evidence: %s\n", learning.Evidence)
		}
		for _, relation := range learning.Relations {
			fmt.Print("  relation: ")
			printLearningRelationInline(relation)
		}
	}
}

func printLearningRelation(relation protocol.LearningRelation) {
	fmt.Print("Recorded relation: ")
	printLearningRelationInline(relation)
}

func printLearningRelationInline(relation protocol.LearningRelation) {
	scope := learningRelationScope(relation)
	if scope != "" {
		scope = " " + scope
	}
	fmt.Printf("%s [%s] %s -> %s%s\n", relation.ID, relation.Type, relation.SourceLearningID, relation.TargetLearningID, scope)
	if strings.TrimSpace(relation.Note) != "" {
		fmt.Printf("    note: %s\n", relation.Note)
	}
}

func learningRelationScope(relation protocol.LearningRelation) string {
	parts := make([]string, 0, 5)
	if relation.ScopeIssueID != "" {
		parts = append(parts, "issue="+relation.ScopeIssueID.String())
	}
	if relation.ScopeReqID != "" {
		parts = append(parts, "req="+relation.ScopeReqID.String())
	}
	if relation.ScopeSessionID != "" {
		parts = append(parts, "session="+relation.ScopeSessionID.String())
	}
	if len(relation.ScopeTags) > 0 {
		parts = append(parts, "tags="+strings.Join(relation.ScopeTags, ","))
	}
	if len(relation.ScopeFiles) > 0 {
		parts = append(parts, "files="+strings.Join(relation.ScopeFiles, ","))
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, " ") + ")"
}

func learningScope(learning protocol.Learning) string {
	parts := make([]string, 0, 3)
	if learning.IssueID != "" {
		parts = append(parts, "issue="+learning.IssueID.String())
	}
	if learning.ReqID != "" {
		parts = append(parts, "req="+learning.ReqID.String())
	}
	if learning.SessionID != "" {
		parts = append(parts, "session="+learning.SessionID.String())
	}
	return strings.Join(parts, " ")
}

func runLearnRPC(cfg *config.Config, command string, body any, out any) error {
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

func parseLearnAddArgs(args []string) (learnAddOpts, error) {
	opts := learnAddOpts{Issue: strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID"))}
	fs := flag.NewFlagSet("learn add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Issue, "issue", opts.Issue, "issue id scope")
	fs.StringVar(&opts.Req, "req", "", "requirement id scope")
	fs.StringVar(&opts.Session, "session", "", "session id scope")
	fs.StringVar(&opts.Summary, "summary", "", "short summary")
	fs.StringVar(&opts.Evidence, "evidence", "", "evidence text")
	addRepeatedStringFlag(fs, "tag", &opts.Tags)
	addRepeatedStringFlag(fs, "file", &opts.Files)
	if err := fs.Parse(args); err != nil {
		return learnAddOpts{}, err
	}
	if fs.NArg() != 0 {
		return learnAddOpts{}, fmt.Errorf("usage: az learn add --evidence <text> [--summary <text>] [--issue <id>] [--req <id>] [--tag <tag> ...] [--file <path> ...] [--json]")
	}
	if strings.TrimSpace(opts.Evidence) == "" {
		return learnAddOpts{}, fmt.Errorf("missing required flag: --evidence")
	}
	return opts, nil
}

func parseLearnRecallArgs(args []string) (learnRecallOpts, error) {
	opts := learnRecallOpts{Limit: 5}
	fs := flag.NewFlagSet("learn recall", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Query, "query", "", "free text query")
	fs.StringVar(&opts.Issue, "issue", "", "filter by issue id")
	fs.StringVar(&opts.Req, "req", "", "filter by requirement id")
	fs.IntVar(&opts.Limit, "limit", opts.Limit, "maximum results")
	fs.BoolVar(&opts.IncludeEvidence, "include-evidence", false, "include long evidence")
	addRepeatedStringFlag(fs, "status", &opts.Statuses)
	addRepeatedStringFlag(fs, "tag", &opts.Tags)
	addRepeatedStringFlag(fs, "file", &opts.Files)
	if err := fs.Parse(args); err != nil {
		return learnRecallOpts{}, err
	}
	if fs.NArg() > 1 {
		return learnRecallOpts{}, fmt.Errorf("usage: az learn recall [--query <text>] [--issue <id>] [--req <id>] [--status <status> ...] [--tag <tag> ...] [--file <path> ...] [--limit N] [--include-evidence] [--json]")
	}
	if fs.NArg() == 1 && strings.TrimSpace(opts.Query) == "" {
		opts.Query = strings.TrimSpace(fs.Arg(0))
	}
	if opts.Limit < 0 {
		return learnRecallOpts{}, fmt.Errorf("limit must be non-negative")
	}
	return opts, nil
}

func parseLearnShowArgs(args []string) (learnShowOpts, error) {
	opts := learnShowOpts{}
	fs := flag.NewFlagSet("learn show", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ID, "id", "", "learning id")
	if err := fs.Parse(args); err != nil {
		return learnShowOpts{}, err
	}
	if fs.NArg() == 1 && strings.TrimSpace(opts.ID) == "" {
		opts.ID = strings.TrimSpace(fs.Arg(0))
	} else if fs.NArg() != 0 {
		return learnShowOpts{}, fmt.Errorf("usage: az learn show <learning-id> [--json]")
	}
	if strings.TrimSpace(opts.ID) == "" {
		return learnShowOpts{}, fmt.Errorf("missing learning id")
	}
	return opts, nil
}

func parseLearnReviewArgs(args []string) (learnReviewOpts, error) {
	opts := learnReviewOpts{Limit: 20}
	fs := flag.NewFlagSet("learn review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ID, "id", "", "learning id to update")
	fs.StringVar(&opts.Status, "status", "", "new status")
	fs.StringVar(&opts.Note, "note", "", "review note")
	fs.IntVar(&opts.Limit, "limit", opts.Limit, "maximum candidate rows")
	if err := fs.Parse(args); err != nil {
		return learnReviewOpts{}, err
	}
	if fs.NArg() != 0 {
		return learnReviewOpts{}, fmt.Errorf("usage: az learn review [--id <learning-id> --status accepted|rejected|stale --note <text>] [--limit N] [--json]")
	}
	opts.ID = strings.TrimSpace(opts.ID)
	opts.Status = strings.TrimSpace(opts.Status)
	opts.Note = strings.TrimSpace(opts.Note)
	if opts.ID == "" && (opts.Status != "" || opts.Note != "") {
		return learnReviewOpts{}, fmt.Errorf("--id is required with --status or --note")
	}
	if opts.ID != "" && opts.Status == "" {
		return learnReviewOpts{}, fmt.Errorf("--status is required with --id")
	}
	if opts.ID != "" && !isLearnReviewStatus(opts.Status) {
		return learnReviewOpts{}, fmt.Errorf("invalid review status: expected accepted|rejected|stale")
	}
	if opts.ID != "" && opts.Note == "" {
		return learnReviewOpts{}, fmt.Errorf("--note is required with review status")
	}
	return opts, nil
}

func isLearnReviewStatus(status string) bool {
	switch status {
	case string(protocol.LearningStatusAccepted), string(protocol.LearningStatusRejected), string(protocol.LearningStatusStale):
		return true
	default:
		return false
	}
}

func parseLearnPromoteArgs(args []string) (learnPromoteOpts, error) {
	opts := learnPromoteOpts{}
	fs := flag.NewFlagSet("learn promote", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Target, "target", "", "promotion target")
	fs.StringVar(&opts.TargetID, "target-id", "", "existing decision/spec/guidance target id")
	fs.StringVar(&opts.Note, "note", "", "promotion note")
	fs.StringVar(&opts.TargetHash, "target-hash", "", "current target content hash")
	addRepeatedKeyValueFlag(fs, "target-meta", &opts.TargetMetadata)
	if err := fs.Parse(args); err != nil {
		return learnPromoteOpts{}, err
	}
	if fs.NArg() != 1 {
		return learnPromoteOpts{}, fmt.Errorf("usage: az learn promote --target rulesync|agents|skill|spec|decision --target-id <id-or-path> <learning-id> [--note <text>] [--target-hash <hash>] [--target-meta key=value ...] [--json]")
	}
	opts.ID = strings.TrimSpace(fs.Arg(0))
	if strings.TrimSpace(opts.Target) == "" {
		return learnPromoteOpts{}, fmt.Errorf("missing required flag: --target")
	}
	if strings.TrimSpace(opts.TargetID) == "" {
		return learnPromoteOpts{}, fmt.Errorf("missing required flag: --target-id")
	}
	return opts, nil
}

func parseLearnRelateArgs(args []string) (learnRelateOpts, error) {
	opts := learnRelateOpts{}
	fs := flag.NewFlagSet("learn relate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Type, "type", "", "relation type")
	fs.StringVar(&opts.Note, "note", "", "audit note")
	fs.StringVar(&opts.ScopeIssue, "scope-issue", "", "issue scope")
	fs.StringVar(&opts.ScopeReq, "scope-req", "", "requirement scope")
	fs.StringVar(&opts.ScopeSession, "scope-session", "", "session scope")
	addRepeatedStringFlag(fs, "scope-tag", &opts.ScopeTags)
	addRepeatedStringFlag(fs, "scope-file", &opts.ScopeFiles)
	if err := fs.Parse(args); err != nil {
		return learnRelateOpts{}, err
	}
	if fs.NArg() != 2 {
		return learnRelateOpts{}, fmt.Errorf("usage: az learn relate --type supersedes|conflicts --note <text> [--scope-issue <id>] [--scope-req <id>] [--scope-session <id>] [--scope-tag <tag> ...] [--scope-file <path> ...] <source-learning-id> <target-learning-id> [--json]")
	}
	opts.SourceLearningID = strings.TrimSpace(fs.Arg(0))
	opts.TargetLearningID = strings.TrimSpace(fs.Arg(1))
	opts.Type = strings.TrimSpace(opts.Type)
	opts.Note = strings.TrimSpace(opts.Note)
	if opts.Type == "" {
		return learnRelateOpts{}, fmt.Errorf("missing required flag: --type")
	}
	if opts.Type != string(protocol.LearningRelationSupersedes) && opts.Type != string(protocol.LearningRelationConflicts) {
		return learnRelateOpts{}, fmt.Errorf("invalid relation type: expected supersedes|conflicts")
	}
	if opts.Note == "" {
		return learnRelateOpts{}, fmt.Errorf("missing required flag: --note")
	}
	return opts, nil
}

func addRepeatedStringFlag(fs *flag.FlagSet, name string, values *[]string) {
	fs.Func(name, "repeatable "+name, func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty %s", name)
		}
		*values = appendUniqueOrdered(*values, trimmed)
		return nil
	})
}

func addRepeatedKeyValueFlag(fs *flag.FlagSet, name string, values *map[string]string) {
	fs.Func(name, "repeatable "+name+" key=value", func(v string) error {
		key, value, ok := strings.Cut(v, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fmt.Errorf("%s must be key=value", name)
		}
		if *values == nil {
			*values = map[string]string{}
		}
		(*values)[key] = strings.TrimSpace(value)
		return nil
	})
}

func printLearnUsage() {
	fmt.Println("Usage: az learn <add|recall|show|review|promote|relate> [arguments]")
	fmt.Println("  add      Capture an evidence-backed candidate learning")
	fmt.Println("  recall   Search accepted/promoted learning summaries")
	fmt.Println("  show     Show a learning with full evidence")
	fmt.Println("  review   List candidates or update learning status")
	fmt.Println("  promote  Mark a learning promoted toward curated guidance")
	fmt.Println("  relate   Record supersession or conflict between learnings")
	fmt.Println("")
	fmt.Println("Commands:")
	fmt.Println("  az learn add --evidence <text> [--summary <text>] [--issue <id>] [--req <id>] [--tag <tag> ...] [--file <path> ...] [--json]")
	fmt.Println("  az learn recall [--query <text>] [--issue <id>] [--req <id>] [--status <status> ...] [--tag <tag> ...] [--file <path> ...] [--limit N] [--include-evidence] [--json]")
	fmt.Println("  az learn show <learning-id> [--json]")
	fmt.Println("  az learn review [--id <learning-id> --status accepted|rejected|stale --note <text>] [--limit N] [--json]")
	fmt.Println("  az learn promote --target rulesync|agents|skill|spec|decision --target-id <id-or-path> <learning-id> [--note <text>] [--target-hash <hash>] [--target-meta key=value ...] [--json]")
	fmt.Println("  az learn relate --type supersedes|conflicts --note <text> [--scope-issue <id>] [--scope-req <id>] [--scope-session <id>] [--scope-tag <tag> ...] [--scope-file <path> ...] <source-learning-id> <target-learning-id> [--json]")
}
