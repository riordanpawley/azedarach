package app

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

var notificationStructuredReferencePattern = regexp.MustCompile(`\b(?:[a-z]{2,10}-\d+|op[-_][a-zA-Z0-9._:-]+)\b`)
var notificationBareIssueReferencePattern = regexp.MustCompile(`\b[a-z]{3,5}[a-z0-9]{0,2}\b`)

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
			ID:        entry.ID,
			CreatedAt: entry.CreatedAt,
			Level:     notificationLevelLabel(entry.Level),
			Reference: entry.Reference,
			Message:   entry.Message,
			Read:      entry.Read,
			Dismissed: entry.Dismissed,
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

func (m *Model) markNotificationDismissed(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	_ = m.feedback.dismissNotice(id)
	m.refreshFeedbackProjectionOutputs(time.Now())
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
