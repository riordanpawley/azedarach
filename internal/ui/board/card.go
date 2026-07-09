package board

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// CardContentHeight is the single source of truth for rendered card content height.
const CardContentHeight = 4
const narrowCardExtraLines = 1
const expandedHeaderWorstCase = "P2 CHE-1234 T Φ9 ◐ W 12h ✓ M:queued(100%) ↑99 ↓99 ✎ +999k/-999k [99/99]"

const tmuxSessionToken = "T"
const descendantTmuxSessionToken = "Td"
const tmuxAttachedToken = "A"
const worktreeToken = "✓"
const nestedIssuePrefix = "↳"

// RuntimeSignals represents runtime metadata rendered for a task card.
// These values are computed independently from session state.
type RuntimeSignals struct {
	HasTmuxSession           bool
	HasDescendantTmuxSession bool
	TmuxAttached             bool
	TmuxAttachedCount        int
	HasWorktree              bool
	GitAheadCount            int
	GitBehindCount           int
	HasUncommittedChanges    bool
	HasConflicts             bool
	ConflictFiles            []string
	GitAdditions             int
	GitDeletions             int
	PendingOperationState    string
	PendingOperationID       string
	PendingOperationPercent  int
}

// ChildProgress summarizes completion progress for a parent task's children.
type ChildProgress struct {
	Total int
	Done  int
}

// CardState bundles the per-card display flags resolved by the column layer so
// renderCard takes one struct instead of an ever-growing positional list.
type CardState struct {
	IsCursor         bool
	IsSelected       bool
	IsMergeCandidate bool
	ShowPhases       bool
	JumpLabel        string
}

