package git

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	integrationJournalVersionV1 = 1
	integrationJournalVersionV2 = 2
	integrationJournalVersion   = integrationJournalVersionV2
	integrationReceiptVersion   = 1
)

const (
	integrationScratchPrefix          = "azedarach-integration-"
	integrationScratchPattern         = integrationScratchPrefix + "*"
	integrationFailureArtifactsPrefix = "azedarach-integration-failure-"
)

type integrationFailureArtifactPathsKey struct{}

// WithIntegrationFailureArtifactPaths configures project-owned relative
// directories that may be preserved from a failed transactional merge.
func WithIntegrationFailureArtifactPaths(ctx context.Context, paths []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	cleaned := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || !filepath.IsLocal(path) {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		cleaned = append(cleaned, path)
	}
	if len(cleaned) == 0 {
		return ctx
	}
	return context.WithValue(ctx, integrationFailureArtifactPathsKey{}, cleaned)
}

func integrationFailureArtifactPaths(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	paths, _ := ctx.Value(integrationFailureArtifactPathsKey{}).([]string)
	return paths
}

type integrationJournal struct {
	Version         int                         `json:"version"`
	TargetWorktree  string                      `json:"target_worktree"`
	TargetHead      string                      `json:"target_head"`
	DesiredHead     string                      `json:"desired_head"`
	ScratchWorktree string                      `json:"scratch_worktree,omitempty"`
	ScratchOwner    integrationScratchOwnership `json:"scratch_owner"`
	Validation      CandidateValidationAttempt  `json:"validation"`
	StartedAt       time.Time                   `json:"started_at"`
}

type integrationScratchOwnership struct {
	AttemptID       string `json:"attempt_id"`
	TargetWorktree  string `json:"target_worktree"`
	GitCommonDir    string `json:"git_common_dir"`
	ScratchWorktree string `json:"scratch_worktree"`
}

type integrationValidationReceipt struct {
	Version        int                        `json:"version"`
	TargetWorktree string                     `json:"target_worktree"`
	CandidateHead  string                     `json:"candidate_head"`
	Validation     CandidateValidationAttempt `json:"validation"`
	AppliedAt      time.Time                  `json:"applied_at"`
}

// IntegrationTargetStaleError reports that the target HEAD observed under the
// integration transaction lock no longer matches the caller's publication
// identity. No scratch candidate has been created when this error is returned.
type IntegrationTargetStaleError struct {
	ExpectedHead string
	ActualHead   string
}

// IntegrationTargetBranchStaleError reports that the target worktree is no
// longer attached to the authoritative branch selected by daemon policy.
type IntegrationTargetBranchStaleError struct {
	ExpectedBranch string
	ActualBranch   string
}

func (e *IntegrationTargetBranchStaleError) Error() string {
	return fmt.Sprintf("integration target branch changed: expected=%s actual=%s", e.ExpectedBranch, e.ActualBranch)
}

func (e *IntegrationTargetStaleError) Error() string {
	return fmt.Sprintf("integration target HEAD changed before candidate creation: expected=%s actual=%s", e.ExpectedHead, e.ActualHead)
}

// MergeCleanlyTransactional performs the merge in a disposable worktree and only
// locks the target worktree for the base snapshot and final publication phases.
func (c *Client) MergeCleanlyTransactional(ctx context.Context, worktree, branch string) (*MergeResult, error) {
	return c.mergeCleanlyTransactional(ctx, worktree, branch, true, "", "", false)
}

// MergeCleanlyTransactionalAtBase binds candidate creation to expectedBase.
// The comparison is made under the same integration transaction lock as the
// target HEAD snapshot, closing the gap between daemon preflight and scratch
// creation.
func (c *Client) MergeCleanlyTransactionalAtBase(ctx context.Context, worktree, branch, expectedBase string) (*MergeResult, error) {
	return c.mergeCleanlyTransactional(ctx, worktree, branch, true, strings.TrimSpace(expectedBase), "", false)
}

// MergeCleanlyTransactionalAtTarget binds publication to the exact target HEAD
// and attached branch selected by daemon authority.
func (c *Client) MergeCleanlyTransactionalAtTarget(ctx context.Context, worktree, branch, expectedBase, expectedTargetBranch string) (*MergeResult, error) {
	return c.mergeCleanlyTransactional(ctx, worktree, branch, true, strings.TrimSpace(expectedBase), strings.TrimSpace(expectedTargetBranch), false)
}

// MergeCleanlyTransactionalComposition performs exact clean conflict-safe
// compare/apply composition without invoking repository publication validation.
// It is reserved for typed non-base integration targets.
func (c *Client) MergeCleanlyTransactionalComposition(ctx context.Context, worktree, branch string) (*MergeResult, error) {
	return c.mergeCleanlyTransactional(ctx, worktree, branch, false, "", "", true)
}

// MergeCleanlyTransactionalCompositionAtBase binds no-ff composition to the
// exact target identity resolved by daemon authority.
func (c *Client) MergeCleanlyTransactionalCompositionAtBase(ctx context.Context, worktree, branch, expectedBase string) (*MergeResult, error) {
	return c.mergeCleanlyTransactional(ctx, worktree, branch, false, strings.TrimSpace(expectedBase), "", true)
}

// MergeCleanlyTransactionalCompositionAtTarget binds no-ff composition to the
// exact target HEAD and attached branch selected by daemon authority.
func (c *Client) MergeCleanlyTransactionalCompositionAtTarget(ctx context.Context, worktree, branch, expectedBase, expectedTargetBranch string) (*MergeResult, error) {
	return c.mergeCleanlyTransactional(ctx, worktree, branch, false, strings.TrimSpace(expectedBase), strings.TrimSpace(expectedTargetBranch), true)
}

