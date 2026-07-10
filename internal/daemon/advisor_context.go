package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

const (
	advisorContextTokenBudget = 4500
	advisorContextFileLimit   = 4
	advisorContextPathLimit   = 12
	advisorContextFileBytes   = 16 * 1024
)

type advisorContextSource struct {
	Kind        string
	Ref         string
	Content     string
	Tokens      int
	Truncated   bool
	ExcludedFor string
}

type advisorContextPack struct {
	BudgetTokens int
	UsedTokens   int
	Sources      []advisorContextSource
}

func (p advisorContextPack) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Decision discussion context pack\nApproximate token budget: %d/%d used\n", p.UsedTokens, p.BudgetTokens)
	b.WriteString("Provenance and filtering:\n")
	for _, source := range p.Sources {
		status := fmt.Sprintf("included, ~%d tokens", source.Tokens)
		if source.Truncated {
			status += ", truncated"
		}
		if source.ExcludedFor != "" {
			status = "excluded: " + source.ExcludedFor
		}
		fmt.Fprintf(&b, "- %s %s: %s\n", sanitizeAdvisorLabel(source.Kind), sanitizeAdvisorLabel(source.Ref), status)
	}
	for _, source := range p.Sources {
		if source.Content == "" {
			continue
		}
		fmt.Fprintf(&b, "\n--- %s %s ---\n%s\n", sanitizeAdvisorLabel(source.Kind), sanitizeAdvisorLabel(source.Ref), source.Content)
	}
	return strings.TrimSpace(b.String())
}

func (p *advisorContextPack) add(kind, ref, content string, sourceBudget int) {
	content = sanitizeAdvisorText(content)
	remaining := p.BudgetTokens - p.UsedTokens
	limit := sourceBudget
	if limit > remaining {
		limit = remaining
	}
	if limit <= 0 || content == "" {
		p.Sources = append(p.Sources, advisorContextSource{Kind: kind, Ref: ref, ExcludedFor: "empty or context budget exhausted"})
		return
	}
	content, truncated := truncateAdvisorText(content, limit)
	tokens := approximateAdvisorTokens(content)
	p.UsedTokens += tokens
	p.Sources = append(p.Sources, advisorContextSource{Kind: kind, Ref: ref, Content: content, Tokens: tokens, Truncated: truncated})
}

func (p *advisorContextPack) exclude(kind, ref, reason string) {
	p.Sources = append(p.Sources, advisorContextSource{Kind: kind, Ref: ref, ExcludedFor: reason})
}

func (d *Daemon) buildAdvisorContextPack(ctx context.Context, projectID string, request domain.InteractionRequest) (advisorContextPack, error) {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return advisorContextPack{}, fmt.Errorf("issue context store unavailable")
	}
	repoDir := strings.TrimSpace(d.resolveRepoDirForProject(projectID))
	if repoDir == "" {
		return advisorContextPack{}, fmt.Errorf("repository context root unavailable")
	}

	pack := advisorContextPack{BudgetTokens: advisorContextTokenBudget}
	pack.add("request", request.ID, renderAdvisorRequest(request), 1100)

	task, err := client.GetWithRuntime(ctx, projectID, request.IssueID)
	if err != nil {
		return advisorContextPack{}, fmt.Errorf("load attached issue %s: %w", request.IssueID, err)
	}
	pack.add("issue", request.IssueID, renderAdvisorIssue(task), 850)
	pack.exclude("issue-notes", request.IssueID, "free-form notes are not needed for the decision pack")

	requirements, err := advisorRequirements(ctx, client, request.IssueID)
	if err != nil {
		return advisorContextPack{}, err
	}
	pack.add("requirements", request.IssueID, renderAdvisorRequirements(requirements), 850)

	decisions, err := advisorDecisions(ctx, client, request.IssueID, requirements)
	if err != nil {
		return advisorContextPack{}, err
	}
	pack.add("decisions", request.IssueID, renderAdvisorDecisions(decisions), 700)

	events, err := client.ListIssueObservationEvents(ctx, request.IssueID, issues.IssueObservationEventListOptions{Limit: 12, NewestFirst: true})
	if err != nil {
		return advisorContextPack{}, fmt.Errorf("load issue history: %w", err)
	}
	pack.add("history", request.IssueID, renderAdvisorHistory(events), 400)
	pack.exclude("history-payloads", request.IssueID, "arbitrary payload, session, and worktree fields are excluded")

	candidateText := strings.Join([]string{
		renderAdvisorRequest(request), renderAdvisorIssue(task),
		renderAdvisorRequirements(requirements), renderAdvisorDecisions(decisions),
	}, "\n")
	addAdvisorRepositorySources(&pack, repoDir, extractAdvisorPathCandidates(candidateText))
	return pack, nil
}

