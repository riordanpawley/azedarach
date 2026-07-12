package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type learnAddOpts struct {
	JSON     bool
	Issue    string
	Req      string
	Session  string
	Summary  string
	Evidence string
	Private  bool
	Tags     []string
	Files    []string
}
type learnCaptureOpts struct {
	JSON                                                                                       bool
	Issue, Req, Session, Observed, Preferred, Outcome, Impact, Source, Actor, Ref, Sensitivity string
	Context                                                                                    map[string]string
	Tags, Files                                                                                []string
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
	IncludePrivate  bool
}
type learnActivateOpts struct {
	JSON                             bool
	Surface, Issue, Req, Explanation string
	Tags, Files, LearningIDs         []string
	TokenCost                        int
}
type learnFeedbackOpts struct {
	JSON                                                       bool
	ActivationID, IdempotencyKey, Outcome, Source, Explanation string
}

type learnShowOpts struct {
	JSON bool
	ID   string
}

type learnReviewOpts struct {
	JSON          bool
	IDs           []string
	Status        string
	Note          string
	Limit         int
	QueueStatuses []string
	Issue         string
	Req           string
	Tags          []string
	Files         []string
	TargetStates  []string
	OlderThan     time.Duration
	BulkStale     bool
}

type learnStaleOpts struct {
	JSON bool
	ID   string
	Note string
}

type learnDemoteOpts struct {
	JSON bool
	ID   string
	Note string
}

type learnPromoteOpts struct {
	JSON                 bool
	ID                   string
	Target               string
	TargetID             string
	Note                 string
	TargetHash           string
	TargetMetadata       map[string]string
	CreateTarget         bool
	TargetTitle          string
	TargetDescription    string
	TargetIssue          string
	DecisionRationale    string
	DecisionContext      string
	DecisionConsequences string
}

type learnRetireOpts struct {
	JSON bool
	ID   string
	Note string
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

type learnSupersedeOpts struct {
	JSON          bool
	NewLearningID string
	OldLearningID string
	Note          string
	ScopeIssue    string
	ScopeReq      string
	ScopeSession  string
	ScopeTags     []string
	ScopeFiles    []string
}

type learnDoctorOpts struct {
	JSON                   bool
	ProjectID              string
	CandidateOlderThanDays int
	InactiveOlderThanDays  int
	Limit                  int
}

type learnGCOpts struct {
	JSON                   bool
	ProjectID              string
	CandidateOlderThanDays int
	InactiveOlderThanDays  int
	Limit                  int
	Confirm                bool
}

type learnSuggestOpts struct {
	JSON, Refresh bool
	Status        string
	Limit         int
}
type learnConsolidateOpts struct {
	JSON                                     bool
	SuggestionID, CanonicalID, Summary, Note string
}
type learnSuggestionRejectOpts struct {
	JSON               bool
	SuggestionID, Note string
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
	case "health":
		return runLearnHealthRPC(cfg, args[1:])
	case "capture":
		opts, err := parseLearnCaptureArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnCaptureRPC(cfg, opts)
	case "activate":
		opts, err := parseLearnActivateArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnActivateRPC(cfg, opts)
	case "feedback":
		opts, err := parseLearnFeedbackArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnFeedbackRPC(cfg, opts)
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
	case "stale":
		opts, err := parseLearnStaleArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnStaleRPC(cfg, opts)
	case "demote":
		opts, err := parseLearnDemoteArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnDemoteRPC(cfg, opts)
	case "promote":
		opts, err := parseLearnPromoteArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnPromoteRPC(cfg, opts)
	case "retire":
		opts, err := parseLearnRetireArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnRetireRPC(cfg, opts)
	case "relate":
		opts, err := parseLearnRelateArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnRelateRPC(cfg, opts)
	case "supersede":
		opts, err := parseLearnSupersedeArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnSupersedeRPC(cfg, opts)
	case "doctor":
		opts, err := parseLearnDoctorArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnDoctorRPC(cfg, opts)
	case "gc":
		opts, err := parseLearnGCArgs(args[1:])
		if err != nil {
			printLearnUsage()
			return err
		}
		return runLearnGCRPC(cfg, opts)
	case "suggest":
		opts, err := parseLearnSuggestArgs(args[1:])
		if err != nil {
			return err
		}
		return runLearnSuggestRPC(cfg, opts)
	case "consolidate":
		opts, err := parseLearnConsolidateArgs(args[1:])
		if err != nil {
			return err
		}
		return runLearnConsolidateRPC(cfg, opts)
	case "suggestion-reject":
		opts, err := parseLearnSuggestionRejectArgs(args[1:])
		if err != nil {
			return err
		}
		return runLearnSuggestionRejectRPC(cfg, opts)
	default:
		return fmt.Errorf("unknown learn command: %s", args[0])
	}
}

func runLearnHealthRPC(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("learn health", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("learn health takes no positional arguments")
	}
	var out protocol.LearnHealthResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnHealth, protocol.LearnHealthRequestBody{}, &out); err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(out)
	}
	h := out.Health
	fmt.Printf("Learning portfolio health (%s)\nCandidates: %d (average %.1fh, maximum %.1fh)\nDuplicate density: %d/%d (%.2f%%)\nUsefulness: %d/%d (%.2f%%); contradictions: %d/%d (%.2f%%)\nPromotion throughput: %d/%d (%.2f%%)\nContextual coverage: %d/%d (%.2f%%)\nSelections: %d; deliveries: %d; delivered tokens: %d; tokens/useful activation: %d/%d (%.2f)\n", h.ProjectID, h.CandidateCount, h.CandidateAgeAverageHours, h.CandidateAgeMaximumHours, h.DuplicateDensity.Numerator, h.DuplicateDensity.Denominator, h.DuplicateDensity.Value*100, h.UsefulnessRate.Numerator, h.UsefulnessRate.Denominator, h.UsefulnessRate.Value*100, h.ContradictionRate.Numerator, h.ContradictionRate.Denominator, h.ContradictionRate.Value*100, h.PromotionThroughput.Numerator, h.PromotionThroughput.Denominator, h.PromotionThroughput.Value*100, h.ContextualCoverage.Numerator, h.ContextualCoverage.Denominator, h.ContextualCoverage.Value*100, h.SelectionCount, h.DeliveryCount, h.DeliveredTokenCost, h.TokensPerUsefulActivation.Numerator, h.TokensPerUsefulActivation.Denominator, h.TokensPerUsefulActivation.Value)
	return nil
}

