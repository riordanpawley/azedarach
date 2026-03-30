package diagnostics

import (
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/services/linearsync"
)

const (
	projectSyncTransportPrimary  = "primary"
	projectSyncTransportFallback = "fallback"
)

// ProjectSyncDiagnostics captures project-level sync state for diagnostics consumers.
type ProjectSyncDiagnostics struct {
	ProjectID      string
	State          linearsync.WorkerState
	Transport      string
	FallbackActive bool
	FallbackReason string
	LastSuccessAt  *time.Time
	LastError      string
	LastErrorAt    *time.Time
	RetryAttempts  int
	RetryBackoff   time.Duration
}

// ProjectSyncProjection is the input used to project worker lifecycle state into diagnostics.
type ProjectSyncProjection struct {
	Lifecycle     linearsync.ProjectWorkerLifecycle
	Fallback      linearsync.WebhookFallbackStatus
	LastSuccessAt *time.Time
	LastError     string
	LastErrorAt   *time.Time
	RetryAttempts int
	RetryPolicy   linearsync.RetryPolicy
}

// ProjectSyncDiagnosticsFromLifecycle projects worker lifecycle and transport state into a diagnostics record.
func ProjectSyncDiagnosticsFromLifecycle(projection ProjectSyncProjection) ProjectSyncDiagnostics {
	lifecycle := projection.Lifecycle
	lifecycle.ProjectID = strings.TrimSpace(lifecycle.ProjectID)

	retryPolicy := projection.RetryPolicy.Normalize()
	retryAttempts := projection.RetryAttempts
	if retryAttempts < 0 {
		retryAttempts = 0
	}

	fallbackDecision := lifecycle.ResolveFallback(projection.Fallback)
	transport := projectSyncTransportPrimary
	if fallbackDecision.Active {
		transport = projectSyncTransportFallback
	}

	retryBackoff := time.Duration(0)
	if lifecycle.State == linearsync.WorkerStateDegraded || lifecycle.State == linearsync.WorkerStateRetrying {
		retryBackoff = retryPolicy.DelayForAttempt(retryAttempts)
	}

	return normalizeProjectSyncDiagnostics(ProjectSyncDiagnostics{
		ProjectID:      lifecycle.ProjectID,
		State:          lifecycle.State,
		Transport:      transport,
		FallbackActive: fallbackDecision.Active,
		FallbackReason: strings.TrimSpace(fallbackDecision.Reason),
		LastSuccessAt:  projection.LastSuccessAt,
		LastError:      strings.TrimSpace(projection.LastError),
		LastErrorAt:    projection.LastErrorAt,
		RetryAttempts:  retryAttempts,
		RetryBackoff:   retryBackoff,
	})
}

func normalizeProjectSyncDiagnostics(diag ProjectSyncDiagnostics) ProjectSyncDiagnostics {
	diag.ProjectID = strings.TrimSpace(diag.ProjectID)
	diag.Transport = strings.TrimSpace(diag.Transport)
	diag.FallbackReason = strings.TrimSpace(diag.FallbackReason)
	diag.LastError = strings.TrimSpace(diag.LastError)
	diag.LastSuccessAt = cloneTimePtr(diag.LastSuccessAt)
	diag.LastErrorAt = cloneTimePtr(diag.LastErrorAt)
	if diag.RetryAttempts < 0 {
		diag.RetryAttempts = 0
	}
	if diag.RetryBackoff < 0 {
		diag.RetryBackoff = 0
	}
	if diag.Transport == "" {
		diag.Transport = projectSyncTransportPrimary
	}
	return diag
}

func cloneProjectSyncDiagnostics(diag ProjectSyncDiagnostics) ProjectSyncDiagnostics {
	return normalizeProjectSyncDiagnostics(diag)
}

func cloneTimePtr(src *time.Time) *time.Time {
	if src == nil {
		return nil
	}
	cloned := src.UTC()
	return &cloned
}

func formatProjectSyncTime(ts *time.Time) string {
	if ts == nil || ts.IsZero() {
		return "never"
	}
	return ts.UTC().Format(time.RFC3339)
}

func (d ProjectSyncDiagnostics) String() string {
	return fmt.Sprintf("project=%s state=%s transport=%s fallback=%t retry=%d backoff=%s", d.ProjectID, d.State, d.Transport, d.FallbackActive, d.RetryAttempts, d.RetryBackoff)
}