func (c *Client) mergeCleanlyTransactional(ctx context.Context, worktree, branch string, validatePublication bool, expectedBase, expectedTargetBranch string, forceNoFF bool) (*MergeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	worktree = strings.TrimSpace(worktree)
	branch = strings.TrimSpace(branch)
	if worktree == "" {
		return nil, fmt.Errorf("target worktree is required")
	}
	if branch == "" {
		return nil, fmt.Errorf("source branch is required")
	}
	absoluteWorktree, err := filepath.Abs(worktree)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute target worktree: %w", err)
	}
	worktree = filepath.Clean(absoluteWorktree)
	info, err := os.Stat(worktree)
	if err != nil {
		return nil, fmt.Errorf("inspect canonical target worktree: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("canonical target worktree is not a directory: %s", worktree)
	}
	var targetHead string
	var earlyResult *MergeResult
	if err := c.withIntegrationTransactionLock(ctx, worktree, func(ctx context.Context) error {
		if err := c.recoverIntegrationJournalLocked(ctx, worktree); err != nil {
			return fmt.Errorf("recover interrupted integration: %w", err)
		}

		targetStatus, err := c.Status(ctx, worktree)
		if err != nil {
			return fmt.Errorf("inspect target status before transactional merge: %w", err)
		}
		if gitStatusDirty(targetStatus) {
			earlyResult = &MergeResult{
				Success: false,
				Message: fmt.Sprintf("target worktree is dirty before transactional merge; leaving files untouched: %s", gitStatusSummary(targetStatus)),
			}
			return nil
		}
		if err := c.requireIntegrationTargetBranch(ctx, worktree, expectedTargetBranch); err != nil {
			return err
		}

		targetHead, err = c.revParseVerify(ctx, worktree, "HEAD")
		if err != nil {
			return fmt.Errorf("resolve target HEAD before transactional merge: %w", err)
		}
		if expectedBase != "" && targetHead != expectedBase {
			return &IntegrationTargetStaleError{ExpectedHead: expectedBase, ActualHead: targetHead}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if earlyResult != nil {
		return earlyResult, nil
	}
	scratchPath, err := os.MkdirTemp("", integrationScratchPattern)
	if err != nil {
		return nil, fmt.Errorf("create scratch integration directory: %w", err)
	}
	if err := os.Remove(scratchPath); err != nil {
		_ = os.RemoveAll(scratchPath)
		return nil, fmt.Errorf("prepare scratch integration directory: %w", err)
	}
	scratchAdded := false
	scratchCleanupHandled := false
	defer func() {
		if scratchCleanupHandled {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeCleanupTimeout)
		defer cancel()
		if _, retained, err := c.existingIntegrationJournalPath(worktree); err != nil || retained {
			if c.logger != nil {
				c.logger.Warn("preserving scratch integration worktree owned by durable journal", "path", scratchPath, "journal_retained", retained, "error", err)
			}
			return
		}
		if scratchAdded {
			if err := c.removeWorktree(cleanupCtx, worktree, scratchPath); err != nil && c.logger != nil {
				c.logger.Warn("failed to remove scratch integration worktree with git", "path", scratchPath, "error", err)
			}
		}
		if err := os.RemoveAll(scratchPath); err != nil && c.logger != nil {
			c.logger.Warn("failed to remove scratch integration path", "path", scratchPath, "error", err)
		}
	}()

	if err := c.addDetachedWorktree(ctx, worktree, scratchPath, targetHead); err != nil {
		return nil, fmt.Errorf("create scratch integration worktree: %w", err)
	}
	scratchAdded = true
	scratchOwner, err := c.createIntegrationScratchOwnership(ctx, worktree, scratchPath)
	if err != nil {
		return nil, fmt.Errorf("persist scratch integration ownership: %w", err)
	}

	result, err := c.mergeCleanlyWithEnv(ctx, scratchPath, branch, []string{"AZEDARACH_SKIP_MERGE_REBASE_GATE=1"}, forceNoFF)
	if err != nil {
		return nil, fmt.Errorf("scratch merge %s: %w", branch, err)
	}
	if result == nil {
		return nil, fmt.Errorf("scratch merge %s returned no result", branch)
	}
	if !result.Success {
		return result, nil
	}
	scratchStatus, err := c.Status(ctx, scratchPath)
	if err != nil {
		return nil, fmt.Errorf("inspect scratch status after merge: %w", err)
	}
	if gitStatusDirty(scratchStatus) {
		return &MergeResult{
			Success: false,
			Message: fmt.Sprintf("scratch merge completed but scratch worktree is dirty; target left untouched: %s", gitStatusSummary(scratchStatus)),
		}, nil
	}
	desiredHead, err := c.revParseVerify(ctx, scratchPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve scratch merge HEAD: %w", err)
	}
	if desiredHead == targetHead {
		return result, nil
	}
	if validatePublication {
		attempt, configured, validationErr := c.validateIntegrationCandidate(ctx, worktree, scratchPath, desiredHead)
		if configured {
			result.ValidationAttempts = append(result.ValidationAttempts, attempt)
		}
		if validationErr != nil {
			if errors.Is(validationErr, context.Canceled) || errors.Is(validationErr, context.DeadlineExceeded) {
				// Cancellation is not a completed gate failure. Return the context
				// error and let deferred scratch cleanup discard partial output.
				return nil, validationErr
			}
			result.Success = false
			result.Message = appendMergeResultDetail(result.Message, validationErr.Error())
			if preservedArtifacts := c.preserveCandidateValidationFailureArtifacts(ctx, scratchPath, result, validationErr); preservedArtifacts != "" {
				result.Message = appendMergeResultDetail(result.Message, "preserved integration failure artifacts at "+preservedArtifacts)
			}
			return result, nil
		}
	}

	applied, cleanupHandled, err := c.applyValidatedScratchMerge(ctx, worktree, scratchPath, targetHead, desiredHead, expectedTargetBranch, scratchOwner, result)
	scratchCleanupHandled = cleanupHandled
	return applied, err
}

func (c *Client) preserveIntegrationFailureArtifacts(ctx context.Context, scratchPath string, result *MergeResult, mergeErr error) string {
	if ctx != nil && ctx.Err() != nil {
		return ""
	}
	if mergeErr == nil && (result == nil || result.Success) {
		return ""
	}
	if !strings.HasPrefix(filepath.Base(scratchPath), integrationScratchPrefix) {
		return ""
	}
	configuredPaths := integrationFailureArtifactPaths(ctx)
	if len(configuredPaths) == 0 {
		return ""
	}
	evaluatedScratch, err := filepath.EvalSymlinks(scratchPath)
	if err != nil {
		return ""
	}
	type artifactSource struct {
		path     string
		relative string
	}
	sources := make([]artifactSource, 0, len(configuredPaths))
	for _, relative := range configuredPaths {
		source := filepath.Join(scratchPath, relative)
		info, statErr := os.Stat(source)
		if statErr != nil || !info.IsDir() {
			continue
		}
		evaluatedSource, evalErr := filepath.EvalSymlinks(source)
		if evalErr != nil || !pathWithinRoot(evaluatedScratch, evaluatedSource) {
			if c.logger != nil {
				c.logger.Warn("refusing integration failure artifact path outside scratch worktree", "source", source, "error", evalErr)
			}
			continue
		}
		sources = append(sources, artifactSource{path: evaluatedSource, relative: relative})
	}
	if len(sources) == 0 {
		return ""
	}
	destination, err := os.MkdirTemp(c.artifactFailureTempDir, integrationFailureArtifactsPrefix)
	if err != nil {
		if c.logger != nil {
			c.logger.Warn("failed to create integration failure artifact directory", "error", err)
		}
		return ""
	}
	for _, source := range sources {
		target := filepath.Join(destination, source.relative)
		if err := c.copyDirectory(ctx, source.path, target); err != nil {
			_ = os.RemoveAll(destination)
			if c.logger != nil {
				c.logger.Warn("failed to preserve integration failure artifacts", "source", source.path, "error", err)
			}
			return ""
		}
	}
	return destination
}

func (c *Client) preserveCandidateValidationFailureArtifacts(ctx context.Context, scratchPath string, result *MergeResult, validationErr error) string {
	if errors.Is(validationErr, context.Canceled) || errors.Is(validationErr, context.DeadlineExceeded) {
		return ""
	}
	return c.preserveIntegrationFailureArtifacts(ctx, scratchPath, result, validationErr)
}

func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}

func (c *Client) copyDirectory(ctx context.Context, source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("resolve artifact path %s: %w", path, err)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create artifact directory %s: %w", target, err)
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		return c.copyArtifactFile(ctx, path, target)
	})
}

