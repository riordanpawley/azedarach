package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const ReviewCoverageContract = "full-diff;active-and-adjacent-paths;analogous-and-sibling-paths;all-lifecycle-endings;trust-boundaries;regression-adequacy;all-defect-family-instances"
const maxReviewPromptBytes = 256 * 1024

const portableReviewPrompt = `Safety and correctness are non-removable review requirements. Review the exact assigned change in one complete pass. Establish the intended invariant from the acceptance contract and diff, then try to disprove it with concrete counterexamples. Inspect the full diff and every changed function, its active callers, adjacent consumers, alternate entrypoints, recovery paths, and analogous or sibling implementations. Enumerate every success, failure, cancellation, cleanup, retry, and other lifecycle ending that applies. Inspect each trust and authority boundary, stale or absent capability behavior, portability, and whether regression tests causally prove the claimed behavior. When you discover a defect family, search the complete assigned change and relevant siblings for every instance before returning one consolidated batch.

Mandatory output: identify the exact base and head revisions, review epoch, prompt source and digest, composition mode, selected risk matrix, covered cells, deliberately skipped cells with reasons, inspected paths and tools, deduplicated findings with evidence and severity, and a clean or findings verdict. Pass the delivered digest and the issue's epoch back through the review decision's review_prompt_digest and review_epoch_event_id fields (CLI: --review-prompt-digest and --review-epoch; use one --review-prompt-binding issue=epoch:digest per issue for a multi-issue acceptance). Never claim unavailable evidence, tests, identity, independence, or capability.`

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
		data, err := readReviewPromptBelowRoot(root, filepath.FromSlash(configured))
		if err != nil {
			return ResolvedReviewPrompt{}, fmt.Errorf("read orchestration review prompt %q: %w", configured, err)
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

// readReviewPromptBelowRoot opens each path component relative to an already
// opened project root. O_NOFOLLOW makes containment apply to the descriptors
// actually read, rather than to a racy path preflight.
func readReviewPromptBelowRoot(root, relative string) ([]byte, error) {
	rootFile, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer rootFile.Close()
	current := rootFile
	components := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for i, component := range components {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if i < len(components)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, openErr := unix.Openat(int(current.Fd()), component, flags, 0)
		if current != rootFile {
			_ = current.Close()
		}
		if openErr != nil {
			return nil, openErr
		}
		current = os.NewFile(uintptr(fd), component)
	}
	if current == rootFile {
		return nil, fmt.Errorf("prompt path must name a file")
	}
	defer current.Close()
	info, err := current.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("must be a readable regular file")
	}
	if info.Size() > maxReviewPromptBytes {
		return nil, fmt.Errorf("exceeds 256 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(current, maxReviewPromptBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxReviewPromptBytes {
		return nil, fmt.Errorf("exceeds 256 KiB")
	}
	return data, nil
}
