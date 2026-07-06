package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

const recoveryNotificationLimit = 20

type asyncRecoveryAction string

const (
	asyncRecoveryActionRetryUpdate asyncRecoveryAction = "retry_update"
	asyncRecoveryActionRetryMerge  asyncRecoveryAction = "retry_merge"
)

type asyncRecoveryNotification struct {
	ID        string
	IssueID   string
	Title     string
	Message   string
	Action    asyncRecoveryAction
	Project   asyncRecoveryProjectContext
	Worktree  string
	SourceID  string
	TargetID  string
	CreatedAt time.Time
}

type asyncRecoveryProjectContext struct {
	ProjectID   string
	ProjectPath string
	BaseBranch  string
}

func (c asyncRecoveryProjectContext) normalized() asyncRecoveryProjectContext {
	c.ProjectID = strings.TrimSpace(c.ProjectID)
	if c.ProjectID != "" {
		c.ProjectID = strings.TrimSpace(protocol.NormalizeProjectID(c.ProjectID))
	}
	c.ProjectPath = strings.TrimSpace(c.ProjectPath)
	c.BaseBranch = strings.TrimSpace(c.BaseBranch)
	return c
}

func (m Model) asyncRecoveryProjectContext() asyncRecoveryProjectContext {
	return asyncRecoveryProjectContext{
		ProjectID:   m.daemonProjectID(),
		ProjectPath: m.activeProjectPath(),
		BaseBranch:  m.resolveBaseBranch(),
	}.normalized()
}

func (m *Model) enqueueAsyncRecoveryNotification(notification asyncRecoveryNotification) {
	notification.IssueID = strings.TrimSpace(notification.IssueID)
	notification.Title = strings.TrimSpace(notification.Title)
	notification.Message = compactSummaryText(notification.Message)
	notification.Project = notification.Project.normalized()
	notification.Worktree = strings.TrimSpace(notification.Worktree)
	notification.SourceID = strings.TrimSpace(notification.SourceID)
	notification.TargetID = strings.TrimSpace(notification.TargetID)
	if notification.Title == "" || notification.Message == "" {
		return
	}

	for i := range m.recoveryNotifications {
		existing := m.recoveryNotifications[i]
		if existing.Action != notification.Action {
			continue
		}
		if existing.IssueID != notification.IssueID || existing.SourceID != notification.SourceID || existing.TargetID != notification.TargetID {
			continue
		}
		if existing.Project.normalized() != notification.Project {
			continue
		}
		if existing.Message != notification.Message {
			continue
		}
		existing.CreatedAt = time.Now()
		m.recoveryNotifications = append(append(m.recoveryNotifications[:i], m.recoveryNotifications[i+1:]...), existing)
		return
	}

	m.recoveryNotificationSeq++
	notification.ID = fmt.Sprintf("recovery-%d", m.recoveryNotificationSeq)
	if notification.CreatedAt.IsZero() {
		notification.CreatedAt = time.Now()
	}
	m.recoveryNotifications = append(m.recoveryNotifications, notification)
	if len(m.recoveryNotifications) > recoveryNotificationLimit {
		m.recoveryNotifications = append([]asyncRecoveryNotification(nil), m.recoveryNotifications[len(m.recoveryNotifications)-recoveryNotificationLimit:]...)
	}
}

func (m *Model) dismissAsyncRecoveryNotification(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for i := range m.recoveryNotifications {
		if m.recoveryNotifications[i].ID != id {
			continue
		}
		m.recoveryNotifications = append(m.recoveryNotifications[:i], m.recoveryNotifications[i+1:]...)
		return true
	}
	return false
}

func (m *Model) clearAsyncRecoveryNotifications() int {
	count := len(m.recoveryNotifications)
	m.recoveryNotifications = m.recoveryNotifications[:0]
	return count
}