func (c *Client) copyArtifactFile(ctx context.Context, source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open artifact %s: %w", source, err)
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		_ = input.Close()
		return fmt.Errorf("create preserved artifact %s: %w", destination, err)
	}
	buffer := make([]byte, 32*1024)
	var copyErr error
	for copyErr == nil {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				copyErr = err
				break
			}
		}
		readCount, readErr := input.Read(buffer)
		for written := 0; written < readCount; {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					copyErr = err
					break
				}
			}
			writeCount, writeErr := output.Write(buffer[written:readCount])
			if writeErr != nil {
				copyErr = writeErr
				break
			}
			if writeCount == 0 {
				copyErr = io.ErrShortWrite
				break
			}
			written += writeCount
			if c.artifactCopyChunk != nil {
				c.artifactCopyChunk(source, writeCount)
			}
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					copyErr = err
					break
				}
			}
		}
		if copyErr != nil {
			break
		}
		switch {
		case errors.Is(readErr, io.EOF):
			copyErr = nil
			goto copyComplete
		case readErr != nil:
			copyErr = readErr
		}
	}

copyComplete:
	inputCloseErr := input.Close()
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("copy artifact %s: %w", source, copyErr)
	}
	if inputCloseErr != nil {
		return fmt.Errorf("close artifact %s: %w", source, inputCloseErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close preserved artifact %s: %w", destination, closeErr)
	}
	return nil
}

func (c *Client) requireIntegrationTargetBranch(ctx context.Context, worktree, expectedBranch string) error {
	expectedBranch = strings.TrimSpace(expectedBranch)
	if expectedBranch == "" {
		return nil
	}
	actualBranch, err := c.CurrentBranch(ctx, worktree)
	if err != nil {
		return fmt.Errorf("resolve integration target branch: %w", err)
	}
	actualBranch = strings.TrimSpace(actualBranch)
	if actualBranch != expectedBranch {
		return &IntegrationTargetBranchStaleError{ExpectedBranch: expectedBranch, ActualBranch: actualBranch}
	}
	return nil
}

