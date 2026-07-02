package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	managedGuidanceBlockKind = "azedarach-learning"
)

type managedGuidanceResult struct {
	Path         string
	TargetHash   string
	Changed      bool
	BlockMissing bool
}

func learningPromotionFileBacked(target protocol.LearningPromotionTarget) bool {
	switch target {
	case protocol.LearningPromotionTargetRulesync, protocol.LearningPromotionTargetAgents, protocol.LearningPromotionTargetSkill:
		return true
	default:
		return false
	}
}

func upsertManagedGuidanceBlock(repoDir string, learning protocol.Learning, note, expectedHash string) (managedGuidanceResult, error) {
	path, relPath, err := resolveManagedGuidancePath(repoDir, learning.TargetID)
	if err != nil {
		return managedGuidanceResult{}, err
	}
	body := buildManagedGuidanceBody(learning, note)
	targetHash := managedGuidanceHash(body)
	block := buildManagedGuidanceBlock(learning.ID, targetHash, body)

	content, mode, err := readManagedGuidanceFile(path)
	if err != nil {
		return managedGuidanceResult{}, err
	}
	blockRange, found, err := findManagedGuidanceBlock(content, learning.ID)
	if err != nil {
		return managedGuidanceResult{}, err
	}
	if found {
		if err := validateManagedGuidanceBlock(blockRange, strings.TrimSpace(expectedHash)); err != nil {
			return managedGuidanceResult{Path: relPath, TargetHash: blockRange.MarkerHash}, err
		}
		existingBlock := content[blockRange.Start:blockRange.End]
		if existingBlock == block {
			return managedGuidanceResult{Path: relPath, TargetHash: targetHash, Changed: false}, nil
		}
		content = content[:blockRange.Start] + block + content[blockRange.End:]
	} else {
		if strings.TrimSpace(expectedHash) != "" {
			return managedGuidanceResult{Path: relPath, BlockMissing: true}, managedGuidanceMissingError(learning.ID, relPath)
		}
		content = appendManagedGuidanceBlock(content, block)
	}
	if err := writeManagedGuidanceFile(path, content, mode); err != nil {
		return managedGuidanceResult{}, err
	}
	return managedGuidanceResult{Path: relPath, TargetHash: targetHash, Changed: true}, nil
}

func removeManagedGuidanceBlock(repoDir string, learning protocol.Learning, expectedHash string) (managedGuidanceResult, error) {
	path, relPath, err := resolveManagedGuidancePath(repoDir, learning.TargetID)
	if err != nil {
		return managedGuidanceResult{}, err
	}
	content, mode, err := readManagedGuidanceFile(path)
	if err != nil {
		return managedGuidanceResult{}, err
	}
	blockRange, found, err := findManagedGuidanceBlock(content, learning.ID)
	if err != nil {
		return managedGuidanceResult{}, err
	}
	if !found {
		return managedGuidanceResult{Path: relPath, BlockMissing: true}, managedGuidanceMissingError(learning.ID, relPath)
	}
	if err := validateManagedGuidanceBlock(blockRange, strings.TrimSpace(expectedHash)); err != nil {
		return managedGuidanceResult{Path: relPath, TargetHash: blockRange.MarkerHash}, err
	}
	content = strings.TrimRight(content[:blockRange.Start]+content[blockRange.End:], "\n")
	if strings.TrimSpace(content) != "" {
		content += "\n"
	}
	if err := writeManagedGuidanceFile(path, content, mode); err != nil {
		return managedGuidanceResult{}, err
	}
	return managedGuidanceResult{Path: relPath, TargetHash: blockRange.MarkerHash, Changed: true}, nil
}

type managedGuidanceBlockRange struct {
	Start      int
	End        int
	MarkerHash string
	ActualHash string
}

func findManagedGuidanceBlock(content, learningID string) (managedGuidanceBlockRange, bool, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	startPrefix := managedGuidanceStartPrefix(learningID)
	start := strings.Index(content, startPrefix)
	if start < 0 {
		return managedGuidanceBlockRange{}, false, nil
	}
	startLineEndRel := strings.IndexByte(content[start:], '\n')
	if startLineEndRel < 0 {
		return managedGuidanceBlockRange{}, false, fmt.Errorf("%w: managed guidance block %s has no body", domain.ErrConflict, learningID)
	}
	startLineEnd := start + startLineEndRel
	startLine := strings.TrimSpace(content[start:startLineEnd])
	markerHash := parseManagedGuidanceMarkerHash(startLine)
	if markerHash == "" {
		return managedGuidanceBlockRange{}, false, fmt.Errorf("%w: managed guidance block %s is missing hash", domain.ErrConflict, learningID)
	}
	bodyStart := startLineEnd + 1
	endMarker := managedGuidanceEndMarker(learningID)
	endRel := strings.Index(content[bodyStart:], endMarker)
	if endRel < 0 {
		return managedGuidanceBlockRange{}, false, fmt.Errorf("%w: managed guidance block %s is missing end marker", domain.ErrConflict, learningID)
	}
	bodyEnd := bodyStart + endRel
	end := bodyEnd + len(endMarker)
	if end < len(content) && content[end] == '\n' {
		end++
	}
	if strings.Contains(content[end:], startPrefix) {
		return managedGuidanceBlockRange{}, false, fmt.Errorf("%w: duplicate managed guidance block for %s", domain.ErrConflict, learningID)
	}
	body := content[bodyStart:bodyEnd]
	return managedGuidanceBlockRange{
		Start:      start,
		End:        end,
		MarkerHash: markerHash,
		ActualHash: managedGuidanceHash(body),
	}, true, nil
}