// renderCard renders a task card
func renderCard(task domain.Task, state CardState, runtimeSignals *RuntimeSignals, childProgress *ChildProgress, phaseInfo *phases.TaskPhaseInfo, width int, s *styles.Styles) string {
	if width < 1 {
		width = 1
	}

	// Choose card style based on state. The card border is a fixed channel for
	// cursor + selection state; the merge-candidate hint lives in its own
	// header badge below so it doesn't fight those colours.
	cardStyle := s.Card
	if state.IsSelected {
		cardStyle = s.CardSelected
	} else if state.IsCursor {
		cardStyle = s.CardActive
	}

	// Apply fixed size to keep all cards same height regardless of content.
	cardStyle = cardStyle.Width(width).Height(CardContentHeight)

	// Priority badge (e.g., "P0", "P1", etc.)
	priorityText := task.Priority.String()
	priorityBadge := s.PriorityBadge(int(task.Priority)).Render(priorityText)

	// Phase badge (if enabled and phase info available)
	var phaseBadge string
	if state.ShowPhases && phaseInfo != nil {
		phaseStyle := s.Card.
			Foreground(styles.Blue).
			Bold(true)
		if phaseInfo.Phase == 0 {
			// Phase 0 is ready (green)
			phaseStyle = phaseStyle.Foreground(styles.Green)
		} else if phaseInfo.Phase > 0 {
			// Phase > 0 is blocked (yellow/orange)
			phaseStyle = phaseStyle.Foreground(styles.Yellow)
		}
		phaseBadge = phaseStyle.Render(fmt.Sprintf("Φ%d", phaseInfo.Phase))
	}

	// Title - truncate if needed
	// Account for border + padding.
	maxLineLen := width - 4
	if maxLineLen < 1 {
		maxLineLen = 1
	}
	// Cursor indicator (▶ symbol when cursor is on this card)
	cursor := ""
	if state.IsCursor {
		cursor = "▶"
	}

	issueToken := task.ID.String()
	if taskIsNested(task) {
		issueToken = nestedIssuePrefix + issueToken
	}
	if cursor != "" {
		issueToken = cursor + issueToken
	}
	headerParts := []string{
		priorityBadge,
	}
	if state.JumpLabel != "" {
		headerParts = append(headerParts, renderJumpLabel(state.JumpLabel, s))
	}
	if state.IsMergeCandidate {
		headerParts = append(headerParts, renderMergeCandidateBadge(s))
	}
	headerParts = append(headerParts, issueToken)
	if phaseBadge != "" {
		headerParts = append(headerParts, phaseBadge)
	}

	// Session status row (if session exists)
	var sessionRow string
	if task.Session != nil {
		sessionRow = renderSessionStatus(task.Session, s)
	}
	sessionCompact := renderSessionStatusCompact(task.Session)
	visibleRuntimeSignals := runtimeSignalsForHeader(task.Session, runtimeSignals)
	runtimeRow := renderRuntimeSignals(visibleRuntimeSignals, s)
	runtimeCompact := renderRuntimeSignalsCompact(visibleRuntimeSignals, s)
	typeBadge := renderTaskTypeBadge(task.Type, s)
	headerTitle := strings.Join(headerParts, " ")
	type headerToken struct {
		full              string
		compact           string
		compactWhenNarrow bool
	}
	preferCompact := maxLineLen < 44
	headerTokens := []headerToken{
		{full: sessionRow, compact: sessionCompact},
		{full: runtimeRow, compact: runtimeCompact},
		{full: renderPullRequestBadge(task.PullRequest, s), compact: renderPullRequestBadgeCompact(task.PullRequest, s), compactWhenNarrow: true},
		{full: renderChildProgressValue(childProgress, s), compact: renderChildProgressValue(childProgress, s)},
		{full: typeBadge, compact: typeBadge},
	}
	if preferCompact {
		headerTokens = []headerToken{
			{full: sessionRow, compact: sessionCompact},
			{full: renderChildProgressValue(childProgress, s), compact: renderChildProgressValue(childProgress, s)},
			{full: typeBadge, compact: typeBadge},
			{full: runtimeRow, compact: runtimeCompact},
			{full: renderPullRequestBadge(task.PullRequest, s), compact: renderPullRequestBadgeCompact(task.PullRequest, s), compactWhenNarrow: true},
		}
	}
	headerLines := []string{headerTitle}
	cardHeight := CardContentHeight
	if shouldExpandCardHeader(maxLineLen) {
		headerLines = []string{headerTitle, ""}
		cardHeight += narrowCardExtraLines
	}
	for _, token := range headerTokens {
		if token.compactWhenNarrow && preferCompact {
			if appendHeaderToken(headerLines, maxLineLen, token.full, token.compact, true) {
				continue
			}
		}
		if appendHeaderToken(headerLines, maxLineLen, token.full, token.full, false) {
			continue
		}
		appendHeaderToken(headerLines, maxLineLen, token.full, token.compact, preferCompact)
	}

	titleLines := renderTitleBodyLines(task.Title, maxLineLen, cardHeight-len(headerLines))

	// Compose fixed content rows to guarantee stable card height.
	lines := make([]string, 0, cardHeight)
	for _, headerLine := range headerLines {
		lines = append(lines, ansi.Truncate(headerLine, maxLineLen, "…"))
	}
	for _, line := range titleLines {
		lines = append(lines, ansi.Truncate(line, maxLineLen, "…"))
	}
	if len(lines) > 0 {
		lines[len(lines)-1] = overlayOriginBadge(lines[len(lines)-1], maxLineLen, task.Origin)
	}
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)

	return cardStyle.Height(cardHeight).Render(content)
}

func taskIsNested(task domain.Task) bool {
	if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) != "" {
		return true
	}
	for _, dep := range task.Dependencies {
		depType := strings.TrimSpace(string(dep.Type))
		if (depType == string(domain.DependencyParentChild) || depType == "parent_child") && strings.TrimSpace(dep.ID.String()) != "" {
			return true
		}
	}
	return false
}

// overlayOriginBadge composes the last content line so that an origin badge
// occupies the bottom-right corner of the card. When the rendered badge would
// not fit, the badge is omitted and the original line is returned unchanged.
func overlayOriginBadge(line string, maxLineLen int, origin string) string {
	badge := renderOriginBadge(origin)
	if badge == "" {
		return line
	}
	badgeWidth := ansi.StringWidth(badge)
	if badgeWidth <= 0 || badgeWidth >= maxLineLen {
		return line
	}
	contentRoom := maxLineLen - badgeWidth - 1
	if contentRoom < 0 {
		contentRoom = 0
	}
	truncated := ansi.Truncate(line, contentRoom, "…")
	gap := maxLineLen - ansi.StringWidth(truncated) - badgeWidth
	if gap < 1 {
		gap = 1
	}
	return truncated + strings.Repeat(" ", gap) + badge
}

