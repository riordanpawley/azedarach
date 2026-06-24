package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
// updates the target worktree after the scratch merge succeeds. Callers that
// share a Client should hold the target's WithWorktreeLock while this runs.
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
	if err := c.RecoverIntegrationJournal(ctx, worktree); err != nil {
		return nil, fmt.Errorf("recover interrupted integration: %w", err)
	}

	targetStatus, err := c.Status(ctx, worktree)
	if err != nil {
		return nil, fmt.Errorf("inspect target status before transactional merge: %w", err)
	}
	if gitStatusDirty(targetStatus) {
		return &MergeResult{
			Success: false,
			Message: fmt.Sprintf("target worktree is dirty before transactional merge; leaving files untouched: %s", gitStatusSummary(targetStatus)),
		}, nil
	}

	targetHead, err := c.revParseVerify(ctx, worktree, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve target HEAD before transactional merge: %w", err)
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

	result, err := c.mergeCleanly(ctx, scratchPath, branch, gitCommandHooksDisabled)
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

	targetStatus, err = c.Status(ctx, worktree)
	if err != nil {
		return nil, fmt.Errorf("inspect target status before final apply: %w", err)
	}
	if gitStatusDirty(targetStatus) {
		return &MergeResult{
			Success: false,
			Message: fmt.Sprintf("target worktree became dirty before final apply; target left untouched: %s", gitStatusSummary(targetStatus)),
		}, nil
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
		return nil, fmt.Errorf("write transactional merge journal: %w", err)
	}
	if err := c.resetHard(ctx, worktree, desiredHead); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeCleanupTimeout)
		defer cancel()
		if recoverErr := c.RecoverIntegrationJournal(cleanupCtx, worktree); recoverErr != nil {
			return nil, fmt.Errorf("apply transactional merge failed (%v); recovery failed: %w", err, recoverErr)
		}
		return &MergeResult{
			Success: false,
			Message: fmt.Sprintf("transactional merge final apply failed; recovered target worktree: %v", err),
		}, nil
	}
	postStatus, err := c.Status(ctx, worktree)
	if err != nil {
		return nil, fmt.Errorf("inspect target status after final apply: %w", err)
	}
	if gitStatusDirty(postStatus) {
		return c.recoverDirtyFinalApply(ctx, worktree, postStatus)
	}
	if err := c.removeIntegrationJournal(ctx, worktree); err != nil && c.logger != nil {
		c.logger.Warn("failed to remove transactional merge journal", "worktree", worktree, "error", err)
	}
	return result, nil
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
	if _, err := c.runInWorktreeNoHooks(ctx, worktree, "worktree", "add", "--detach", scratchPath, ref); err != nil {
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