func (c *Client) validateIntegrationCandidate(ctx context.Context, gateRoot, scratchPath, candidateHead string) (CandidateValidationAttempt, bool, error) {
	if !filepath.IsAbs(gateRoot) {
		return CandidateValidationAttempt{}, false, fmt.Errorf("target gate authority path must be absolute: %s", gateRoot)
	}
	gateRoot = filepath.Clean(gateRoot)
	configuredCommand, _ := ctx.Value(candidateValidationCommandKey{}).(string)
	configuredCommand = strings.TrimSpace(configuredCommand)
	gatePath := filepath.Join(gateRoot, "scripts", "git-merge-rebase-gate.sh")
	attempt := CandidateValidationAttempt{
		CandidateHead: candidateHead,
		Status:        CandidateValidationRunning,
		Canonical:     false,
	}
	if ticketID, ticketScoped := ctx.Value(candidateValidationTicketKey{}).(naming.TicketID); ticketScoped {
		if _, err := naming.ParseTicketID(ticketID.String()); err != nil {
			attempt.Status = CandidateValidationFailed
			attempt.Message = "ticket-scoped candidate validation requires a valid durable ticket identity"
			notifyCandidateValidation(ctx, attempt)
			return attempt, true, fmt.Errorf("%s: %w", attempt.Message, err)
		}
	}
	if configuredCommand == "" {
		if _, err := os.Stat(gatePath); err != nil {
			if os.IsNotExist(err) {
				return CandidateValidationAttempt{}, false, nil
			}
			attempt.Status = CandidateValidationFailed
			attempt.Message = "candidate validation gate could not be inspected"
			notifyCandidateValidation(ctx, attempt)
			return attempt, true, fmt.Errorf("inspect candidate validation gate: %w", err)
		}
	}
	notifyCandidateValidation(ctx, attempt)

	actualHead, err := c.revParseVerify(ctx, scratchPath, "HEAD")
	if err != nil {
		attempt.Status = CandidateValidationFailed
		attempt.Message = "candidate HEAD could not be resolved before validation"
		notifyCandidateValidation(ctx, attempt)
		return attempt, true, fmt.Errorf("resolve candidate HEAD before validation: %w", err)
	}
	if actualHead != candidateHead {
		attempt.Status = CandidateValidationSuperseded
		attempt.Message = fmt.Sprintf("candidate HEAD changed before validation: expected %s, found %s", candidateHead, actualHead)
		notifyCandidateValidation(ctx, attempt)
		return attempt, true, errors.New(attempt.Message)
	}
	status, err := c.Status(ctx, scratchPath)
	if err != nil {
		attempt.Status = CandidateValidationFailed
		attempt.Message = "candidate status could not be read before validation"
		notifyCandidateValidation(ctx, attempt)
		return attempt, true, fmt.Errorf("inspect candidate status before validation: %w", err)
	}
	if gitStatusDirty(status) {
		attempt.Status = CandidateValidationFailed
		attempt.Message = "candidate worktree was dirty before validation: " + gitStatusSummary(status)
		notifyCandidateValidation(ctx, attempt)
		return attempt, true, errors.New(attempt.Message)
	}
	var finishAdmission func(CandidateValidationAttempt) error
	if admission, _ := ctx.Value(candidateValidationAdmissionKey{}).(CandidateValidationAdmission); admission != nil {
		reused, finish, admissionErr := admission(ctx, candidateHead)
		if admissionErr != nil {
			attempt.Status = CandidateValidationFailed
			attempt.Message = "candidate validation admission failed: " + admissionErr.Error()
			notifyCandidateValidation(ctx, attempt)
			return attempt, true, errors.New(attempt.Message)
		}
		finishAdmission = finish
		if reused {
			attempt.Status = CandidateValidationPassed
			attempt.Message = "compatible exact candidate validation reused; awaiting exact apply"
			notifyCandidateValidation(ctx, attempt)
			return attempt, true, nil
		}
	}

	env := gitEnvWithOverrides(sanitizedGitEnv(os.Environ()), []string{
		"AZEDARACH_CANDIDATE_HEAD=" + candidateHead,
		"AZEDARACH_MERGE_GATE_BODY=" + filepath.Join(gateRoot, "scripts", "git-merge-rebase-gate-body.sh"),
		"AZEDARACH_SKIP_MERGE_REBASE_GATE=0",
	})
	env = candidateValidationTicketEnv(env, ctx)
	var stdout, stderr string
	var runErr error
	if stages, ok := ctx.Value(candidateValidationDAGKey{}).([]CandidateValidationStage); ok {
		var dagResult CandidateValidationDAGResult
		dagResult, runErr = runCandidateValidationDAG(ctx, scratchPath, env, stages)
		attempt.Stages = append([]CandidateValidationStageResult(nil), dagResult.Stages...)
		parts := make([]string, 0, len(dagResult.Stages))
		for _, stage := range dagResult.Stages {
			parts = append(parts, fmt.Sprintf("%s=%s", stage.ID, stage.Status))
		}
		stdout = strings.Join(parts, ", ")
	} else if configuredCommand != "" {
		stdout, stderr, runErr = runProcessGroupCommand(ctx, scratchPath, env, "/bin/sh", "-lc", configuredCommand)
	} else {
		stdout, stderr, runErr = runProcessGroupCommand(ctx, scratchPath, env, gatePath)
	}
	if runErr != nil {
		attempt.Status = CandidateValidationFailed
		if ctxErr := ctx.Err(); ctxErr != nil {
			attempt.Status = CandidateValidationCancelled
			attempt.Message = "candidate validation cancelled; evidence is noncanonical"
			notifyCandidateValidation(ctx, attempt)
			if finishAdmission != nil {
				_ = finishAdmission(attempt)
			}
			return attempt, true, fmt.Errorf("validate candidate %s: %w", candidateHead, ctxErr)
		}
		detail := candidateValidationOutput(stdout, stderr)
		detail = boundedCandidateValidationDetail(detail)
		attempt.Message = "candidate validation failed"
		if detail != "" {
			attempt.Message += ": " + detail
		}
		notifyCandidateValidation(ctx, attempt)
		if finishAdmission != nil {
			if finishErr := finishAdmission(attempt); finishErr != nil {
				return attempt, true, fmt.Errorf("record failed candidate validation: %w", finishErr)
			}
		}
		return attempt, true, fmt.Errorf("validate candidate %s: %s", candidateHead, attempt.Message)
	}

	actualHead, err = c.revParseVerify(ctx, scratchPath, "HEAD")
	if err != nil || actualHead != candidateHead {
		attempt.Status = CandidateValidationSuperseded
		attempt.Message = fmt.Sprintf("candidate HEAD changed during validation: expected %s, found %s", candidateHead, actualHead)
		notifyCandidateValidation(ctx, attempt)
		if finishAdmission != nil {
			_ = finishAdmission(attempt)
		}
		if err != nil {
			return attempt, true, fmt.Errorf("resolve candidate HEAD after validation: %w", err)
		}
		return attempt, true, errors.New(attempt.Message)
	}
	status, err = c.Status(ctx, scratchPath)
	if err != nil || gitStatusDirty(status) {
		attempt.Status = CandidateValidationFailed
		attempt.Message = "candidate worktree was not clean after validation"
		notifyCandidateValidation(ctx, attempt)
		if finishAdmission != nil {
			_ = finishAdmission(attempt)
		}
		if err != nil {
			return attempt, true, fmt.Errorf("inspect candidate status after validation: %w", err)
		}
		return attempt, true, fmt.Errorf("%s: %s", attempt.Message, gitStatusSummary(status))
	}
	attempt.Status = CandidateValidationPassed
	attempt.Message = "candidate validation passed; awaiting exact apply"
	if finishAdmission != nil {
		if finishErr := finishAdmission(attempt); finishErr != nil {
			attempt.Status = CandidateValidationFailed
			attempt.Message = "candidate validation passed but durable evidence recording failed: " + finishErr.Error()
			notifyCandidateValidation(ctx, attempt)
			return attempt, true, errors.New(attempt.Message)
		}
	}
	// Publish "passed" only after the daemon has durably completed the exact
	// validation lease. A crash after this notification can then recover by
	// reusing completed evidence instead of executing the same gate again.
	notifyCandidateValidation(ctx, attempt)
	return attempt, true, nil
}

func candidateValidationTicketEnv(env []string, ctx context.Context) []string {
	filtered := make([]string, 0, len(env)+2)
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (key == "AZEDARACH_TICKET_ID" || key == "AZEDARACH_ISSUE_ID" || strings.HasPrefix(key, "AZEDARACH_REVIEW_") || key == "AZEDARACH_REVIEWER_ID" || key == "AZEDARACH_REVIEWER_KIND" || key == "AZEDARACH_PUBLICATION_OPERATION_ID" || key == "AZEDARACH_ACCEPTED_REVIEW_EVENT_ID" || key == "AZEDARACH_ACCEPTED_PUBLICATION_OPERATION_ID") {
			continue
		}
		filtered = append(filtered, entry)
	}
	ticketID, ticketScoped := ctx.Value(candidateValidationTicketKey{}).(naming.TicketID)
	if !ticketScoped {
		return filtered
	}
	identity := strings.TrimSpace(ticketID.String())
	if identity == "" {
		return gitEnvWithOverrides(filtered, []string{
			"AZEDARACH_TICKET_ID=",
			"AZEDARACH_ISSUE_ID=",
		})
	}
	overrides := []string{
		"AZEDARACH_TICKET_ID=" + identity,
		"AZEDARACH_ISSUE_ID=" + identity,
	}
	if authority, ok := ctx.Value(candidateValidationReviewAuthorityKey{}).(CandidateValidationReviewAuthority); ok {
		overrides = append(overrides,
			"AZEDARACH_REVIEWER_ID="+authority.ReviewerID,
			"AZEDARACH_REVIEWER_KIND="+authority.ReviewerKind,
			fmt.Sprintf("AZEDARACH_REVIEW_EPOCH_EVENT_ID=%d", authority.ReviewEpochEventID),
			"AZEDARACH_PUBLICATION_OPERATION_ID="+authority.PublicationOperationID,
			fmt.Sprintf("AZEDARACH_ACCEPTED_REVIEW_EVENT_ID=%d", authority.AcceptedReviewEventID),
			"AZEDARACH_ACCEPTED_PUBLICATION_OPERATION_ID="+authority.AcceptedPublicationOperationID,
		)
	}
	return gitEnvWithOverrides(filtered, overrides)
}

func candidateValidationOutput(stdout, stderr string) string {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + "\n" + stderr
	}
}

func boundedCandidateValidationDetail(detail string) string {
	// Keep the same practical envelope as durable validation failure summaries
	// so task.close can surface actionable output alongside configured artifacts.
	const maxRunes = 32 * 1024
	detail = strings.TrimSpace(detail)
	runes := []rune(detail)
	if len(runes) <= maxRunes {
		return detail
	}
	return "..." + string(runes[len(runes)-maxRunes:])
}

