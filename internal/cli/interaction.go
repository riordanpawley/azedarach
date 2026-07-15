package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const interactionUsage = `Usage: az interaction <list|get|discuss|answer|resolve|withdraw> [arguments]

Commands:
  list [--project <id>] [--issue <issue-id>] [--json]
  get [--project <id>] [--json] <request-id>
  discuss [--project <id>] --revision <n> [--json] <request-id>
  answer [--project <id>] --revision <n> --significance <routine|material|critical> (--option <key> --rationale <text> | --answer-json <json> | --answer-file <path>) [--constraint <text> ...] [--json] <request-id>
  resolve [--project <id>] --revision <n> --significance <routine|material|critical> (--use-proposal | --option <key> --rationale <text> | --answer-json <json> | --answer-file <path>) [--constraint <text> ...] [--json] <request-id>
  withdraw [--project <id>] --revision <n> --reason <text> [--json] <request-id>

State and authority:
  Mutations require the current revision. A conflict means the request changed; run get and retry deliberately.
  discuss asks the daemon to start, resume, or attach the request's singleton read-only advisor session.
  answer is the direct human-answer path. resolve reviews/edits an advisor proposal or supplies a human final answer.
  Advisor sessions may propose answers but cannot answer, resolve, withdraw, or mutate issue/spec/decision state.
  --significance is always required for a human final answer; it overrides an AI proposal recommendation.
  --use-proposal copies proposal text/constraints only; durable effects require explicit human answer JSON.
  routine answers are evidence-only. Material/critical durable effects must be explicitly present in the answer JSON.

Answer JSON is an InteractionAnswerPayload. The CLI always replaces its revision and significance_recommendation
with the explicit --revision and --significance values before sending it to daemon authority.`

type InteractionOptions struct {
	Command      string
	Project      string
	IssueID      string
	RequestID    string
	Revision     int64
	Significance domain.InteractionSignificance
	Option       string
	Rationale    string
	Constraints  []string
	AnswerJSON   string
	AnswerFile   string
	UseProposal  bool
	Reason       string
	JSON         bool
}

func PrintInteractionUsage() { fmt.Println(interactionUsage) }

