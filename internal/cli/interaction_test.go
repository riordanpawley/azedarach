package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestParseInteractionArgsLocksRevisionAndHumanSignificance(t *testing.T) {
	opts, err := ParseInteractionArgs([]string{"resolve", "--project", "proj-a", "--revision", "7", "--significance", "material", "--use-proposal", "req-1", "--json"})
	if err == nil {
		t.Fatal("flags after request id should be rejected")
	}
	opts, err = ParseInteractionArgs([]string{"resolve", "--project", "proj-a", "--revision", "7", "--significance", "material", "--use-proposal", "--json", "req-1"})
	if err != nil {
		t.Fatalf("ParseInteractionArgs: %v", err)
	}
	if opts.Command != "resolve" || opts.Project != "proj-a" || opts.RequestID != "req-1" || opts.Revision != 7 || opts.Significance != domain.InteractionSignificanceMaterial || !opts.UseProposal || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}

	for _, args := range [][]string{
		{"discuss", "req-1"},
		{"get", "--issue", "az-1", "req-1"},
		{"list", "--revision", "1"},
		{"answer", "--revision", "1", "--option", "yes", "--rationale", "ship", "req-1"},
		{"resolve", "--revision", "1", "--significance", "routine", "--use-proposal", "--answer-json", `{}`, "req-1"},
		{"withdraw", "--revision", "1", "req-1"},
	} {
		if _, err := ParseInteractionArgs(args); err == nil {
			t.Fatalf("ParseInteractionArgs(%v) succeeded", args)
		}
	}
}

func TestInteractionCommandUsesTypedDaemonAuthorityAndAttachesDiscussion(t *testing.T) {
	routes := registerCLIProjects(t, "other", "proj-interaction")
	now := time.Date(2026, time.July, 11, 2, 0, 0, 0, time.UTC)
	request := interactionCLIRequest(now)
	transport := &fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		if req.Meta.ProjectID != naming.ProjectID(routes["proj-interaction"]) {
			t.Fatalf("project id = %q", req.Meta.ProjectID)
		}
		if req.Command != protocol.CommandInteractionDiscuss {
			t.Fatalf("command = %q", req.Command)
		}
		var body protocol.InteractionMutationRequestBody
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.ID != request.ID || body.ExpectedRevision != 1 || body.Actor != "human" || body.SessionID != "" {
			t.Fatalf("body = %+v", body)
		}
		request.State = domain.InteractionDiscussing
		request.Revision = 2
		request.SessionID = "advisor-req-1"
		return responseWithJSON(req, protocol.InteractionResponseBody{Request: request, SessionAttached: true}), nil
	}}
	deps := &Dependencies{DaemonClient: daemonclient.New(transport).WithProjectID(routes["other"]), ProjectID: routes["other"]}
	out := captureStdout(t, func() error {
		return InteractionCommand(deps, InteractionOptions{Command: "discuss", Project: "proj-interaction", RequestID: request.ID, Revision: 1})
	})
	if !strings.Contains(out, "Discussion: advisor-req-1 (advisor session attached)") {
		t.Fatalf("output = %q", out)
	}
	if deps.ProjectID != routes["other"] {
		t.Fatalf("project override was not restored: %q", deps.ProjectID)
	}
}

func TestInteractionResolveReviewsProposalAndOverridesAISignificance(t *testing.T) {
	now := time.Date(2026, time.July, 11, 2, 0, 0, 0, time.UTC)
	request := interactionCLIRequest(now)
	request.State = domain.InteractionAnswerProposed
	request.Revision = 3
	proposedTitle := "AI-approved title must not leak"
	request.Proposal = &domain.InteractionAnswerAudit{Actor: "advisor:req-1", CreatedAt: now, Answer: domain.InteractionAnswerPayload{SelectedOption: "yes", Rationale: "AI rationale", SignificanceRecommendation: domain.InteractionSignificanceCritical, Revision: 2, ApprovedIssueFieldEffects: domain.InteractionIssueFieldEffects{Title: &proposedTitle}, ApprovedDecisionEffect: &domain.InteractionDecisionEffect{Title: "AI decision"}}}
	var commands []string
	transport := &fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		commands = append(commands, req.Command)
		switch req.Command {
		case protocol.CommandInteractionGet:
			return responseWithJSON(req, protocol.InteractionResponseBody{Request: request}), nil
		case protocol.CommandInteractionResolve:
			var body protocol.InteractionResolveRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("decode resolve: %v", err)
			}
			if body.Actor != "human" || body.ExpectedRevision != 3 || body.Answer.Revision != 3 || body.Answer.SignificanceRecommendation != domain.InteractionSignificanceMaterial || body.Answer.Rationale != "AI rationale" {
				t.Fatalf("resolve body = %+v", body)
			}
			if body.Answer.ApprovedIssueFieldEffects.Any() || len(body.Answer.ApprovedRequirementEffects) != 0 || body.Answer.ApprovedDecisionEffect != nil {
				t.Fatalf("AI-proposed durable effects leaked into human resolution: %+v", body.Answer)
			}
			request.State = domain.InteractionResolved
			request.Revision = 4
			request.FinalAnswer = &domain.InteractionAnswerAudit{Actor: "human", CreatedAt: now, Answer: body.Answer}
			return responseWithJSON(req, protocol.InteractionResponseBody{Request: request}), nil
		default:
			t.Fatalf("unexpected command %q", req.Command)
			return protocol.ResponseEnvelope{}, nil
		}
	}}
	deps := &Dependencies{DaemonClient: daemonclient.New(transport).WithProjectID("proj")}
	if err := InteractionCommand(deps, InteractionOptions{Command: "resolve", RequestID: request.ID, Revision: 3, Significance: domain.InteractionSignificanceMaterial, UseProposal: true, JSON: true}); err != nil {
		t.Fatalf("InteractionCommand: %v", err)
	}
	if strings.Join(commands, ",") != protocol.CommandInteractionGet+","+protocol.CommandInteractionResolve {
		t.Fatalf("commands = %v", commands)
	}
}