func setCandidateValidationDisposition(ctx context.Context, result *MergeResult, candidateHead string, status CandidateValidationStatus, canonical bool, message string) {
	if result == nil {
		return
	}
	for i := len(result.ValidationAttempts) - 1; i >= 0; i-- {
		attempt := &result.ValidationAttempts[i]
		if attempt.CandidateHead != candidateHead {
			continue
		}
		attempt.Status = status
		attempt.Canonical = canonical
		if strings.TrimSpace(message) != "" {
			attempt.Message = strings.TrimSpace(message)
		}
		notifyCandidateValidation(ctx, *attempt)
		return
	}
}

func (c *Client) applyValidatedScratchMerge(ctx context.Context, worktree, scratchPath, targetHead, desiredHead, expectedTargetBranch string, scratchOwner integrationScratchOwnership, result *MergeResult) (*MergeResult, bool, error) {
	var out *MergeResult
	cleanupHandled := false
	if err := c.withIntegrationTransactionLock(ctx, worktree, func(ctx context.Context) error {
		if err := c.recoverIntegrationJournalLocked(ctx, worktree); err != nil {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationFailed, false, "candidate apply recovery failed; evidence is noncanonical")
			return fmt.Errorf("recover interrupted integration before final apply: %w", err)
		}
		targetStatus, err := c.Status(ctx, worktree)
		if err != nil {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationFailed, false, "target status failed before candidate apply; evidence is noncanonical")
			return fmt.Errorf("inspect target status before final apply: %w", err)
		}
		if gitStatusDirty(targetStatus) {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationSuperseded, false, "target became dirty after candidate validation; evidence is noncanonical")
			out = &MergeResult{
				Success:            false,
				Message:            fmt.Sprintf("target worktree became dirty before final apply; target left untouched: %s", gitStatusSummary(targetStatus)),
				ValidationAttempts: append([]CandidateValidationAttempt(nil), result.ValidationAttempts...),
			}
			return nil
		}
		if err := c.requireIntegrationTargetBranch(ctx, worktree, expectedTargetBranch); err != nil {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationSuperseded, false, "target branch changed after candidate validation; evidence is noncanonical")
			return err
		}
		currentHead, err := c.revParseVerify(ctx, worktree, "HEAD")
		if err != nil {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationFailed, false, "target HEAD failed before candidate apply; evidence is noncanonical")
			return fmt.Errorf("resolve target HEAD before final apply: %w", err)
		}
		if currentHead != targetHead {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationSuperseded, false, "target HEAD moved after candidate validation; evidence is noncanonical")
			out = &MergeResult{
				Success:            false,
				Message:            fmt.Sprintf("target HEAD moved from %s to %s after scratch validation; retry integration with a fresh scratch merge", targetHead, currentHead),
				ValidationAttempts: append([]CandidateValidationAttempt(nil), result.ValidationAttempts...),
			}
			return nil
		}

		journal := integrationJournal{
			Version:         integrationJournalVersion,
			TargetWorktree:  worktree,
			TargetHead:      targetHead,
			DesiredHead:     desiredHead,
			ScratchWorktree: scratchPath,
			ScratchOwner:    scratchOwner,
			StartedAt:       time.Now().UTC(),
		}
		for i := len(result.ValidationAttempts) - 1; i >= 0; i-- {
			attempt := result.ValidationAttempts[i]
			if attempt.CandidateHead == desiredHead && attempt.Status == CandidateValidationPassed && !attempt.Canonical {
				journal.Validation = attempt
				break
			}
		}
		if err := c.writeIntegrationJournal(ctx, worktree, journal); err != nil {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationFailed, false, "candidate apply journal failed; evidence is noncanonical")
			return fmt.Errorf("write transactional merge journal: %w", err)
		}
		if err := c.resetHard(ctx, worktree, desiredHead); err != nil {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationFailed, false, "candidate apply failed; evidence is noncanonical")
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeCleanupTimeout)
			defer cancel()
			if recoverErr := c.recoverIntegrationJournalLocked(cleanupCtx, worktree); recoverErr != nil {
				return fmt.Errorf("apply transactional merge failed (%v); recovery failed: %w", err, recoverErr)
			}
			out = &MergeResult{
				Success:            false,
				Message:            fmt.Sprintf("transactional merge final apply failed; recovered target worktree: %v", err),
				ValidationAttempts: append([]CandidateValidationAttempt(nil), result.ValidationAttempts...),
			}
			return nil
		}
		postStatus, err := c.Status(ctx, worktree)
		if err != nil {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationFailed, false, "target status failed after candidate apply; evidence is noncanonical")
			return fmt.Errorf("inspect target status after final apply: %w", err)
		}
		if gitStatusDirty(postStatus) {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationFailed, false, "candidate apply left the target dirty; evidence is noncanonical")
			var recoverErr error
			out, recoverErr = c.recoverDirtyFinalApply(ctx, worktree, postStatus)
			if out != nil {
				out.ValidationAttempts = append([]CandidateValidationAttempt(nil), result.ValidationAttempts...)
			}
			return recoverErr
		}
		if journalHasExactValidation(journal) {
			if err := c.writeCanonicalIntegrationReceipt(ctx, worktree, journal); err != nil {
				return fmt.Errorf("persist canonical candidate validation receipt: %w", err)
			}
		}
		provenScratch, err := c.proveIntegrationScratchWorktree(ctx, worktree, scratchPath, desiredHead, scratchOwner)
		if err != nil {
			return fmt.Errorf("re-prove exact integration scratch ownership before completed cleanup; journal and scratch retained: %w", err)
		}
		if err := c.removeIntegrationJournal(ctx, worktree); err != nil {
			return fmt.Errorf("remove completed transactional merge journal; scratch retained: %w", err)
		}
		cleanupHandled = true
		if err := c.removeWorktree(ctx, worktree, provenScratch); err != nil && c.logger != nil {
			c.logger.Warn("failed to remove proven scratch after transactional integration completed", "path", provenScratch, "error", err)
		}
		setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationPassed, true, "candidate validation passed and the exact candidate was applied")
		out = result
		return nil
	}); err != nil {
		return nil, cleanupHandled, err
	}
	return out, cleanupHandled, nil
}