func renderAdvisorRequest(request domain.InteractionRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Decision key: %s\nState: %s\nRevision: %d\nQuestion: %s\nWhy needed: %s\nSignificance: %s\nRespondent: %s\n", request.DecisionKey, request.State, request.Revision, request.Question, request.Why, request.Significance, request.Respondent)
	if request.Context != "" {
		fmt.Fprintf(&b, "Supplied context: %s\n", request.Context)
	}
	if len(request.Options) > 0 {
		b.WriteString("Options:\n")
		for _, option := range request.Options {
			fmt.Fprintf(&b, "- %s: %s — %s\n", option.Key, option.Label, option.Description)
		}
	}
	if len(request.RequiredDecisions) > 0 {
		fmt.Fprintf(&b, "Required decisions: %s\n", strings.Join(request.RequiredDecisions, "; "))
	}
	fmt.Fprintf(&b, "Decision packet summary: %s\n", request.DecisionPacket.Summary)
	if len(request.DecisionPacket.Alternatives) > 0 {
		fmt.Fprintf(&b, "Alternatives: %s\n", strings.Join(request.DecisionPacket.Alternatives, "; "))
	}
	if request.DecisionPacket.Recommendation != "" {
		fmt.Fprintf(&b, "Requester's recommendation: %s\n", request.DecisionPacket.Recommendation)
	}
	if request.Proposal != nil {
		fmt.Fprintf(&b, "Current proposal (%s at %s): %s\n", request.Proposal.Actor, request.Proposal.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), request.Proposal.Answer)
	}
	return b.String()
}

func renderAdvisorIssue(task domain.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\nType: %s\nStatus: %s\nPriority: %s\nDescription: %s\nDesign: %s\nAcceptance: %s\n",
		task.Title, task.Type, task.Status, task.Priority, task.Description, task.Design, task.Acceptance)
	if task.ParentID != nil {
		fmt.Fprintf(&b, "Parent: %s\n", task.ParentID.String())
	}
	if len(task.Dependencies) > 0 {
		b.WriteString("Dependencies:\n")
		for _, dependency := range task.Dependencies {
			fmt.Fprintf(&b, "- %s: %s\n", dependency.Type, dependency.ID)
		}
	}
	return b.String()
}

func advisorRequirements(ctx context.Context, client *issues.Client, issueID string) ([]issues.Requirement, error) {
	links, err := client.ListSpecLinks(ctx, issues.SpecLinkFilter{IssueID: issueID})
	if err != nil {
		return nil, fmt.Errorf("load requirement links: %w", err)
	}
	out := make([]issues.Requirement, 0, len(links))
	for _, link := range links {
		requirement, getErr := client.GetRequirement(ctx, link.RequirementID)
		if getErr != nil {
			return nil, fmt.Errorf("load requirement %s: %w", link.RequirementID, getErr)
		}
		out = append(out, requirement)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LocalID < out[j].LocalID })
	return out, nil
}

