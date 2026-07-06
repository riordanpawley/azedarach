package app

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/types"
)

const localNoticeIDPrefix = "local-notice-"

type feedbackProjection struct {
	noticesByID   map[string]protocol.NoticeRecord
	localNotices  []feedbackLocalNotice
	localFailures map[string]taskMutationFailure
	localSeq      uint64
}

type feedbackLocalNotice struct {
	ID        string
	CreatedAt time.Time
	Toast     Toast
	Read      bool
	Dismissed bool
}

type feedbackProjectionOutput struct {
	toasts                []Toast
	activeToastHistoryIDs []string
	history               []notificationHistoryEntry
	failures              map[string]taskMutationFailure
}

type feedbackToastItem struct {
	ID        string
	CreatedAt time.Time
	Toast     Toast
}

func newFeedbackProjection() feedbackProjection {
	return feedbackProjection{
		noticesByID:   make(map[string]protocol.NoticeRecord),
		localNotices:  []feedbackLocalNotice{},
		localFailures: make(map[string]taskMutationFailure),
	}
}

func (p *feedbackProjection) ensure() {
	if p.noticesByID == nil {
		p.noticesByID = make(map[string]protocol.NoticeRecord)
	}
	if p.localFailures == nil {
		p.localFailures = make(map[string]taskMutationFailure)
	}
}

func (p *feedbackProjection) replaceDaemonNotices(notices []protocol.NoticeRecord) {
	p.ensure()
	next := make(map[string]protocol.NoticeRecord, len(notices))
	for _, notice := range notices {
		id := strings.TrimSpace(notice.NoticeID)
		if id == "" {
			continue
		}
		next[id] = notice
	}
	p.noticesByID = next
}

func (p *feedbackProjection) applyDaemonNoticeEvent(body protocol.NoticeEventBody) {
	p.ensure()
	noticeID := strings.TrimSpace(body.NoticeID)
	if body.Notice != nil {
		noticeID = strings.TrimSpace(body.Notice.NoticeID)
	}
	if noticeID == "" {
		return
	}
	if body.Notice == nil || body.State == protocol.NoticeStateExpired {
		delete(p.noticesByID, noticeID)
		return
	}
	p.noticesByID[noticeID] = *body.Notice
}

func (p *feedbackProjection) addLocalToast(toast Toast, createdAt time.Time) string {
	message := compactSummaryText(toast.Message)
	if message == "" {
		return ""
	}
	p.ensure()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	p.localSeq++
	id := localNoticeIDPrefix + strconv.FormatUint(p.localSeq, 10)
	p.localNotices = append(p.localNotices, feedbackLocalNotice{
		ID:        id,
		CreatedAt: createdAt,
		Toast: Toast{
			Level:     toast.Level,
			Message:   message,
			CreatedAt: createdAt,
			Expires:   toast.Expires,
		},
	})
	if len(p.localNotices) > notificationHistoryCapacity {
		p.localNotices = append([]feedbackLocalNotice(nil), p.localNotices[len(p.localNotices)-notificationHistoryCapacity:]...)
	}
	return id
}

func (p *feedbackProjection) expireLocalToasts(now time.Time) {
	// Active-toast expiry is derived during materialization. History entries
	// remain unread until the user opens or dismisses them.
}

func (p *feedbackProjection) dismissNotice(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for i := range p.localNotices {
		if p.localNotices[i].ID == id {
			p.localNotices[i].Dismissed = true
			return true
		}
	}
	if notice, ok := p.noticesByID[id]; ok {
		notice.State = protocol.NoticeStateDismissed
		now := time.Now().UTC()
		notice.DismissedAt = &now
		p.noticesByID[id] = notice
		return true
	}
	return false
}

func (p *feedbackProjection) markRead(id string) bool {
	return p.setRead(id, true)
}

func (p *feedbackProjection) setRead(id string, read bool) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for i := range p.localNotices {
		if p.localNotices[i].ID == id {
			p.localNotices[i].Read = read
			return true
		}
	}
	if notice, ok := p.noticesByID[id]; ok {
		notice.Read = read
		p.noticesByID[id] = notice
		return true
	}
	return false
}

func (p *feedbackProjection) setLocalFailure(taskID string, failure taskMutationFailure) {
	key := taskIDKey(taskID)
	if key == "" {
		return
	}
	p.ensure()
	p.localFailures[key] = failure
}

func (p *feedbackProjection) clearLocalFailure(taskID string) {
	if len(p.localFailures) == 0 {
		return
	}
	delete(p.localFailures, taskIDKey(taskID))
}

