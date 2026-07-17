package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/riordanpawley/azedarach/internal/latencytrace"
)

// ResolveBaseGitRoot resolves the repository root from any nested worktree path.
func ResolveBaseGitRoot(startDir string) (string, error) {
	return ResolveBaseGitRootContext(context.Background(), startDir)
}

// ResolveBaseGitRootContext resolves the repository root from any nested
// worktree path and parents git-root probe spans under ctx.
func ResolveBaseGitRootContext(ctx context.Context, startDir string) (string, error) {
	root, finishGitProbe, gitErr := resolveBaseGitRootWithGitExec(ctx, startDir)
	if gitErr == nil {
		finishGitProbe("success", nil)
		return root, nil
	}
	if root, err := resolveBaseGitRootFromGitMarker(startDir); err == nil {
		finishGitProbe("fallback", nil)
		return root, nil
	}
	finishGitProbe("failure", gitErr)
	return "", fmt.Errorf("unable to resolve git root from %s", startDir)
}

// ResolveWorktreeRoot resolves the nearest git worktree root from a nested path.
// For non-git paths it falls back to the absolute path.
func ResolveWorktreeRoot(startPath string) (string, error) {
	return ResolveWorktreeRootContext(context.Background(), startPath)
}

// ResolveWorktreeRootContext resolves the nearest git worktree root from a
// nested path and parents git-root probe spans under ctx.
func ResolveWorktreeRootContext(ctx context.Context, startPath string) (string, error) {
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
	root, finishGitProbe, gitErr := resolveWorktreeRootWithGitExec(ctx, abs)
	if gitErr == nil {
		finishGitProbe("success", nil)
		return root, nil
	}
	if root, err := resolveWorktreeRootFromGitMarker(abs); err == nil {
		finishGitProbe("fallback", nil)
		return root, nil
	}
	finishGitProbe("fallback", nil)
	return abs, nil
}

type gitRootProbeFinisher func(outcome string, err error)

func resolveBaseGitRootWithGitExec(ctx context.Context, startDir string) (string, gitRootProbeFinisher, error) {
	out, err, finish := runGitExecCommandProbe(ctx, startDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", finish, fmt.Errorf("resolve git common dir: %w", err)
	}
	root, err := baseGitRootFromCommonDir(startDir, strings.TrimSpace(string(out)))
	if err != nil {
		return "", finish, err
	}
	return root, finish, nil
}

func resolveWorktreeRootWithGitExec(ctx context.Context, startDir string) (string, gitRootProbeFinisher, error) {
	out, err, finish := runGitExecCommandProbe(ctx, startDir, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return "", finish, fmt.Errorf("resolve git worktree root: %w", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", finish, fmt.Errorf("resolve git worktree root: empty output")
	}
	return root, finish, nil
}

func runGitExecCommand(ctx context.Context, startDir string, operation string, args ...string) (out []byte, err error) {
	out, err, finish := runGitExecCommandProbe(ctx, startDir, operation, args...)
	if err != nil {
		finish("failure", err)
		return out, err
	}
	finish("success", nil)
	return out, nil
}

func runGitExecCommandProbe(ctx context.Context, startDir string, operation string, args ...string) (out []byte, err error, finish gitRootProbeFinisher) {
	ctx, endSpan := latencytrace.StartSpanWithEndAttributes(ctx, "dependency", "git_root",
		"dependency.name", "git",
		"dependency.operation", operation,
		"arg_count", len(args)+1,
	)
	finish = func(outcome string, spanErr error) {
		endSpan(spanErr, "outcome", outcome)
	}
	cmdArgs := append([]string{operation}, args...)
	cmd := gitExecCommandContext(ctx, startDir, cmdArgs...)
	out, err = cmd.Output()
	return out, err, finish
}

func gitExecCommand(startDir string, args ...string) *exec.Cmd {
	return gitExecCommandContext(context.Background(), startDir, args...)
}

func gitExecCommandContext(ctx context.Context, startDir string, args ...string) *exec.Cmd {
	cmdArgs := append([]string{"-C", startDir}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Env = gitExecEnv(os.Environ())
	return cmd
}

func gitExecEnv(env []string) []string {
	const (
		gitDirKey           = "GIT_DIR="
		gitWorkTreeKey      = "GIT_WORK_TREE="
		gitCommonDirKey     = "GIT_COMMON_DIR="
		gitIndexFileKey     = "GIT_INDEX_FILE="
		gitObjectDirKey     = "GIT_OBJECT_DIRECTORY="
		gitAltObjectDirsKey = "GIT_ALTERNATE_OBJECT_DIRECTORIES="
	)
	out := make([]string, 0, len(env))
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, gitDirKey),
			strings.HasPrefix(kv, gitWorkTreeKey),
			strings.HasPrefix(kv, gitCommonDirKey),
			strings.HasPrefix(kv, gitIndexFileKey),
			strings.HasPrefix(kv, gitObjectDirKey),
			strings.HasPrefix(kv, gitAltObjectDirsKey):
			continue
		default:
			out = append(out, kv)
		}
	}
	return out
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

// ResolveProjectRoot is the dependency-light local resolver for CLI/config
// project ownership. It returns an absolute directory suitable for
// project-scoped state. For Git repositories (including worktrees), this is
// always the base repository root. For non-Git paths, this falls back to the
// absolute path.
func ResolveProjectRoot(startPath string) (string, error) {
	return ResolveProjectRootContext(context.Background(), startPath)
}

// ResolveProjectRootContext returns an absolute directory suitable for
// project-scoped state and parents git-root probe spans under ctx.
func ResolveProjectRootContext(ctx context.Context, startPath string) (string, error) {
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

	if baseRoot, err := ResolveBaseGitRootContext(ctx, abs); err == nil {
		return baseRoot, nil
	}
	return abs, nil
}
