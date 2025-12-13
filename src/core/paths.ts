import * as path from "node:path"

// Pure functions for path operations - no Effect needed

/**
 * Get the project name from a directory path
 */
export function getProjectName(projectPath: string): string {
	return path.basename(projectPath)
}

/**
 * Generate worktree path for a bead
 * Convention: ../ProjectName-<bead-id>/
 */
export function getWorktreePath(projectPath: string, beadId: string): string {
	const projectName = getProjectName(projectPath)
	const parentDir = path.dirname(projectPath)
	return path.join(parentDir, `${projectName}-${beadId}`)
}

/**
 * Generate tmux session name for a bead
 */
export function getSessionName(beadId: string): string {
	return beadId // Use bead ID directly as session name
}

/**
 * Normalize a path for consistent comparison
 */
export function normalizePath(p: string): string {
	return path.resolve(p)
}