// RenderOriginBadge returns a styled badge identifying the origination of an
// issue (e.g. "linear" vs "local"). Unknown providers render with a neutral
// fallback style so new providers are still visible without code changes.
func RenderOriginBadge(origin string) string {
	return renderOriginBadge(origin)
}

func renderOriginBadge(origin string) string {
	origin = strings.TrimSpace(strings.ToLower(origin))
	if origin == "" {
		return ""
	}
	var (
		label string
		bg    lipgloss.Color
	)
	switch origin {
	case "local":
		label = "loc"
		bg = styles.Overlay1
	case "linear":
		label = "lin"
		bg = styles.Lavender
	case "github", "gh":
		label = "gh "
		bg = styles.Mauve
	default:
		runes := []rune(origin)
		label = string(runes[:1])
		if len(runes) > 1 {
			label += string(runes[1:2])
		}
		bg = styles.Overlay2
	}
	return lipgloss.NewStyle().
		Foreground(styles.Base).
		Background(bg).
		Bold(true).
		Render(label)
}

func renderPullRequestBadge(pr *domain.PullRequest, s *styles.Styles) string {
	if pr == nil {
		return ""
	}
	label := pullRequestLabel(pr)
	if status := pullRequestBadgeStatus(pr); status != "" {
		label += "/" + status
	}
	return s.Card.Foreground(styles.Mauve).Bold(true).Render(label)
}

func renderPullRequestBadgeCompact(pr *domain.PullRequest, s *styles.Styles) string {
	if pr == nil {
		return ""
	}
	label := "PR" + pullRequestCompactStatusIcon(pr)
	return s.Card.Foreground(styles.Mauve).Bold(true).Render(label)
}

func pullRequestBadgeStatus(pr *domain.PullRequest) string {
	if pr == nil {
		return ""
	}
	if pr.Draft {
		return "dr"
	}
	if state := compactTerminalPullRequestState(pr.State); state != "" {
		return state
	}
	if checks := compactChecksStatus(pr.ChecksStatus); checks != "" {
		return checks
	}
	return compactPullRequestState(pr.State)
}

func pullRequestCompactStatusIcon(pr *domain.PullRequest) string {
	if pr == nil {
		return ""
	}
	if pr.Draft {
		return "D"
	}
	if icon := compactTerminalPullRequestStateIcon(pr.State); icon != "" {
		return icon
	}
	status := strings.TrimSpace(strings.ToLower(pr.ChecksStatus))
	if status == "" {
		status = strings.TrimSpace(strings.ToLower(pr.State))
	}
	switch status {
	case "pass", "passing", "success", "succeeded":
		return "✓"
	case "fail", "failing", "failure", "failed", "error":
		return "✗"
	case "pending", "queued", "in_progress", "inprogress", "running", "waiting", "expected", "action_required":
		return "…"
	case "cancelled", "canceled", "skipped", "neutral":
		return "-"
	default:
		return ""
	}
}

func compactTerminalPullRequestState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "merged":
		return "mrg"
	default:
		return ""
	}
}

func compactTerminalPullRequestStateIcon(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "merged":
		return "M"
	default:
		return ""
	}
}

func pullRequestLabel(pr *domain.PullRequest) string {
	if pr == nil {
		return ""
	}
	if displayKey := strings.TrimSpace(pr.DisplayKey); displayKey != "" {
		return "PR" + displayKey
	}
	if pr.Number > 0 {
		return fmt.Sprintf("PR#%d", pr.Number)
	}
	if remoteKey := strings.TrimSpace(pr.RemoteKey); remoteKey != "" {
		return "PR " + remoteKey
	}
	return "PR"
}

func compactChecksStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "":
		return ""
	case "pass", "passing", "success", "succeeded":
		return "ok"
	case "fail", "failing", "failure", "failed", "error":
		return "fail"
	case "pending", "queued", "in_progress", "inprogress", "running", "waiting", "expected", "action_required":
		return "pend"
	case "cancelled", "canceled":
		return "can"
	case "skipped", "neutral":
		return "skip"
	default:
		return status
	}
}

