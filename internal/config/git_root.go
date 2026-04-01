package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolveBaseGitRoot resolves the repository root from any nested worktree path.
func ResolveBaseGitRoot(startDir string) (string, error) {
	if root, err := resolveBaseGitRootWithGitExec(startDir); err == nil {
		return root, nil
	}
	if root, err := resolveBaseGitRootFromGitMarker(startDir); err == nil {
		return root, nil
	}
	return "", fmt.Errorf("unable to resolve git root from %s", startDir)
}

// ResolveWorktreeRoot resolves the nearest git worktree root from a nested path.
// For non-git paths it falls back to the absolute path.
func ResolveWorktreeRoot(startPath string) (string, error) {
	if strings.TrimSpace(startPath) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		startPath = cwd
	}
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(abs); statErr == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	if root, err := resolveWorktreeRootWithGitExec(abs); err == nil {
		return root, nil
	}
	if root, err := resolveWorktreeRootFromGitMarker(abs); err == nil {
		return root, nil
	}
	return abs, nil
}

func resolveBaseGitRootWithGitExec(startDir string) (string, error) {
	out, err := gitCommandWithoutGitDirEnv("-C", startDir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("resolve git common dir: %w", err)
	}
	return baseGitRootFromCommonDir(startDir, strings.TrimSpace(string(out)))
}

func resolveWorktreeRootWithGitExec(startDir string) (string, error) {
	out, err := gitCommandWithoutGitDirEnv("-C", startDir, "rev-parse", "--path-format=absolute", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("resolve git worktree root: %w", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("resolve git worktree root: empty output")
	}
	return root, nil
}

func gitCommandWithoutGitDirEnv(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_DIR=") ||
			strings.HasPrefix(entry, "GIT_WORK_TREE=") ||
			strings.HasPrefix(entry, "GIT_INDEX_FILE=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	cmd.Env = filtered
	return cmd
}

func resolveBaseGitRootFromGitMarker(startDir string) (string, error) {
	absStart, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for dir := absStart; ; dir = filepath.Dir(dir) {
		marker := filepath.Join(dir, ".git")
		info, statErr := os.Stat(marker)
		if statErr == nil {
			if info.IsDir() {
				return dir, nil
			}

			content, readErr := os.ReadFile(marker)
			if readErr != nil {
				return "", fmt.Errorf("read git marker %s: %w", marker, readErr)
			}
			gitDir, parseErr := parseGitDirPointer(string(content))
			if parseErr != nil {
				return "", fmt.Errorf("parse git marker %s: %w", marker, parseErr)
			}
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Clean(filepath.Join(dir, gitDir))
			}
			return baseGitRootFromCommonDir(dir, gitDir)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	return "", fmt.Errorf("no .git marker found for %s", absStart)
}

func resolveWorktreeRootFromGitMarker(startDir string) (string, error) {
	absStart, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for dir := absStart; ; dir = filepath.Dir(dir) {
		marker := filepath.Join(dir, ".git")
		if _, statErr := os.Stat(marker); statErr == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	return "", fmt.Errorf("no .git marker found for %s", absStart)
}

func parseGitDirPointer(content string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "gitdir:") {
			continue
		}
		target := strings.TrimSpace(strings.TrimPrefix(trimmed, "gitdir:"))
		if target == "" {
			return "", fmt.Errorf("empty gitdir target")
		}
		return target, nil
	}
	return "", fmt.Errorf("missing gitdir pointer")
}

func baseGitRootFromCommonDir(startDir, commonDir string) (string, error) {
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return "", fmt.Errorf("resolve git common dir: empty output")
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Clean(filepath.Join(startDir, commonDir))
	}
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir), nil
	}

	segments := strings.Split(filepath.ToSlash(commonDir), "/")
	for i, seg := range segments {
		if seg != ".git" {
			continue
		}
		gitDir := filepath.FromSlash(strings.Join(segments[:i+1], "/"))
		return filepath.Dir(gitDir), nil
	}

	return "", fmt.Errorf("resolve git common dir: expected .git path, got %s", commonDir)
}

// ResolveProjectRoot returns an absolute directory suitable for project-scoped
// state. For Git repositories (including worktrees), this is always the base
// repository root. For non-Git paths, this falls back to the absolute path.
func ResolveProjectRoot(startPath string) (string, error) {
	if strings.TrimSpace(startPath) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		startPath = cwd
	}

	abs, err := filepath.Abs(startPath)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(abs); statErr == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}

	if baseRoot, err := ResolveBaseGitRoot(abs); err == nil {
		return baseRoot, nil
	}
	return abs, nil
}
