package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
		reviewerKind := flags.String("reviewer-kind", os.Getenv("AZEDARACH_REVIEWER_KIND"), "typed reviewer owner kind")
		reviewEpoch := flags.Int64("review-epoch-event-id", envInt64("AZEDARACH_REVIEW_EPOCH_EVENT_ID"), "exact review request epoch event id")
		publicationOperation := flags.String("publication-operation-id", os.Getenv("AZEDARACH_PUBLICATION_OPERATION_ID"), "exact accepted publication operation id")
		acceptedReviewEvent := flags.Int64("accepted-review-event-id", envInt64("AZEDARACH_ACCEPTED_REVIEW_EVENT_ID"), "exact accepted review event id")
		acceptedPublicationOperation := flags.String("accepted-publication-operation-id", os.Getenv("AZEDARACH_ACCEPTED_PUBLICATION_OPERATION_ID"), "publication operation named by the accepted review event")
		ttl := flags.Int("ttl", 30, "heartbeat expiry seconds")
		wait := flags.Bool("wait", false, "wait until active")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		request := protocol.ValidationAcquireRequest{RequestID: *requestID, LeaseToken: *leaseToken, IssueID: *issueID, Class: domain.ValidationClass(*class), Scope: domain.ValidationScope(*scope), Purpose: domain.ValidationPurpose(*purpose), IsolationMode: *isolation, EnvironmentFingerprint: *fingerprint, Override: domain.ValidationOverride(*override), OverrideActor: *overrideActor, OverrideReason: *overrideReason, Profile: *profile, Command: *command, SourceRevision: *revision, ReviewerID: *reviewer, ReviewerKind: *reviewerKind, ReviewEpochEventID: *reviewEpoch, PublicationOperationID: *publicationOperation, AcceptedReviewEventID: *acceptedReviewEvent, AcceptedPublicationOperationID: *acceptedPublicationOperation, TTLSeconds: *ttl}
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
	case "artifact-read":
		flags := flag.NewFlagSet("validation artifact-read", flag.ContinueOnError)
		reference := flags.String("reference", "", "opaque artifact reference")
		output := flags.String("output", "", "write content to this file instead of stdout")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return retrieveValidationArtifact(context.Background(), strings.TrimSpace(*reference), strings.TrimSpace(*output), os.Stdout, deps.DaemonClient.ValidationArtifactRead)
		})
	case "evidence-record":
		flags := flag.NewFlagSet("validation evidence-record", flag.ContinueOnError)
		evidenceID := flags.String("id", "", "stable evidence id")
		issueID := flags.String("issue", os.Getenv("AZEDARACH_TICKET_ID"), "issue id")
		layer := flags.String("layer", "", "patch_review or active_path")
		validationRequestID := flags.String("validation-request", "", "completed validation request id")
		reusedFrom := flags.String("reused-from", "", "optional prior evidence id")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			result, err := deps.DaemonClient.PublicationEvidenceRecord(context.Background(), protocol.PublicationEvidenceRecordRequest{
				EvidenceID: *evidenceID, IssueID: *issueID, Layer: domain.PublicationEvidenceLayer(*layer),
				ValidationRequestID: *validationRequestID, ReusedFromEvidenceID: *reusedFrom,
			})
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
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			result, err := deps.DaemonClient.PublicationEvidenceEvaluate(context.Background(), protocol.PublicationEvidenceEvaluateRequest{IssueID: *issueID})
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

type validationArtifactReader func(context.Context, protocol.ValidationArtifactReadRequest) (protocol.ValidationArtifactReadResponse, error)

func retrieveValidationArtifact(ctx context.Context, reference, output string, stdout io.Writer, read validationArtifactReader) (returnErr error) {
	return retrieveValidationArtifactWithOps(ctx, reference, output, stdout, read, func(dir, pattern string) (validationArtifactStage, error) {
		return os.CreateTemp(dir, pattern)
	}, defaultValidationArtifactFileOps())
}

type validationArtifactStage interface {
	io.Writer
	Name() string
	Chmod(os.FileMode) error
	Sync() error
	Close() error
}

type validationArtifactStager func(string, string) (validationArtifactStage, error)

func retrieveValidationArtifactWithStager(ctx context.Context, reference, output string, stdout io.Writer, read validationArtifactReader, createStage validationArtifactStager) (returnErr error) {
	return retrieveValidationArtifactWithOps(ctx, reference, output, stdout, read, createStage, defaultValidationArtifactFileOps())
}

type validationArtifactDirectory interface {
	Sync() error
	Close() error
}

type validationArtifactFileOps struct {
	openDirectory func(string) (validationArtifactDirectory, error)
	rename        func(string, string) error
	backup        func(string) (string, bool, error)
	remove        func(string) error
}

