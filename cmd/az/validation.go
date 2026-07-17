package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func runValidationCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		printValidationUsage()
		return nil
	}
	switch args[0] {
	case "acquire":
		flags := flag.NewFlagSet("validation acquire", flag.ContinueOnError)
		requestID := flags.String("request", "", "stable request id")
		leaseToken := flags.String("token", os.Getenv("AZEDARACH_VALIDATION_LEASE_TOKEN"), "secret lease fencing token")
		issueID := flags.String("issue", os.Getenv("AZEDARACH_TICKET_ID"), "owning issue id")
		class := flags.String("class", "shared", "aggregate, shared, or safe")
		scope := flags.String("scope", "", "repository or ticket")
		purpose := flags.String("purpose", "", "capacity, development, push_gate, or review_evidence")
		isolation := flags.String("isolation", "", "validation isolation mode")
		fingerprint := flags.String("environment-fingerprint", "", "toolchain and environment fingerprint")
		override := flags.String("override", "none", "none, no_reuse, force_rerun, or emergency_skip")
		overrideActor := flags.String("override-actor", "", "emergency skip actor")
		overrideReason := flags.String("override-reason", "", "emergency skip reason")
		profile := flags.String("profile", "", "validation profile")
		command := flags.String("command", "", "command description")
		revision := flags.String("revision", "", "source revision")
		reviewer := flags.String("reviewer", validationReviewerID(), "reviewer identity for a review-assigned aggregate gate")
		ttl := flags.Int("ttl", 30, "heartbeat expiry seconds")
		wait := flags.Bool("wait", false, "wait until active")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		request := protocol.ValidationAcquireRequest{RequestID: *requestID, LeaseToken: *leaseToken, IssueID: *issueID, Class: domain.ValidationClass(*class), Scope: domain.ValidationScope(*scope), Purpose: domain.ValidationPurpose(*purpose), IsolationMode: *isolation, EnvironmentFingerprint: *fingerprint, Override: domain.ValidationOverride(*override), OverrideActor: *overrideActor, OverrideReason: *overrideReason, Profile: *profile, Command: *command, SourceRevision: *revision, ReviewerID: *reviewer, TTLSeconds: *ttl}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			for {
				result, err := deps.DaemonClient.ValidationAcquire(context.Background(), request)
				if err != nil {
					return err
				}
				if result.Request.State == domain.ValidationRequestActive || !*wait {
					return printValidationValue(result, *jsonOutput)
				}
				if result.Request.State != domain.ValidationRequestQueued {
					return fmt.Errorf("validation request %s became %s while waiting", result.Request.RequestID, result.Request.State)
				}
				time.Sleep(500 * time.Millisecond)
			}
		})
	case "heartbeat":
		flags := flag.NewFlagSet("validation heartbeat", flag.ContinueOnError)
		requestID := flags.String("request", "", "request id")
		leaseToken := flags.String("token", os.Getenv("AZEDARACH_VALIDATION_LEASE_TOKEN"), "secret lease fencing token")
		ttl := flags.Int("ttl", 30, "heartbeat expiry seconds")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			_, err := deps.DaemonClient.ValidationHeartbeat(context.Background(), protocol.ValidationHeartbeatRequest{RequestID: *requestID, LeaseToken: *leaseToken, TTLSeconds: *ttl})
			return err
		})
	case "authorize-nested":
		flags := flag.NewFlagSet("validation authorize-nested", flag.ContinueOnError)
		requestID := flags.String("request", "", "request id")
		leaseToken := flags.String("token", os.Getenv("AZEDARACH_VALIDATION_LEASE_TOKEN"), "secret lease fencing token")
		class := flags.String("class", "shared", "aggregate, shared, or safe")
		scope := flags.String("scope", "", "inherited repository or ticket scope")
		purpose := flags.String("purpose", "", "inherited validation purpose")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			_, err := deps.DaemonClient.ValidationAuthorizeNested(context.Background(), protocol.ValidationAuthorizeNestedRequest{RequestID: *requestID, LeaseToken: *leaseToken, Class: domain.ValidationClass(*class), Scope: domain.ValidationScope(*scope), Purpose: domain.ValidationPurpose(*purpose)})
			return err
		})
	case "finish":
		flags := flag.NewFlagSet("validation finish", flag.ContinueOnError)
		requestID := flags.String("request", "", "request id")
		leaseToken := flags.String("token", os.Getenv("AZEDARACH_VALIDATION_LEASE_TOKEN"), "secret lease fencing token")
		state := flags.String("state", string(domain.ValidationRequestCompleted), "completed, cancelled, or failed")
		outcome := flags.String("outcome", "", "outcome summary")
		evidenceJSON := flags.String("evidence-json", "", "machine validation evidence JSON")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			var evidence domain.ValidationEvidence
			if strings.TrimSpace(*evidenceJSON) != "" {
				if err := json.Unmarshal([]byte(*evidenceJSON), &evidence); err != nil {
					return fmt.Errorf("decode validation evidence: %w", err)
				}
			}
			result, err := deps.DaemonClient.ValidationFinish(context.Background(), protocol.ValidationFinishRequest{RequestID: *requestID, LeaseToken: *leaseToken, State: domain.ValidationRequestState(*state), Outcome: *outcome, Evidence: evidence})
			if err != nil {
				return err
			}
			return printValidationValue(result, *jsonOutput)
		})
	case "evidence-record":
		flags := flag.NewFlagSet("validation evidence-record", flag.ContinueOnError)
		body := flags.String("body", "", "publication evidence JSON object")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		var evidence domain.PublicationEvidence
		if err := json.Unmarshal([]byte(*body), &evidence); err != nil {
			return fmt.Errorf("decode publication evidence: %w", err)
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			result, err := deps.DaemonClient.PublicationEvidenceRecord(context.Background(), protocol.PublicationEvidenceRecordRequest{Evidence: evidence})
			if err != nil {
				return err
			}
			return printValidationValue(result, *jsonOutput)
		})
	case "evidence-invalidate":
		flags := flag.NewFlagSet("validation evidence-invalidate", flag.ContinueOnError)
		body := flags.String("body", "", "publication evidence invalidation JSON object")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		var invalidation domain.PublicationEvidenceInvalidation
		if err := json.Unmarshal([]byte(*body), &invalidation); err != nil {
			return fmt.Errorf("decode publication evidence invalidation: %w", err)
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			result, err := deps.DaemonClient.PublicationEvidenceInvalidate(context.Background(), protocol.PublicationEvidenceInvalidateRequest{Invalidation: invalidation})
			if err != nil {
				return err
			}
			return printValidationValue(result, *jsonOutput)
		})
	case "evidence-status":
		flags := flag.NewFlagSet("validation evidence-status", flag.ContinueOnError)
		issueID := flags.String("issue", "", "optional issue id filter")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			result, err := deps.DaemonClient.PublicationEvidenceStatus(context.Background(), protocol.PublicationEvidenceStatusRequest{IssueID: *issueID})
			if err != nil {
				return err
			}
			return printValidationValue(result, *jsonOutput)
		})
	case "evidence-evaluate":
		flags := flag.NewFlagSet("validation evidence-evaluate", flag.ContinueOnError)
		issueID := flags.String("issue", os.Getenv("AZEDARACH_TICKET_ID"), "issue id")
		candidateJSON := flags.String("candidate-json", "", "candidate identity and intervening-change JSON")
		policyJSON := flags.String("policy-json", "", "project evidence policy JSON")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		var candidate domain.PublicationEvidenceCandidate
		if err := json.Unmarshal([]byte(*candidateJSON), &candidate); err != nil {
			return fmt.Errorf("decode publication evidence candidate: %w", err)
		}
		var policy domain.PublicationEvidencePolicy
		if err := json.Unmarshal([]byte(*policyJSON), &policy); err != nil {
			return fmt.Errorf("decode publication evidence policy: %w", err)
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			result, err := deps.DaemonClient.PublicationEvidenceEvaluate(context.Background(), protocol.PublicationEvidenceEvaluateRequest{IssueID: *issueID, Candidate: candidate, Policy: policy})
			if err != nil {
				return err
			}
			return printValidationValue(result, *jsonOutput)
		})
	case "status", "watch":
		flags := flag.NewFlagSet("validation "+args[0], flag.ContinueOnError)
		jsonOutput := flags.Bool("json", false, "emit JSON")
		interval := flags.Duration("interval", time.Second, "watch interval")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		watch := args[0] == "watch"
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			var previous string
			for {
				result, err := deps.DaemonClient.ValidationStatus(context.Background())
				if err != nil {
					return err
				}
				encoded, _ := json.Marshal(result.Snapshot)
				if string(encoded) != previous {
					if err := printValidationValue(result.Snapshot, *jsonOutput || watch); err != nil {
						return err
					}
					previous = string(encoded)
				}
				if !watch {
					return nil
				}
				if *interval <= 0 {
					return errors.New("watch interval must be positive")
				}
				time.Sleep(*interval)
			}
		})
	default:
		return fmt.Errorf("unknown validation command %q", args[0])
	}
}