func TestInteractionMutationConflictIsActionable(t *testing.T) {
	transport := &fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Error: &protocol.ErrorEnvelope{Code: protocol.ErrorCodeConflict, Message: "stale interaction revision: expected 2, current 3"}}, nil
	}}
	deps := &Dependencies{DaemonClient: daemonclient.New(transport).WithProjectID("proj")}
	err := InteractionCommand(deps, InteractionOptions{Command: "withdraw", RequestID: "req-1", Revision: 2, Reason: "no longer needed"})
	if err == nil || !strings.Contains(err.Error(), "az interaction get req-1") || !strings.Contains(err.Error(), "current --revision") {
		t.Fatalf("error = %v", err)
	}
}

func TestInteractionAnswerJSONRejectsUnknownOrTrailingFields(t *testing.T) {
	for _, raw := range []string{
		`{"selected_option":"yes","rationale":"ship","significance_recomendation":"material"}`,
		`{"selected_option":"yes","rationale":"ship"} {"selected_option":"no"}`,
	} {
		_, err := interactionAnswer(context.Background(), nil, InteractionOptions{RequestID: "req-1", Revision: 2, Significance: domain.InteractionSignificanceMaterial, AnswerJSON: raw})
		if err == nil {
			t.Fatalf("interactionAnswer(%q) succeeded", raw)
		}
	}
}

func TestInteractionOutputsStableTextAndJSON(t *testing.T) {
	now := time.Date(2026, time.July, 11, 2, 0, 0, 0, time.UTC)
	request := interactionCLIRequest(now)
	list := protocol.InteractionListResponseBody{Requests: []domain.InteractionRequest{request}, Ages: map[string]domain.InteractionAgeView{request.ID: {AgeSeconds: 90, Stale: true}}}
	var textOut bytes.Buffer
	if err := printInteractionList(&textOut, list, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "ISSUE", "STATE", "REV", "req-1", "1m30s stale", "Ship now?"} {
		if !strings.Contains(textOut.String(), want) {
			t.Fatalf("text output missing %q: %q", want, textOut.String())
		}
	}
	var jsonOut bytes.Buffer
	if err := printInteractionList(&jsonOut, list, true); err != nil {
		t.Fatal(err)
	}
	var decoded protocol.InteractionListResponseBody
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil || len(decoded.Requests) != 1 || decoded.Requests[0].ID != request.ID || !decoded.Ages[request.ID].Stale {
		t.Fatalf("json output = %s, err=%v", jsonOut.String(), err)
	}
	var detail bytes.Buffer
	if err := printInteractionResponse(&detail, protocol.InteractionResponseBody{Request: request}, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Options:", "yes: Ship", "no: Wait", "Decision summary: Choose timing"} {
		if !strings.Contains(detail.String(), want) {
			t.Fatalf("detail output missing %q: %q", want, detail.String())
		}
	}
}

func interactionCLIRequest(now time.Time) domain.InteractionRequest {
	return domain.InteractionRequest{
		ID: "req-1", IssueID: "az-1", DecisionKey: "ship", OrchestrationScope: "root:az-1",
		Question: "Ship now?", Why: "Deployment timing matters", Options: []domain.InteractionOption{{Key: "yes", Label: "Ship"}, {Key: "no", Label: "Wait"}},
		Significance: domain.InteractionSignificanceMaterial, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose timing"},
		State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
}
