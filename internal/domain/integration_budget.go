package domain

import "time"

type IntegrationCandidateValidationStatus string

const (
	IntegrationCandidateValidationRunning    IntegrationCandidateValidationStatus = "running"
	IntegrationCandidateValidationPassed     IntegrationCandidateValidationStatus = "passed"
	IntegrationCandidateValidationFailed     IntegrationCandidateValidationStatus = "failed"
	IntegrationCandidateValidationCancelled  IntegrationCandidateValidationStatus = "cancelled"
	IntegrationCandidateValidationSuperseded IntegrationCandidateValidationStatus = "superseded"
)

// IntegrationCandidateValidationAttempt is canonical evidence only when the
// exact validated candidate OID was subsequently applied to the target.
type IntegrationCandidateValidationAttempt struct {
	CandidateHead string                               `json:"candidate_head"`
	Status        IntegrationCandidateValidationStatus `json:"status"`
	Canonical     bool                                 `json:"canonical"`
	Message       string                               `json:"message,omitempty"`
	Stages        []ValidationStageEvidence            `json:"stages,omitempty"`
}

type ValidationStageEvidence struct {
	ID            string   `json:"id"`
	Status        string   `json:"status"`
	OutputRoot    string   `json:"output_root,omitempty"`
	TempRoot      string   `json:"temp_root,omitempty"`
	ArtifactPaths []string `json:"artifact_paths,omitempty"`
	Stdout        string   `json:"stdout,omitempty"`
	Stderr        string   `json:"stderr,omitempty"`
}

// Integration validation and lifecycle budgets are deliberately layered. The
// repository merge gate owns the validation window, Git retains time to finish
// the merge and post-merge hooks after validation, and task close retains a
// final cleanup/status-write reserve after Git completes.
const (
	IntegrationValidationTimeout  = 10 * time.Minute
	IntegrationTestBinaryTimeout  = 8 * time.Minute
	IntegrationFinalizeReserve    = 5 * time.Minute
	IntegrationMergeTimeout       = IntegrationValidationTimeout + IntegrationFinalizeReserve
	IntegrationCloseReserve       = 5 * time.Minute
	IntegrationCloseTimeout       = IntegrationMergeTimeout + IntegrationCloseReserve
	IntegrationClientReserve      = 1 * time.Minute
	IntegrationClientTimeout      = IntegrationCloseTimeout + IntegrationClientReserve
	LifecycleCleanupTimeout       = IntegrationCloseReserve
	LifecycleCleanupClientTimeout = LifecycleCleanupTimeout + IntegrationClientReserve
)
