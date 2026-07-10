package domain

import "time"

// WorkerObservationState is the daemon-derived orchestration state for one worker issue.
type WorkerObservationState string

const (
	WorkerObservationRunnable       WorkerObservationState = "runnable"
	WorkerObservationWorking        WorkerObservationState = "working"
	WorkerObservationWaitingHuman   WorkerObservationState = "waiting_human"
	WorkerObservationBlocked        WorkerObservationState = "blocked"
	WorkerObservationReviewReady    WorkerObservationState = "review_ready"
	WorkerObservationStale          WorkerObservationState = "stale"
	WorkerObservationFailed         WorkerObservationState = "failed"
	WorkerObservationCleanupPending WorkerObservationState = "cleanup_pending"
	WorkerObservationDone           WorkerObservationState = "done"
)

// WorkerObservationSourcePolicy describes the source-of-truth policy behind a worker observation.
type WorkerObservationSourcePolicy struct {
	IssueGraph       string `json:"issue_graph"`
	SessionRuntime   string `json:"session_runtime"`
	WorktreeGit      string `json:"worktree_git"`
	MailboxEvidence  string `json:"mailbox_evidence"`
	ActiveOperations string `json:"active_operations"`
}

// WorkerObservationEventSummary captures the latest meaningful fact that influenced a worker observation.
type WorkerObservationEventSummary struct {
	Kind      string    `json:"kind"`
	Type      string    `json:"type"`
	At        time.Time `json:"at,omitempty,omitzero"`
	Source    string    `json:"source,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Seq       int64     `json:"seq,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Worktree  string    `json:"worktree,omitempty"`
}

// WorkerObservation is the daemon-owned projection consumed by CLI/TUI orchestration views.
type WorkerObservation struct {
	IssueID            string                         `json:"issue_id"`
	State              WorkerObservationState         `json:"state"`
	Reason             string                         `json:"reason"`
	WaitingHumanSource WaitingHumanSource             `json:"waiting_human_source,omitempty"`
	WaitingHumanReason string                         `json:"waiting_human_reason,omitempty"`
	LastEvent          *WorkerObservationEventSummary `json:"last_meaningful_event,omitempty"`
	EvidenceSummary    []string                       `json:"evidence_summary,omitempty"`
	Risks              []string                       `json:"risks,omitempty"`
	NextActions        []string                       `json:"next_actions,omitempty"`
	SourceTruthPolicy  WorkerObservationSourcePolicy  `json:"source_truth_policy"`
}