func (c *Client) recoverDirtyFinalApply(ctx context.Context, worktree string, postStatus *GitStatus) (*MergeResult, error) {
	dirtySummary := gitStatusSummary(postStatus)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeCleanupTimeout)
	defer cancel()
	if err := c.recoverIntegrationJournalLocked(cleanupCtx, worktree); err != nil {
		return nil, fmt.Errorf("transactional merge final apply left target dirty (%s); recovery failed: %w", dirtySummary, err)
	}
	recoveredStatus, err := c.Status(cleanupCtx, worktree)
	if err != nil {
		return nil, fmt.Errorf("transactional merge final apply left target dirty (%s); inspect recovered target: %w", dirtySummary, err)
	}
	if gitStatusDirty(recoveredStatus) {
		return &MergeResult{
			Success: false,
			Message: fmt.Sprintf("transactional merge final apply left target dirty after recovery: %s", gitStatusSummary(recoveredStatus)),
		}, nil
	}
	return &MergeResult{
		Success: false,
		Message: fmt.Sprintf("transactional merge final apply left target dirty; recovered target worktree: %s", dirtySummary),
	}, nil
}

// RecoverIntegrationJournal completes or rolls back the brief final-apply phase
// if the daemon was interrupted after a scratch merge succeeded.
func (c *Client) RecoverIntegrationJournal(ctx context.Context, worktree string) error {
	journal, _, found, err := c.readIntegrationJournal(ctx, worktree)
	if err != nil || !found {
		return err
	}
	if journal.Version != integrationJournalVersionV1 && journal.Version != integrationJournalVersionV2 {
		return fmt.Errorf("unsupported integration journal version %d", journal.Version)
	}
	if legacyIntegrationJournalNeedsOperatorRecovery(journal) {
		return fmt.Errorf("legacy integration journal v1 has no scratch ownership proof; journal and scratch retained for operator recovery")
	}
	return c.withIntegrationTransactionLock(ctx, worktree, func(ctx context.Context) error {
		return c.recoverIntegrationJournalLocked(ctx, worktree)
	})
}

func (c *Client) recoverIntegrationJournalLocked(ctx context.Context, worktree string) error {
	journal, journalPath, found, err := c.readIntegrationJournal(ctx, worktree)
	if err != nil || !found {
		return err
	}
	switch journal.Version {
	case integrationJournalVersionV1, integrationJournalVersionV2:
		// Supported. Version 1 has no durable validation proof and therefore
		// always follows the rollback path below.
	default:
		return fmt.Errorf("unsupported integration journal version %d", journal.Version)
	}
	if legacyIntegrationJournalNeedsOperatorRecovery(journal) {
		return fmt.Errorf("legacy integration journal v1 has no scratch ownership proof; journal and scratch retained for operator recovery")
	}
	targetHead := strings.TrimSpace(journal.TargetHead)
	desiredHead := strings.TrimSpace(journal.DesiredHead)
	if targetHead == "" || desiredHead == "" {
		return fmt.Errorf("integration journal missing target or desired head")
	}
	if normalizeWorktreeLockKey(journal.TargetWorktree) != normalizeWorktreeLockKey(worktree) {
		return fmt.Errorf("integration journal target %s does not match recovery target %s", journal.TargetWorktree, worktree)
	}
	if strings.TrimSpace(journal.ScratchWorktree) == "" {
		return fmt.Errorf("integration journal missing scratch worktree identity; journal retained")
	}
	provenScratch, err := c.proveIntegrationScratchWorktree(ctx, worktree, journal.ScratchWorktree, desiredHead, journal.ScratchOwner)
	if err != nil {
		return fmt.Errorf("prove integration scratch identity before recovery; journal and scratch retained: %w", err)
	}
	targetStatus, err := c.Status(ctx, worktree)
	if err != nil {
		return fmt.Errorf("inspect target before integration recovery: %w", err)
	}
	if gitStatusDirty(targetStatus) {
		return fmt.Errorf("target is dirty before integration recovery; journal and scratch retained: %s", gitStatusSummary(targetStatus))
	}

	currentHead, err := c.revParseVerify(ctx, worktree, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve current HEAD for integration recovery: %w", err)
	}
	recoverHead := targetHead
	validatedDesired := journalHasExactValidation(journal)
	if currentHead == desiredHead && validatedDesired {
		recoverHead = desiredHead
	} else if currentHead != targetHead {
		if currentHead != desiredHead {
			return fmt.Errorf("integration journal found but current HEAD %s is neither target %s nor desired %s", currentHead, targetHead, desiredHead)
		}
	}
	if err := c.resetHard(ctx, worktree, recoverHead); err != nil {
		return fmt.Errorf("reset target during integration recovery: %w", err)
	}
	if recoverHead == desiredHead {
		status, err := c.Status(ctx, worktree)
		if err != nil {
			return fmt.Errorf("inspect recovered candidate before canonical publication: %w", err)
		}
		if gitStatusDirty(status) {
			return fmt.Errorf("recovered candidate is dirty before rollback; journal and scratch retained without destructive reset: %s", gitStatusSummary(status))
		} else if err := c.writeCanonicalIntegrationReceipt(ctx, worktree, journal); err != nil {
			return fmt.Errorf("persist recovered canonical candidate validation receipt: %w", err)
		}
	}
	cleanupScratch, err := c.proveIntegrationScratchWorktree(ctx, worktree, journal.ScratchWorktree, desiredHead, journal.ScratchOwner)
	if err != nil || cleanupScratch != provenScratch {
		if err == nil {
			err = fmt.Errorf("scratch identity changed from %s to %s", provenScratch, cleanupScratch)
		}
		return fmt.Errorf("re-prove exact integration scratch ownership before recovery cleanup; journal and scratch retained: %w", err)
	}
	if err := c.removeIntegrationJournalAtPath(journalPath); err != nil {
		return fmt.Errorf("remove recovered integration journal; scratch retained: %w", err)
	}
	if provenScratch != "" {
		if err := c.removeWorktree(ctx, worktree, provenScratch); err != nil && c.logger != nil {
			c.logger.Warn("failed to remove proven scratch after durable recovery completed", "path", provenScratch, "error", err)
		}
	}
	if c.logger != nil {
		c.logger.Warn("recovered interrupted transactional integration",
			"worktree", worktree,
			"target_head", targetHead,
			"desired_head", desiredHead,
			"recovered_head", recoverHead,
		)
	}
	return nil
}

func legacyIntegrationJournalNeedsOperatorRecovery(journal integrationJournal) bool {
	return journal.Version == integrationJournalVersionV1 && strings.TrimSpace(journal.ScratchOwner.AttemptID) == ""
}

