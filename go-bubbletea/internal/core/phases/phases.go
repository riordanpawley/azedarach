// Package phases provides dependency phase computation for tasks.
//
// Computes phases using Kahn's algorithm (topological sort) to determine
// which tasks can be worked in parallel (same phase) vs. which are blocked.
//
// Phase 0 = tasks with no blocking dependencies (ready now)
// Phase N = tasks only blocked by Phase 0..N-1 tasks
package phases

import (
	"github.com/riordanpawley/azedarach/internal/domain"
)

// TaskPhaseInfo contains phase information for a single task
type TaskPhaseInfo struct {
	// Phase number (0 = ready, 1+ = blocked by earlier phases)
	Phase int
	// IDs of sibling tasks blocking this one (empty for Phase 0)
	BlockedBy []string
}

// PhaseComputationResult contains the result of phase computation for all children in an epic
type PhaseComputationResult struct {
	// Map from task ID to phase info
	Phases map[string]TaskPhaseInfo
	// Maximum phase number (for UI iteration)
	MaxPhase int
	// Count of tasks per phase
	PhaseCounts map[int]int
}

// ComputeDependencyPhases computes dependency phases for epic children using Kahn's algorithm
//
// Parameters:
//   - childIDs: Set of child task IDs in the epic
//   - tasks: Map of task ID to full Task (with dependencies)
//
// Returns: Phase computation result with phase assignments
//
// Example:
//
//	result := ComputeDependencyPhases(childIDs, tasks)
//	// result.Phases["az-xyz"] => { Phase: 2, BlockedBy: []string{"az-abc"} }
func ComputeDependencyPhases(childIDs map[string]bool, tasks map[string]domain.Task) PhaseComputationResult {
	// Build adjacency list: who blocks whom (among siblings only)
	// blockers[taskId] = list of sibling task IDs that block this task
	blockers := make(map[string][]string)
	for childID := range childIDs {
		blockers[childID] = []string{}
	}

	// For each child, find its "blocks" dependencies that are also siblings
	for childID := range childIDs {
		task, exists := tasks[childID]
		if !exists || task.Dependencies == nil {
			continue
		}

		// Find blocking dependencies that are siblings
		var siblingBlockers []string
		for _, dep := range task.Dependencies {
			if dep.Type == domain.DependencyBlocks && childIDs[dep.ID] {
				siblingBlockers = append(siblingBlockers, dep.ID)
			}
		}

		blockers[childID] = siblingBlockers
	}

	// Kahn's algorithm: compute phases by removing nodes with no blockers
	phases := make(map[string]TaskPhaseInfo)
	remaining := make(map[string]bool)
	for id := range childIDs {
		remaining[id] = true
	}

	currentPhase := 0

	for len(remaining) > 0 {
		// Find all tasks with no remaining blockers (or all blockers resolved)
		var readyThisPhase []string

		for taskID := range remaining {
			taskBlockers := blockers[taskID]
			unresolvedBlockers := []string{}

			for _, blocker := range taskBlockers {
				if remaining[blocker] {
					unresolvedBlockers = append(unresolvedBlockers, blocker)
				}
			}

			if len(unresolvedBlockers) == 0 {
				readyThisPhase = append(readyThisPhase, taskID)
			}
		}

		// If no tasks are ready but we still have remaining, there's a cycle
		// Assign all remaining to current phase to avoid infinite loop
		if len(readyThisPhase) == 0 && len(remaining) > 0 {
			for taskID := range remaining {
				originalBlockers := blockers[taskID]
				// Only include blockers that are still unresolved
				blockedBy := []string{}
				for _, b := range originalBlockers {
					if remaining[b] {
						blockedBy = append(blockedBy, b)
					}
				}
				phases[taskID] = TaskPhaseInfo{
					Phase:     currentPhase,
					BlockedBy: blockedBy,
				}
			}
			break
		}

		// Assign phase to ready tasks
		for _, taskID := range readyThisPhase {
			originalBlockers := blockers[taskID]
			phases[taskID] = TaskPhaseInfo{
				Phase:     currentPhase,
				BlockedBy: originalBlockers, // Original blockers (now resolved)
			}
			delete(remaining, taskID)
		}

		currentPhase++
	}

	// Compute phase counts and maxPhase
	phaseCounts := make(map[int]int)
	maxPhase := 0
	for _, info := range phases {
		phaseCounts[info.Phase]++
		if info.Phase > maxPhase {
			maxPhase = info.Phase
		}
	}

	return PhaseComputationResult{
		Phases:      phases,
		MaxPhase:    maxPhase,
		PhaseCounts: phaseCounts,
	}
}

// GetTasksByPhase groups tasks by phase, sorted by phase number
//
// Parameters:
//   - phases: Phase computation result
//
// Returns: Slice of [phase, taskIds] pairs, sorted by phase
func GetTasksByPhase(phases map[string]TaskPhaseInfo) []struct {
	Phase   int
	TaskIDs []string
} {
	byPhase := make(map[int][]string)

	for taskID, info := range phases {
		byPhase[info.Phase] = append(byPhase[info.Phase], taskID)
	}

	// Convert to sorted slice
	var result []struct {
		Phase   int
		TaskIDs []string
	}

	// Find max phase to iterate in order
	maxPhase := 0
	for phase := range byPhase {
		if phase > maxPhase {
			maxPhase = phase
		}
	}

	for phase := 0; phase <= maxPhase; phase++ {
		if taskIDs, exists := byPhase[phase]; exists {
			result = append(result, struct {
				Phase   int
				TaskIDs []string
			}{
				Phase:   phase,
				TaskIDs: taskIDs,
			})
		}
	}

	return result
}

// IsTaskBlocked returns true if a task is blocked (phase > 0)
func IsTaskBlocked(taskID string, phases map[string]TaskPhaseInfo) bool {
	info, exists := phases[taskID]
	return exists && info.Phase > 0
}

// GetBlockerTitles returns titles of tasks blocking the given task
//
// Parameters:
//   - taskID: The blocked task ID
//   - phases: Phase computation result
//   - tasks: Map of task ID to full Task
//
// Returns: Slice of blocker titles, or empty if not blocked
func GetBlockerTitles(taskID string, phases map[string]TaskPhaseInfo, tasks map[string]domain.Task) []string {
	info, exists := phases[taskID]
	if !exists || len(info.BlockedBy) == 0 {
		return []string{}
	}

	titles := make([]string, 0, len(info.BlockedBy))
	for _, blockerID := range info.BlockedBy {
		if blocker, exists := tasks[blockerID]; exists {
			titles = append(titles, blocker.Title)
		} else {
			titles = append(titles, blockerID)
		}
	}

	return titles
}
