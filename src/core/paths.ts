/**
 * Pure path utility functions that don't require Path service
 *
 * Session naming convention: [beadId]
 * - Each bead has exactly one tmux session
 * - Windows within the session handle different concerns (code, dev, etc.)
 */

import type { CliToolName } from "./CliToolRegistry.js"

/**
 * Standard window names for bead sessions
 */
export const WINDOW_NAMES = {
	CODE: "code",
	DEV: "dev",
	CHAT: "chat",
	BACKGROUND: "background",
} as const

export const AI_SESSION_PREFIXES = ["claude-", "opencode-"]

/**
 * Generate tmux session name for a bead
 *
 * Returns exactly the beadId for consistent naming across:
 * - Session creation (WorktreeSessionService)
 * - Session monitoring (TmuxSessionMonitor)
 * - Hook notifications
 *
 * @param beadId - The bead ID
 */
export function getBeadSessionName(beadId: string): string {
	return beadId
}

/**
 * Session types that can be parsed from tmux session names
 */
export type SessionType = "claude" | "opencode" | "dev" | "chat" | "bead"

/**
 * Parse a session name to extract type and beadId
 *
 * Returns undefined if the session name doesn't match the expected format.
 */
export function parseSessionName(
	sessionName: string,
): { type: SessionType; beadId: string } | undefined {
	// Validate beadId pattern (prefix-suffix pattern like "az-bqzy")
	const beadIdPattern = /^[a-z]+-[a-z0-9]+$/i

	// First try if it's a direct beadId session
	if (beadIdPattern.test(sessionName)) {
		return { type: "bead", beadId: sessionName }
	}

	// Try each legacy prefix
	for (const [prefix, type] of [
		["claude-", "claude"],
		["opencode-", "opencode"],
		["dev-", "dev"],
		["chat-", "chat"],
	] as const) {
		if (sessionName.startsWith(prefix)) {
			const beadId = sessionName.slice(prefix.length)
			if (beadIdPattern.test(beadId)) {
				return { type, beadId }
			}
		}
	}
	return undefined
}

/**
 * Check if a session type represents an AI tool session (claude or opencode)
 */
export function isAiToolSession(type: SessionType): type is "claude" | "opencode" {
	return type === "claude" || type === "opencode"
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