func (c *Client) proveIntegrationScratchWorktree(ctx context.Context, worktree, scratchPath, desiredHead string, owner integrationScratchOwnership) (string, error) {
	scratchPath = normalizeWorktreeLockKey(scratchPath)
	tempRoot := normalizeWorktreeLockKey(os.TempDir())
	scratchParent := normalizeWorktreeLockKey(filepath.Dir(scratchPath))
	if scratchParent != tempRoot || !strings.HasPrefix(filepath.Base(scratchPath), integrationScratchPrefix) {
		return "", fmt.Errorf("scratch path %s is outside the managed integration temp namespace", scratchPath)
	}
	if scratchPath == normalizeWorktreeLockKey(worktree) {
		return "", fmt.Errorf("scratch path resolves to target worktree %s", worktree)
	}
	output, err := c.runInWorktree(ctx, worktree, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("list registered worktrees: %w", err)
	}
	for _, entry := range parseWorktreeEntries(output) {
		if normalizeWorktreeLockKey(entry.Path) != scratchPath {
			continue
		}
		if strings.TrimSpace(entry.Head) != strings.TrimSpace(desiredHead) {
			return "", fmt.Errorf("registered scratch HEAD %s does not match desired HEAD %s", entry.Head, desiredHead)
		}
		if !entry.Detached || entry.Branch != "" || entry.Locked || entry.Prunable {
			return "", fmt.Errorf("registered scratch does not have disposable detached identity")
		}
		if err := c.validateIntegrationScratchOwnership(ctx, worktree, scratchPath, owner); err != nil {
			return "", err
		}
		return scratchPath, nil
	}
	return "", fmt.Errorf("scratch path %s is not a registered worktree", scratchPath)
}

func (c *Client) createIntegrationScratchOwnership(ctx context.Context, worktree, scratchPath string) (integrationScratchOwnership, error) {
	commonDir, err := c.gitCommonDir(ctx, worktree)
	if err != nil {
		return integrationScratchOwnership{}, fmt.Errorf("resolve Git common directory: %w", err)
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return integrationScratchOwnership{}, fmt.Errorf("generate ownership attempt ID: %w", err)
	}
	owner := integrationScratchOwnership{
		AttemptID:       hex.EncodeToString(token),
		TargetWorktree:  normalizeWorktreeLockKey(worktree),
		GitCommonDir:    normalizeWorktreeLockKey(commonDir),
		ScratchWorktree: normalizeWorktreeLockKey(scratchPath),
	}
	path, err := c.integrationScratchOwnershipPath(ctx, scratchPath, commonDir)
	if err != nil {
		return integrationScratchOwnership{}, err
	}
	payload, err := json.MarshalIndent(owner, "", "  ")
	if err != nil {
		return integrationScratchOwnership{}, err
	}
	if err := writeFileAtomic(path, append(payload, '\n'), 0o600); err != nil {
		return integrationScratchOwnership{}, err
	}
	return owner, nil
}

func (c *Client) validateIntegrationScratchOwnership(ctx context.Context, worktree, scratchPath string, owner integrationScratchOwnership) error {
	commonDir, err := c.gitCommonDir(ctx, worktree)
	if err != nil {
		return fmt.Errorf("resolve recovery Git common directory: %w", err)
	}
	if strings.TrimSpace(owner.AttemptID) == "" ||
		normalizeWorktreeLockKey(owner.TargetWorktree) != normalizeWorktreeLockKey(worktree) ||
		normalizeWorktreeLockKey(owner.GitCommonDir) != normalizeWorktreeLockKey(commonDir) ||
		normalizeWorktreeLockKey(owner.ScratchWorktree) != normalizeWorktreeLockKey(scratchPath) {
		return fmt.Errorf("journal scratch ownership is missing or does not match target/common-dir/scratch identity")
	}
	path, err := c.integrationScratchOwnershipPath(ctx, scratchPath, commonDir)
	if err != nil {
		return err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read registered scratch ownership marker: %w", err)
	}
	var marker integrationScratchOwnership
	if err := json.Unmarshal(payload, &marker); err != nil {
		return fmt.Errorf("decode registered scratch ownership marker: %w", err)
	}
	if marker != owner {
		return fmt.Errorf("registered scratch ownership marker does not match journal attempt")
	}
	return nil
}

func (c *Client) integrationScratchOwnershipPath(ctx context.Context, scratchPath, commonDir string) (string, error) {
	gitDir, err := c.runInWorktree(ctx, scratchPath, "rev-parse", "--git-dir")
	if err != nil {
		return "", fmt.Errorf("resolve scratch Git directory: %w", err)
	}
	gitDir = strings.TrimSpace(gitDir)
	if gitDir == "" {
		return "", fmt.Errorf("empty scratch Git directory")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(scratchPath, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	worktreeAdminRoot := filepath.Join(normalizeWorktreeLockKey(commonDir), "worktrees")
	canonicalGitDir := normalizeWorktreeLockKey(gitDir)
	adminName := ""
	if filepath.Dir(canonicalGitDir) == worktreeAdminRoot {
		adminName = filepath.Base(canonicalGitDir)
	} else {
		lexicalAdminRoot := filepath.Join(filepath.Clean(commonDir), "worktrees")
		relativeName, relativeErr := filepath.Rel(lexicalAdminRoot, gitDir)
		if relativeErr != nil || relativeName == "." || filepath.Dir(relativeName) != "." {
			return "", fmt.Errorf("scratch Git directory %s is not a direct child of common-directory worktree administration %s", gitDir, worktreeAdminRoot)
		}
		adminName = relativeName
	}
	gitDir = filepath.Join(worktreeAdminRoot, adminName)
	return filepath.Join(gitDir, "azedarach-integration-owner.json"), nil
}

// CanonicalIntegrationValidation returns durable proof only when the exact
// candidate OID was validated and successfully applied to the target.
func (c *Client) CanonicalIntegrationValidation(ctx context.Context, worktree, candidateHead string) (CandidateValidationAttempt, bool, error) {
	path, found, err := c.existingIntegrationReceiptPath(worktree)
	if err != nil {
		return CandidateValidationAttempt{}, false, err
	}
	if !found {
		return CandidateValidationAttempt{}, false, nil
	}
	payload, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return CandidateValidationAttempt{}, false, nil
	}
	if err != nil {
		return CandidateValidationAttempt{}, false, err
	}
	var receipt integrationValidationReceipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		return CandidateValidationAttempt{}, false, fmt.Errorf("decode integration validation receipt: %w", err)
	}
	candidateHead = strings.TrimSpace(candidateHead)
	if receipt.Version != integrationReceiptVersion ||
		normalizeWorktreeLockKey(receipt.TargetWorktree) != normalizeWorktreeLockKey(worktree) ||
		receipt.CandidateHead != candidateHead ||
		receipt.Validation.CandidateHead != candidateHead || receipt.Validation.Status != CandidateValidationPassed || !receipt.Validation.Canonical {
		return CandidateValidationAttempt{}, false, nil
	}
	return receipt.Validation, true, nil
}

func (c *Client) writeCanonicalIntegrationReceipt(ctx context.Context, worktree string, journal integrationJournal) error {
	if !journalHasExactValidation(journal) {
		return fmt.Errorf("journal does not contain exact noncanonical validation for %s", journal.DesiredHead)
	}
	attempt := journal.Validation
	attempt.Status = CandidateValidationPassed
	attempt.Canonical = true
	attempt.Message = "candidate validation passed and the exact candidate was applied"
	receipt := integrationValidationReceipt{
		Version:        integrationReceiptVersion,
		TargetWorktree: worktree,
		CandidateHead:  journal.DesiredHead,
		Validation:     attempt,
		AppliedAt:      time.Now().UTC(),
	}
	path, err := c.integrationReceiptPath(ctx, worktree)
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(payload, '\n'), 0o644)
}

