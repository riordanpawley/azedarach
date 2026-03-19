import type { Issue, PhaseComputationResult } from "../contracts.js"

export const computeDependencyPhases = (
	childIds: ReadonlySet<string>,
	childDetails: ReadonlyMap<string, Issue>,
): PhaseComputationResult => {
	const blockers = new Map<string, string[]>()
	for (const childId of childIds) {
		blockers.set(childId, [])
	}

	for (const childId of childIds) {
		const issue = childDetails.get(childId)
		if (!issue?.dependencies) continue

		const siblingBlockers = issue.dependencies
			.filter((dep) => {
				if (dep.dependency_type !== "blocks") return false
				if (!childIds.has(dep.id)) return false
				const blockerIssue = childDetails.get(dep.id)
				return blockerIssue?.status !== "closed"
			})
			.map((dep) => dep.id)

		blockers.set(childId, siblingBlockers)
	}

	const phases = new Map<string, { phase: number; blockedBy: readonly string[] }>()
	const remaining = new Set(childIds)
	let currentPhase = 1

	while (remaining.size > 0) {
		const readyThisPhase: string[] = []

		for (const taskId of remaining) {
			const taskBlockers = blockers.get(taskId) ?? []
			const unresolvedBlockers = taskBlockers.filter((blockerId) => remaining.has(blockerId))
			if (unresolvedBlockers.length === 0) {
				readyThisPhase.push(taskId)
			}
		}

		if (readyThisPhase.length === 0 && remaining.size > 0) {
			for (const taskId of remaining) {
				const originalBlockers = blockers.get(taskId) ?? []
				const blockedBy = originalBlockers.filter((blockerId) => remaining.has(blockerId))
				phases.set(taskId, { phase: currentPhase, blockedBy })
			}
			break
		}

		for (const taskId of readyThisPhase) {
			const originalBlockers = blockers.get(taskId) ?? []
			phases.set(taskId, {
				phase: currentPhase,
				blockedBy: originalBlockers,
			})
			remaining.delete(taskId)
		}

		currentPhase++
	}

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
