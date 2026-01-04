/**
 * Dependency phase computation for epic drill-down visualization
 *
 * Computes phases using Kahn's algorithm (topological sort) to determine
 * which tasks can be worked in parallel (same phase) vs. which are blocked.
 *
 * Phase 1 = tasks with no blocking dependencies among siblings (ready now)
 * Phase 2 = tasks only blocked by Phase 1 tasks
 * Phase N = tasks only blocked by Phase 1..N-1 tasks
 */

import type { Issue } from "./BeadsClient.js"

/**
 * Phase information for a single task
 */
export interface TaskPhaseInfo {
	/** Phase number (1 = ready, 2+ = blocked by earlier phases) */
	readonly phase: number
	/** IDs of sibling tasks blocking this one (empty for Phase 1) */
	readonly blockedBy: readonly string[]
}

/**
 * Result of phase computation for all children in an epic
 */
export interface PhaseComputationResult {
	/** Map from task ID to phase info */
	readonly phases: ReadonlyMap<string, TaskPhaseInfo>
	/** Maximum phase number (for UI iteration) */
	readonly maxPhase: number
	/** Count of tasks per phase */
	readonly phaseCounts: ReadonlyMap<number, number>
}

/**
 * Compute dependency phases for epic children using Kahn's algorithm
 *
 * @param childIds - Set of child task IDs in the epic
 * @param childDetails - Map of child ID to full Issue (with dependencies)
 * @returns Phase computation result with phase assignments
 *
 * @example
 * ```ts
 * const result = computeDependencyPhases(childIds, childDetails)
 * // result.phases.get("az-xyz") => { phase: 2, blockedBy: ["az-abc"] }
 * ```
 */
export const computeDependencyPhases = (
	childIds: ReadonlySet<string>,
	childDetails: ReadonlyMap<string, Issue>,
): PhaseComputationResult => {
	// Build adjacency list: who blocks whom (among siblings only)
	// blockers[taskId] = list of sibling task IDs that block this task
	const blockers = new Map<string, string[]>()
	for (const childId of childIds) {
		blockers.set(childId, [])
	}

	// For each child, find its "blocks" dependencies that are also siblings
	for (const childId of childIds) {
		const issue = childDetails.get(childId)
		if (!issue?.dependencies) continue

		// Find blocking dependencies that are siblings AND not already closed
		// Closed tasks are considered "resolved" and no longer block dependents
		const siblingBlockers = issue.dependencies
			.filter((dep) => {
				if (dep.dependency_type !== "blocks") return false
				if (!childIds.has(dep.id)) return false
				// Skip blockers that are already closed (work complete)
				const blockerIssue = childDetails.get(dep.id)
				return blockerIssue?.status !== "closed"
			})
			.map((dep) => dep.id)

		blockers.set(childId, siblingBlockers)
	}

	// Kahn's algorithm: compute phases by removing nodes with no blockers
	const phases = new Map<string, TaskPhaseInfo>()
	const remaining = new Set(childIds)
	let currentPhase = 1

	while (remaining.size > 0) {
		// Find all tasks with no remaining blockers (or all blockers resolved)
		const readyThisPhase: string[] = []

		for (const taskId of remaining) {
			const taskBlockers = blockers.get(taskId) ?? []
			const unresolvedBlockers = taskBlockers.filter((b) => remaining.has(b))

			if (unresolvedBlockers.length === 0) {
				readyThisPhase.push(taskId)
			}
		}

		// If no tasks are ready but we still have remaining, there's a cycle
		// Assign all remaining to current phase to avoid infinite loop
		if (readyThisPhase.length === 0 && remaining.size > 0) {
			for (const taskId of remaining) {
				const originalBlockers = blockers.get(taskId) ?? []
				// Only include blockers that are still unresolved
				const blockedBy = originalBlockers.filter((b) => remaining.has(b))
				phases.set(taskId, { phase: currentPhase, blockedBy })
			}
			break
		}

		// Assign phase to ready tasks
		for (const taskId of readyThisPhase) {
			const originalBlockers = blockers.get(taskId) ?? []
			phases.set(taskId, {
				phase: currentPhase,
				blockedBy: originalBlockers, // Original blockers (now resolved)
			})
			remaining.delete(taskId)
		}

		currentPhase++
	}

	// Compute phase counts
	const phaseCounts = new Map<number, number>()
	for (const info of phases.values()) {
		phaseCounts.set(info.phase, (phaseCounts.get(info.phase) ?? 0) + 1)
	}

	return {
		phases,
		maxPhase: currentPhase - 1,
		phaseCounts,
	}
}

/**
 * Get tasks grouped by phase, sorted by phase number
 *
 * @param phases - Phase computation result
 * @returns Array of [phase, taskIds[]] tuples, sorted by phase
 */
export const getTasksByPhase = (
	phases: ReadonlyMap<string, TaskPhaseInfo>,
): ReadonlyArray<readonly [number, readonly string[]]> => {
	const byPhase = new Map<number, string[]>()

	for (const [taskId, info] of phases) {
		const existing = byPhase.get(info.phase) ?? []
		existing.push(taskId)
		byPhase.set(info.phase, existing)
	}

	// Sort by phase number
	return [...byPhase.entries()].sort((a, b) => a[0] - b[0])
}

/**
 * Check if a task is blocked (phase > 1)
 */
export const isTaskBlocked = (
	taskId: string,
	phases: ReadonlyMap<string, TaskPhaseInfo>,
): boolean => {
	const info = phases.get(taskId)
	return info !== undefined && info.phase > 1
}

/**
 * Get blocker titles for a blocked task
 *
 * @param taskId - The blocked task ID
 * @param phases - Phase computation result
 * @param childDetails - Map of child ID to full Issue
 * @returns Array of blocker titles, or empty if not blocked
 */
export const getBlockerTitles = (
	taskId: string,
	phases: ReadonlyMap<string, TaskPhaseInfo>,
	childDetails: ReadonlyMap<string, Issue>,
): readonly string[] => {
	const info = phases.get(taskId)
	if (!info || info.blockedBy.length === 0) return []

	return info.blockedBy.map((blockerId) => {
		const blocker = childDetails.get(blockerId)
		return blocker?.title ?? blockerId
	})
}
