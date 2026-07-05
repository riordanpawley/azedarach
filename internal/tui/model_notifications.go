package app

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

var notificationStructuredReferencePattern = regexp.MustCompile(`\b(?:[a-z]{2,10}-\d+|op[-_][a-zA-Z0-9._:-]+)\b`)
var notificationBareIssueReferencePattern = regexp.MustCompile(`\b[a-z]{3,5}[a-z0-9]{0,2}\b`)
var writeNotificationClipboardText = writeClipboardText

func (m *Model) recordNotificationHistory(toast Toast, createdAt time.Time) string {
	id := m.feedback.addLocalToast(toast, createdAt)
	m.refreshFeedbackProjectionOutputs(createdAt)
	return id
}

func notificationReference(message string) string {
	normalized := strings.ToLower(message)
	if match := notificationStructuredReferencePattern.FindString(normalized); match != "" {
		return strings.Trim(match, ".,:;()[]{}")
	}
	for _, match := range notificationBareIssueReferencePattern.FindAllString(normalized, -1) {
		match = strings.Trim(match, ".,:;()[]{}")
		if match == "" {
			continue
		}
		// Prefer short lower-case issue keys such as ctk and azx, but avoid
		// common words that appear in notification prose.
		if !isCommonNotificationWord(match) {
			return match
		}
	}
	return ""
}

func isCommonNotificationWord(value string) bool {
	switch value {
	case "and", "are", "because", "current", "error", "failed", "for", "from", "has", "into", "next", "notice", "queued", "task", "the", "this", "update", "warning", "with":
		return true
	default:
		return false
	}
}

func (m Model) notificationHistoryIndicator() string {
	errors, notices := m.notificationAttentionCounts()
	parts := make([]string, 0, 2)
	if errors > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", errors, plural(errors, "error", "errors")))
	}
	if notices > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", notices, plural(notices, "notice", "notices")))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " / ") + " (N)"
}

func (m Model) notificationAttentionCounts() (errors, notices int) {
	for _, entry := range m.notificationHistory {
		if entry.Read || entry.Dismissed {
			continue
		}
		if entry.Level == ToastError {
			errors++
			continue
		}
		notices++
	}
	return errors, notices
}

func plural(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func (m Model) alertIndicator() string {
	parts := make([]string, 0, 2)
	if recover := m.recoveryNotificationIndicator(); recover != "" {
		parts = append(parts, recover)
	}
	if notices := m.notificationHistoryIndicator(); notices != "" {
		parts = append(parts, notices)
	}
	return strings.Join(parts, "  ")
}

func (m *Model) openNotificationHistoryOverlayCmd() tea.Cmd {
	daemonNoticeIDs := make([]string, 0, len(m.notificationHistory))
	items := make([]overlay.NotificationHistoryItem, 0, len(m.notificationHistory))
	for i := len(m.notificationHistory) - 1; i >= 0; i-- {
		entry := m.notificationHistory[i]
		_ = m.feedback.markRead(entry.ID)
		if strings.TrimSpace(entry.DaemonNoticeID) != "" && !entry.Read {
			daemonNoticeIDs = append(daemonNoticeIDs, entry.DaemonNoticeID)
		}
		entry.Read = true
		items = append(items, overlay.NotificationHistoryItem{
			ID:             entry.ID,
			DaemonNoticeID: strings.TrimSpace(entry.DaemonNoticeID),
			CreatedAt:      entry.CreatedAt,
			Level:          notificationLevelLabel(entry.Level),
			Category:       strings.TrimSpace(entry.Category),
			State:          string(entry.State),
			Reference:      entry.Reference,
			ScopeType:      strings.TrimSpace(entry.ScopeType),
			ScopeID:        strings.TrimSpace(entry.ScopeID),
			OperationID:    strings.TrimSpace(entry.OperationID),
			Message:        entry.Message,
			Detail:         entry.Detail,
			Read:           entry.Read,
			Dismissed:      entry.Dismissed,
			Actions:        cloneOverlayNoticeActions(entry.Actions),
		})
	}
	m.refreshFeedbackProjectionOutputs(time.Now())
	return tea.Batch(
		m.openOverlay(overlay.NewNotificationHistoryOverlay(items)),
		m.markDaemonNoticesReadCmd(daemonNoticeIDs),
	)
}

func notificationLevelLabel(level ToastLevel) string {
	switch level {
	case ToastSuccess:
		return "success"
	case ToastWarning:
		return "warning"
	case ToastError:
		return "error"
	default:
		return "info"
	}
}

func cloneOverlayNoticeActions(actions []protocol.NoticeAction) []protocol.NoticeAction {
	if len(actions) == 0 {
		return nil
	}
	copied := make([]protocol.NoticeAction, len(actions))
	copy(copied, actions)
	return copied
}

func (m *Model) markNotificationDismissed(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	_ = m.feedback.dismissNotice(id)
	m.refreshFeedbackProjectionOutputs(time.Now())
}

func (m Model) updateNotificationReadCmd(action overlay.NotificationActionCenterMsg) tea.Cmd {
	daemonNoticeID := strings.TrimSpace(action.DaemonNoticeID)
	if daemonNoticeID == "" {
		return nil
	}
	client := m.daemonClient
	projectID := m.daemonProjectID()
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		read := action.Read
		notice, err := client.UpdateNotice(ctx, daemonNoticeID, &read, "")
		label := "Marked read"
		if !read {
			label = "Marked unread"
		}
		return noticeUpdateResultMsg{projectID: projectID, notice: notice, label: label, err: err}
	}
}