func renderAdvisorRequirements(requirements []issues.Requirement) string {
	if len(requirements) == 0 {
		return "No linked requirements."
	}
	var b strings.Builder
	for _, requirement := range requirements {
		fmt.Fprintf(&b, "- %s [%s] %s: %s\n", requirement.LocalID, requirement.Status, requirement.Title, requirement.Description)
	}
	return b.String()
}

func advisorDecisions(ctx context.Context, client *issues.Client, issueID string, requirements []issues.Requirement) ([]issues.Decision, error) {
	byID := map[string]issues.Decision{}
	add := func(found []issues.Decision) {
		for _, decision := range found {
			byID[decision.LocalID] = decision
		}
	}
	linked, err := client.ListDecisions(ctx, issues.DecisionFilter{IssueID: issueID})
	if err != nil {
		return nil, fmt.Errorf("load issue decisions: %w", err)
	}
	add(linked)
	for _, requirement := range requirements {
		linked, err = client.ListDecisions(ctx, issues.DecisionFilter{RequirementID: requirement.LocalID})
		if err != nil {
			return nil, fmt.Errorf("load decisions for requirement %s: %w", requirement.LocalID, err)
		}
		add(linked)
	}
	out := make([]issues.Decision, 0, len(byID))
	for _, decision := range byID {
		out = append(out, decision)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LocalID < out[j].LocalID })
	return out, nil
}

func renderAdvisorDecisions(decisions []issues.Decision) string {
	if len(decisions) == 0 {
		return "No linked decisions."
	}
	var b strings.Builder
	for _, decision := range decisions {
		fmt.Fprintf(&b, "- %s %s\n  Rationale: %s\n  Context: %s\n  Consequences: %s\n", decision.LocalID, decision.Title, decision.Rationale, decision.Context, decision.Consequences)
	}
	return b.String()
}

func renderAdvisorHistory(events []domain.IssueObservationEvent) string {
	if len(events) == 0 {
		return "No issue history events."
	}
	var b strings.Builder
	for _, event := range events {
		fmt.Fprintf(&b, "- %s %s source=%s", event.ObservedAt.UTC().Format("2006-01-02T15:04:05Z"), sanitizeAdvisorLabel(string(event.Type)), sanitizeAdvisorLabel(event.Source))
		if details := renderAdvisorHistoryDetails(event.Payload); details != "" {
			fmt.Fprintf(&b, " (%s)", details)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func renderAdvisorHistoryDetails(payload map[string]any) string {
	allowed := []string{"from_status", "to_status", "dependency_type", "depends_on_id", "issue_type", "priority", "status", "title"}
	details := make([]string, 0, len(allowed))
	for _, key := range allowed {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		switch value.(type) {
		case string, float64, bool:
			details = append(details, key+"="+sanitizeAdvisorLabel(fmt.Sprint(value)))
		}
	}
	return strings.Join(details, ", ")
}

var advisorPathPattern = regexp.MustCompile(`(?:^|[[:space:]\x60'"(\[])(/?([A-Za-z0-9_.@+-]+/)+[A-Za-z0-9_.@+-]+)`)

func extractAdvisorPathCandidates(text string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, advisorContextFileLimit)
	for _, match := range advisorPathPattern.FindAllStringSubmatch(text, -1) {
		candidate := strings.Trim(strings.TrimSpace(match[1]), ".,;:!?)]}'\"")
		if candidate == "" {
			continue
		}
		candidate = filepath.ToSlash(filepath.Clean(candidate))
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	sort.Strings(out)
	return out
}

func addAdvisorRepositorySources(pack *advisorContextPack, repoDir string, candidates []string) {
	if len(candidates) > advisorContextPathLimit {
		pack.exclude("repository-paths", fmt.Sprintf("%d additional candidates", len(candidates)-advisorContextPathLimit), "candidate count limit reached")
		candidates = candidates[:advisorContextPathLimit]
	}
	included := 0
	for _, candidate := range candidates {
		if included >= advisorContextFileLimit {
			pack.exclude("repository-path", candidate, "repository excerpt count limit reached")
			continue
		}
		content, reason := readAdvisorRepositoryFile(repoDir, candidate)
		if reason != "" {
			pack.exclude("repository-path", candidate, reason)
			continue
		}
		pack.add("repository-file", candidate, content, 350)
		included++
	}
}

func readAdvisorRepositoryFile(repoDir, candidate string) (string, string) {
	if filepath.IsAbs(candidate) {
		return "", "absolute paths are excluded"
	}
	clean := filepath.Clean(candidate)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "path escapes the repository"
	}
	for _, component := range strings.Split(filepath.ToSlash(clean), "/") {
		lower := strings.ToLower(component)
		if strings.HasPrefix(component, ".") || lower == "vendor" || lower == "node_modules" || lower == "dist" || lower == "build" {
			return "", "hidden, generated, or vendored paths are excluded"
		}
	}
	base := strings.ToLower(filepath.Base(clean))
	ext := strings.ToLower(filepath.Ext(clean))
	if strings.HasPrefix(base, ".env") || base == "credentials.json" || strings.Contains(base, "secret") || ext == ".pem" || ext == ".key" || ext == ".p12" {
		return "", "credential-bearing paths are excluded"
	}
	allowed := map[string]bool{".go": true, ".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true, ".sql": true, ".sh": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".css": true, ".html": true}
	if !allowed[ext] {
		return "", "non-text or unsupported file type"
	}
	root, err := filepath.Abs(repoDir)
	if err != nil {
		return "", "repository root is unavailable"
	}
	if resolvedRoot, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolvedRoot
	}
	path := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "path does not resolve to a repository file"
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "symlink escapes the repository"
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", "path is unreadable"
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, advisorContextFileBytes+1))
	if err != nil {
		return "", "path is unreadable"
	}
	if len(data) > advisorContextFileBytes {
		data = data[:advisorContextFileBytes]
	}
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return "", "binary content is excluded"
	}
	return string(data), ""
}