func defaultValidationArtifactFileOps() validationArtifactFileOps {
	return validationArtifactFileOps{
		openDirectory: func(path string) (validationArtifactDirectory, error) { return os.Open(path) },
		rename:        os.Rename,
		backup:        backupValidationArtifactDestination,
		remove:        os.Remove,
	}
}

func backupValidationArtifactDestination(output string) (string, bool, error) {
	if _, err := os.Lstat(output); errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}
	placeholder, err := os.CreateTemp(filepath.Dir(output), ".az-validation-artifact-backup-*")
	if err != nil {
		return "", false, err
	}
	backup := placeholder.Name()
	if err = placeholder.Close(); err != nil {
		_ = os.Remove(backup)
		return "", false, err
	}
	if err = os.Remove(backup); err != nil {
		return "", false, err
	}
	if err = os.Link(output, backup); err != nil {
		return "", false, err
	}
	return backup, true, nil
}

func retrieveValidationArtifactWithOps(ctx context.Context, reference, output string, stdout io.Writer, read validationArtifactReader, createStage validationArtifactStager, fileOps validationArtifactFileOps) (returnErr error) {
	expectedDigest, err := validationArtifactReferenceDigest(reference)
	if err != nil {
		return err
	}
	tempDir := ""
	if output != "" {
		tempDir = filepath.Dir(output)
	}
	staged, err := createStage(tempDir, ".az-validation-artifact-*")
	if err != nil {
		return fmt.Errorf("stage validation artifact: %w", err)
	}
	stagedPath := staged.Name()
	defer func() {
		_ = staged.Close()
		_ = os.Remove(stagedPath)
	}()
	if err = staged.Chmod(0o600); err != nil {
		return fmt.Errorf("secure staged validation artifact: %w", err)
	}

	hash := sha256.New()
	var offset, totalSize int64
	var responseDigest string
	for {
		result, readErr := read(ctx, protocol.ValidationArtifactReadRequest{Reference: reference, Offset: offset})
		if readErr != nil {
			return readErr
		}
		if result.Reference != reference || result.Digest != "sha256:"+expectedDigest {
			return fmt.Errorf("validation artifact identity changed during retrieval")
		}
		if offset == 0 {
			totalSize, responseDigest = result.TotalSize, result.Digest
			if totalSize < 0 {
				return fmt.Errorf("validation artifact returned invalid total size")
			}
		} else if result.TotalSize != totalSize || result.Digest != responseDigest {
			return fmt.Errorf("validation artifact metadata changed during retrieval")
		}
		if result.Offset != offset || result.NextOffset != offset+int64(len(result.Content)) || result.NextOffset > totalSize {
			return fmt.Errorf("validation artifact returned non-contiguous chunk")
		}
		if _, err = io.MultiWriter(staged, hash).Write(result.Content); err != nil {
			return fmt.Errorf("write staged validation artifact: %w", err)
		}
		offset = result.NextOffset
		if result.Complete {
			if offset != totalSize {
				return fmt.Errorf("validation artifact completed at unexpected size")
			}
			break
		}
		if offset == result.Offset {
			return fmt.Errorf("validation artifact read made no progress")
		}
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != expectedDigest {
		return fmt.Errorf("validation artifact stream digest mismatch")
	}
	if err = staged.Sync(); err != nil {
		return fmt.Errorf("sync staged validation artifact: %w", err)
	}
	if err = staged.Close(); err != nil {
		return fmt.Errorf("close staged validation artifact: %w", err)
	}
	if output == "" {
		verified, openErr := os.Open(stagedPath)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(stdout, verified)
		closeErr := verified.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	dir, openErr := fileOps.openDirectory(filepath.Dir(output))
	if openErr != nil {
		return fmt.Errorf("open validation artifact destination directory: %w", openErr)
	}
	dirClosed := false
	defer func() {
		if !dirClosed {
			_ = dir.Close()
		}
	}()
	backupPath, hadDestination, backupErr := fileOps.backup(output)
	if backupErr != nil {
		return fmt.Errorf("backup validation artifact destination: %w", backupErr)
	}
	if backupPath != "" {
		defer func() { _ = fileOps.remove(backupPath) }()
	}
	rollbackPublish := func(operation string, cause error) error {
		if hadDestination {
			recoveryBackup := backupPath
			backupPath = ""
			if restoreErr := fileOps.rename(recoveryBackup, output); restoreErr != nil {
				return fmt.Errorf("%s: %w (restore destination: %v; recovery backup retained at %s)", operation, cause, restoreErr, recoveryBackup)
			}
			return fmt.Errorf("%s: %w", operation, cause)
		}
		if removeErr := fileOps.remove(output); removeErr != nil {
			return fmt.Errorf("%s: %w (remove published output: %v)", operation, cause, removeErr)
		}
		return fmt.Errorf("%s: %w", operation, cause)
	}
	if err = fileOps.rename(stagedPath, output); err != nil {
		return fmt.Errorf("publish validation artifact: %w", err)
	}
	if err = dir.Sync(); err != nil {
		return rollbackPublish("sync validation artifact destination", err)
	}
	if err = dir.Close(); err != nil {
		dirClosed = true
		return rollbackPublish("close validation artifact destination directory", err)
	}
	dirClosed = true
	if backupPath != "" {
		if err = fileOps.remove(backupPath); err != nil {
			return rollbackPublish("remove validation artifact destination backup", err)
		}
		backupPath = ""
	}
	stagedPath = ""
	return nil
}

func validationArtifactReferenceDigest(reference string) (string, error) {
	const prefix = "artifact:sha256/"
	digest := strings.TrimPrefix(reference, prefix)
	if prefix+digest != reference || len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("invalid validation artifact reference")
	}
	for _, char := range digest {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return "", fmt.Errorf("invalid validation artifact reference")
		}
	}
	return digest, nil
}

