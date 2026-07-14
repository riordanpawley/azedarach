package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const integrationJournalVersion = 1

type integrationJournal struct {
	Version         int       `json:"version"`
	TargetWorktree  string    `json:"target_worktree"`
	TargetHead      string    `json:"target_head"`
	DesiredHead     string    `json:"desired_head"`
	ScratchWorktree string    `json:"scratch_worktree,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

// MergeCleanlyTransactional performs the merge in a disposable worktree and only
// locks the target worktree for the base snapshot and final apply phases.
func (c *Client) MergeCleanlyTransactional(ctx context.Context, worktree, branch string) (*MergeResult, error) {
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
	var targetHead string
	var earlyResult *MergeResult
	if err := c.WithWorktreeLock(ctx, worktree, func(ctx context.Context) error {
		if err := c.RecoverIntegrationJournal(ctx, worktree); err != nil {
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

		targetHead, err = c.revParseVerify(ctx, worktree, "HEAD")
		if err != nil {
			return fmt.Errorf("resolve target HEAD before transactional merge: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if earlyResult != nil {
		return earlyResult, nil
	}
	scratchPath, err := os.MkdirTemp("", "azedarach-integration-*")
	if err != nil {
		return nil, fmt.Errorf("create scratch integration directory: %w", err)
	}
	if err := os.Remove(scratchPath); err != nil {
		_ = os.RemoveAll(scratchPath)
		return nil, fmt.Errorf("prepare scratch integration directory: %w", err)
	}
	scratchAdded := false
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeCleanupTimeout)
		defer cancel()
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

	result, err := c.mergeCleanlyWithEnv(ctx, scratchPath, branch, []string{"AZEDARACH_SKIP_MERGE_REBASE_GATE=1"})
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
	attempt, configured, validationErr := c.validateIntegrationCandidate(ctx, worktree, scratchPath, desiredHead)
	if configured {
		result.ValidationAttempts = append(result.ValidationAttempts, attempt)
	}
	if validationErr != nil {
		if errors.Is(validationErr, context.Canceled) || errors.Is(validationErr, context.DeadlineExceeded) {
			return nil, validationErr
		}
		result.Success = false
		result.Message = appendMergeResultDetail(result.Message, validationErr.Error())
		return result, nil
	}

	return c.applyValidatedScratchMerge(ctx, worktree, scratchPath, targetHead, desiredHead, result)
}

func (c *Client) validateIntegrationCandidate(ctx context.Context, gateRoot, scratchPath, candidateHead string) (CandidateValidationAttempt, bool, error) {
	gatePath := filepath.Join(gateRoot, "scripts", "git-merge-rebase-gate.sh")
	attempt := CandidateValidationAttempt{
		CandidateHead: candidateHead,
		Status:        CandidateValidationRunning,
		Canonical:     false,
	}
	if _, err := os.Stat(gatePath); err != nil {
		if os.IsNotExist(err) {
			return CandidateValidationAttempt{}, false, nil
		}
		attempt.Status = CandidateValidationFailed
		attempt.Message = "candidate validation gate could not be inspected"
		notifyCandidateValidation(ctx, attempt)
		return attempt, true, fmt.Errorf("inspect candidate validation gate: %w", err)
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

	env := gitEnvWithOverrides(sanitizedGitEnv(os.Environ()), []string{
		"AZEDARACH_CANDIDATE_HEAD=" + candidateHead,
		"AZEDARACH_MERGE_GATE_BODY=" + filepath.Join(gateRoot, "scripts", "git-merge-rebase-gate-body.sh"),
		"AZEDARACH_SKIP_MERGE_REBASE_GATE=0",
	})
	stdout, stderr, runErr := runProcessGroupCommand(ctx, scratchPath, env, gatePath)
	if runErr != nil {
		attempt.Status = CandidateValidationFailed
		if ctxErr := ctx.Err(); ctxErr != nil {
			attempt.Status = CandidateValidationCancelled
			attempt.Message = "candidate validation cancelled; evidence is noncanonical"
			notifyCandidateValidation(ctx, attempt)
			return attempt, true, fmt.Errorf("validate candidate %s: %w", candidateHead, ctxErr)
		}
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		detail = boundedCandidateValidationDetail(detail)
		attempt.Message = "candidate validation failed"
		if detail != "" {
			attempt.Message += ": " + detail
		}
		notifyCandidateValidation(ctx, attempt)
		return attempt, true, fmt.Errorf("validate candidate %s: %s", candidateHead, attempt.Message)
	}

	actualHead, err = c.revParseVerify(ctx, scratchPath, "HEAD")
	if err != nil || actualHead != candidateHead {
		attempt.Status = CandidateValidationSuperseded
		attempt.Message = fmt.Sprintf("candidate HEAD changed during validation: expected %s, found %s", candidateHead, actualHead)
		notifyCandidateValidation(ctx, attempt)
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
		if err != nil {
			return attempt, true, fmt.Errorf("inspect candidate status after validation: %w", err)
		}
		return attempt, true, fmt.Errorf("%s: %s", attempt.Message, gitStatusSummary(status))
	}
	attempt.Status = CandidateValidationPassed
	attempt.Message = "candidate validation passed; awaiting exact apply"
	notifyCandidateValidation(ctx, attempt)
	return attempt, true, nil
}

func boundedCandidateValidationDetail(detail string) string {
	const maxRunes = 4096
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

func (c *Client) applyValidatedScratchMerge(ctx context.Context, worktree, scratchPath, targetHead, desiredHead string, result *MergeResult) (*MergeResult, error) {
	var out *MergeResult
	if err := c.WithWorktreeLock(ctx, worktree, func(ctx context.Context) error {
		if err := c.RecoverIntegrationJournal(ctx, worktree); err != nil {
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
			StartedAt:       time.Now().UTC(),
		}
		if err := c.writeIntegrationJournal(ctx, worktree, journal); err != nil {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationFailed, false, "candidate apply journal failed; evidence is noncanonical")
			return fmt.Errorf("write transactional merge journal: %w", err)
		}
		if err := c.resetHard(ctx, worktree, desiredHead); err != nil {
			setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationFailed, false, "candidate apply failed; evidence is noncanonical")
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeCleanupTimeout)
			defer cancel()
			if recoverErr := c.RecoverIntegrationJournal(cleanupCtx, worktree); recoverErr != nil {
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
		if err := c.removeIntegrationJournal(ctx, worktree); err != nil && c.logger != nil {
			c.logger.Warn("failed to remove transactional merge journal", "worktree", worktree, "error", err)
		}
		setCandidateValidationDisposition(ctx, result, desiredHead, CandidateValidationPassed, true, "candidate validation passed and the exact candidate was applied")
		out = result
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) recoverDirtyFinalApply(ctx context.Context, worktree string, postStatus *GitStatus) (*MergeResult, error) {
	dirtySummary := gitStatusSummary(postStatus)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeCleanupTimeout)
	defer cancel()
	if err := c.RecoverIntegrationJournal(cleanupCtx, worktree); err != nil {
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
	journal, journalPath, found, err := c.readIntegrationJournal(ctx, worktree)
	if err != nil || !found {
		return err
	}
	if journal.Version != integrationJournalVersion {
		return fmt.Errorf("unsupported integration journal version %d", journal.Version)
	}
	targetHead := strings.TrimSpace(journal.TargetHead)
	desiredHead := strings.TrimSpace(journal.DesiredHead)
	if targetHead == "" || desiredHead == "" {
		return fmt.Errorf("integration journal missing target or desired head")
	}

	currentHead, err := c.revParseVerify(ctx, worktree, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve current HEAD for integration recovery: %w", err)
	}
	recoverHead := targetHead
	if currentHead == desiredHead {
		recoverHead = desiredHead
	} else if currentHead != targetHead {
		return fmt.Errorf("integration journal found but current HEAD %s is neither target %s nor desired %s", currentHead, targetHead, desiredHead)
	}
	if err := c.resetHard(ctx, worktree, recoverHead); err != nil {
		return fmt.Errorf("reset target during integration recovery: %w", err)
	}
	if scratch := strings.TrimSpace(journal.ScratchWorktree); scratch != "" {
		if err := c.removeWorktree(ctx, worktree, scratch); err != nil && c.logger != nil {
			c.logger.Warn("failed to remove scratch integration worktree during recovery", "path", scratch, "error", err)
		}
		if err := os.RemoveAll(scratch); err != nil && c.logger != nil {
			c.logger.Warn("failed to remove scratch integration path during recovery", "path", scratch, "error", err)
		}
	}
	if err := removeIntegrationJournalPath(journalPath); err != nil {
		return err
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o644)
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
	return removeIntegrationJournalPath(path)
}

func removeIntegrationJournalPath(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (c *Client) integrationJournalPath(ctx context.Context, worktree string) (string, error) {
	commonDir, err := c.gitCommonDir(ctx, worktree)
	if err != nil {
		return "", err
	}
	return filepath.Join(commonDir, "azedarach", integrationJournalName(worktree)), nil
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