var advisorSensitiveLinePattern = regexp.MustCompile(`(?i)(password|secret|api[_-]?key|access[_-]?token|auth[_-]?token|refresh[_-]?token|bearer[_-]?token|private[_-]?key)["']?[[:space:]]*[:=]`)
var advisorSecretValuePattern = regexp.MustCompile(`(?i)(gh[pousr]_[a-z0-9_]{20,}|sk-[a-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|Bearer[[:space:]]+[a-z0-9._~+/=-]{16,})`)

func sanitizeAdvisorLabel(label string) string {
	label = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(label))
	label = strings.Join(strings.Fields(label), " ")
	runes := []rune(label)
	if len(runes) > 160 {
		label = string(runes[:160]) + "…"
	}
	return label
}

func sanitizeAdvisorText(text string) string {
	text = strings.ReplaceAll(text, "\x00", "")
	text = advisorSecretValuePattern.ReplaceAllString(text, "[REDACTED secret value]")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if advisorSensitiveLinePattern.MatchString(line) || strings.Contains(line, "-----BEGIN PRIVATE KEY-----") {
			lines[i] = "[REDACTED sensitive line]"
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func approximateAdvisorTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len([]byte(text)) + 3) / 4
}

func truncateAdvisorText(text string, maxTokens int) (string, bool) {
	maxBytes := maxTokens * 4
	data := []byte(text)
	if len(data) <= maxBytes {
		return text, false
	}
	marker := []byte("\n[TRUNCATED]")
	contentBytes := maxBytes - len(marker)
	if contentBytes < 0 {
		contentBytes = 0
	}
	data = data[:contentBytes]
	for !utf8.Valid(data) && len(data) > 0 {
		data = data[:len(data)-1]
	}
	return strings.TrimSpace(string(data)) + string(marker), true
}