func validationReviewerID() string {
	for _, name := range []string{"AZEDARACH_REVIEWER_ID", "AZEDARACH_AUDIT_ACTOR", "USER", "LOGNAME"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func envInt64(name string) int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64)
	return value
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
		fmt.Printf("%s %s %s priority=%s order=%s scope=%s purpose=%s execution=%s source=%s override=%s ticket=%s\n", value.Request.RequestID, value.Request.State, value.Request.Profile, value.Request.IssuePriority.String(), value.Request.OrderingReason, value.Request.Scope, value.Request.Purpose, value.Request.Execution, value.Request.AuthoritativeRequestID, value.Request.Override, value.Request.IssueID)
		if value.Summary.FailureSummary != "" {
			fmt.Printf("failure=%s\n", value.Summary.FailureSummary)
		}
		fmt.Printf("context_schema=%s output_retention=%s artifacts=%d summary_schema=%s\n", value.Context.Schema, value.Summary.OutputRetention, len(value.Summary.ArtifactLinks), value.Summary.Schema)
	case domain.ValidationSnapshot:
		fmt.Printf("active=%d queued=%d revision=%d\n", len(value.Active), len(value.Queued), value.Revision)
		for _, request := range append(append(append([]domain.ValidationRequest{}, value.Active...), value.Queued...), value.Recent...) {
			fmt.Printf("%s %s class=%s priority=%s queue_position=%d order=%s bypasses=%d scope=%s purpose=%s execution=%s source=%s override=%s ticket=%s revision=%s\n", request.RequestID, request.State, request.Class, request.IssuePriority.String(), request.QueuePosition, request.OrderingReason, request.PriorityBypassCount, request.Scope, request.Purpose, request.Execution, request.AuthoritativeRequestID, request.Override, request.IssueID, request.SourceRevision)
		}
	case protocol.PublicationEvidenceRecordResponse:
		fmt.Printf("%s layer=%s ticket=%s source=%s base=%s result=%s policy=%s reused_from=%s\n", value.Evidence.EvidenceID, value.Evidence.Layer, value.Evidence.IssueID, value.Evidence.SourceRevision, value.Evidence.BaseRevision, value.Evidence.ResultRevision, value.Evidence.PolicyVersion, value.Evidence.ReusedFromEvidenceID)
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
	fmt.Println(strings.TrimSpace(`Usage: az validation <acquire|heartbeat|authorize-nested|finish|artifact-read|status|watch|evidence-record|evidence-status|evidence-evaluate> [flags]
  acquire --request <id> --token <secret> --scope repository|ticket --purpose capacity|development|push_gate|review_evidence --isolation <mode> --environment-fingerprint <hash> --override none|no_reuse|force_rerun|emergency_skip [--override-actor <id> --override-reason <text>] [--issue <id>] --class aggregate|shared|safe --profile <name> --command <text> --revision <sha> [--wait] [--json]
  heartbeat --request <id> --token <secret> [--ttl 30]
  authorize-nested --request <id> --token <secret> --class aggregate|shared|safe
  finish --request <id> --token <secret> --state completed|cancelled|failed [--outcome text] [--evidence-json object] [--json]
  artifact-read --reference artifact:sha256/<digest> [--output <path>]
  evidence-record --id <id> --issue <id> --layer patch_review|active_path --validation-request <id> [--reused-from <id>] [--json]
  evidence-status [--issue <id>] [--json]
  evidence-evaluate --issue <id> [--json]
  status [--json]
  watch [--interval 1s] [--json]`))
}
