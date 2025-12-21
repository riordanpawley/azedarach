/**
 * Pure path utility functions that don't require Path service
 */

/**
 * Prefix for Claude session names in tmux
 *
 * All Claude Code sessions managed by Azedarach use this prefix.
 * This allows TmuxSessionMonitor to identify Claude sessions and
 * enables hooks to set status on the correct session.
 */
export const CLAUDE_SESSION_PREFIX = "claude-"

/**
 * Generate tmux session name for a bead
 *
 * Returns "claude-<beadId>" for consistent naming across:
 * - Session creation (ClaudeSessionManager)
 * - Session monitoring (TmuxSessionMonitor)
 * - Hook notifications (az-notify.sh)
 */
export function getSessionName(beadId: string): string {
	return `${CLAUDE_SESSION_PREFIX}${beadId}`
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
	const lastSlash = projectPath.lastIndexOf("/")
	const parentDir = projectPath.slice(0, lastSlash)
	const projectName = projectPath.slice(lastSlash + 1)
	return `${parentDir}/${projectName}-${beadId}`
}
