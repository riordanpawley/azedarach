/**
 * Pure path utility functions that don't require Path service
 *
 * Session naming convention: [type]-[beadId]
 * - Claude sessions: claude-{beadId}
 * - OpenCode sessions: opencode-{beadId}
 * - Dev servers: dev-{beadId}
 * - Chat sessions: chat-{beadId}
 */

import type { CliToolName } from "./CliToolRegistry.js"

/**
 * Prefix for Claude session names in tmux
 *
 * All Claude Code sessions managed by Azedarach use this prefix.
 * This allows TmuxSessionMonitor to identify Claude sessions and
 * enables hooks to set status on the correct session.
 */
export const CLAUDE_SESSION_PREFIX = "claude-"

/**
 * Prefix for OpenCode session names in tmux
 */
export const OPENCODE_SESSION_PREFIX = "opencode-"

/**
 * Prefix for dev server session names in tmux
 */
export const DEV_SESSION_PREFIX = "dev-"

/**
 * Prefix for chat session names in tmux
 */
export const CHAT_SESSION_PREFIX = "chat-"

/**
 * All AI tool session prefixes for TmuxSessionMonitor to scan
 */
export const AI_SESSION_PREFIXES = [CLAUDE_SESSION_PREFIX, OPENCODE_SESSION_PREFIX] as const

/**
 * Generate tmux session name for a CLI tool session
 *
 * Returns "{tool}-{beadId}" for consistent naming across:
 * - Session creation (ClaudeSessionManager)
 * - Session monitoring (TmuxSessionMonitor)
 * - Hook notifications
 *
 * @param beadId - The bead ID
 * @param cliTool - The CLI tool being used ("claude" or "opencode")
 */
export function getSessionNameForTool(beadId: string, cliTool: CliToolName): string {
	const prefix = cliTool === "opencode" ? OPENCODE_SESSION_PREFIX : CLAUDE_SESSION_PREFIX
	return `${prefix}${beadId}`
}

/**
 * Generate tmux session name for a dev server
 *
 * Returns "dev-{beadId}" for consistent naming.
 */
export function getDevSessionName(beadId: string): string {
	return `${DEV_SESSION_PREFIX}${beadId}`
}

/**
 * Generate tmux session name for a chat session
 *
 * Returns "chat-{beadId}" for consistent naming.
 */
export function getChatSessionName(beadId: string): string {
	return `${CHAT_SESSION_PREFIX}${beadId}`
}

/**
 * Session types that can be parsed from tmux session names
 */
export type SessionType = "claude" | "opencode" | "dev" | "chat"

/**
 * Parse a session name to extract type and beadId
 *
 * Returns undefined if the session name doesn't match the expected format.
 */
export function parseSessionName(
	sessionName: string,
): { type: SessionType; beadId: string } | undefined {
	// Try each prefix
	for (const [prefix, type] of [
		[CLAUDE_SESSION_PREFIX, "claude"],
		[OPENCODE_SESSION_PREFIX, "opencode"],
		[DEV_SESSION_PREFIX, "dev"],
		[CHAT_SESSION_PREFIX, "chat"],
	] as const) {
		if (sessionName.startsWith(prefix)) {
			const beadId = sessionName.slice(prefix.length)
			// Validate beadId looks right (prefix-suffix pattern like "az-bqzy")
			const beadIdPattern = /^[a-z]+-[a-z0-9]+$/i
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