func (m *Model) popAsyncRecoveryNotification(id string) (asyncRecoveryNotification, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return asyncRecoveryNotification{}, false
	}
	for i := range m.recoveryNotifications {
		if m.recoveryNotifications[i].ID != id {
			continue
		}
		notification := m.recoveryNotifications[i]
		m.recoveryNotifications = append(m.recoveryNotifications[:i], m.recoveryNotifications[i+1:]...)
		return notification, true
	}
	return asyncRecoveryNotification{}, false
}

func (m Model) recoveryNotificationIndicator() string {
	count := len(m.recoveryNotifications)
	if count == 0 {
		return ""
	}
	icon := "*"
	if (time.Now().UnixMilli()/450)%2 == 0 {
		icon = "o"
	}
	return fmt.Sprintf("%s recover:%d (n)", icon, count)
}

func (m *Model) openRecoveryOverlayCmd() tea.Cmd {
	if len(m.recoveryNotifications) == 0 {
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: "No recoverable async failures queued",
			Expires: time.Now().Add(3 * time.Second),
		})
		return nil
	}

	items := make([]overlay.RecoveryNotificationItem, 0, len(m.recoveryNotifications))
	for i := len(m.recoveryNotifications) - 1; i >= 0; i-- {
		notification := m.recoveryNotifications[i]
		recoverLabel := "retry"
		switch notification.Action {
		case asyncRecoveryActionRetryUpdate:
			recoverLabel = "retry update"
		case asyncRecoveryActionRetryMerge:
			recoverLabel = "retry merge"
		}
		items = append(items, overlay.RecoveryNotificationItem{
			ID:           notification.ID,
			IssueID:      notification.IssueID,
			Title:        notification.Title,
			Message:      notification.Message,
			RecoverLabel: recoverLabel,
			CreatedAt:    notification.CreatedAt,
		})
	}

	return m.openOverlay(overlay.NewRecoveryOverlay(items))
}

func (m Model) recoverAsyncFailureCmd(notification asyncRecoveryNotification) tea.Cmd {
	recoveryModel := m.withAsyncRecoveryProject(notification.Project)
	switch notification.Action {
	case asyncRecoveryActionRetryUpdate:
		if notification.IssueID == "" {
			return nil
		}
		return recoveryModel.updateFromBaseCmd(notification.IssueID, notification.Worktree, false)
	case asyncRecoveryActionRetryMerge:
		if notification.SourceID == "" {
			return nil
		}
		if notification.TargetID == "" || notification.TargetID == "main" || notification.TargetID == mergeBaseTargetID {
			return recoveryModel.resolveMergeToBaseCmd(notification.SourceID, true)
		}
		return recoveryModel.resolveFollowOnMergeCmd(notification.SourceID, notification.TargetID, domain.SessionIdle, false, true)
	default:
		return nil
	}
}

func (m Model) withAsyncRecoveryProject(project asyncRecoveryProjectContext) Model {
	project = project.normalized()
	if project.ProjectID == "" && project.ProjectPath == "" && project.BaseBranch == "" {
		return m
	}
	scoped := m
	if project.ProjectPath != "" {
		scoped.repoDir = project.ProjectPath
		scoped.currentProject = ""
	}
	if project.BaseBranch != "" {
		if scoped.config == nil {
			scoped.config = &config.Config{}
		} else {
			cfg := *scoped.config
			scoped.config = &cfg
		}
		scoped.config.Git.BaseBranch = project.BaseBranch
	}
	if project.ProjectID != "" && scoped.daemonClient != nil {
		if routeID, err := naming.ParseProjectID(project.ProjectID); err == nil {
			scoped.daemonProjectRouteID = routeID
			scoped.daemonClient = scoped.daemonClient.ForProjectRouteID(routeID)
		}
	}
	return scoped
}

func (m Model) asyncRecoveryNotificationByID(id string) (asyncRecoveryNotification, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return asyncRecoveryNotification{}, false
	}
	for i := range m.recoveryNotifications {
		if m.recoveryNotifications[i].ID == id {
			return m.recoveryNotifications[i], true
		}
	}
	return asyncRecoveryNotification{}, false
}