func (m Model) dismissDaemonNoticesCmd(actions []overlay.NotificationActionCenterMsg) tea.Cmd {
	client := m.daemonClient
	projectID := m.daemonProjectID()
	if client == nil || len(actions) == 0 {
		return nil
	}
	ids := make([]string, 0, len(actions))
	seen := map[string]struct{}{}
	for _, action := range actions {
		id := strings.TrimSpace(action.DaemonNoticeID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var last protocol.NoticeRecord
		count := 0
		for _, id := range ids {
			notice, err := client.UpdateNotice(ctx, id, nil, protocol.NoticeStateDismissed)
			if err != nil {
				return noticeUpdateResultMsg{projectID: projectID, label: "Dismiss notification", err: err}
			}
			count++
			last = notice
		}
		label := "Dismissed notification"
		if count > 1 {
			label = fmt.Sprintf("Dismissed %d notifications", count)
		}
		return noticeUpdateResultMsg{projectID: projectID, notice: last, label: label}
	}
}

func (m Model) runNotificationActionCmd(action overlay.NotificationActionCenterMsg) tea.Cmd {
	daemonNoticeID := strings.TrimSpace(action.DaemonNoticeID)
	actionID := strings.TrimSpace(action.ActionID)
	if daemonNoticeID == "" || actionID == "" {
		return nil
	}
	client := m.daemonClient
	projectID := m.daemonProjectID()
	if client == nil {
		return nil
	}
	label := strings.TrimSpace(action.Label)
	if label == "" {
		label = actionID
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		notice, err := client.RunNoticeAction(ctx, daemonNoticeID, actionID, nil)
		return noticeActionResultMsg{projectID: projectID, notice: notice, label: label, err: err}
	}
}

func (m Model) copyNotificationDetailsCmd(details string) tea.Cmd {
	details = strings.TrimSpace(details)
	if details == "" {
		return func() tea.Msg { return notificationCopyDetailsResultMsg{err: fmt.Errorf("notice details are empty")} }
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return notificationCopyDetailsResultMsg{err: writeNotificationClipboardText(ctx, details)}
	}
}

func (m Model) handleNotificationActionCenterSelection(msg overlay.SelectionMsg) (tea.Model, tea.Cmd) {
	switch msg.Key {
	case "notification_mark_read":
		action, ok := msg.Value.(overlay.NotificationActionCenterMsg)
		if !ok {
			return m, nil
		}
		if strings.TrimSpace(action.DaemonNoticeID) == "" {
			_ = m.feedback.setRead(action.NoticeID, action.Read)
			m.refreshFeedbackProjectionOutputs(time.Now())
			return m, nil
		}
		return m, m.updateNotificationReadCmd(action)

	case "notification_dismiss":
		action, ok := msg.Value.(overlay.NotificationActionCenterMsg)
		if !ok {
			return m, nil
		}
		if strings.TrimSpace(action.DaemonNoticeID) == "" {
			m.markNotificationDismissed(action.NoticeID)
			return m, nil
		}
		return m, m.dismissDaemonNoticesCmd([]overlay.NotificationActionCenterMsg{action})

	case "notification_dismiss_all":
		actions, ok := msg.Value.([]overlay.NotificationActionCenterMsg)
		if !ok {
			return m, nil
		}
		for _, action := range actions {
			if strings.TrimSpace(action.DaemonNoticeID) == "" {
				m.markNotificationDismissed(action.NoticeID)
			}
		}
		return m, m.dismissDaemonNoticesCmd(actions)

	case "notification_action":
		action, ok := msg.Value.(overlay.NotificationActionCenterMsg)
		if !ok {
			return m, nil
		}
		if notificationActionIsClientLocal(action) {
			return m.handleNotificationClientAction(action)
		}
		return m, m.runNotificationActionCmd(action)
	}
	return m, nil
}

func (m Model) handleNotificationClientAction(action overlay.NotificationActionCenterMsg) (tea.Model, tea.Cmd) {
	kind := strings.TrimSpace(action.Kind)
	if kind == "" {
		kind = strings.TrimSpace(action.ActionID)
	}
	switch kind {
	case "client.open_task", "open_task":
		issueID := notificationActionIssueID(action)
		if issueID == "" {
			m.addToast(Toast{Level: ToastWarning, Message: "Notification has no task target", Expires: time.Now().Add(4 * time.Second)})
			return m, nil
		}
		m.overlayStack.Pop()
		return m.openTaskWorkspaceByID(issueID)

	case "client.open_workspace", "open_workspace":
		issueID := notificationActionIssueID(action)
		if issueID == "" {
			m.addToast(Toast{Level: ToastWarning, Message: "Notification has no workspace target", Expires: time.Now().Add(4 * time.Second)})
			return m, nil
		}
		m.overlayStack.Pop()
		return m.openTaskWorkspaceByID(issueID)

	case "client.open_logs", "open_logs":
		m.overlayStack.Pop()
		return m, tea.Batch(
			m.openOverlay(overlay.NewEventLogOverlayWithLogFiles(
				m.runtimeEvents,
				m.eventLogFilePath(),
				m.daemonLogFilePath(),
			)),
			m.loadHookLogEventsCmd(),
		)

	case "client.copy_details", "copy_details":
		return m, m.copyNotificationDetailsCmd(action.Details)
	}
	return m, m.runNotificationActionCmd(action)
}

func notificationActionIsClientLocal(action overlay.NotificationActionCenterMsg) bool {
	kind := strings.TrimSpace(action.Kind)
	if strings.HasPrefix(kind, "client.") {
		return true
	}
	switch strings.TrimSpace(action.ActionID) {
	case "open_task", "open_workspace", "open_logs", "copy_details":
		return true
	default:
		return false
	}
}

func notificationActionIssueID(action overlay.NotificationActionCenterMsg) string {
	if strings.TrimSpace(action.ScopeType) == "issue" && strings.TrimSpace(action.ScopeID) != "" {
		return strings.TrimSpace(action.ScopeID)
	}
	return ""
}

func writeClipboardText(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("clipboard text is empty")
	}
	candidates := [][]string{
		{"pbcopy"},
		{"wl-copy"},
		{"xclip", "-selection", "clipboard"},
	}
	var tried []string
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate[0])
		if err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, path, candidate[1:]...)
		cmd.Stdin = strings.NewReader(text)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			tried = append(tried, candidate[0]+": "+strings.TrimSpace(stderr.String()))
			continue
		}
		return nil
	}
	if len(tried) > 0 {
		return fmt.Errorf("copy details: %s", strings.Join(tried, "; "))
	}
	return fmt.Errorf("no clipboard command found")
}

func (m *Model) refreshFeedbackProjectionOutputs(now time.Time) {
	out := m.feedback.materialize(m.tasks, now)
	m.toasts = out.toasts
	m.activeToastHistoryIDs = out.activeToastHistoryIDs
	m.notificationHistory = out.history
	m.pendingFailures = out.failures
}

func (m *Model) applyFeedbackNoticeSnapshot(notices []protocol.NoticeRecord) {
	m.feedback.replaceDaemonNotices(notices)
	m.refreshFeedbackProjectionOutputs(time.Now())
	m.syncTaskWorkspaceOverlay()
}

func (m Model) feedbackNotices() []protocol.NoticeRecord {
	notices := make([]protocol.NoticeRecord, 0, len(m.feedback.noticesByID))
	for _, notice := range m.feedback.noticesByID {
		notices = append(notices, notice)
	}
	return notices
}

func (m *Model) applyFeedbackNoticeEvent(body protocol.NoticeEventBody) {
	m.feedback.applyDaemonNoticeEvent(body)
	m.refreshFeedbackProjectionOutputs(body.UpdatedAt)
	m.syncTaskWorkspaceOverlay()
}
