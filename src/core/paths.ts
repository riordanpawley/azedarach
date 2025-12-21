/**
 * Pure path utility functions that don't require Path service
 *
 * Session naming convention: [type]-[projectName]-[beadId]
 * - Claude sessions: claude-{projectName}-{beadId}
 * - Dev servers: dev-{projectName}-{beadId}
 * - Chat sessions: chat-{projectName}-{beadId}
 *
 * This ensures uniqueness across multiple projects.
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
 * Prefix for dev server session names in tmux
 */
export const DEV_SESSION_PREFIX = "dev-"

/**
 * Prefix for chat session names in tmux
 */
export const CHAT_SESSION_PREFIX = "chat-"

/**
 * Extract project name from project path
 *
 * Takes the last component of the path as the project name.
 * Handles trailing slashes gracefully.
 */
export function getProjectName(projectPath: string): string {
	// Remove trailing slashes and get the last path component
	const normalized = projectPath.replace(/\/+$/, "")
	const lastSlash = normalized.lastIndexOf("/")
	return lastSlash >= 0 ? normalized.slice(lastSlash + 1) : normalized
}

/**
 * Generate tmux session name for a Claude session
 *
 * Returns "claude-{projectName}-{beadId}" for consistent naming across:
 * - Session creation (ClaudeSessionManager)
 * - Session monitoring (TmuxSessionMonitor)
 * - Hook notifications (az-notify.sh)
 */
export function getSessionName(projectPath: string, beadId: string): string {
	const projectName = getProjectName(projectPath)
	return `${CLAUDE_SESSION_PREFIX}${projectName}-${beadId}`
}

/**
 * Generate tmux session name for a dev server
 *
 * Returns "dev-{projectName}-{beadId}" for consistent naming.
 */
export function getDevSessionName(projectPath: string, beadId: string): string {
	const projectName = getProjectName(projectPath)
	return `${DEV_SESSION_PREFIX}${projectName}-${beadId}`
}

/**
 * Generate tmux session name for a chat session
 *
 * Returns "chat-{projectName}-{beadId}" for consistent naming.
 */
export function getChatSessionName(projectPath: string, beadId: string): string {
	const projectName = getProjectName(projectPath)
	return `${CHAT_SESSION_PREFIX}${projectName}-${beadId}`
}

/**
 * Parse a session name to extract type, project, and beadId
 *
 * Returns undefined if the session name doesn't match the expected format.
 */
export function parseSessionName(
	sessionName: string,
): { type: "claude" | "dev" | "chat"; projectName: string; beadId: string } | undefined {
	// Try each prefix
	for (const [prefix, type] of [
		[CLAUDE_SESSION_PREFIX, "claude"],
		[DEV_SESSION_PREFIX, "dev"],
		[CHAT_SESSION_PREFIX, "chat"],
	] as const) {
		if (sessionName.startsWith(prefix)) {
			const rest = sessionName.slice(prefix.length)
			// Find the last hyphen to split projectName-beadId
			// beadId format is like "az-bqzy", so we need to find project-beadPrefix-beadSuffix
			// The beadId contains a hyphen, so we split on the second-to-last segment
			const parts = rest.split("-")
			if (parts.length >= 3) {
				// Last two parts are the beadId (e.g., "az", "bqzy")
				const beadId = `${parts[parts.length - 2]}-${parts[parts.length - 1]}`
				// Everything before is the project name (could have hyphens)
				const projectName = parts.slice(0, -2).join("-")
				if (projectName && beadId) {
					return { type, projectName, beadId }
				}
			}
		}
	}
	return undefined
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