func validationReviewerID() string {
	for _, name := range []string{"AZEDARACH_AUDIT_ACTOR", "USER", "LOGNAME"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func printValidationValue(value any, asJSON bool) error {
	if asJSON {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	switch value := value.(type) {
	case protocol.ValidationRequestResponse:
		fmt.Printf("%s %s %s scope=%s purpose=%s execution=%s source=%s override=%s ticket=%s\n", value.Request.RequestID, value.Request.State, value.Request.Profile, value.Request.Scope, value.Request.Purpose, value.Request.Execution, value.Request.AuthoritativeRequestID, value.Request.Override, value.Request.IssueID)
	case domain.ValidationSnapshot:
		fmt.Printf("active=%d queued=%d revision=%d\n", len(value.Active), len(value.Queued), value.Revision)
		for _, request := range append(append(append([]domain.ValidationRequest{}, value.Active...), value.Queued...), value.Recent...) {
			fmt.Printf("%s %s class=%s scope=%s purpose=%s execution=%s source=%s override=%s ticket=%s revision=%s\n", request.RequestID, request.State, request.Class, request.Scope, request.Purpose, request.Execution, request.AuthoritativeRequestID, request.Override, request.IssueID, request.SourceRevision)
		}
	case protocol.PublicationEvidenceRecordResponse:
		fmt.Printf("%s layer=%s ticket=%s source=%s base=%s result=%s policy=%s reused_from=%s\n", value.Evidence.EvidenceID, value.Evidence.Layer, value.Evidence.IssueID, value.Evidence.SourceRevision, value.Evidence.BaseRevision, value.Evidence.ResultRevision, value.Evidence.PolicyVersion, value.Evidence.ReusedFromEvidenceID)
	case protocol.PublicationEvidenceInvalidateResponse:
		fmt.Printf("%s evidence=%s reason=%s details=%s\n", value.Invalidation.InvalidationID, value.Invalidation.EvidenceID, value.Invalidation.Reason, value.Invalidation.Details)
	case protocol.PublicationEvidenceStatusResponse:
		fmt.Printf("evidence=%d invalidations=%d revision=%d ticket=%s\n", len(value.Snapshot.Evidence), len(value.Snapshot.Invalidations), value.Snapshot.Revision, value.Snapshot.IssueID)
		for _, evidence := range value.Snapshot.Evidence {
			fmt.Printf("%s layer=%s source=%s base=%s result=%s policy=%s environment=%s reused_from=%s\n", evidence.EvidenceID, evidence.Layer, evidence.SourceRevision, evidence.BaseRevision, evidence.ResultRevision, evidence.PolicyVersion, evidence.EnvironmentFingerprint, evidence.ReusedFromEvidenceID)
		}
		for _, invalidation := range value.Snapshot.Invalidations {
			fmt.Printf("%s evidence=%s invalidated=%s details=%s\n", invalidation.InvalidationID, invalidation.EvidenceID, invalidation.Reason, invalidation.Details)
		}
	case protocol.PublicationEvidenceEvaluateResponse:
		for _, assessment := range value.Assessments {
			state := "retained"
			if !assessment.Retained {
				state = "invalidated"
			}
			fmt.Printf("%s layer=%s state=%s base_movement_only=%t reasons=%v\n", assessment.EvidenceID, assessment.Layer, state, assessment.BaseMovementOnly, assessment.Reasons)
		}
	default:
		return fmt.Errorf("unsupported validation output %T", value)
	}
	return nil
}

func printValidationUsage() {
	fmt.Println(strings.TrimSpace(`Usage: az validation <acquire|heartbeat|authorize-nested|finish|status|watch|evidence-record|evidence-invalidate|evidence-status|evidence-evaluate> [flags]
  acquire --request <id> --token <secret> --scope repository|ticket --purpose capacity|development|push_gate|review_evidence --isolation <mode> --environment-fingerprint <hash> --override none|no_reuse|force_rerun|emergency_skip [--override-actor <id> --override-reason <text>] [--issue <id>] --class aggregate|shared|safe --profile <name> --command <text> --revision <sha> [--wait] [--json]
  heartbeat --request <id> --token <secret> [--ttl 30]
  authorize-nested --request <id> --token <secret> --class aggregate|shared|safe
  finish --request <id> --token <secret> --state completed|cancelled|failed [--outcome text] [--evidence-json object] [--json]
  evidence-record --body <publication-evidence-json> [--json]
  evidence-invalidate --body <invalidation-json> [--json]
  evidence-status [--issue <id>] [--json]
  evidence-evaluate --issue <id> --candidate-json <object> --policy-json <object> [--json]
  status [--json]
  watch [--interval 1s] [--json]`))
}
