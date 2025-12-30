package app

import (
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// computePhases computes dependency phases for all visible tasks
func (m Model) computePhases() map[string]phases.TaskPhaseInfo {
	// Create task map and ID set
	taskMap := make(map[string]domain.Task)
	taskIDs := make(map[string]bool)
	
	for _, task := range m.tasks {
		taskMap[task.ID] = task
		taskIDs[task.ID] = true
	}
	
	// Compute phases
	result := phases.ComputeDependencyPhases(taskIDs, taskMap)
	return result.Phases
}
