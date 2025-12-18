/**
 * Pure path utility functions that don't require Path service
 */

/**
 * Generate tmux session name for a bead
 */
export function getSessionName(beadId: string): string {
	return beadId // Use bead ID directly as session name
}

/**
 * Compute the worktree path for a bead
 *
 * Worktrees are created as siblings to the project directory:
 * ../ProjectName-beadId/
 *
 * @param projectPath - Absolute path to the project directory
 * @param beadId - The bead ID
 * @returns Absolute path to the worktree directory
 */
export function getWorktreePath(projectPath: string, beadId: string): string {
	const projectName = projectPath.split("/").pop() || "project"
	const parentDir = projectPath.split("/").slice(0, -1).join("/")
	return `${parentDir}/${projectName}-${beadId}`
}