func (p *feedbackProjection) materialize(tasks []domain.Task, now time.Time) feedbackProjectionOutput {
	p.ensure()
	if now.IsZero() {
		now = time.Now()
	}

	history := make([]notificationHistoryEntry, 0, len(p.localNotices)+len(p.noticesByID))
	toastItems := make([]feedbackToastItem, 0, len(p.localNotices)+len(p.noticesByID))
	failures := make(map[string]taskMutationFailure, len(p.localFailures))

	for key, failure := range p.localFailures {
		failures[key] = failure
	}

	for _, local := range p.localNotices {
		entry := notificationHistoryEntry{
			ID:        local.ID,
			CreatedAt: local.CreatedAt,
			Level:     local.Toast.Level,
			Category:  "local",
			State:     protocol.NoticeStateActive,
			Reference: notificationReference(local.Toast.Message),
			Message:   local.Toast.Message,
			Read:      local.Read,
			Dismissed: local.Dismissed,
		}
		history = append(history, entry)
		if !local.Dismissed && (local.Toast.Expires.IsZero() || local.Toast.Expires.After(now)) {
			toastItems = append(toastItems, feedbackToastItem{
				ID:        local.ID,
				CreatedAt: local.CreatedAt,
				Toast:     local.Toast,
			})
		}
	}

	taskStatusByID := make(map[string]domain.Status, len(tasks))
	for _, task := range tasks {
		taskStatusByID[taskIDKey(task.ID.String())] = task.Status
	}

	for _, notice := range p.noticesByID {
		if notice.State == protocol.NoticeStateExpired {
			continue
		}
		entry := notificationHistoryEntry{
			ID:             strings.TrimSpace(notice.NoticeID),
			DaemonNoticeID: strings.TrimSpace(notice.NoticeID),
			CreatedAt:      noticeCreatedAt(notice),
			Level:          noticeToastLevel(notice.Severity),
			Category:       strings.TrimSpace(notice.Category),
			State:          notice.State,
			Reference:      noticeReference(notice),
			ScopeType:      strings.TrimSpace(notice.Scope.Type),
			ScopeID:        strings.TrimSpace(notice.Scope.ID),
			Message:        noticeSummary(notice),
			Detail:         compactSummaryText(notice.Detail),
			Read:           notice.Read,
			Dismissed:      notice.State == protocol.NoticeStateDismissed,
			Actions:        cloneNoticeActions(notice.Actions),
		}
		if notice.Source != nil {
			entry.OperationID = strings.TrimSpace(notice.Source.OperationID.String())
		}
		if entry.Message != "" {
			history = append(history, entry)
		}
		if notice.State == protocol.NoticeStateActive {
			if failure, ok := taskFailureFromNotice(notice, taskStatusByID); ok {
				failures[taskIDKey(notice.Scope.ID)] = failure
			}
			if noticeToastVisible(notice, now) {
				toastItems = append(toastItems, feedbackToastItem{
					ID:        entry.ID,
					CreatedAt: entry.CreatedAt,
					Toast: Toast{
						Level:     entry.Level,
						Message:   entry.Message,
						CreatedAt: entry.CreatedAt,
						Expires:   noticeToastExpires(notice, now),
					},
				})
			}
		}
	}

	sort.SliceStable(history, func(i, j int) bool {
		if history[i].CreatedAt.Equal(history[j].CreatedAt) {
			return history[i].ID < history[j].ID
		}
		return history[i].CreatedAt.Before(history[j].CreatedAt)
	})
	if len(history) > notificationHistoryCapacity {
		history = append([]notificationHistoryEntry(nil), history[len(history)-notificationHistoryCapacity:]...)
	}
	sort.SliceStable(toastItems, func(i, j int) bool {
		if toastItems[i].CreatedAt.Equal(toastItems[j].CreatedAt) {
			return toastItems[i].ID < toastItems[j].ID
		}
		return toastItems[i].CreatedAt.Before(toastItems[j].CreatedAt)
	})
	if len(toastItems) > 3 {
		toastItems = append([]feedbackToastItem(nil), toastItems[len(toastItems)-3:]...)
	}
	toasts := make([]Toast, 0, len(toastItems))
	toastIDs := make([]string, 0, len(toastItems))
	for _, item := range toastItems {
		toasts = append(toasts, item.Toast)
		toastIDs = append(toastIDs, item.ID)
	}

	return feedbackProjectionOutput{
		toasts:                toasts,
		activeToastHistoryIDs: toastIDs,
		history:               history,
		failures:              failures,
	}
}