func compactPullRequestState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "":
		return ""
	case "open":
		return "open"
	case "closed":
		return "cls"
	case "merged":
		return "mrg"
	default:
		return state
	}
}

// renderMergeCandidateBadge paints a single-character badge in the card
// header indicating that this task is an eligible merge target while the
// in-board merge picker is active. Using a header badge (rather than the
// card border) avoids colliding with the cursor and selection styles.
func renderMergeCandidateBadge(s *styles.Styles) string {
	style := lipgloss.NewStyle().
		Foreground(styles.Base).
		Background(styles.Green).
		Bold(true).
		Padding(0, 1)
	if s != nil {
		style = s.MenuKey.
			Foreground(styles.Base).
			Background(styles.Green).
			Bold(true)
	}
	return style.Render("M")
}

func renderJumpLabel(label string, s *styles.Styles) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	labelStyle := lipgloss.NewStyle().
		Foreground(styles.Base).
		Background(styles.Yellow).
		Bold(true).
		Padding(0, 1)
	if s != nil {
		labelStyle = s.MenuKey.
			Foreground(styles.Base).
			Background(styles.Yellow).
			Bold(true)
	}
	return labelStyle.Render(label)
}

func shouldExpandCardHeader(maxLineLen int) bool {
	return ansi.StringWidth(expandedHeaderWorstCase) > maxLineLen
}

func appendHeaderToken(headerLines []string, maxLineLen int, full string, compact string, preferCompact bool) bool {
	if full == "" && compact == "" {
		return true
	}

	try := func(lineIdx int, token string) bool {
		if token == "" {
			return false
		}
		if lineIdx < 0 || lineIdx >= len(headerLines) {
			return false
		}
		candidate := headerLines[lineIdx]
		if strings.TrimSpace(candidate) != "" {
			candidate += " "
		} else {
			candidate = ""
		}
		candidate += token
		if ansi.StringWidth(candidate) <= maxLineLen {
			headerLines[lineIdx] = candidate
			return true
		}
		return false
	}

	appendToAnyLine := func(token string) bool {
		for i := range headerLines {
			if try(i, token) {
				return true
			}
		}
		return false
	}

	if preferCompact {
		if appendToAnyLine(compact) {
			return true
		}
		if appendToAnyLine(full) {
			return true
		}
		return false
	}

	if appendToAnyLine(full) {
		return true
	}
	if appendToAnyLine(compact) {
		return true
	}
	return false
}

func runtimeSignalsForHeader(session *domain.Session, signals *RuntimeSignals) *RuntimeSignals {
	if signals == nil {
		return nil
	}
	normalized := *signals
	if session != nil {
		// Session badge already communicates active session state; omit duplicate tmux tokens.
		normalized.HasTmuxSession = false
		normalized.HasDescendantTmuxSession = false
	}
	return &normalized
}

func renderTaskTypeBadge(taskType domain.TaskType, s *styles.Styles) string {
	letter := taskType.Short()
	background := styles.Surface1

	switch taskType {
	case domain.TypeEpic:
		background = styles.Mauve
	case domain.TypeFeature:
		background = styles.Green
	case domain.TypeBug:
		background = styles.Red
	case domain.TypeTask:
		background = styles.Blue
	case domain.TypeChore:
		background = styles.Yellow
	}

	badgeStyle := lipgloss.NewStyle().
		Foreground(styles.Base).
		Background(background).
		Bold(true).
		Padding(0, 1)

	if s != nil {
		badgeStyle = s.TypeBadge.
			Foreground(styles.Base).
			Background(background).
			Bold(true)
	}

	return badgeStyle.Render(letter)
}

