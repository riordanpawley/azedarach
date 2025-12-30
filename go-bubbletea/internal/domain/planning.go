package domain

import "time"

// PlannedTask represents a task within a planning spec
type PlannedTask struct {
	ID             string   `json:"id"`              // Temporary ID for dependency linking (e.g., "task-1")
	Title          string   `json:"title"`           // Task title
	Description    string   `json:"description"`     // What this task accomplishes
	Type           TaskType `json:"type"`            // Task type (task, bug, feature, chore)
	Priority       int      `json:"priority"`        // Priority (1-4)
	Estimate       *int     `json:"estimate"`        // Hours estimate (optional)
	DependsOn      []string `json:"dependsOn"`       // IDs of tasks this depends on
	CanParallelize bool     `json:"canParallelize"`  // Can run in parallel with siblings
	Design         string   `json:"design"`          // Technical design notes
	Acceptance     string   `json:"acceptance"`      // Acceptance criteria
}

// Plan represents a complete implementation plan
type Plan struct {
	EpicTitle            string        `json:"epicTitle"`            // Epic title
	EpicDescription      string        `json:"epicDescription"`      // Epic description
	Summary              string        `json:"summary"`              // Brief summary of approach
	Tasks                []PlannedTask `json:"tasks"`                // List of planned tasks
	ReviewNotes          string        `json:"reviewNotes"`          // Notes from review passes
	ParallelizationScore int           `json:"parallelizationScore"` // 0-100, how parallelizable
}

// ReviewFeedback represents AI review feedback for a plan
type ReviewFeedback struct {
	Score                        int                 `json:"score"`                        // 0-100 quality score
	Issues                       []string            `json:"issues"`                       // List of issues found
	Suggestions                  []string            `json:"suggestions"`                  // List of suggestions
	ParallelizationOpportunities []string            `json:"parallelizationOpportunities"` // Parallelization opportunities
	TasksTooLarge                []string            `json:"tasksTooLarge"`                // Task IDs that should be split
	MissingDependencies          []MissingDependency `json:"missingDependencies"`          // Missing dependency relationships
	IsApproved                   bool                `json:"isApproved"`                   // Ready for beads generation?
}

// MissingDependency represents a missing dependency relationship
type MissingDependency struct {
	TaskID        string `json:"taskId"`        // Task that should have dependency
	ShouldDependOn string `json:"shouldDependOn"` // Task it should depend on
	Reason        string `json:"reason"`        // Why this dependency is needed
}

// PlanningStatus represents the current status of planning workflow
type PlanningStatus string

const (
	PlanningIdle          PlanningStatus = "idle"
	PlanningGenerating    PlanningStatus = "generating"
	PlanningReviewing     PlanningStatus = "reviewing"
	PlanningRefining      PlanningStatus = "refining"
	PlanningCreatingBeads PlanningStatus = "creating_beads"
	PlanningComplete      PlanningStatus = "complete"
	PlanningErrorStatus   PlanningStatus = "error"
)

// PlanningState tracks the state of a planning session
type PlanningState struct {
	Status             PlanningStatus   // Current status
	FeatureDescription string           // Original feature description
	CurrentPlan        *Plan            // Current plan being worked on
	ReviewPass         int              // Current review pass number
	MaxReviewPasses    int              // Maximum number of review passes
	ReviewHistory      []ReviewFeedback // History of review feedback
	CreatedBeads       []Task           // Beads created from the plan
	Error              string           // Error message if status is error
	UpdatedAt          time.Time        // Last update time
}

// PlanningError represents an error during planning
type PlanningError struct {
	Phase   string // Phase: "generation", "review", "refinement", "beads_creation"
	Message string // Error message
	Err     error  // Underlying error
}

func (e *PlanningError) Error() string {
	if e.Err != nil {
		return e.Phase + ": " + e.Message + ": " + e.Err.Error()
	}
	return e.Phase + ": " + e.Message
}

func (e *PlanningError) Unwrap() error {
	return e.Err
}
