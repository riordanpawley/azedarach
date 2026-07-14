package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

const (
	decisionMDSubdir = "docs/decisions"
)

// SyncMD writes (or, with Check=true, compares) one markdown file per
// recorded decision under <repoDir>/docs/decisions/. It returns the list of
// files that changed and whether any did.
func (s issueDecisionService) SyncMD(ctx context.Context, req protocol.DecisionSyncMDRequestBody) (protocol.DecisionSyncMDResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionSyncMDResponseBody{}, err
	}
	if s.daemon == nil {
		return protocol.DecisionSyncMDResponseBody{}, errors.New("decision sync_md unavailable: daemon nil")
	}
	repoDir, err := decisionMDRepoDir(s.daemon.resolveRepoDirForProject(daemonProjectIDFromContext(ctx)), req.RepoDir)
	if err != nil {
		return protocol.DecisionSyncMDResponseBody{}, errors.New("decision sync_md unavailable: repo dir not resolved")
	}

	decisions, err := c.ListDecisions(ctx, issues.DecisionFilter{})
	if err != nil {
		return protocol.DecisionSyncMDResponseBody{}, err
	}

	exports := make([]decisionMDExport, 0, len(decisions))
	for _, decision := range decisions {
		links, err := c.ListDecisionLinks(ctx, issues.DecisionLinkFilter{DecisionID: decision.LocalID})
		if err != nil {
			return protocol.DecisionSyncMDResponseBody{}, err
		}
		incoming, err := c.ListDecisionLinks(ctx, issues.DecisionLinkFilter{
			TargetKind: issues.DecisionTargetDecision,
			TargetID:   decision.LocalID,
		})
		if err != nil {
			return protocol.DecisionSyncMDResponseBody{}, err
		}
		body := renderDecisionMarkdown(decision, links, incoming)
		exports = append(exports, decisionMDExport{Decision: decision, Body: body})
	}

	changedFiles, err := reconcileDecisionMarkdown(repoDir, exports, req.Check)
	if err != nil {
		return protocol.DecisionSyncMDResponseBody{}, err
	}

	return protocol.DecisionSyncMDResponseBody{
		Check:   req.Check,
		Changed: len(changedFiles) > 0,
		Files:   changedFiles,
	}, nil
}

type decisionMDExport struct {
	Decision issues.Decision
	Body     []byte
}

// reconcileDecisionMarkdown makes docs/decisions an exact export of the live
// decision store. Canonical files are written before obsolete artifacts are
// removed so a failed rename reconciliation never destroys the only copy of a
// live decision. Reserved decision filenames and parseable decision exports are
// reconciled; unrelated Markdown is left alone.
func reconcileDecisionMarkdown(repoDir string, exports []decisionMDExport, check bool) ([]string, error) {
	targetDir := filepath.Join(repoDir, decisionMDSubdir)
	if !check {
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", targetDir, err)
		}
	}

	desiredPaths := make(map[string]struct{}, len(exports))
	changedPaths := make(map[string]struct{}, len(exports))
	for _, export := range exports {
		path := filepath.Join(targetDir, decisionMDFilename(export.Decision))
		desiredPaths[path] = struct{}{}
		existing, readErr := os.ReadFile(path)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, fmt.Errorf("read %s: %w", path, readErr)
		}
		if errors.Is(readErr, os.ErrNotExist) || !bytes.Equal(existing, export.Body) {
			changedPaths[path] = struct{}{}
			if !check {
				if err := os.WriteFile(path, export.Body, 0o644); err != nil {
					return nil, fmt.Errorf("write %s: %w", path, err)
				}
			}
		}
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sortedDecisionMDChanges(repoDir, changedPaths), nil
		}
		return nil, fmt.Errorf("read %s: %w", targetDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(targetDir, entry.Name())
		if _, desired := desiredPaths[path]; desired {
			continue
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", path, readErr)
		}
		if !isDecisionMDFilename(entry.Name()) {
			if _, parseErr := parseDecisionMarkdown(content); parseErr != nil {
				continue
			}
		}
		changedPaths[path] = struct{}{}
		if !check {
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("remove obsolete decision markdown %s: %w", path, err)
			}
		}
	}
	return sortedDecisionMDChanges(repoDir, changedPaths), nil
}