func journalHasExactValidation(journal integrationJournal) bool {
	desiredHead := strings.TrimSpace(journal.DesiredHead)
	return journal.Version == integrationJournalVersion && desiredHead != "" &&
		journal.Validation.CandidateHead == desiredHead &&
		journal.Validation.Status == CandidateValidationPassed &&
		!journal.Validation.Canonical
}

func (c *Client) addDetachedWorktree(ctx context.Context, worktree, scratchPath, ref string) error {
	if _, err := c.runInWorktree(ctx, worktree, "worktree", "add", "--detach", scratchPath, ref); err != nil {
		return err
	}
	return nil
}

func (c *Client) removeWorktree(ctx context.Context, worktree, scratchPath string) error {
	if _, err := c.runInWorktree(ctx, worktree, "worktree", "remove", "--force", scratchPath); err != nil {
		return err
	}
	return nil
}

func (c *Client) resetHard(ctx context.Context, worktree, ref string) error {
	if _, err := c.runInWorktree(ctx, worktree, "reset", "--hard", ref); err != nil {
		return err
	}
	return nil
}

func (c *Client) revParseVerify(ctx context.Context, worktree, ref string) (string, error) {
	output, err := c.runInWorktree(ctx, worktree, "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return "", fmt.Errorf("empty rev-parse output for %s", ref)
	}
	return output, nil
}

func (c *Client) writeIntegrationJournal(ctx context.Context, worktree string, journal integrationJournal) error {
	path, err := c.integrationJournalPath(ctx, worktree)
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomic(path, payload, 0o644)
}

func writeFileAtomic(path string, payload []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (c *Client) readIntegrationJournal(_ context.Context, worktree string) (integrationJournal, string, bool, error) {
	path, found, err := c.existingIntegrationJournalPath(worktree)
	if err != nil {
		return integrationJournal{}, "", false, err
	}
	if !found {
		return integrationJournal{}, "", false, nil
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return integrationJournal{}, "", false, nil
		}
		return integrationJournal{}, "", false, err
	}
	var journal integrationJournal
	if err := json.Unmarshal(payload, &journal); err != nil {
		return integrationJournal{}, "", false, err
	}
	return journal, path, true, nil
}

func (c *Client) removeIntegrationJournal(ctx context.Context, worktree string) error {
	path, err := c.integrationJournalPath(ctx, worktree)
	if err != nil {
		return err
	}
	return c.removeIntegrationJournalAtPath(path)
}

func (c *Client) removeIntegrationJournalAtPath(path string) error {
	if c.removeJournal != nil {
		return c.removeJournal(path)
	}
	return removeIntegrationJournalPathWithSync(path, c.syncJournalDir)
}

func removeIntegrationJournalPath(path string) error {
	return removeIntegrationJournalPathWithSync(path, syncDirectory)
}

func removeIntegrationJournalPathWithSync(path string, syncDir func(string) error) error {
	payload, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read integration journal before durable unlink: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect integration journal before durable unlink: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if syncDir == nil {
		syncDir = syncDirectory
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		restoreErr := writeFileAtomic(path, payload, info.Mode().Perm())
		if restoreErr != nil {
			return fmt.Errorf("sync integration journal unlink: %w; restore journal after failed sync: %v", err, restoreErr)
		}
		return fmt.Errorf("sync integration journal unlink (journal restored): %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (c *Client) integrationJournalPath(ctx context.Context, worktree string) (string, error) {
	commonDir, err := c.gitCommonDir(ctx, worktree)
	if err != nil {
		return "", err
	}
	return filepath.Join(commonDir, "azedarach", integrationJournalName(worktree)), nil
}

func (c *Client) integrationReceiptPath(ctx context.Context, worktree string) (string, error) {
	commonDir, err := c.gitCommonDir(ctx, worktree)
	if err != nil {
		return "", err
	}
	return filepath.Join(commonDir, "azedarach", integrationReceiptName(worktree)), nil
}

func (c *Client) existingIntegrationReceiptPath(worktree string) (string, bool, error) {
	for _, commonDir := range integrationJournalCandidateCommonDirs(worktree) {
		path := filepath.Join(commonDir, "azedarach", integrationReceiptName(worktree))
		_, err := os.Stat(path)
		switch {
		case err == nil:
			return path, true, nil
		case os.IsNotExist(err):
			continue
		default:
			return "", false, err
		}
	}
	return "", false, nil
}

func integrationReceiptName(worktree string) string {
	return "validation-" + integrationJournalName(worktree)
}

func (c *Client) existingIntegrationJournalPath(worktree string) (string, bool, error) {
	for _, commonDir := range integrationJournalCandidateCommonDirs(worktree) {
		path := filepath.Join(commonDir, "azedarach", integrationJournalName(worktree))
		_, err := os.Stat(path)
		switch {
		case err == nil:
			return path, true, nil
		case os.IsNotExist(err):
			continue
		default:
			return "", false, err
		}
	}
	return "", false, nil
}

func integrationJournalName(worktree string) string {
	key := normalizeWorktreeLockKey(worktree)
	sum := sha256.Sum256([]byte(key))
	return "integration-" + hex.EncodeToString(sum[:])[:16] + ".json"
}

func integrationLockName(worktree string) string {
	return strings.TrimSuffix(integrationJournalName(worktree), ".json") + ".lock"
}

func integrationJournalCandidateCommonDirs(worktree string) []string {
	worktree = normalizeWorktreeLockKey(worktree)
	gitPath := filepath.Join(worktree, ".git")
	var dirs []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(worktree, dir)
		}
		dir = filepath.Clean(dir)
		for _, existing := range dirs {
			if existing == dir {
				return
			}
		}
		dirs = append(dirs, dir)
	}
	if info, err := os.Stat(gitPath); err == nil && info.IsDir() {
		add(gitPath)
		return dirs
	}
	payload, err := os.ReadFile(gitPath)
	if err != nil {
		return dirs
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(payload)), "gitdir:"))
	if gitDir == strings.TrimSpace(string(payload)) {
		return dirs
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktree, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	if commonPayload, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		commonDir := strings.TrimSpace(string(commonPayload))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
		add(commonDir)
	}
	add(gitDir)
	return dirs
}

func (c *Client) gitCommonDir(ctx context.Context, worktree string) (string, error) {
	output, err := c.runInWorktree(ctx, worktree, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(output)
	if dir == "" {
		return "", fmt.Errorf("empty git common dir")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(worktree, dir)
	}
	return filepath.Clean(dir), nil
}
