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
		request := protocol.ValidationAcquireRequest{RequestID: *requestID, LeaseToken: *leaseToken, IssueID: *issueID, Class: domain.ValidationClass(*class), Profile: *profile, Command: *command, SourceRevision: *revision, ReviewerID: *reviewer, TTLSeconds: *ttl}
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
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			_, err := deps.DaemonClient.ValidationAuthorizeNested(context.Background(), protocol.ValidationAuthorizeNestedRequest{RequestID: *requestID, LeaseToken: *leaseToken, Class: domain.ValidationClass(*class)})
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
		fmt.Printf("%s %s %s (%s)\n", value.Request.RequestID, value.Request.State, value.Request.Profile, value.Request.IssueID)
	case domain.ValidationSnapshot:
		fmt.Printf("active=%d queued=%d revision=%d\n", len(value.Active), len(value.Queued), value.Revision)
	default:
		return fmt.Errorf("unsupported validation output %T", value)
	}
	return nil
}

func printValidationUsage() {
	fmt.Println(strings.TrimSpace(`Usage: az validation <acquire|heartbeat|authorize-nested|finish|status|watch> [flags]
  acquire --request <id> --token <secret> --issue <id> --class aggregate|shared|safe --profile <name> --command <text> --revision <sha> [--wait] [--json]
  heartbeat --request <id> --token <secret> [--ttl 30]
  authorize-nested --request <id> --token <secret> --class aggregate|shared|safe
  finish --request <id> --token <secret> --state completed|cancelled|failed [--outcome text] [--evidence-json object] [--json]
  status [--json]
  watch [--interval 1s] [--json]`))
}