func renderTitleBodyLines(title string, maxLineLen int, maxLines int) []string {
	if maxLines <= 0 {
		return []string{}
	}
	lines := make([]string, 0, maxLines)
	if maxLineLen < 1 {
		for len(lines) < maxLines {
			lines = append(lines, "")
		}
		return lines
	}

	runes := []rune(title)
	for len(lines) < maxLines {
		if len(runes) == 0 {
			lines = append(lines, "")
			continue
		}
		if len(runes) <= maxLineLen {
			lines = append(lines, string(runes))
			runes = nil
			continue
		}
		if len(lines) == maxLines-1 {
			if maxLineLen == 1 {
				lines = append(lines, "…")
				break
			}
			lines = append(lines, string(runes[:maxLineLen-1])+"…")
			break
		}
		lines = append(lines, string(runes[:maxLineLen]))
		runes = runes[maxLineLen:]
	}
	return lines
}

// renderSessionStatus renders the session status line with icon and elapsed time
func renderSessionStatus(session *domain.Session, s *styles.Styles) string {
	icon := session.DisplayIcon()

	// Elapsed time if active/waiting and started.
	var elapsed string
	displayState, hasDisplayState := session.DisplayState()
	if !hasDisplayState {
		if session.DisplayActivity() == "no-agent" {
			displayState = domain.SessionIdle
		} else {
			displayState = session.State
		}
	}
	if session.StartedAt != nil && (displayState == domain.SessionBusy || displayState == domain.SessionWaiting) {
		d := time.Since(*session.StartedAt)
		elapsed = formatCompactDuration(d)
	}

	stateStyle := s.Session(session)
	label := renderSessionStatusLabel(session)
	var value string
	if label == "" {
		value = icon
	} else if elapsed != "" {
		value = fmt.Sprintf("%s %s %s", icon, label, elapsed)
	} else {
		value = fmt.Sprintf("%s %s", icon, label)
	}
	return stateStyle.Render(value)
}

func renderSessionStatusLabel(session *domain.Session) string {
	if session == nil {
		return "idle"
	}
	if session.IsPartial() {
		return ""
	}
	if displayState, ok := session.DisplayState(); ok {
		return sessionStateCardLabel(displayState)
	}
	if session.DisplayActivity() != "" {
		return ""
	}
	return sessionStateCardLabel(session.State)
}

func sessionStateCardLabel(state domain.SessionState) string {
	switch state {
	case domain.SessionBusy:
		return "busy"
	case domain.SessionIdle:
		return "idle"
	case domain.SessionWaiting:
		return "wait"
	case domain.SessionDone:
		return "done"
	default:
		return ""
	}
}

func renderSessionStatusCompact(session *domain.Session) string {
	if session == nil {
		return ""
	}

	return session.DisplayIcon()
}

func renderRuntimeSignals(signals *RuntimeSignals, s *styles.Styles) string {
	if signals == nil {
		return ""
	}

	hasLineChanges := signals.GitAdditions > 0 || signals.GitDeletions > 0
	parts := make([]string, 0, 6)
	if signals.HasTmuxSession {
		parts = append(parts, renderRuntimeSignalToken(tmuxSessionToken, styles.Sky, s))
	}
	if signals.HasDescendantTmuxSession {
		parts = append(parts, renderRuntimeSignalToken(descendantTmuxSessionToken, styles.Sky, s))
	}
	if token := tmuxAttachedRuntimeToken(signals, false); token != "" {
		parts = append(parts, renderRuntimeSignalToken(token, styles.Blue, s))
	}
	if signals.HasWorktree {
		parts = append(parts, renderRuntimeSignalToken(worktreeToken, styles.Teal, s))
	}
	if pendingToken := renderPendingOperationToken(signals.PendingOperationState, signals.PendingOperationPercent, false); pendingToken != "" {
		parts = append(parts, renderRuntimeSignalToken(pendingToken, pendingOperationTokenColor(signals.PendingOperationState), s))
	}
	if signals.HasConflicts {
		parts = append(parts, renderRuntimeSignalToken("conflict", styles.Red, s))
	}
	if signals.GitAheadCount > 0 {
		parts = append(parts, renderRuntimeSignalToken(fmt.Sprintf("↑%s", formatCompactCount(signals.GitAheadCount)), styles.Green, s))
	}
	if signals.GitBehindCount > 0 {
		parts = append(parts, renderRuntimeSignalToken(fmt.Sprintf("↓%s", formatCompactCount(signals.GitBehindCount)), styles.Yellow, s))
	}
	if signals.HasUncommittedChanges {
		parts = append(parts, renderRuntimeSignalToken("✎", styles.Peach, s))
	}
	if hasLineChanges {
		if s == nil {
			parts = append(parts, fmt.Sprintf("+%s/-%s", formatCompactCount(signals.GitAdditions), formatCompactCount(signals.GitDeletions)))
		} else {
			add := renderRuntimeSignalToken(fmt.Sprintf("+%s", formatCompactCount(signals.GitAdditions)), styles.Green, s)
			del := renderRuntimeSignalToken(fmt.Sprintf("-%s", formatCompactCount(signals.GitDeletions)), styles.Red, s)
			sep := lipgloss.NewStyle().Foreground(styles.Overlay0).Render("/")
			parts = append(parts, add+sep+del)
		}
	}

	return strings.Join(parts, " ")
}