func runLearnSuggestRPC(cfg *config.Config, opts learnSuggestOpts) error {
	var out protocol.LearnSuggestResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnSuggest, protocol.LearnSuggestRequestBody{Refresh: opts.Refresh, Status: opts.Status, Limit: opts.Limit}, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	if len(out.Suggestions) == 0 {
		fmt.Println("No learning consolidation suggestions.")
		return nil
	}
	for _, s := range out.Suggestions {
		fmt.Printf("%s [%s/%s] %s + %s score=%d: %s\n", s.ID, s.Kind, s.Status, s.LeftLearningID, s.RightLearningID, s.Score, s.Reason)
	}
	return nil
}
func runLearnConsolidateRPC(cfg *config.Config, opts learnConsolidateOpts) error {
	var out protocol.LearnConsolidateResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnConsolidate, protocol.LearnConsolidateRequestBody{SuggestionID: opts.SuggestionID, CanonicalLearningID: opts.CanonicalID, Summary: opts.Summary, Note: opts.Note}, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Consolidated %s into %s\n", out.Suggestion.ID, out.Learning.ID)
	return nil
}
func runLearnSuggestionRejectRPC(cfg *config.Config, opts learnSuggestionRejectOpts) error {
	var out protocol.LearnSuggestionRejectResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnSuggestionReject, protocol.LearnSuggestionRejectRequestBody{SuggestionID: opts.SuggestionID, Note: opts.Note}, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Rejected learning suggestion: %s\n", out.Suggestion.ID)
	return nil
}
func parseLearnSuggestArgs(args []string) (learnSuggestOpts, error) {
	opts := learnSuggestOpts{Status: "pending", Limit: 100}
	fs := flag.NewFlagSet("learn suggest", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json")
	fs.BoolVar(&opts.Refresh, "refresh", false, "refresh suggestions")
	fs.StringVar(&opts.Status, "status", opts.Status, "status")
	fs.IntVar(&opts.Limit, "limit", opts.Limit, "limit")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("usage: az learn suggest [--refresh] [--status pending|rejected|confirmed] [--limit N] [--json]")
	}
	if opts.Limit < 0 {
		return opts, errors.New("limit must be non-negative")
	}
	return opts, nil
}
func parseLearnConsolidateArgs(args []string) (learnConsolidateOpts, error) {
	var opts learnConsolidateOpts
	fs := flag.NewFlagSet("learn consolidate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json")
	fs.StringVar(&opts.CanonicalID, "canonical", "", "canonical learning id")
	fs.StringVar(&opts.Summary, "summary", "", "merged summary")
	fs.StringVar(&opts.Note, "note", "", "audit note")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 1 || strings.TrimSpace(opts.CanonicalID) == "" || strings.TrimSpace(opts.Note) == "" {
		return opts, fmt.Errorf("usage: az learn consolidate --canonical <learning-id> --note <text> [--summary <text>] <suggestion-id> [--json]")
	}
	opts.SuggestionID = strings.TrimSpace(fs.Arg(0))
	return opts, nil
}
func parseLearnSuggestionRejectArgs(args []string) (learnSuggestionRejectOpts, error) {
	var opts learnSuggestionRejectOpts
	fs := flag.NewFlagSet("learn suggestion-reject", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json")
	fs.StringVar(&opts.Note, "note", "", "audit note")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 1 || strings.TrimSpace(opts.Note) == "" {
		return opts, fmt.Errorf("usage: az learn suggestion-reject --note <text> <suggestion-id> [--json]")
	}
	opts.SuggestionID = strings.TrimSpace(fs.Arg(0))
	return opts, nil
}
func runLearnActivateRPC(cfg *config.Config, o learnActivateOpts) error {
	var out protocol.LearnActivateResponseBody
	err := runLearnRPC(cfg, protocol.CommandLearnActivate, protocol.LearnActivateRequestBody{Surface: o.Surface, ContextIssueID: naming.IssueID(o.Issue), ContextReqID: naming.RequirementID(o.Req), ContextTags: o.Tags, ContextFiles: o.Files, LearningIDs: o.LearningIDs, TokenCost: o.TokenCost, Explanation: o.Explanation}, &out)
	if err != nil {
		return err
	}
	if o.JSON {
		return printJSON(out)
	}
	fmt.Printf("Delivered learning activation: %s\n", out.Activation.ActivationID)
	return nil
}
func runLearnFeedbackRPC(cfg *config.Config, o learnFeedbackOpts) error {
	var out protocol.LearnFeedbackResponseBody
	err := runLearnRPC(cfg, protocol.CommandLearnFeedback, protocol.LearnFeedbackRequestBody{ActivationID: o.ActivationID, IdempotencyKey: o.IdempotencyKey, Outcome: o.Outcome, Source: o.Source, Explanation: o.Explanation}, &out)
	if err != nil {
		return err
	}
	if o.JSON {
		return printJSON(out)
	}
	fmt.Printf("Recorded activation feedback: %s created=%t\n", out.Feedback.Outcome, out.Created)
	return nil
}
func parseLearnActivateArgs(args []string) (learnActivateOpts, error) {
	var o learnActivateOpts
	fs := flag.NewFlagSet("learn activate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.JSON, "json", false, "json output")
	fs.StringVar(&o.Surface, "surface", "", "delivery surface")
	fs.StringVar(&o.Issue, "issue", "", "context issue")
	fs.StringVar(&o.Req, "req", "", "context requirement")
	fs.StringVar(&o.Explanation, "explanation", "", "ranking explanation")
	fs.IntVar(&o.TokenCost, "token-cost", 0, "delivered token cost")
	addRepeatedStringFlag(fs, "learning", &o.LearningIDs)
	addRepeatedStringFlag(fs, "tag", &o.Tags)
	addRepeatedStringFlag(fs, "file", &o.Files)
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.LearningIDs = append(o.LearningIDs, fs.Args()...)
	o.LearningIDs = compactCLIStrings(o.LearningIDs)
	o.Surface = strings.TrimSpace(o.Surface)
	if o.Surface == "" || len(o.LearningIDs) == 0 {
		return o, fmt.Errorf("--surface and at least one learning id are required")
	}
	if o.TokenCost < 0 || o.TokenCost > 32768 {
		return o, fmt.Errorf("--token-cost must be between 0 and 32768")
	}
	return o, nil
}
func parseLearnFeedbackArgs(args []string) (learnFeedbackOpts, error) {
	var o learnFeedbackOpts
	fs := flag.NewFlagSet("learn feedback", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.JSON, "json", false, "json output")
	fs.StringVar(&o.IdempotencyKey, "idempotency-key", "", "deduplication key")
	fs.StringVar(&o.Outcome, "outcome", "", "helpful|followed|contradicted|unknown")
	fs.StringVar(&o.Source, "source", "human", "human|agent|inferred")
	fs.StringVar(&o.Explanation, "explanation", "", "outcome explanation")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 1 {
		return o, fmt.Errorf("activation id is required")
	}
	o.ActivationID = strings.TrimSpace(fs.Arg(0))
	if o.IdempotencyKey == "" || !domain.LearningActivationOutcome(o.Outcome).Valid() || !domain.LearningOutcomeSource(o.Source).Valid() {
		return o, fmt.Errorf("valid --idempotency-key, --outcome, and --source are required")
	}
	return o, nil
}

func runLearnAddRPC(cfg *config.Config, opts learnAddOpts) error {
	req := protocol.LearnAddRequestBody{
		IssueID:   naming.IssueID(opts.Issue),
		ReqID:     naming.RequirementID(opts.Req),
		SessionID: naming.SessionID(opts.Session),
		Summary:   opts.Summary,
		Evidence:  opts.Evidence,
		Private:   opts.Private,
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

func runLearnCaptureRPC(cfg *config.Config, o learnCaptureOpts) error {
	req := protocol.LearnCaptureRequestBody{IssueID: naming.IssueID(o.Issue), ReqID: naming.RequirementID(o.Req), SessionID: naming.SessionID(o.Session), ObservedBehavior: o.Observed, PreferredBehavior: o.Preferred, Outcome: o.Outcome, Impact: o.Impact, Context: o.Context, Provenance: protocol.LearningObservationProvenance{Source: o.Source, Actor: o.Actor, Ref: o.Ref}, Sensitivity: o.Sensitivity, Tags: o.Tags, Files: o.Files}
	var out protocol.LearnCaptureResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnCapture, req, &out); err != nil {
		return err
	}
	if o.JSON {
		return printJSON(out)
	}
	fmt.Printf("Recorded learning observation: %s (%s)\n", out.Observation.ID, out.Observation.Learning.ID)
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
		IncludePrivate:  opts.IncludePrivate,
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
	queueStatuses := make([]protocol.LearningStatus, 0, len(opts.QueueStatuses))
	for _, status := range opts.QueueStatuses {
		queueStatuses = append(queueStatuses, protocol.LearningStatus(status))
	}
	targetStates := make([]protocol.LearningTargetState, 0, len(opts.TargetStates))
	for _, state := range opts.TargetStates {
		targetStates = append(targetStates, protocol.LearningTargetState(state))
	}
	req := protocol.LearnReviewRequestBody{
		IDs:              opts.IDs,
		Status:           protocol.LearningStatus(opts.Status),
		Note:             opts.Note,
		Limit:            opts.Limit,
		QueueStatuses:    queueStatuses,
		IssueID:          naming.IssueID(opts.Issue),
		ReqID:            naming.RequirementID(opts.Req),
		Tags:             opts.Tags,
		Files:            opts.Files,
		TargetStates:     targetStates,
		OlderThanSeconds: int64(opts.OlderThan / time.Second),
		BulkStale:        opts.BulkStale,
	}
	var out protocol.LearnReviewResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnReview, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	if len(out.UpdatedLearnings) > 0 {
		fmt.Printf("Updated %d learning(s):\n", len(out.UpdatedLearnings))
		printReviewLearnings(out.UpdatedLearnings)
		return nil
	}
	if out.Updated != nil {
		fmt.Printf("Updated learning: %s [%s]\n", out.Updated.ID, out.Updated.Status)
		return nil
	}
	printReviewLearnings(out.Learnings)
	return nil
}

func runLearnStaleRPC(cfg *config.Config, opts learnStaleOpts) error {
	req := protocol.LearnStaleRequestBody{ID: opts.ID, Note: opts.Note}
	var out protocol.LearnStaleResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnStale, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Marked learning stale: %s [%s]\n", out.Learning.ID, out.Learning.Status)
	return nil
}

func runLearnDemoteRPC(cfg *config.Config, opts learnDemoteOpts) error {
	req := protocol.LearnDemoteRequestBody{ID: opts.ID, Note: opts.Note}
	var out protocol.LearnDemoteResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnDemote, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Demoted learning: %s [%s]\n", out.Learning.ID, out.Learning.Status)
	return nil
}

func runLearnPromoteRPC(cfg *config.Config, opts learnPromoteOpts) error {
	req := protocol.LearnPromoteRequestBody{
		ID:                   opts.ID,
		Target:               protocol.LearningPromotionTarget(opts.Target),
		TargetID:             opts.TargetID,
		Note:                 opts.Note,
		TargetHash:           opts.TargetHash,
		TargetMetadata:       opts.TargetMetadata,
		CreateTarget:         opts.CreateTarget,
		TargetTitle:          opts.TargetTitle,
		TargetDescription:    opts.TargetDescription,
		TargetIssueID:        naming.IssueID(opts.TargetIssue),
		DecisionRationale:    opts.DecisionRationale,
		DecisionContext:      opts.DecisionContext,
		DecisionConsequences: opts.DecisionConsequences,
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

func runLearnRetireRPC(cfg *config.Config, opts learnRetireOpts) error {
	var out protocol.LearnRetireResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnRetire, protocol.LearnRetireRequestBody{ID: opts.ID, Note: opts.Note}, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	fmt.Printf("Retired promoted learning: %s\n%s\n", out.Learning.ID, out.Guidance)
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

func runLearnSupersedeRPC(cfg *config.Config, opts learnSupersedeOpts) error {
	req := protocol.LearnSupersedeRequestBody{
		NewLearningID:  opts.NewLearningID,
		OldLearningID:  opts.OldLearningID,
		Note:           opts.Note,
		ScopeIssueID:   naming.IssueID(opts.ScopeIssue),
		ScopeReqID:     naming.RequirementID(opts.ScopeReq),
		ScopeSessionID: naming.SessionID(opts.ScopeSession),
		ScopeTags:      opts.ScopeTags,
		ScopeFiles:     opts.ScopeFiles,
	}
	var out protocol.LearnSupersedeResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnSupersede, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	printLearningRelation(out.Relation)
	return nil
}

func runLearnDoctorRPC(cfg *config.Config, opts learnDoctorOpts) error {
	req := protocol.LearnDoctorRequestBody{
		ProjectID:              opts.ProjectID,
		CandidateOlderThanDays: opts.CandidateOlderThanDays,
		InactiveOlderThanDays:  opts.InactiveOlderThanDays,
		Limit:                  opts.Limit,
	}
	var out protocol.LearnDoctorResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnDoctor, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	printLearningMaintenanceFindings(out.Findings, "No learning maintenance problems found.")
	return nil
}

func runLearnGCRPC(cfg *config.Config, opts learnGCOpts) error {
	req := protocol.LearnGCRequestBody{
		ProjectID:              opts.ProjectID,
		CandidateOlderThanDays: opts.CandidateOlderThanDays,
		InactiveOlderThanDays:  opts.InactiveOlderThanDays,
		Limit:                  opts.Limit,
		Confirm:                opts.Confirm,
	}
	var out protocol.LearnGCResponseBody
	if err := runLearnRPC(cfg, protocol.CommandLearnGC, req, &out); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out)
	}
	if out.DryRun {
		fmt.Println("Dry run: no learning rows were deleted. Re-run with --confirm to apply cleanup.")
	}
	printLearningMaintenanceFindings(out.Deleted, "No learning rows eligible for cleanup.")
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
		fmt.Printf("%s [%s]%s %s\n", learning.ID, learningStatusLabel(learning), scope, learning.Summary)
		if len(learning.Tags) > 0 {
			fmt.Printf("  tags: %s\n", strings.Join(learning.Tags, ", "))
		}
		if reason := strings.TrimSpace(learning.RecallReason); reason != "" {
			fmt.Printf("  reason: %s\n", reason)
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

func printReviewLearnings(learnings []protocol.Learning) {
	if len(learnings) == 0 {
		fmt.Println("No learnings found.")
		return
	}
	for _, learning := range learnings {
		printLearnings([]protocol.Learning{learning}, false)
		if len(learning.Files) > 0 {
			fmt.Printf("  files: %s\n", strings.Join(learning.Files, ", "))
		}
		if learning.Target != "" || learning.TargetID != "" || learning.TargetState != "" {
			fmt.Printf("  target: %s:%s", learning.Target, learning.TargetID)
			if learning.TargetState != "" {
				fmt.Printf(" state=%s", learning.TargetState)
			}
			fmt.Println()
		}
		if learning.ReviewNote != "" {
			fmt.Printf("  review: %s\n", learning.ReviewNote)
		}
		if learning.UpdatedAt != "" {
			fmt.Printf("  updated: %s\n", learning.UpdatedAt)
		}
	}
}

func printLearningMaintenanceFindings(findings []protocol.LearnMaintenanceFinding, empty string) {
	if len(findings) == 0 {
		fmt.Println(empty)
		return
	}
	for _, finding := range findings {
		learningID := strings.TrimSpace(finding.LearningID)
		if learningID == "" {
			learningID = "project"
		}
		fmt.Printf("%s [%s] %s: %s\n", learningID, finding.Severity, finding.Type, finding.Message)
		if summary := strings.TrimSpace(finding.Learning.Summary); summary != "" {
			fmt.Printf("  summary: %s\n", summary)
		}
		if action := strings.TrimSpace(finding.Action); action != "" {
			fmt.Printf("  action: %s\n", action)
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

func learningStatusLabel(learning protocol.Learning) string {
	if learning.EvidencePrivate {
		return fmt.Sprintf("%s, private", learning.Status)
	}
	return string(learning.Status)
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
	fs.BoolVar(&opts.Private, "private", false, "exclude from prime/default recall output")
	addRepeatedStringFlag(fs, "tag", &opts.Tags)
	addRepeatedStringFlag(fs, "file", &opts.Files)
	if err := fs.Parse(args); err != nil {
		return learnAddOpts{}, err
	}
	if fs.NArg() != 0 {
		return learnAddOpts{}, fmt.Errorf("usage: az learn add --evidence <text> [--summary <text>] [--private] [--issue <id>] [--req <id>] [--tag <tag> ...] [--file <path> ...] [--json]")
	}
	if strings.TrimSpace(opts.Evidence) == "" {
		return learnAddOpts{}, fmt.Errorf("missing required flag: --evidence")
	}
	return opts, nil
}

func parseLearnCaptureArgs(args []string) (learnCaptureOpts, error) {
	o := learnCaptureOpts{Issue: strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID")), Sensitivity: "public", Context: map[string]string{}}
	fs := flag.NewFlagSet("learn capture", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.JSON, "json", false, "json output")
	fs.StringVar(&o.Issue, "issue", o.Issue, "issue id")
	fs.StringVar(&o.Req, "req", "", "requirement id")
	fs.StringVar(&o.Session, "session", "", "session id")
	fs.StringVar(&o.Observed, "observed", "", "observed behavior")
	fs.StringVar(&o.Preferred, "preferred", "", "preferred behavior")
	fs.StringVar(&o.Outcome, "outcome", "", "outcome")
	fs.StringVar(&o.Impact, "impact", "", "impact")
	fs.StringVar(&o.Source, "source", "", "provenance source")
	fs.StringVar(&o.Actor, "actor", "", "provenance actor")
	fs.StringVar(&o.Ref, "ref", "", "provenance reference")
	fs.StringVar(&o.Sensitivity, "sensitivity", o.Sensitivity, "public or private")
	addRepeatedKeyValueFlag(fs, "context", &o.Context)
	addRepeatedStringFlag(fs, "tag", &o.Tags)
	addRepeatedStringFlag(fs, "file", &o.Files)
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 || strings.TrimSpace(o.Observed) == "" || strings.TrimSpace(o.Preferred) == "" || strings.TrimSpace(o.Source) == "" || !domain.LearningSensitivity(o.Sensitivity).Valid() {
		return o, fmt.Errorf("usage: az learn capture --observed <text> --preferred <text> --source <source> [--outcome <text>] [--impact <text>] [--context key=value ...] [--sensitivity public|private] [--json]")
	}
	return o, nil
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
	fs.BoolVar(&opts.IncludePrivate, "include-private", false, "include private evidence rows")
	addRepeatedStringFlag(fs, "status", &opts.Statuses)
	addRepeatedStringFlag(fs, "tag", &opts.Tags)
	addRepeatedStringFlag(fs, "file", &opts.Files)
	if err := fs.Parse(args); err != nil {
		return learnRecallOpts{}, err
	}
	if fs.NArg() > 1 {
		return learnRecallOpts{}, fmt.Errorf("usage: az learn recall [--query <text>] [--issue <id>] [--req <id>] [--status <status> ...] [--tag <tag> ...] [--file <path> ...] [--limit N] [--include-evidence] [--include-private] [--json]")
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
	fs.StringVar(&opts.Status, "status", "", "new status")
	fs.StringVar(&opts.Note, "note", "", "review note")
	fs.IntVar(&opts.Limit, "limit", opts.Limit, "maximum candidate rows")
	fs.StringVar(&opts.Issue, "issue", "", "filter review queue by issue id")
	fs.StringVar(&opts.Req, "req", "", "filter review queue by requirement id")
	fs.BoolVar(&opts.BulkStale, "bulk-stale", false, "mark old matching candidate rows stale")
	addRepeatedStringFlag(fs, "id", &opts.IDs)
	addRepeatedStringFlag(fs, "queue-status", &opts.QueueStatuses)
	addRepeatedStringFlag(fs, "tag", &opts.Tags)
	addRepeatedStringFlag(fs, "file", &opts.Files)
	addRepeatedStringFlag(fs, "target-state", &opts.TargetStates)
	var olderThanRaw string
	fs.StringVar(&olderThanRaw, "older-than", "", "filter rows updated before a duration such as 30d or 72h")
	if err := fs.Parse(args); err != nil {
		return learnReviewOpts{}, err
	}
	if fs.NArg() > 0 {
		opts.IDs = append(opts.IDs, fs.Args()...)
	}
	opts.IDs = compactCLIStrings(opts.IDs)
	opts.Status = strings.TrimSpace(opts.Status)
	opts.Note = strings.TrimSpace(opts.Note)
	if olderThanRaw != "" {
		parsed, err := parseLearningAgeDuration(olderThanRaw)
		if err != nil {
			return learnReviewOpts{}, err
		}
		opts.OlderThan = parsed
	}
	if opts.Limit < 0 {
		return learnReviewOpts{}, fmt.Errorf("limit must be non-negative")
	}
	if len(opts.IDs) == 0 && (opts.Status != "" || opts.Note != "") && !opts.BulkStale {
		return learnReviewOpts{}, fmt.Errorf("--id is required with --status or --note")
	}
	if len(opts.IDs) > 0 && opts.Status == "" {
		return learnReviewOpts{}, fmt.Errorf("--status is required with --id")
	}
	if len(opts.IDs) > 0 && !isLearnReviewStatus(opts.Status) {
		return learnReviewOpts{}, fmt.Errorf("invalid review status: expected accepted|rejected|stale")
	}
	if len(opts.IDs) > 0 && opts.Note == "" {
		return learnReviewOpts{}, fmt.Errorf("--note is required with review status")
	}
	if opts.BulkStale {
		if len(opts.IDs) > 0 {
			return learnReviewOpts{}, fmt.Errorf("--bulk-stale cannot be combined with --id")
		}
		if opts.Status != "" {
			return learnReviewOpts{}, fmt.Errorf("--bulk-stale does not accept --status")
		}
		if opts.Note == "" {
			return learnReviewOpts{}, fmt.Errorf("--note is required with --bulk-stale")
		}
		if opts.OlderThan <= 0 {
			return learnReviewOpts{}, fmt.Errorf("--older-than is required with --bulk-stale")
		}
	}
	for _, status := range opts.QueueStatuses {
		if !protocol.LearningStatus(status).Valid() {
			return learnReviewOpts{}, fmt.Errorf("invalid queue status: expected candidate|accepted|rejected|promoted|stale")
		}
	}
	for _, state := range opts.TargetStates {
		if !protocol.LearningTargetState(state).Valid() {
			return learnReviewOpts{}, fmt.Errorf("invalid target state: expected active|retired|drifted|missing")
		}
	}
	return opts, nil
}

func parseLearnStaleArgs(args []string) (learnStaleOpts, error) {
	opts := learnStaleOpts{}
	fs := flag.NewFlagSet("learn stale", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Note, "note", "", "audit note")
	if err := fs.Parse(args); err != nil {
		return learnStaleOpts{}, err
	}
	if fs.NArg() != 1 {
		return learnStaleOpts{}, fmt.Errorf("usage: az learn stale --note <text> <learning-id> [--json]")
	}
	opts.ID = strings.TrimSpace(fs.Arg(0))
	opts.Note = strings.TrimSpace(opts.Note)
	if opts.ID == "" {
		return learnStaleOpts{}, fmt.Errorf("missing learning id")
	}
	if opts.Note == "" {
		return learnStaleOpts{}, fmt.Errorf("missing required flag: --note")
	}
	return opts, nil
}

func parseLearnDemoteArgs(args []string) (learnDemoteOpts, error) {
	opts := learnDemoteOpts{}
	fs := flag.NewFlagSet("learn demote", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Note, "note", "", "audit note")
	if err := fs.Parse(args); err != nil {
		return learnDemoteOpts{}, err
	}
	if fs.NArg() != 1 {
		return learnDemoteOpts{}, fmt.Errorf("usage: az learn demote --note <text> <learning-id> [--json]")
	}
	opts.ID = strings.TrimSpace(fs.Arg(0))
	opts.Note = strings.TrimSpace(opts.Note)
	if opts.ID == "" {
		return learnDemoteOpts{}, fmt.Errorf("missing learning id")
	}
	if opts.Note == "" {
		return learnDemoteOpts{}, fmt.Errorf("missing required flag: --note")
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
	fs.BoolVar(&opts.CreateTarget, "create-target", false, "create a missing structured decision/spec target")
	fs.StringVar(&opts.TargetTitle, "target-title", "", "structured decision/spec title")
	fs.StringVar(&opts.TargetDescription, "target-description", "", "structured spec requirement description")
	fs.StringVar(&opts.TargetIssue, "target-issue", "", "issue id to link/own a structured spec target")
	fs.StringVar(&opts.DecisionRationale, "decision-rationale", "", "structured decision rationale")
	fs.StringVar(&opts.DecisionContext, "decision-context", "", "structured decision context")
	fs.StringVar(&opts.DecisionConsequences, "decision-consequences", "", "structured decision consequences")
	addRepeatedKeyValueFlag(fs, "target-meta", &opts.TargetMetadata)
	if err := fs.Parse(args); err != nil {
		return learnPromoteOpts{}, err
	}
	if fs.NArg() != 1 {
		return learnPromoteOpts{}, fmt.Errorf("usage: az learn promote --target rulesync|agents|skill|spec|decision [--target-id <id-or-path>] <learning-id> [--create-target] [--target-title <text>] [--target-description <text>] [--target-issue <id>] [--decision-rationale <text>] [--decision-context <text>] [--decision-consequences <text>] [--note <text>] [--target-hash <hash>] [--target-meta key=value ...] [--json]")
	}
	opts.ID = strings.TrimSpace(fs.Arg(0))
	opts.Target = strings.TrimSpace(opts.Target)
	opts.TargetID = strings.TrimSpace(opts.TargetID)
	opts.TargetTitle = strings.TrimSpace(opts.TargetTitle)
	opts.TargetDescription = strings.TrimSpace(opts.TargetDescription)
	opts.TargetIssue = strings.TrimSpace(opts.TargetIssue)
	opts.DecisionRationale = strings.TrimSpace(opts.DecisionRationale)
	opts.DecisionContext = strings.TrimSpace(opts.DecisionContext)
	opts.DecisionConsequences = strings.TrimSpace(opts.DecisionConsequences)
	if opts.Target == "" {
		return learnPromoteOpts{}, fmt.Errorf("missing required flag: --target")
	}
	if opts.TargetID == "" && (!opts.CreateTarget || opts.Target != string(protocol.LearningPromotionTargetDecision)) {
		return learnPromoteOpts{}, fmt.Errorf("missing required flag: --target-id")
	}
	if opts.CreateTarget && opts.Target == string(protocol.LearningPromotionTargetDecision) && opts.TargetID == "" && (opts.TargetTitle == "" || opts.DecisionRationale == "") {
		return learnPromoteOpts{}, fmt.Errorf("--create-target decision requires --target-title and --decision-rationale")
	}
	return opts, nil
}

func parseLearnRetireArgs(args []string) (learnRetireOpts, error) {
	opts := learnRetireOpts{}
	fs := flag.NewFlagSet("learn retire", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Note, "note", "", "retirement note")
	if err := fs.Parse(args); err != nil {
		return learnRetireOpts{}, err
	}
	if fs.NArg() != 1 {
		return learnRetireOpts{}, fmt.Errorf("usage: az learn retire --note <text> <learning-id> [--json]")
	}
	opts.ID = strings.TrimSpace(fs.Arg(0))
	opts.Note = strings.TrimSpace(opts.Note)
	if opts.ID == "" {
		return learnRetireOpts{}, fmt.Errorf("missing learning id")
	}
	if opts.Note == "" {
		return learnRetireOpts{}, fmt.Errorf("missing required flag: --note")
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

func parseLearnSupersedeArgs(args []string) (learnSupersedeOpts, error) {
	opts := learnSupersedeOpts{}
	fs := flag.NewFlagSet("learn supersede", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.Note, "note", "", "audit note")
	fs.StringVar(&opts.ScopeIssue, "scope-issue", "", "issue scope")
	fs.StringVar(&opts.ScopeReq, "scope-req", "", "requirement scope")
	fs.StringVar(&opts.ScopeSession, "scope-session", "", "session scope")
	addRepeatedStringFlag(fs, "scope-tag", &opts.ScopeTags)
	addRepeatedStringFlag(fs, "scope-file", &opts.ScopeFiles)
	if err := fs.Parse(args); err != nil {
		return learnSupersedeOpts{}, err
	}
	if fs.NArg() != 2 {
		return learnSupersedeOpts{}, fmt.Errorf("usage: az learn supersede --note <text> [--scope-issue <id>] [--scope-req <id>] [--scope-session <id>] [--scope-tag <tag> ...] [--scope-file <path> ...] <new-learning-id> <old-learning-id> [--json]")
	}
	opts.NewLearningID = strings.TrimSpace(fs.Arg(0))
	opts.OldLearningID = strings.TrimSpace(fs.Arg(1))
	opts.Note = strings.TrimSpace(opts.Note)
	if opts.NewLearningID == "" || opts.OldLearningID == "" {
		return learnSupersedeOpts{}, fmt.Errorf("missing learning id")
	}
	if opts.Note == "" {
		return learnSupersedeOpts{}, fmt.Errorf("missing required flag: --note")
	}
	return opts, nil
}

func parseLearnDoctorArgs(args []string) (learnDoctorOpts, error) {
	opts := learnDoctorOpts{CandidateOlderThanDays: 30, InactiveOlderThanDays: 30, Limit: 50}
	fs := flag.NewFlagSet("learn doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ProjectID, "project", "", "project id")
	fs.IntVar(&opts.CandidateOlderThanDays, "candidate-older-than-days", opts.CandidateOlderThanDays, "candidate age threshold")
	fs.IntVar(&opts.InactiveOlderThanDays, "inactive-older-than-days", opts.InactiveOlderThanDays, "inactive row age threshold")
	fs.IntVar(&opts.Limit, "limit", opts.Limit, "maximum findings")
	if err := fs.Parse(args); err != nil {
		return learnDoctorOpts{}, err
	}
	if fs.NArg() != 0 {
		return learnDoctorOpts{}, fmt.Errorf("usage: az learn doctor [--candidate-older-than-days N] [--inactive-older-than-days N] [--limit N] [--json]")
	}
	opts.ProjectID = strings.TrimSpace(opts.ProjectID)
	if opts.CandidateOlderThanDays < 0 || opts.InactiveOlderThanDays < 0 {
		return learnDoctorOpts{}, fmt.Errorf("age thresholds must be non-negative")
	}
	if opts.Limit < 0 {
		return learnDoctorOpts{}, fmt.Errorf("limit must be non-negative")
	}
	return opts, nil
}

func parseLearnGCArgs(args []string) (learnGCOpts, error) {
	opts := learnGCOpts{CandidateOlderThanDays: 30, InactiveOlderThanDays: 30, Limit: 50}
	fs := flag.NewFlagSet("learn gc", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ProjectID, "project", "", "project id")
	fs.IntVar(&opts.CandidateOlderThanDays, "candidate-older-than-days", opts.CandidateOlderThanDays, "candidate age threshold")
	fs.IntVar(&opts.InactiveOlderThanDays, "inactive-older-than-days", opts.InactiveOlderThanDays, "inactive row age threshold")
	fs.IntVar(&opts.Limit, "limit", opts.Limit, "maximum rows")
	fs.BoolVar(&opts.Confirm, "confirm", false, "apply cleanup")
	if err := fs.Parse(args); err != nil {
		return learnGCOpts{}, err
	}
	if fs.NArg() != 0 {
		return learnGCOpts{}, fmt.Errorf("usage: az learn gc [--confirm] [--candidate-older-than-days N] [--inactive-older-than-days N] [--limit N] [--json]")
	}
	opts.ProjectID = strings.TrimSpace(opts.ProjectID)
	if opts.CandidateOlderThanDays < 0 || opts.InactiveOlderThanDays < 0 {
		return learnGCOpts{}, fmt.Errorf("age thresholds must be non-negative")
	}
	if opts.Limit < 0 {
		return learnGCOpts{}, fmt.Errorf("limit must be non-negative")
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

func compactCLIStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseLearningAgeDuration(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(value); err == nil {
		if d < 0 {
			return 0, fmt.Errorf("--older-than must be non-negative")
		}
		return d, nil
	}
	multiplier := time.Duration(0)
	number := value
	switch {
	case strings.HasSuffix(value, "d"):
		multiplier = 24 * time.Hour
		number = strings.TrimSuffix(value, "d")
	case strings.HasSuffix(value, "w"):
		multiplier = 7 * 24 * time.Hour
		number = strings.TrimSuffix(value, "w")
	default:
		return 0, fmt.Errorf("invalid --older-than duration %q", raw)
	}
	count, err := strconv.ParseInt(strings.TrimSpace(number), 10, 64)
	if err != nil || count < 0 {
		return 0, fmt.Errorf("invalid --older-than duration %q", raw)
	}
	return time.Duration(count) * multiplier, nil
}

func printLearnUsage() {
	fmt.Println("Usage: az learn <capture|add|recall|activate|feedback|health|show|review|stale|demote|promote|retire|relate|supersede|suggest|consolidate|suggestion-reject|doctor|gc> [arguments]")
	fmt.Println("  capture  Capture a typed learning correction or observation")
	fmt.Println("  add      Capture an evidence-backed candidate learning")
	fmt.Println("  recall   Search accepted/promoted learning summaries")
	fmt.Println("  activate Record actual delivery of selected public learnings")
	fmt.Println("  feedback Record an idempotent explicit or inferred activation outcome")
	fmt.Println("  health   Report deterministic privacy-safe portfolio metrics")
	fmt.Println("  show     Show a learning with full evidence")
	fmt.Println("  review   List review queues or bulk update selected learnings")
	fmt.Println("  stale    Mark a learning stale with an audit note")
	fmt.Println("  demote   Move a learning back to candidate review")
	fmt.Println("  promote  Mark a learning promoted toward curated guidance")
	fmt.Println("  retire   Retire an Az-managed promoted guidance block")
	fmt.Println("  relate   Record supersession or conflict between learnings")
	fmt.Println("  supersede Record that a newer learning supersedes an older one")
	fmt.Println("  suggest  Review or refresh deterministic duplicate/conflict suggestions")
	fmt.Println("  consolidate Human-confirm a suggestion into a canonical learning")
	fmt.Println("  suggestion-reject Reject a suggestion with an audit note")
	fmt.Println("  doctor   Report learning lifecycle maintenance problems without mutation")
	fmt.Println("  gc       Dry-run or confirm bounded cleanup of inactive learnings")
	fmt.Println("")
	fmt.Println("Commands:")
	fmt.Println("  az learn capture --observed <text> --preferred <text> --source <source> [--outcome <text>] [--impact <text>] [--context key=value ...] [--sensitivity public|private] [--json]")
	fmt.Println("  az learn add --evidence <text> [--summary <text>] [--private] [--issue <id>] [--req <id>] [--tag <tag> ...] [--file <path> ...] [--json]")
	fmt.Println("  az learn recall [--query <text>] [--issue <id>] [--req <id>] [--status <status> ...] [--tag <tag> ...] [--file <path> ...] [--limit N] [--include-evidence] [--include-private] [--json]")
	fmt.Println("  az learn activate --surface <name> [--token-cost N] [--issue <id>] [--req <id>] [--tag <tag> ...] [--file <path> ...] [--explanation <text>] <learning-id> ... [--json]")
	fmt.Println("  az learn feedback --idempotency-key <key> --outcome helpful|followed|contradicted|unknown [--source explicit|inferred] [--explanation <text>] <activation-id> [--json]")
	fmt.Println("  az learn health [--json]")
	fmt.Println("  az learn show <learning-id> [--json]")
	fmt.Println("  az learn review [--queue-status <status> ...] [--issue <id>] [--req <id>] [--tag <tag> ...] [--file <path> ...] [--target-state active|retired|drifted|missing ...] [--older-than 30d] [--limit N] [--json]")
	fmt.Println("  az learn review --id <learning-id> [--id <learning-id> ...] --status accepted|rejected|stale --note <text> [--json]")
	fmt.Println("  az learn review --bulk-stale --older-than 30d --note <reason> [--issue <id>] [--req <id>] [--tag <tag> ...] [--file <path> ...] [--limit N] [--json]")
	fmt.Println("  az learn stale --note <text> <learning-id> [--json]")
	fmt.Println("  az learn demote --note <text> <learning-id> [--json]")
	fmt.Println("  az learn promote --target rulesync|agents|skill|spec|decision [--target-id <id-or-path>] <learning-id> [--create-target] [--target-title <text>] [--target-description <text>] [--target-issue <id>] [--decision-rationale <text>] [--decision-context <text>] [--decision-consequences <text>] [--note <text>] [--target-hash <hash>] [--target-meta key=value ...] [--json]")
	fmt.Println("  az learn retire --note <text> <learning-id> [--json]")
	fmt.Println("  az learn relate --type supersedes|conflicts --note <text> [--scope-issue <id>] [--scope-req <id>] [--scope-session <id>] [--scope-tag <tag> ...] [--scope-file <path> ...] <source-learning-id> <target-learning-id> [--json]")
	fmt.Println("  az learn supersede --note <text> [--scope-issue <id>] [--scope-req <id>] [--scope-session <id>] [--scope-tag <tag> ...] [--scope-file <path> ...] <new-learning-id> <old-learning-id> [--json]")
	fmt.Println("  az learn suggest [--refresh] [--status pending|rejected|confirmed] [--limit N] [--json]")
	fmt.Println("  az learn consolidate --canonical <learning-id> --note <text> [--summary <text>] <suggestion-id> [--json]")
	fmt.Println("  az learn suggestion-reject --note <text> <suggestion-id> [--json]")
	fmt.Println("  az learn doctor [--candidate-older-than-days N] [--inactive-older-than-days N] [--limit N] [--json]")
	fmt.Println("  az learn gc [--confirm] [--candidate-older-than-days N] [--inactive-older-than-days N] [--limit N] [--json]")
}