func ParseInteractionArgs(args []string) (InteractionOptions, error) {
	var opts InteractionOptions
	if len(args) == 0 {
		return opts, fmt.Errorf("interaction command is required")
	}
	opts.Command = strings.ToLower(strings.TrimSpace(args[0]))
	fs := flag.NewFlagSet("interaction "+opts.Command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.BoolVar(&opts.JSON, "json", false, "emit stable JSON")
	fs.StringVar(&opts.IssueID, "issue", "", "filter by issue id")
	fs.Int64Var(&opts.Revision, "revision", 0, "expected request revision")
	var significance string
	fs.StringVar(&significance, "significance", "", "human-confirmed significance")
	fs.StringVar(&opts.Option, "option", "", "selected option key")
	fs.StringVar(&opts.Rationale, "rationale", "", "answer rationale")
	var constraints repeatedStringFlag
	fs.Var(&constraints, "constraint", "answer constraint (repeatable)")
	fs.StringVar(&opts.AnswerJSON, "answer-json", "", "structured InteractionAnswerPayload JSON")
	fs.StringVar(&opts.AnswerFile, "answer-file", "", "path containing InteractionAnswerPayload JSON")
	fs.BoolVar(&opts.UseProposal, "use-proposal", false, "use the current AI proposal as the human answer")
	fs.StringVar(&opts.Reason, "reason", "", "withdrawal reason")
	if err := fs.Parse(args[1:]); err != nil {
		return opts, err
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.IssueID = strings.TrimSpace(opts.IssueID)
	opts.Significance = domain.InteractionSignificance(strings.ToLower(strings.TrimSpace(significance)))
	opts.Constraints = trimInteractionValues(constraints)
	positionals := fs.Args()

	switch opts.Command {
	case "list":
		if len(positionals) != 0 {
			return opts, fmt.Errorf("list does not accept positional arguments")
		}
		if opts.Revision != 0 || opts.Significance != "" || interactionHasAnswerFlags(opts) || strings.TrimSpace(opts.Reason) != "" {
			return opts, fmt.Errorf("list only accepts --project, --issue, and --json")
		}
	case "get":
		if len(positionals) != 1 || strings.TrimSpace(positionals[0]) == "" {
			return opts, fmt.Errorf("get requires exactly one request id")
		}
		if opts.IssueID != "" || opts.Revision != 0 || opts.Significance != "" || interactionHasAnswerFlags(opts) || strings.TrimSpace(opts.Reason) != "" {
			return opts, fmt.Errorf("get only accepts --project and --json")
		}
		opts.RequestID = strings.TrimSpace(positionals[0])
	case "discuss":
		if err := parseInteractionMutationTarget(&opts, positionals); err != nil {
			return opts, err
		}
		if opts.IssueID != "" || opts.Significance != "" || interactionHasAnswerFlags(opts) || strings.TrimSpace(opts.Reason) != "" {
			return opts, fmt.Errorf("discuss only accepts --project, --revision, and --json")
		}
	case "answer", "resolve":
		if err := parseInteractionMutationTarget(&opts, positionals); err != nil {
			return opts, err
		}
		if !validInteractionSignificance(opts.Significance) {
			return opts, fmt.Errorf("--significance must be routine, material, or critical")
		}
		if opts.IssueID != "" || strings.TrimSpace(opts.Reason) != "" {
			return opts, fmt.Errorf("%s does not accept --issue or --reason", opts.Command)
		}
		modes := 0
		if strings.TrimSpace(opts.AnswerJSON) != "" {
			modes++
		}
		if strings.TrimSpace(opts.AnswerFile) != "" {
			modes++
		}
		if strings.TrimSpace(opts.Option) != "" || strings.TrimSpace(opts.Rationale) != "" || len(opts.Constraints) > 0 {
			modes++
		}
		if opts.UseProposal {
			modes++
		}
		if modes != 1 {
			return opts, fmt.Errorf("choose exactly one answer source: --use-proposal, --option/--rationale, --answer-json, or --answer-file")
		}
		if opts.Command == "answer" && opts.UseProposal {
			return opts, fmt.Errorf("--use-proposal is only valid with resolve")
		}
		if !opts.UseProposal && strings.TrimSpace(opts.AnswerJSON) == "" && strings.TrimSpace(opts.AnswerFile) == "" && (strings.TrimSpace(opts.Option) == "" || strings.TrimSpace(opts.Rationale) == "") {
			return opts, fmt.Errorf("--option and --rationale are both required for an inline answer")
		}
	case "withdraw":
		if err := parseInteractionMutationTarget(&opts, positionals); err != nil {
			return opts, err
		}
		if strings.TrimSpace(opts.Reason) == "" {
			return opts, fmt.Errorf("withdraw requires --reason")
		}
		if opts.IssueID != "" || opts.Significance != "" || interactionHasAnswerFlags(opts) {
			return opts, fmt.Errorf("withdraw only accepts --project, --revision, --reason, and --json")
		}
	default:
		return opts, fmt.Errorf("unknown interaction command %q", opts.Command)
	}
	return opts, nil
}

func interactionHasAnswerFlags(opts InteractionOptions) bool {
	return strings.TrimSpace(opts.Option) != "" || strings.TrimSpace(opts.Rationale) != "" || len(opts.Constraints) > 0 || strings.TrimSpace(opts.AnswerJSON) != "" || strings.TrimSpace(opts.AnswerFile) != "" || opts.UseProposal
}

func parseInteractionMutationTarget(opts *InteractionOptions, positionals []string) error {
	if len(positionals) != 1 || strings.TrimSpace(positionals[0]) == "" {
		return fmt.Errorf("%s requires exactly one request id", opts.Command)
	}
	if opts.Revision < 1 {
		return fmt.Errorf("%s requires --revision with the current positive revision", opts.Command)
	}
	opts.RequestID = strings.TrimSpace(positionals[0])
	return nil
}

func trimInteractionValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func validInteractionSignificance(value domain.InteractionSignificance) bool {
	return value == domain.InteractionSignificanceRoutine || value == domain.InteractionSignificanceMaterial || value == domain.InteractionSignificanceCritical
}

func InteractionCommand(deps *Dependencies, opts InteractionOptions) error {
	if deps == nil || deps.DaemonClient == nil {
		return fmt.Errorf("daemon client unavailable")
	}
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()
	ctx := commandTraceContext(deps)
	client := deps.DaemonClient

	switch opts.Command {
	case "list":
		out, err := client.ListInteractions(ctx, protocol.InteractionListRequestBody{IssueID: opts.IssueID})
		if err != nil {
			return fmt.Errorf("list interaction requests: %w", err)
		}
		return printInteractionList(os.Stdout, out, opts.JSON)
	case "get":
		out, err := client.GetInteraction(ctx, opts.RequestID)
		if err != nil {
			return fmt.Errorf("get interaction %s: %w", opts.RequestID, err)
		}
		return printInteractionResponse(os.Stdout, out, opts.JSON)
	case "discuss":
		out, err := client.MutateInteraction(ctx, daemonclient.CommandInteractionDiscuss, protocol.InteractionMutationRequestBody{ID: opts.RequestID, ExpectedRevision: opts.Revision, Actor: "human"})
		if err != nil {
			return interactionMutationError(opts.RequestID, err)
		}
		return printInteractionResponse(os.Stdout, out, opts.JSON)
	case "answer", "resolve":
		answer, err := interactionAnswer(ctx, client, opts)
		if err != nil {
			return err
		}
		if opts.Command == "answer" {
			out, err := client.MutateInteraction(ctx, daemonclient.CommandInteractionAnswer, protocol.InteractionMutationRequestBody{ID: opts.RequestID, ExpectedRevision: opts.Revision, Actor: "human", Answer: answer})
			if err != nil {
				return interactionMutationError(opts.RequestID, err)
			}
			return printInteractionResponse(os.Stdout, out, opts.JSON)
		}
		out, err := client.ResolveInteraction(ctx, protocol.InteractionResolveRequestBody{InteractionMutationRequestBody: protocol.InteractionMutationRequestBody{ID: opts.RequestID, ExpectedRevision: opts.Revision, Actor: "human", Answer: answer}})
		if err != nil {
			return interactionMutationError(opts.RequestID, err)
		}
		return printInteractionResponse(os.Stdout, out, opts.JSON)
	case "withdraw":
		out, err := client.MutateInteraction(ctx, daemonclient.CommandInteractionWithdraw, protocol.InteractionMutationRequestBody{ID: opts.RequestID, ExpectedRevision: opts.Revision, Actor: "human", Reason: strings.TrimSpace(opts.Reason)})
		if err != nil {
			return interactionMutationError(opts.RequestID, err)
		}
		return printInteractionResponse(os.Stdout, out, opts.JSON)
	default:
		return fmt.Errorf("unsupported interaction command %q", opts.Command)
	}
}

func interactionAnswer(ctx context.Context, client *daemonclient.Client, opts InteractionOptions) (domain.InteractionAnswerPayload, error) {
	var answer domain.InteractionAnswerPayload
	switch {
	case opts.UseProposal:
		out, err := client.GetInteraction(ctx, opts.RequestID)
		if err != nil {
			return answer, fmt.Errorf("load proposal for interaction %s: %w", opts.RequestID, err)
		}
		if out.Request.Proposal == nil {
			return answer, fmt.Errorf("interaction %s has no AI proposal to resolve", opts.RequestID)
		}
		answer = out.Request.Proposal.Answer
		answer.ApprovedIssueFieldEffects = domain.InteractionIssueFieldEffects{}
		answer.ApprovedRequirementEffects = nil
		answer.ApprovedDecisionEffect = nil
	case strings.TrimSpace(opts.AnswerJSON) != "":
		if err := decodeInteractionAnswer([]byte(opts.AnswerJSON), &answer); err != nil {
			return answer, fmt.Errorf("decode --answer-json: %w", err)
		}
	case strings.TrimSpace(opts.AnswerFile) != "":
		data, err := os.ReadFile(opts.AnswerFile)
		if err != nil {
			return answer, fmt.Errorf("read --answer-file: %w", err)
		}
		if err := decodeInteractionAnswer(data, &answer); err != nil {
			return answer, fmt.Errorf("decode --answer-file: %w", err)
		}
	default:
		answer.SelectedOption = strings.TrimSpace(opts.Option)
		answer.Rationale = strings.TrimSpace(opts.Rationale)
		answer.Constraints = append([]string(nil), opts.Constraints...)
	}
	answer.Revision = opts.Revision
	answer.SignificanceRecommendation = opts.Significance
	return answer, nil
}

func decodeInteractionAnswer(data []byte, answer *domain.InteractionAnswerPayload) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(answer); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func interactionMutationError(id string, err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "stale interaction revision") || strings.Contains(message, "conflict") {
		return fmt.Errorf("interaction %s changed: %w; run `az interaction get %s` and retry with its current --revision", id, err, id)
	}
	return fmt.Errorf("update interaction %s: %w", id, err)
}

func printInteractionList(w io.Writer, out protocol.InteractionListResponseBody, jsonOutput bool) error {
	if jsonOutput {
		return writeInteractionJSON(w, out)
	}
	if len(out.Requests) == 0 {
		fmt.Fprintln(w, "No interaction requests.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tISSUE\tSTATE\tREV\tSIGNIFICANCE\tAGE\tQUESTION")
	for _, request := range out.Requests {
		age := out.Ages[request.ID]
		ageText := (time.Duration(age.AgeSeconds) * time.Second).Round(time.Second).String()
		if age.Stale {
			ageText += " stale"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n", request.ID, request.IssueID, request.State, request.Revision, request.Significance, ageText, request.Question)
	}
	return tw.Flush()
}

func printInteractionResponse(w io.Writer, out protocol.InteractionResponseBody, jsonOutput bool) error {
	if jsonOutput {
		return writeInteractionJSON(w, out)
	}
	r := out.Request
	fmt.Fprintf(w, "Interaction: %s\nIssue: %s\nState: %s\nRevision: %d\nSignificance: %s\nRespondent: %s\nQuestion: %s\nWhy: %s\n", r.ID, r.IssueID, r.State, r.Revision, r.Significance, r.Respondent, r.Question, r.Why)
	if len(r.Options) > 0 {
		fmt.Fprintln(w, "Options:")
		for _, option := range r.Options {
			if strings.TrimSpace(option.Description) == "" {
				fmt.Fprintf(w, "  %s: %s\n", option.Key, option.Label)
			} else {
				fmt.Fprintf(w, "  %s: %s — %s\n", option.Key, option.Label, option.Description)
			}
		}
	}
	if len(r.RequiredDecisions) > 0 {
		fmt.Fprintln(w, "Required decisions:")
		for _, decision := range r.RequiredDecisions {
			fmt.Fprintf(w, "  - %s\n", decision)
		}
	}
	if strings.TrimSpace(r.DecisionPacket.Summary) != "" {
		fmt.Fprintf(w, "Decision summary: %s\n", r.DecisionPacket.Summary)
	}
	if strings.TrimSpace(r.DecisionPacket.Recommendation) != "" {
		fmt.Fprintf(w, "Recommendation: %s\n", r.DecisionPacket.Recommendation)
	}
	if strings.TrimSpace(r.Context) != "" {
		fmt.Fprintf(w, "Context: %s\n", r.Context)
	}
	if r.SessionID != "" {
		status := "advisor session"
		switch {
		case out.SessionStarted:
			status = "advisor session started"
		case out.SessionResumed:
			status = "advisor session resumed"
		case out.SessionAttached:
			status = "advisor session attached"
		}
		fmt.Fprintf(w, "Discussion: %s (%s)\n", r.SessionID, status)
	}
	if r.Proposal != nil {
		fmt.Fprintf(w, "AI proposal: option=%s significance=%s\nProposal rationale: %s\n", r.Proposal.Answer.SelectedOption, r.Proposal.Answer.SignificanceRecommendation, r.Proposal.Answer.Rationale)
	}
	if r.FinalAnswer != nil {
		fmt.Fprintf(w, "Final answer: option=%s significance=%s actor=%s\nFinal rationale: %s\n", r.FinalAnswer.Answer.SelectedOption, r.FinalAnswer.Answer.SignificanceRecommendation, r.FinalAnswer.Actor, r.FinalAnswer.Answer.Rationale)
	}
	if r.ResolutionTrace != nil {
		if r.ResolutionTrace.DecisionID != "" {
			fmt.Fprintf(w, "Applied decision: %s\n", r.ResolutionTrace.DecisionID)
		}
		if len(r.ResolutionTrace.RequirementIDs) > 0 {
			fmt.Fprintf(w, "Applied requirements: %s\n", strings.Join(r.ResolutionTrace.RequirementIDs, ", "))
		}
	}
	if out.Age.Stale {
		fmt.Fprintln(w, "Age: stale")
	}
	return nil
}

func writeInteractionJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
