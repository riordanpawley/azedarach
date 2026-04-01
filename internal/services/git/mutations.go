package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const DefaultCheckpointMessage = "chore: pre-merge checkpoint"

var ErrNoChangesToCommit = errors.New("no changes to commit")

// MergePreflightResult captures the daemon-side inputs needed to predict whether
// a merge can proceed without touching client-owned authority paths.
type MergePreflightResult struct {
	SourceStatus  GitStatus `json:"source_status"`
	TargetStatus  GitStatus `json:"target_status"`
	HasConflicts  bool      `json:"has_conflicts"`
	ConflictFiles []string  `json:"conflict_files,omitempty"`
}

// MergePreflight reads source/target status and predicts whether merging the
// source branch into the target ref would conflict.
func (c *Client) MergePreflight(ctx context.Context, sourceWorktree, targetWorktree, targetRef, sourceBranch string) (*MergePreflightResult, error) {
	sourceStatus, err := c.Status(ctx, sourceWorktree)
	if err != nil {
		return nil, fmt.Errorf("read source status: %w", err)
	}
	targetStatus, err := c.Status(ctx, targetWorktree)
	if err != nil {
		return nil, fmt.Errorf("read target status: %w", err)
	}

	result := &MergePreflightResult{
		SourceStatus: *sourceStatus,
		TargetStatus: *targetStatus,
	}

	targetRef = strings.TrimSpace(targetRef)
	sourceBranch = strings.TrimSpace(sourceBranch)
	if targetRef == "" || sourceBranch == "" {
		return result, nil
	}

	output, err := c.MergeTreeWriteTree(ctx, targetWorktree, targetRef, sourceBranch)
	if predictsMergeTreeConflicts(output, err) {
		result.HasConflicts = true
		result.ConflictFiles = parseMergeTreeConflictFiles(output)
		if len(result.ConflictFiles) == 0 && err != nil {
			result.ConflictFiles = parseMergeTreeConflictFiles(err.Error())
		}
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

// DiscardChanges removes tracked and untracked changes from a worktree.
func (c *Client) DiscardChanges(ctx context.Context, worktree string) error {
	if err := c.RestoreAll(ctx, worktree); err != nil {
		return err
	}
	if err := c.CleanForce(ctx, worktree); err != nil {
		return err
	}
	return nil
}

// CreateCheckpoint stages all changes and creates a checkpoint commit.
func (c *Client) CreateCheckpoint(ctx context.Context, worktree, message string) error {
	status, err := c.Status(ctx, worktree)
	if err != nil {
		return err
	}
	if status == nil || !status.HasChanges {
		return ErrNoChangesToCommit
	}

	message = strings.TrimSpace(message)
	if message == "" {
		message = DefaultCheckpointMessage
	}

	if err := c.AddAll(ctx, worktree); err != nil {
		return err
	}
	if err := c.Commit(ctx, worktree, message); err != nil {
		return err
	}
	return nil
}

func predictsMergeTreeConflicts(output string, err error) bool {
	if strings.Contains(output, "CONFLICT") {
		return true
	}
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "CONFLICT")
}

func parseMergeTreeConflictFiles(output string) []string {
	conflicts := make([]string, 0)
	seen := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "CONFLICT") {
			continue
		}
		if strings.Contains(line, "Merge conflict in ") {
			parts := strings.Split(line, "Merge conflict in ")
			if len(parts) >= 2 {
				file := strings.TrimSpace(parts[1])
				if file != "" {
					if _, ok := seen[file]; !ok {
						seen[file] = struct{}{}
						conflicts = append(conflicts, file)
					}
				}
			}
			continue
		}
		if idx := strings.Index(line, "): "); idx != -1 {
			rest := line[idx+3:]
			var file string
			if idx2 := strings.Index(rest, " deleted in "); idx2 != -1 {
				file = strings.TrimSpace(rest[:idx2])
			} else if idx2 := strings.Index(rest, " modified in "); idx2 != -1 {
				file = strings.TrimSpace(rest[:idx2])
			}
			if file != "" {
				if _, ok := seen[file]; !ok {
					seen[file] = struct{}{}
					conflicts = append(conflicts, file)
				}
			}
		}
	}
	return conflicts
}