func renderRuntimeSignalsCompact(signals *RuntimeSignals, s *styles.Styles) string {
	if signals == nil {
		return ""
	}

	parts := make([]string, 0, 3)
	if signals.HasTmuxSession {
		parts = append(parts, renderRuntimeSignalToken("T", styles.Sky, s))
	}
	if signals.HasDescendantTmuxSession {
		parts = append(parts, renderRuntimeSignalToken("Td", styles.Sky, s))
	}
	if token := tmuxAttachedRuntimeToken(signals, true); token != "" {
		parts = append(parts, renderRuntimeSignalToken(token, styles.Blue, s))
	}
	if signals.HasWorktree {
		parts = append(parts, renderRuntimeSignalToken(worktreeToken, styles.Teal, s))
	}
	if pendingToken := renderPendingOperationToken(signals.PendingOperationState, signals.PendingOperationPercent, true); pendingToken != "" {
		parts = append(parts, renderRuntimeSignalToken(pendingToken, pendingOperationTokenColor(signals.PendingOperationState), s))
	}
	if signals.HasConflicts {
		parts = append(parts, renderRuntimeSignalToken("C!", styles.Red, s))
	}

	hasChanges := signals.HasUncommittedChanges || signals.GitAdditions > 0 || signals.GitDeletions > 0
	if hasChanges {
		token := "G*"
		if divergence := formatAheadBehindToken(signals.GitAheadCount, signals.GitBehindCount); divergence != "" {
			token += divergence
		}
		parts = append(parts, renderRuntimeSignalToken(token, styles.Peach, s))
	} else if signals.GitAheadCount > 0 || signals.GitBehindCount > 0 {
		parts = append(parts, renderRuntimeSignalToken("G"+formatAheadBehindToken(signals.GitAheadCount, signals.GitBehindCount), styles.Yellow, s))
	}

	return strings.Join(parts, "")
}

func tmuxAttachedRuntimeToken(signals *RuntimeSignals, compact bool) string {
	if signals == nil {
		return ""
	}
	count := signals.TmuxAttachedCount
	if signals.TmuxAttached && count <= 0 {
		count = 1
	}
	if count <= 0 {
		return ""
	}
	if compact || count == 1 {
		return tmuxAttachedToken
	}
	return fmt.Sprintf("%s%d", tmuxAttachedToken, count)
}

func formatAheadBehindToken(ahead, behind int) string {
	if ahead > 0 && behind > 0 {
		return fmt.Sprintf("↑%s/↓%s", formatCompactCount(ahead), formatCompactCount(behind))
	}
	if behind > 0 {
		return fmt.Sprintf("↓%s", formatCompactCount(behind))
	}
	if ahead > 0 {
		return fmt.Sprintf("↑%s", formatCompactCount(ahead))
	}
	return ""
}