func cloneNoticeActions(actions []protocol.NoticeAction) []protocol.NoticeAction {
	if len(actions) == 0 {
		return nil
	}
	copied := make([]protocol.NoticeAction, len(actions))
	copy(copied, actions)
	return copied
}

func noticeCreatedAt(notice protocol.NoticeRecord) time.Time {
	for _, candidate := range []time.Time{notice.LastOccurrenceAt, notice.UpdatedAt, notice.CreatedAt, notice.FirstOccurrenceAt} {
		if !candidate.IsZero() {
			return candidate.UTC()
		}
	}
	return time.Time{}
}

func noticeReference(notice protocol.NoticeRecord) string {
	if strings.TrimSpace(notice.Scope.ID) != "" {
		return strings.TrimSpace(notice.Scope.ID)
	}
	return notificationReference(noticeSummary(notice))
}

func noticeSummary(notice protocol.NoticeRecord) string {
	for _, value := range []string{notice.Summary, notice.Title, notice.Detail} {
		if compact := compactSummaryText(value); compact != "" {
			return compact
		}
	}
	if notice.Cause != nil {
		return compactSummaryText(notice.Cause.Message)
	}
	return ""
}

func noticeToastLevel(severity protocol.NoticeSeverity) ToastLevel {
	switch severity {
	case protocol.NoticeSeveritySuccess:
		return types.ToastSuccess
	case protocol.NoticeSeverityWarning:
		return types.ToastWarning
	case protocol.NoticeSeverityError:
		return types.ToastError
	default:
		return types.ToastInfo
	}
}

func noticeToastVisible(notice protocol.NoticeRecord, now time.Time) bool {
	if notice.State != protocol.NoticeStateActive || notice.Read {
		return false
	}
	if expires := noticeToastExpires(notice, now); !expires.IsZero() && !expires.After(now) {
		return false
	}
	return noticeSummary(notice) != ""
}

func noticeToastExpires(notice protocol.NoticeRecord, now time.Time) time.Time {
	if notice.ExpiresAt != nil && !notice.ExpiresAt.IsZero() {
		return notice.ExpiresAt.UTC()
	}
	createdAt := noticeCreatedAt(notice)
	if createdAt.IsZero() {
		createdAt = now.UTC()
	}
	return createdAt.Add(8 * time.Second)
}

func taskFailureFromNotice(notice protocol.NoticeRecord, taskStatusByID map[string]domain.Status) (taskMutationFailure, bool) {
	if notice.State != protocol.NoticeStateActive {
		return taskMutationFailure{}, false
	}
	if !noticeScopeTargetsTask(notice.Scope.Type) || taskIDKey(notice.Scope.ID) == "" {
		return taskMutationFailure{}, false
	}
	if strings.TrimSpace(notice.Category) != "operation_failed" {
		return taskMutationFailure{}, false
	}
	status := taskStatusByID[taskIDKey(notice.Scope.ID)]
	failure := taskMutationFailure{
		action:        noticeFailureAction(notice),
		message:       noticeSummary(notice),
		reason:        noticeFailureReason(notice),
		recovery:      noticeFailureRecovery(notice),
		currentStatus: status,
		updatedAt:     noticeCreatedAt(notice),
	}
	if notice.Source != nil {
		failure.operationID = strings.TrimSpace(notice.Source.OperationID.String())
	}
	return failure, failure.message != ""
}

func noticeFailureAction(notice protocol.NoticeRecord) string {
	if notice.Source != nil && strings.TrimSpace(notice.Source.OperationKind) != "" {
		return strings.TrimSpace(notice.Source.OperationKind)
	}
	if strings.TrimSpace(notice.Title) != "" {
		return compactSummaryText(notice.Title)
	}
	return strings.TrimSpace(notice.Category)
}

func noticeFailureReason(notice protocol.NoticeRecord) string {
	if notice.Cause != nil && strings.TrimSpace(notice.Cause.Message) != "" {
		return compactSummaryText(notice.Cause.Message)
	}
	return noticeSummary(notice)
}

func noticeFailureRecovery(notice protocol.NoticeRecord) string {
	if strings.TrimSpace(notice.Detail) != "" {
		return compactSummaryText(notice.Detail)
	}
	for _, action := range notice.Actions {
		if action.Enabled && strings.TrimSpace(action.Label) != "" {
			return compactSummaryText(action.Label)
		}
	}
	return ""
}

func noticeScopeTargetsTask(scopeType string) bool {
	switch strings.TrimSpace(scopeType) {
	case "issue", "task":
		return true
	default:
		return false
	}
}