func validateManagedGuidanceBlock(block managedGuidanceBlockRange, expectedHash string) error {
	if block.MarkerHash != block.ActualHash {
		return fmt.Errorf("%w: managed guidance block drifted (marker hash %s, actual %s)", domain.ErrConflict, block.MarkerHash, block.ActualHash)
	}
	if expectedHash != "" && expectedHash != block.MarkerHash {
		return fmt.Errorf("%w: managed guidance block drifted from recorded hash %s to %s", domain.ErrConflict, expectedHash, block.MarkerHash)
	}
	return nil
}

func buildManagedGuidanceBlock(learningID, targetHash, body string) string {
	return managedGuidanceStartMarker(learningID, targetHash) + "\n" + body + managedGuidanceEndMarker(learningID) + "\n"
}

func buildManagedGuidanceBody(learning protocol.Learning, note string) string {
	summary := strings.Join(strings.Fields(learning.Summary), " ")
	if summary == "" {
		summary = "Promoted learning guidance"
	}
	lines := []string{
		"- " + summary,
		"",
		"  Source learning: " + learning.ID,
	}
	if trimmed := strings.TrimSpace(note); trimmed != "" {
		lines = append(lines, "  Promotion note: "+strings.Join(strings.Fields(trimmed), " "))
	}
	return strings.Join(lines, "\n") + "\n"
}

func appendManagedGuidanceBlock(content, block string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if strings.TrimSpace(content) == "" {
		return block
	}
	content = strings.TrimRight(content, "\n")
	return content + "\n\n" + block
}

func managedGuidanceStartPrefix(learningID string) string {
	return fmt.Sprintf("<!-- azedarach:learning begin id=%q hash=", learningID)
}

func managedGuidanceStartMarker(learningID, targetHash string) string {
	return fmt.Sprintf("%s%q -->", managedGuidanceStartPrefix(learningID), targetHash)
}

func managedGuidanceEndMarker(learningID string) string {
	return fmt.Sprintf("<!-- azedarach:learning end id=%q -->", learningID)
}

func parseManagedGuidanceMarkerHash(marker string) string {
	const key = `hash="`
	idx := strings.Index(marker, key)
	if idx < 0 {
		return ""
	}
	rest := marker[idx+len(key):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func managedGuidanceHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func resolveManagedGuidancePath(repoDir, targetID string) (string, string, error) {
	repoDir = strings.TrimSpace(repoDir)
	targetID = strings.TrimSpace(targetID)
	if repoDir == "" {
		return "", "", errors.New("repository directory is required for file-backed promotion")
	}
	if targetID == "" {
		return "", "", errors.New("target path is required for file-backed promotion")
	}
	if filepath.IsAbs(targetID) {
		return "", "", fmt.Errorf("%w: file-backed promotion target must be repository-relative", domain.ErrConflict)
	}
	cleanRel := filepath.Clean(targetID)
	if cleanRel == "." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) || cleanRel == ".." {
		return "", "", fmt.Errorf("%w: file-backed promotion target must stay inside the repository", domain.ErrConflict)
	}
	path := filepath.Join(repoDir, cleanRel)
	if err := rejectManagedGuidanceSymlinkPath(repoDir, cleanRel); err != nil {
		return "", "", err
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return "", "", fmt.Errorf("%w: file-backed promotion target is a directory: %s", domain.ErrConflict, cleanRel)
	} else if err != nil && !os.IsNotExist(err) {
		return "", "", err
	}
	return path, filepath.ToSlash(cleanRel), nil
}

func rejectManagedGuidanceSymlinkPath(repoDir, cleanRel string) error {
	current := strings.TrimSpace(repoDir)
	for _, part := range strings.Split(cleanRel, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: file-backed promotion target may not traverse symlink %s", domain.ErrConflict, filepath.ToSlash(cleanRel))
		}
	}
	return nil
}

func readManagedGuidanceFile(path string) (string, os.FileMode, error) {
	mode := os.FileMode(0o644)
	info, err := os.Stat(path)
	if err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return "", 0, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", mode, nil
		}
		return "", 0, err
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n"), mode, nil
}

func writeManagedGuidanceFile(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".az-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func managedGuidanceMissingError(learningID, path string) error {
	return fmt.Errorf("%w: managed guidance block for %s is missing from %s", domain.ErrConflict, learningID, path)
}
