package phases

import (
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// Helper to create a task with dependencies
func makeTask(id, title string, blockedBy ...string) domain.Task {
	var deps []domain.Dependency
	for _, blockerID := range blockedBy {
		deps = append(deps, domain.Dependency{
			ID:   blockerID,
			Type: domain.DependencyBlocks,
		})
	}

	return domain.Task{
		ID:           id,
		Title:        title,
		Status:       domain.StatusOpen,
		Priority:     domain.P2,
		Type:         domain.TypeTask,
		Dependencies: deps,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

// Helper to create a set of task IDs
func makeIDSet(ids ...string) map[string]bool {
	set := make(map[string]bool)
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// Helper to create a map of tasks
func makeTaskMap(tasks ...domain.Task) map[string]domain.Task {
	m := make(map[string]domain.Task)
	for _, task := range tasks {
		m[task.ID] = task
	}
	return m
}

func TestComputeDependencyPhases_NoDependencies(t *testing.T) {
	// Three parallel tasks with no dependencies
	tasks := makeTaskMap(
		makeTask("az-1", "Task 1"),
		makeTask("az-2", "Task 2"),
		makeTask("az-3", "Task 3"),
	)
	childIDs := makeIDSet("az-1", "az-2", "az-3")

	result := ComputeDependencyPhases(childIDs, tasks)

	// All tasks should be in phase 0
	if result.MaxPhase != 0 {
		t.Errorf("Expected MaxPhase 0, got %d", result.MaxPhase)
	}

	for _, id := range []string{"az-1", "az-2", "az-3"} {
		info, exists := result.Phases[id]
		if !exists {
			t.Fatalf("Task %s not in result", id)
		}
		if info.Phase != 0 {
			t.Errorf("Task %s: expected phase 0, got %d", id, info.Phase)
		}
		if len(info.BlockedBy) != 0 {
			t.Errorf("Task %s: expected no blockers, got %v", id, info.BlockedBy)
		}
	}

	// Phase count should be 3 tasks in phase 0
	if result.PhaseCounts[0] != 3 {
		t.Errorf("Expected 3 tasks in phase 0, got %d", result.PhaseCounts[0])
	}
}

func TestComputeDependencyPhases_LinearChain(t *testing.T) {
	// Linear dependency: az-1 -> az-2 -> az-3
	// (az-2 is blocked by az-1, az-3 is blocked by az-2)
	tasks := makeTaskMap(
		makeTask("az-1", "Task 1"),
		makeTask("az-2", "Task 2", "az-1"),
		makeTask("az-3", "Task 3", "az-2"),
	)
	childIDs := makeIDSet("az-1", "az-2", "az-3")

	result := ComputeDependencyPhases(childIDs, tasks)

	// Should have 3 phases (0, 1, 2)
	if result.MaxPhase != 2 {
		t.Errorf("Expected MaxPhase 2, got %d", result.MaxPhase)
	}

	// az-1 should be phase 0
	info1, exists := result.Phases["az-1"]
	if !exists {
		t.Fatal("Task az-1 not in result")
	}
	if info1.Phase != 0 {
		t.Errorf("az-1: expected phase 0, got %d", info1.Phase)
	}

	// az-2 should be phase 1, blocked by az-1
	info2, exists := result.Phases["az-2"]
	if !exists {
		t.Fatal("Task az-2 not in result")
	}
	if info2.Phase != 1 {
		t.Errorf("az-2: expected phase 1, got %d", info2.Phase)
	}
	if len(info2.BlockedBy) != 1 || info2.BlockedBy[0] != "az-1" {
		t.Errorf("az-2: expected blocked by [az-1], got %v", info2.BlockedBy)
	}

	// az-3 should be phase 2, blocked by az-2
	info3, exists := result.Phases["az-3"]
	if !exists {
		t.Fatal("Task az-3 not in result")
	}
	if info3.Phase != 2 {
		t.Errorf("az-3: expected phase 2, got %d", info3.Phase)
	}
	if len(info3.BlockedBy) != 1 || info3.BlockedBy[0] != "az-2" {
		t.Errorf("az-3: expected blocked by [az-2], got %v", info3.BlockedBy)
	}
}

func TestComputeDependencyPhases_DiamondGraph(t *testing.T) {
	// Diamond dependency:
	//     az-1 (phase 0)
	//    /    \
	// az-2    az-3 (phase 1)
	//    \    /
	//     az-4 (phase 2)
	tasks := makeTaskMap(
		makeTask("az-1", "Task 1"),
		makeTask("az-2", "Task 2", "az-1"),
		makeTask("az-3", "Task 3", "az-1"),
		makeTask("az-4", "Task 4", "az-2", "az-3"),
	)
	childIDs := makeIDSet("az-1", "az-2", "az-3", "az-4")

	result := ComputeDependencyPhases(childIDs, tasks)

	// Should have 3 phases (0, 1, 2)
	if result.MaxPhase != 2 {
		t.Errorf("Expected MaxPhase 2, got %d", result.MaxPhase)
	}

	// az-1 should be phase 0
	if result.Phases["az-1"].Phase != 0 {
		t.Errorf("az-1: expected phase 0, got %d", result.Phases["az-1"].Phase)
	}

	// az-2 and az-3 should be phase 1 (can run in parallel)
	if result.Phases["az-2"].Phase != 1 {
		t.Errorf("az-2: expected phase 1, got %d", result.Phases["az-2"].Phase)
	}
	if result.Phases["az-3"].Phase != 1 {
		t.Errorf("az-3: expected phase 1, got %d", result.Phases["az-3"].Phase)
	}

	// az-4 should be phase 2, blocked by both az-2 and az-3
	info4 := result.Phases["az-4"]
	if info4.Phase != 2 {
		t.Errorf("az-4: expected phase 2, got %d", info4.Phase)
	}
	if len(info4.BlockedBy) != 2 {
		t.Errorf("az-4: expected 2 blockers, got %d", len(info4.BlockedBy))
	}

	// Phase counts
	if result.PhaseCounts[0] != 1 {
		t.Errorf("Expected 1 task in phase 0, got %d", result.PhaseCounts[0])
	}
	if result.PhaseCounts[1] != 2 {
		t.Errorf("Expected 2 tasks in phase 1, got %d", result.PhaseCounts[1])
	}
	if result.PhaseCounts[2] != 1 {
		t.Errorf("Expected 1 task in phase 2, got %d", result.PhaseCounts[2])
	}
}

func TestComputeDependencyPhases_CircularDependency(t *testing.T) {
	// Circular dependency: az-1 -> az-2 -> az-3 -> az-1
	tasks := makeTaskMap(
		makeTask("az-1", "Task 1", "az-3"),
		makeTask("az-2", "Task 2", "az-1"),
		makeTask("az-3", "Task 3", "az-2"),
	)
	childIDs := makeIDSet("az-1", "az-2", "az-3")

	result := ComputeDependencyPhases(childIDs, tasks)

	// All tasks should be assigned to the same phase (cycle detected)
	// The algorithm should not loop infinitely
	if result.MaxPhase != 0 {
		t.Errorf("Expected MaxPhase 0 for circular dependency, got %d", result.MaxPhase)
	}

	// All should be in the same phase with unresolved blockers
	for _, id := range []string{"az-1", "az-2", "az-3"} {
		info, exists := result.Phases[id]
		if !exists {
			t.Fatalf("Task %s not in result", id)
		}
		if len(info.BlockedBy) == 0 {
			t.Errorf("Task %s in circular dependency should have blockers", id)
		}
	}
}

func TestComputeDependencyPhases_IgnoreExternalDependencies(t *testing.T) {
	// az-1 depends on az-external (not in childIDs)
	// az-2 depends on az-1
	// az-external should be ignored
	tasks := makeTaskMap(
		makeTask("az-1", "Task 1", "az-external"),
		makeTask("az-2", "Task 2", "az-1"),
		makeTask("az-external", "External Task"),
	)
	childIDs := makeIDSet("az-1", "az-2") // az-external not included

	result := ComputeDependencyPhases(childIDs, tasks)

	// az-1 should be phase 0 (external dependency ignored)
	if result.Phases["az-1"].Phase != 0 {
		t.Errorf("az-1: expected phase 0 (external dep ignored), got %d", result.Phases["az-1"].Phase)
	}

	// az-2 should be phase 1, blocked by az-1
	if result.Phases["az-2"].Phase != 1 {
		t.Errorf("az-2: expected phase 1, got %d", result.Phases["az-2"].Phase)
	}
}

func TestComputeDependencyPhases_ComplexMixed(t *testing.T) {
	// Complex mixed graph:
	//     az-1 (phase 0)
	//    /    \
	// az-2    az-3 (phase 1)
	//   |       |
	// az-4    az-5 (phase 2)
	//    \    /
	//     az-6 (phase 3)
	// Plus az-7 independent (phase 0)
	tasks := makeTaskMap(
		makeTask("az-1", "Task 1"),
		makeTask("az-2", "Task 2", "az-1"),
		makeTask("az-3", "Task 3", "az-1"),
		makeTask("az-4", "Task 4", "az-2"),
		makeTask("az-5", "Task 5", "az-3"),
		makeTask("az-6", "Task 6", "az-4", "az-5"),
		makeTask("az-7", "Task 7"), // Independent
	)
	childIDs := makeIDSet("az-1", "az-2", "az-3", "az-4", "az-5", "az-6", "az-7")

	result := ComputeDependencyPhases(childIDs, tasks)

	// Should have 4 phases (0, 1, 2, 3)
	if result.MaxPhase != 3 {
		t.Errorf("Expected MaxPhase 3, got %d", result.MaxPhase)
	}

	// Check phase assignments
	expectedPhases := map[string]int{
		"az-1": 0,
		"az-7": 0,
		"az-2": 1,
		"az-3": 1,
		"az-4": 2,
		"az-5": 2,
		"az-6": 3,
	}

	for id, expectedPhase := range expectedPhases {
		info, exists := result.Phases[id]
		if !exists {
			t.Fatalf("Task %s not in result", id)
		}
		if info.Phase != expectedPhase {
			t.Errorf("Task %s: expected phase %d, got %d", id, expectedPhase, info.Phase)
		}
	}

	// Phase counts
	if result.PhaseCounts[0] != 2 {
		t.Errorf("Expected 2 tasks in phase 0, got %d", result.PhaseCounts[0])
	}
	if result.PhaseCounts[1] != 2 {
		t.Errorf("Expected 2 tasks in phase 1, got %d", result.PhaseCounts[1])
	}
	if result.PhaseCounts[2] != 2 {
		t.Errorf("Expected 2 tasks in phase 2, got %d", result.PhaseCounts[2])
	}
	if result.PhaseCounts[3] != 1 {
		t.Errorf("Expected 1 task in phase 3, got %d", result.PhaseCounts[3])
	}
}

func TestGetTasksByPhase(t *testing.T) {
	phases := map[string]TaskPhaseInfo{
		"az-1": {Phase: 0, BlockedBy: []string{}},
		"az-2": {Phase: 1, BlockedBy: []string{"az-1"}},
		"az-3": {Phase: 1, BlockedBy: []string{"az-1"}},
		"az-4": {Phase: 2, BlockedBy: []string{"az-2"}},
	}

	result := GetTasksByPhase(phases)

	// Should have 3 phase groups
	if len(result) != 3 {
		t.Errorf("Expected 3 phase groups, got %d", len(result))
	}

	// Check phase 0
	if result[0].Phase != 0 {
		t.Errorf("Expected first group to be phase 0, got %d", result[0].Phase)
	}
	if len(result[0].TaskIDs) != 1 {
		t.Errorf("Expected 1 task in phase 0, got %d", len(result[0].TaskIDs))
	}

	// Check phase 1
	if result[1].Phase != 1 {
		t.Errorf("Expected second group to be phase 1, got %d", result[1].Phase)
	}
	if len(result[1].TaskIDs) != 2 {
		t.Errorf("Expected 2 tasks in phase 1, got %d", len(result[1].TaskIDs))
	}

	// Check phase 2
	if result[2].Phase != 2 {
		t.Errorf("Expected third group to be phase 2, got %d", result[2].Phase)
	}
	if len(result[2].TaskIDs) != 1 {
		t.Errorf("Expected 1 task in phase 2, got %d", len(result[2].TaskIDs))
	}
}

func TestIsTaskBlocked(t *testing.T) {
	phases := map[string]TaskPhaseInfo{
		"az-1": {Phase: 0, BlockedBy: []string{}},
		"az-2": {Phase: 1, BlockedBy: []string{"az-1"}},
	}

	if IsTaskBlocked("az-1", phases) {
		t.Error("az-1 should not be blocked (phase 0)")
	}

	if !IsTaskBlocked("az-2", phases) {
		t.Error("az-2 should be blocked (phase > 0)")
	}

	if IsTaskBlocked("az-3", phases) {
		t.Error("az-3 should not be blocked (not in phases)")
	}
}

func TestGetBlockerTitles(t *testing.T) {
	tasks := makeTaskMap(
		makeTask("az-1", "First Task"),
		makeTask("az-2", "Second Task", "az-1"),
		makeTask("az-3", "Third Task", "az-1", "az-2"),
	)

	phases := map[string]TaskPhaseInfo{
		"az-1": {Phase: 0, BlockedBy: []string{}},
		"az-2": {Phase: 1, BlockedBy: []string{"az-1"}},
		"az-3": {Phase: 2, BlockedBy: []string{"az-1", "az-2"}},
	}

	// az-1 has no blockers
	titles1 := GetBlockerTitles("az-1", phases, tasks)
	if len(titles1) != 0 {
		t.Errorf("az-1: expected no blocker titles, got %v", titles1)
	}

	// az-2 blocked by az-1
	titles2 := GetBlockerTitles("az-2", phases, tasks)
	if len(titles2) != 1 {
		t.Errorf("az-2: expected 1 blocker title, got %d", len(titles2))
	}
	if titles2[0] != "First Task" {
		t.Errorf("az-2: expected blocker title 'First Task', got '%s'", titles2[0])
	}

	// az-3 blocked by az-1 and az-2
	titles3 := GetBlockerTitles("az-3", phases, tasks)
	if len(titles3) != 2 {
		t.Errorf("az-3: expected 2 blocker titles, got %d", len(titles3))
	}
}

func TestComputeDependencyPhases_EmptyInput(t *testing.T) {
	tasks := makeTaskMap()
	childIDs := makeIDSet()

	result := ComputeDependencyPhases(childIDs, tasks)

	if result.MaxPhase != 0 {
		t.Errorf("Expected MaxPhase 0 for empty input, got %d", result.MaxPhase)
	}
	if len(result.Phases) != 0 {
		t.Errorf("Expected empty phases map, got %d entries", len(result.Phases))
	}
	if len(result.PhaseCounts) != 0 {
		t.Errorf("Expected empty phase counts, got %d entries", len(result.PhaseCounts))
	}
}

func TestComputeDependencyPhases_OnlyBlockedByDependencies(t *testing.T) {
	// Test that only "blocks" dependencies are considered
	// "blocked_by", "related_to", and "parent_child" should be ignored
	task1 := makeTask("az-1", "Task 1")
	task2 := domain.Task{
		ID:     "az-2",
		Title:  "Task 2",
		Status: domain.StatusOpen,
		Dependencies: []domain.Dependency{
			{ID: "az-1", Type: domain.DependencyBlockedBy}, // Should be ignored
		},
	}
	task3 := domain.Task{
		ID:     "az-3",
		Title:  "Task 3",
		Status: domain.StatusOpen,
		Dependencies: []domain.Dependency{
			{ID: "az-1", Type: domain.DependencyRelatedTo}, // Should be ignored
		},
	}

	tasks := makeTaskMap(task1, task2, task3)
	childIDs := makeIDSet("az-1", "az-2", "az-3")

	result := ComputeDependencyPhases(childIDs, tasks)

	// All should be phase 0 since only "blocks" dependencies count
	for _, id := range []string{"az-1", "az-2", "az-3"} {
		if result.Phases[id].Phase != 0 {
			t.Errorf("%s: expected phase 0 (non-blocks deps ignored), got %d", id, result.Phases[id].Phase)
		}
	}
}