func renderPendingOperationToken(state string, percent int, compact bool) string {
	state = strings.ToLower(strings.TrimSpace(state))
	if state == "" {
		return ""
	}
	if state == "failed" {
		return "M:!"
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	if compact {
		if percent > 0 {
			return fmt.Sprintf("M:%s%d", strings.ToUpper(string(state[0])), percent)
		}
		return "M:" + strings.ToUpper(string(state[0]))
	}
	if percent > 0 {
		return fmt.Sprintf("M:%s(%d%%)", state, percent)
	}
	return "M:" + state
}

func pendingOperationTokenColor(state string) lipgloss.Color {
	if strings.EqualFold(strings.TrimSpace(state), "failed") {
		return styles.Red
	}
	return styles.Mauve
}

func renderRuntimeSignalToken(token string, color lipgloss.Color, s *styles.Styles) string {
	if s == nil {
		return token
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(token)
}

// formatDuration formats a duration as "2d 2h", "2h 34m", or "45m".
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalHours := int(d.Hours())
	days := totalHours / 24
	h := totalHours % 24
	m := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, h)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func formatCompactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalHours := int(d.Hours())
	days := totalHours / 24
	h := totalHours % 24
	m := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	if h > 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dm", m)
}

func formatCompactCount(n int) string {
	sign := ""
	value := n
	if value < 0 {
		sign = "-"
		value = -value
	}
	switch {
	case value >= 1000000:
		return fmt.Sprintf("%s%dM", sign, roundedUnit(value, 1000000))
	case value >= 1000:
		rounded := roundedUnit(value, 1000)
		if rounded >= 1000 {
			return fmt.Sprintf("%s1M", sign)
		}
		return fmt.Sprintf("%s%dk", sign, rounded)
	default:
		return fmt.Sprintf("%s%d", sign, value)
	}
}

func roundedUnit(value, unit int) int {
	if value <= 0 {
		return 0
	}
	return (value + unit/2) / unit
}

// renderChildProgress renders child completion progress with completion ratio.
func renderChildProgress(progress ChildProgress, s *styles.Styles) string {
	if progress.Total <= 0 {
		return ""
	}
	return s.EpicProgress.Render(fmt.Sprintf("[%d/%d]", progress.Done, progress.Total))
}

func renderChildProgressValue(progress *ChildProgress, s *styles.Styles) string {
	if progress == nil {
		return ""
	}
	return renderChildProgress(*progress, s)
}

// RenderCard is the exported version for testing
func RenderCard(task domain.Task, isCursor bool, isSelected bool, width int, s *styles.Styles) string {
	return renderCard(task, CardState{IsCursor: isCursor, IsSelected: isSelected}, nil, nil, nil, width, s)
}

// RenderCardWithRuntimeSignals renders a task card with daemon-authored runtime metadata.
func RenderCardWithRuntimeSignals(task domain.Task, runtimeSignals *RuntimeSignals, isCursor bool, isSelected bool, width int, s *styles.Styles) string {
	return renderCard(task, CardState{IsCursor: isCursor, IsSelected: isSelected}, runtimeSignals, nil, nil, width, s)
}

// CardLineFootprint returns the number of terminal lines consumed by one card.
// Column rendering stacks cards with newline separators and uses this value as
// the row stride for cursor/viewport math.
func CardLineFootprint(s *styles.Styles, width int) int {
	if width < 1 {
		width = 1
	}
	sample := domain.Task{
		ID:       "sample",
		Title:    "sample",
		Status:   domain.StatusOpen,
		Priority: domain.P2,
		Type:     domain.TypeTask,
	}
	cardLines := lipgloss.Height(renderCard(sample, CardState{}, nil, nil, nil, width, s))
	if cardLines < 1 {
		cardLines = 1
	}
	return cardLines
}

// BuildChildProgress computes done/total child counts keyed by parent task ID.
func BuildChildProgress(tasks []domain.Task) map[string]ChildProgress {
	progressByParent := make(map[string]ChildProgress)
	for _, task := range tasks {
		parentID := ""
		if task.ParentID != nil && task.ParentID.String() != "" {
			parentID = task.ParentID.String()
		} else {
			for _, dep := range task.Dependencies {
				if dep.Type == domain.DependencyParentChild && dep.ID.String() != "" {
					parentID = dep.ID.String()
					break
				}
			}
		}
		if parentID == "" {
			continue
		}
		progress := progressByParent[parentID]
		progress.Total++
		if task.Status == domain.StatusDone {
			progress.Done++
		}
		progressByParent[parentID] = progress
	}
	return progressByParent
}
