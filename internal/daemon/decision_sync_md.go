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
	var resp protocol.DecisionSyncMDResponseBody
	_, err = s.withDecisionMDTransferAuthority(ctx, c, req.RepoDir, req.FullProject, func(ownerCtx context.Context, target decisionMDTransferTarget) error {
		// Decision ownership and filesystem reconciliation form one authority
		// operation. Holding the canonical issue-store mutation lock prevents a
		// link transfer from invalidating the owner snapshot before files are
		// written or removed.
		decisions, err := c.ListDecisions(ownerCtx, issues.DecisionFilter{IncludeDeleted: true})
		if err != nil {
			return err
		}

		exports := make([]decisionMDExport, 0, len(decisions))
		provenance := make(map[string][]string, len(decisions))
		owners := make(map[string]string, len(decisions))
		authorized := make(map[string]struct{}, len(decisions))
		for _, decision := range decisions {
			var links, provenanceLinks, incoming []issues.DecisionLink
			if decision.DeletedAt != nil {
				provenanceLinks, err = c.ListDecisionLinks(ownerCtx, issues.DecisionLinkFilter{DecisionID: decision.LocalID, IncludeDeleted: true})
				if err != nil {
					return err
				}
			} else {
				links, err = c.ListDecisionLinks(ownerCtx, issues.DecisionLinkFilter{DecisionID: decision.LocalID})
				if err != nil {
					return err
				}
				provenanceLinks = links
				incoming, err = c.ListDecisionLinks(ownerCtx, issues.DecisionLinkFilter{TargetKind: issues.DecisionTargetDecision, TargetID: decision.LocalID})
				if err != nil {
					return err
				}
			}
			issueIDs := decisionIssueIDsAtDecisionState(decision, provenanceLinks)
			provenance[decision.LocalID] = issueIDs
			owners[decision.LocalID] = decisionOwnerIssueID(issueIDs)
			if target.FullProject || owners[decision.LocalID] == target.IssueID {
				authorized[decision.LocalID] = struct{}{}
				if decision.DeletedAt == nil {
					exports = append(exports, decisionMDExport{Decision: decision, Body: renderDecisionMarkdown(decision, links, incoming)})
				}
			}
		}

		// Planning may take time. Re-read live HEAD and both live/durable
		// worktree ownership under the canonical target lock immediately before
		// any filesystem reconciliation.
		s.beforeDecisionMDTransferRevalidation("sync_md", target)
		if err := s.revalidateDecisionMDTransferTarget(ownerCtx, target); err != nil {
			return fmt.Errorf("revalidate decision sync_md target: %w", err)
		}
		results, err := reconcileDecisionMarkdownScoped(target.RepoDir, exports, authorized, provenance, owners, target.FullProject, req.Check)
		if err != nil {
			return err
		}
		changedFiles := make([]string, 0, len(results))
		for _, result := range results {
			if !result.Skipped {
				changedFiles = append(changedFiles, result.Path)
			}
		}
		resp = protocol.DecisionSyncMDResponseBody{Check: req.Check, Changed: len(changedFiles) > 0, Files: changedFiles, TargetRepoDir: target.RepoDir, TargetRevision: target.Revision, TargetIssueID: target.IssueID, FullProject: target.FullProject, Results: results}
		return nil
	})
	if err != nil {
		return protocol.DecisionSyncMDResponseBody{}, fmt.Errorf("decision sync_md target: %w", err)
	}
	return resp, nil
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
	authorized := make(map[string]struct{}, len(exports))
	for _, export := range exports {
		authorized[export.Decision.LocalID] = struct{}{}
	}
	results, err := reconcileDecisionMarkdownScoped(repoDir, exports, authorized, nil, nil, true, check)
	if err != nil {
		return nil, err
	}
	changed := make([]string, 0, len(results))
	for _, result := range results {
		if !result.Skipped {
			changed = append(changed, result.Path)
		}
	}
	return changed, nil
}