func isDecisionMDFilename(name string) bool {
	stem := strings.TrimSuffix(name, ".md")
	if stem == name || !strings.HasPrefix(stem, "dec-") {
		return false
	}
	numeric := strings.TrimPrefix(stem, "dec-")
	if i := strings.IndexByte(numeric, '-'); i >= 0 {
		numeric = numeric[:i]
	}
	if numeric == "" {
		return false
	}
	for _, r := range numeric {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func sortedDecisionMDChanges(repoDir string, paths map[string]struct{}) []string {
	changed := make([]string, 0, len(paths))
	for path := range paths {
		rel, err := filepath.Rel(repoDir, path)
		if err != nil || rel == "" {
			rel = path
		}
		changed = append(changed, rel)
	}
	sort.Strings(changed)
	return changed
}

func decisionMDRepoDir(fallbackRepoDir, requestRepoDir string) (string, error) {
	if repoDir := strings.TrimSpace(requestRepoDir); repoDir != "" {
		if !filepath.IsAbs(repoDir) {
			return "", fmt.Errorf("repo dir must be absolute: %s", repoDir)
		}
		return repoDir, nil
	}
	if repoDir := strings.TrimSpace(fallbackRepoDir); repoDir != "" {
		return repoDir, nil
	}
	return "", errors.New("repo dir not resolved")
}

// decisionMDFilename produces a stable file name for a decision. Format is
// <local-id>-<title-slug>.md so files sort chronologically by id and remain
// stable when the title is edited (the id never changes).
func decisionMDFilename(d issues.Decision) string {
	slug := slugifyDecisionTitle(d.Title)
	if slug == "" {
		return d.LocalID + ".md"
	}
	return d.LocalID + "-" + slug + ".md"
}

func slugifyDecisionTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return ""
	}
	var b strings.Builder
	prevDash := true
	for _, r := range title {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteRune('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	const maxLen = 50
	if len(out) > maxLen {
		out = strings.TrimRight(out[:maxLen], "-")
	}
	return out
}

// renderDecisionMarkdown produces the canonical markdown representation of a
// decision. The output is deterministic: identical input always produces
// identical bytes, which is what lets Check mode detect drift cheaply.
func renderDecisionMarkdown(d issues.Decision, outgoing, incoming []issues.DecisionLink) []byte {
	var b bytes.Buffer

	fmt.Fprintf(&b, "# %s: %s\n\n", d.LocalID, strings.TrimSpace(d.Title))

	fmt.Fprintf(&b, "- Created: %s\n", d.CreatedAt.UTC().Format("2006-01-02"))
	fmt.Fprintf(&b, "- Updated: %s\n", d.UpdatedAt.UTC().Format("2006-01-02"))
	if revisedBy := decisionRevisedBy(incoming); revisedBy != "" {
		fmt.Fprintf(&b, "- Revised by: %s\n", revisedBy)
	}
	b.WriteString("\n")

	if rationale := strings.TrimSpace(d.Rationale); rationale != "" {
		b.WriteString("## Rationale\n\n")
		b.WriteString(rationale)
		b.WriteString("\n\n")
	}
	if ctx := strings.TrimSpace(d.Context); ctx != "" {
		b.WriteString("## Context\n\n")
		b.WriteString(ctx)
		b.WriteString("\n\n")
	}
	if cons := strings.TrimSpace(d.Consequences); cons != "" {
		b.WriteString("## Consequences\n\n")
		b.WriteString(cons)
		b.WriteString("\n\n")
	}

	if linkLines := renderDecisionLinkLines(outgoing, incoming); len(linkLines) > 0 {
		b.WriteString("## Links\n\n")
		for _, line := range linkLines {
			b.WriteString("- ")
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	return []byte(strings.TrimRight(b.String(), "\n") + "\n")
}

func decisionRevisedBy(incoming []issues.DecisionLink) string {
	for _, link := range incoming {
		if link.Relation == issues.DecisionRelationRevises {
			return link.DecisionID
		}
	}
	return ""
}

func renderDecisionLinkLines(outgoing, incoming []issues.DecisionLink) []string {
	type entry struct {
		text string
	}
	entries := make([]entry, 0, len(outgoing)+len(incoming))
	for _, link := range outgoing {
		line := fmt.Sprintf("%s %s:%s", link.Relation, link.TargetKind, link.TargetID)
		if link.Note != nil && strings.TrimSpace(*link.Note) != "" {
			line += " — " + strings.TrimSpace(*link.Note)
		}
		entries = append(entries, entry{text: line})
	}
	for _, link := range incoming {
		// Skip the "revised-by" link here; it's already surfaced in the header.
		if link.Relation == issues.DecisionRelationRevises {
			continue
		}
		line := fmt.Sprintf("(incoming) %s decision:%s", link.Relation, link.DecisionID)
		entries = append(entries, entry{text: line})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].text < entries[j].text })
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.text)
	}
	return out
}
