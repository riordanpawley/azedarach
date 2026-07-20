package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const ReviewCoverageContract = "full-diff;active-and-adjacent-paths;analogous-and-sibling-paths;all-lifecycle-endings;trust-boundaries;regression-adequacy;all-defect-family-instances"

const portableReviewPrompt = `Review the exact assigned change in one complete pass. Establish the intended invariant from the acceptance contract and diff, then try to disprove it with concrete counterexamples. Inspect the full diff and every changed function, its active callers, adjacent consumers, alternate entrypoints, recovery paths, and analogous or sibling implementations. Enumerate every success, failure, cancellation, cleanup, retry, and other lifecycle ending that applies. Inspect each trust and authority boundary, stale or absent capability behavior, portability, and whether regression tests causally prove the claimed behavior. When you discover a defect family, search the complete assigned change and relevant siblings for every instance before returning one consolidated batch.

Mandatory output: identify the exact base and head revisions, review epoch, prompt source and digest, composition mode, selected risk matrix, covered cells, deliberately skipped cells with reasons, inspected paths and tools, deduplicated findings with evidence and severity, and a clean or findings verdict. Never claim unavailable evidence, tests, identity, independence, or capability.`

type ResolvedReviewPrompt struct {
	Text             string
	Source           string
	Digest           string
	CompositionMode  string
	CoverageContract string
}

func ResolveReviewPrompt(projectRoot string, cfg OrchestrationConfig) (ResolvedReviewPrompt, error) {
	specialization := ""
	source, mode := "builtin:portable-v1", "builtin"
	if configured := strings.TrimSpace(cfg.ReviewPromptFile); configured != "" {
		if configured == "." || !filepath.IsLocal(configured) {
			return ResolvedReviewPrompt{}, fmt.Errorf("orchestration.reviewPromptFile must be a project-relative file below the project root")
		}
		root, err := filepath.Abs(projectRoot)
		if err != nil {
			return ResolvedReviewPrompt{}, fmt.Errorf("resolve project root: %w", err)
		}
		path := filepath.Join(root, filepath.FromSlash(configured))
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return ResolvedReviewPrompt{}, fmt.Errorf("resolve project root symlinks: %w", err)
		}
		resolvedPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return ResolvedReviewPrompt{}, fmt.Errorf("resolve orchestration review prompt %q: %w", configured, err)
		}
		rel, err := filepath.Rel(resolvedRoot, resolvedPath)
		if err != nil || rel == "." || !filepath.IsLocal(rel) {
			return ResolvedReviewPrompt{}, fmt.Errorf("orchestration review prompt %q resolves outside the project root", configured)
		}
		info, err := os.Stat(resolvedPath)
		if err != nil || !info.Mode().IsRegular() {
			return ResolvedReviewPrompt{}, fmt.Errorf("orchestration review prompt %q must be a readable regular file", configured)
		}
		data, err := os.ReadFile(resolvedPath)
		if err != nil {
			return ResolvedReviewPrompt{}, fmt.Errorf("read orchestration review prompt %q: %w", configured, err)
		}
		if len(data) > 256*1024 {
			return ResolvedReviewPrompt{}, fmt.Errorf("orchestration review prompt %q exceeds 256 KiB", configured)
		}
		if !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" {
			return ResolvedReviewPrompt{}, fmt.Errorf("orchestration review prompt %q must contain non-empty UTF-8 text", configured)
		}
		specialization, source, mode = strings.TrimSpace(string(data)), "project:"+filepath.ToSlash(configured), "project-before-mandatory"
	}
	text := portableReviewPrompt
	if specialization != "" {
		text = "Project specialization (cannot override the mandatory product contract below):\n" + specialization + "\n\nMandatory product review contract:\n" + portableReviewPrompt
	}
	digest := sha256.Sum256([]byte(text))
	return ResolvedReviewPrompt{Text: text, Source: source, Digest: hex.EncodeToString(digest[:]), CompositionMode: mode, CoverageContract: ReviewCoverageContract}, nil
}