func reconcileDecisionMarkdownScoped(repoDir string, exports []decisionMDExport, authorized map[string]struct{}, provenance map[string][]string, owners map[string]string, fullProject, check bool) ([]protocol.DecisionMDFileResult, error) {
	targetDir := filepath.Join(repoDir, decisionMDSubdir)
	if !check {
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", targetDir, err)
		}
	}

	desiredPaths := make(map[string]struct{}, len(exports))
	resultsByPath := make(map[string]protocol.DecisionMDFileResult, len(exports))
	for _, export := range exports {
		path := filepath.Join(targetDir, decisionMDFilename(export.Decision))
		desiredPaths[path] = struct{}{}
		existing, readErr := os.ReadFile(path)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, fmt.Errorf("read %s: %w", path, readErr)
		}
		if errors.Is(readErr, os.ErrNotExist) || !bytes.Equal(existing, export.Body) {
			resultsByPath[path] = protocol.DecisionMDFileResult{Path: path, DecisionID: export.Decision.LocalID, IssueIDs: provenance[export.Decision.LocalID], OwnerIssueID: owners[export.Decision.LocalID], Action: "write", Applied: !check}
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
			return sortedDecisionMDResults(repoDir, resultsByPath), nil
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
		decisionID := decisionMDID(entry.Name(), content)
		if decisionID == "" {
			if _, parseErr := parseDecisionMarkdown(content); parseErr != nil {
				continue
			}
		}
		if !fullProject {
			if _, ok := authorized[decisionID]; !ok {
				resultsByPath[path] = protocol.DecisionMDFileResult{Path: path, DecisionID: decisionID, IssueIDs: provenance[decisionID], OwnerIssueID: owners[decisionID], Action: "preserve", Skipped: true, SkipReason: "foreign, unowned, or ambiguously owned decision artifact"}
				continue
			}
		}
		resultsByPath[path] = protocol.DecisionMDFileResult{Path: path, DecisionID: decisionID, IssueIDs: provenance[decisionID], OwnerIssueID: owners[decisionID], Action: "remove", Applied: !check}
		if !check {
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("remove obsolete decision markdown %s: %w", path, err)
			}
		}
	}
	return sortedDecisionMDResults(repoDir, resultsByPath), nil
}

func decisionIssueIDs(links []issues.DecisionLink) []string {
	seen := map[string]struct{}{}
	for _, link := range links {
		if link.TargetKind == issues.DecisionTargetIssue && strings.TrimSpace(link.TargetID) != "" {
			seen[strings.TrimSpace(link.TargetID)] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func decisionIssueIDsAtDecisionState(decision issues.Decision, links []issues.DecisionLink) []string {
	if decision.DeletedAt == nil {
		return decisionIssueIDs(links)
	}
	atDeletion := make([]issues.DecisionLink, 0, len(links))
	for _, link := range links {
		if link.DeletedAt != nil && link.DeletedAt.Equal(*decision.DeletedAt) {
			atDeletion = append(atDeletion, link)
		}
	}
	return decisionIssueIDs(atDeletion)
}

func decisionOwnerIssueID(issueIDs []string) string {
	if len(issueIDs) == 1 {
		return issueIDs[0]
	}
	return ""
}

func decisionMDID(name string, content []byte) string {
	if parsed, err := parseDecisionMarkdown(content); err == nil {
		return parsed.LocalID
	}
	stem := strings.TrimSuffix(name, ".md")
	if !isDecisionMDFilename(name) {
		return ""
	}
	if i := strings.IndexByte(stem, '-'); i >= 0 {
		rest := stem[i+1:]
		if j := strings.IndexByte(rest, '-'); j >= 0 {
			rest = rest[:j]
		}
		return "dec-" + rest
	}
	return ""
}

func sortedDecisionMDResults(repoDir string, results map[string]protocol.DecisionMDFileResult) []protocol.DecisionMDFileResult {
	paths := make([]string, 0, len(results))
	for path := range results {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]protocol.DecisionMDFileResult, 0, len(paths))
	for _, path := range paths {
		result := results[path]
		if rel, err := filepath.Rel(repoDir, path); err == nil && rel != "" {
			result.Path = rel
		}
		out = append(out, result)
	}
	return out
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
