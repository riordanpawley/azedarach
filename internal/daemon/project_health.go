package daemon

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const projectIssueStoreHealthBackoff = 5 * time.Minute

type projectIssueStoreHealthState struct {
	Message       string
	FirstFailedAt time.Time
	LastFailedAt  time.Time
	RetryAfter    time.Time
	FailureCount  int
}

func (d *Daemon) projectIssueStoreHealthError(projectID string) (error, bool) {
	if d == nil {
		return nil, false
	}
	projectID = d.canonicalProjectID(projectID)
	now := timeNow().UTC()

	d.projectIssueStoreHealthMu.Lock()
	defer d.projectIssueStoreHealthMu.Unlock()
	state, ok := d.projectIssueStoreHealthByProject[projectID]
	if !ok {
		return nil, false
	}
	if !now.Before(state.RetryAfter) {
		delete(d.projectIssueStoreHealthByProject, projectID)
		return nil, false
	}
	return fmt.Errorf("%s", d.projectIssueStoreHealthMessageLocked(projectID, state, true)), true
}

func (d *Daemon) recordProjectIssueStoreFailure(projectID string, err error) error {
	if err == nil || !isDeterministicProjectIssueStoreOpenFailure(err) {
		return err
	}
	return d.recordProjectIssueStoreUnavailable(projectID, err)
}

func (d *Daemon) recordProjectIssueStoreUnavailable(projectID string, err error) error {
	if err == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)
	now := timeNow().UTC()

	d.projectIssueStoreHealthMu.Lock()
	defer d.projectIssueStoreHealthMu.Unlock()
	if d.projectIssueStoreHealthByProject == nil {
		d.projectIssueStoreHealthByProject = map[string]projectIssueStoreHealthState{}
	}
	state := d.projectIssueStoreHealthByProject[projectID]
	if state.FirstFailedAt.IsZero() {
		state.FirstFailedAt = now
	}
	state.LastFailedAt = now
	state.RetryAfter = now.Add(projectIssueStoreHealthBackoff)
	state.FailureCount++
	state.Message = strings.TrimSpace(err.Error())
	d.projectIssueStoreHealthByProject[projectID] = state

	if d.cfg.Logger != nil {
		d.cfg.Logger.Warn("project issue store marked unhealthy",
			"project_id", projectID,
			"retry_after", state.RetryAfter.Format(time.RFC3339),
			"failure_count", state.FailureCount,
			"error", state.Message,
		)
	}

	return fmt.Errorf("%s", d.projectIssueStoreHealthMessageLocked(projectID, state, false))
}

func (d *Daemon) clearProjectIssueStoreHealth(projectID string) {
	if d == nil {
		return
	}
	projectID = d.canonicalProjectID(projectID)
	d.projectIssueStoreHealthMu.Lock()
	defer d.projectIssueStoreHealthMu.Unlock()
	delete(d.projectIssueStoreHealthByProject, projectID)
}

func (d *Daemon) projectIssueStoreHealthMessageLocked(projectID string, state projectIssueStoreHealthState, cached bool) string {
	prefix := "project issue store unhealthy"
	if cached {
		prefix += " (cached)"
	}
	detail := strings.TrimSpace(state.Message)
	if detail == "" {
		detail = "schema migration/open failed"
	}
	return fmt.Sprintf("%s for project %s: %s; suppressing repeated polling until %s. Repair the project database, then retry after the backoff or restart the daemon to clear cached health.",
		prefix,
		protocol.NormalizeProjectID(projectID),
		detail,
		state.RetryAfter.Format(time.RFC3339),
	)
}

func isDeterministicProjectIssueStoreOpenFailure(err error) bool {
	if err == nil {
		return false
	}
	var storeErr *domain.TaskStoreError
	if !errors.As(err, &storeErr) || strings.TrimSpace(storeErr.Op) != "open-db" {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "database is locked") || strings.Contains(msg, "busy") {
		return false
	}
	return strings.Contains(msg, "apply migration") ||
		strings.Contains(msg, "schema migration") ||
		strings.Contains(msg, "no such column") ||
		strings.Contains(msg, "sql logic error")
}

func isCachedProjectIssueStoreHealthErrorMessage(message string) bool {
	return strings.HasPrefix(strings.TrimSpace(message), "project issue store unhealthy (cached)")
}

func projectIssueStoreHealthErrorCode(err error) protocol.ErrorCode {
	if err != nil && strings.HasPrefix(strings.TrimSpace(err.Error()), "project issue store unhealthy") {
		return protocol.ErrorCodeUnavailable
	}
	return protocol.ErrorCodeInternal
}
