/**
 * Pure path utility functions that don't require Path service
 *
 * Session naming convention: canonical tmux-safe encoding of issueId
 * - Each issue has exactly one tmux session (with backwards-compatible parsing)
 * - Windows within the session handle different concerns (code, dev, etc.)
 */

/**
 * Standard window names for issue sessions
 */
export const WINDOW_NAMES = {
	CODE: "code",
	DEV: "dev",
	CHAT: "chat",
	HX: "hx",
	BACKGROUND: "background",
} as const

/**
 * Dev server window name prefix
 */
export const DEV_WINDOW_PREFIX = "dev-"

/**
 * Generate window name for a dev server
 *
 * @param serverName - The dev server name (e.g., "frontend", "api")
 * @returns Window name like "dev-frontend", "dev-api"
 */
export function getDevWindowName(serverName: string): string {
	return `${DEV_WINDOW_PREFIX}${serverName}`
}

/**
 * Parse a window name to extract dev server name
 *
 * @param windowName - The tmux window name
 * @returns The server name if it's a dev window, undefined otherwise
 */
export function parseDevWindowName(windowName: string): string | undefined {
	if (windowName.startsWith(DEV_WINDOW_PREFIX)) {
		return windowName.slice(DEV_WINDOW_PREFIX.length)
	}
	return undefined
}

const SESSION_ESCAPE_PREFIX = "_x"
const SESSION_ESCAPE_SUFFIX = "_"
const SAFE_SESSION_CHAR_PATTERN = /^[A-Za-z0-9-]$/

const encodeSessionChar = (char: string): string => {
	if (SAFE_SESSION_CHAR_PATTERN.test(char)) {
		return char
	}

	const codepoint = char.codePointAt(0)
	if (codepoint === undefined) {
		return char
	}

	return `${SESSION_ESCAPE_PREFIX}${codepoint.toString(16)}${SESSION_ESCAPE_SUFFIX}`
}

/**
 * Generate canonical tmux session name for an issue.
 *
 * Tmux session names must be shell-safe and cannot rely on raw issue IDs
 * containing punctuation (for example, dots). We keep the mapping bijective
 * by escaping unsupported characters as _x<hex>_ tokens.
 */
export function getIssueSessionName(issueId: string): string {
	return [...issueId].map(encodeSessionChar).join("")
}

/**
 * Decode a canonical tmux issue session name back to the original issue ID.
 */
export function decodeIssueSessionName(sessionName: string): string {
	return sessionName.replace(/_x([0-9a-f]+)_/gi, (_full, hex: string) => {
		const codepoint = Number.parseInt(hex, 16)
		if (!Number.isFinite(codepoint)) {
			return _full
		}

		try {
			return String.fromCodePoint(codepoint)
		} catch {
			return _full
		}
	})
}

/**
 * Session types that can be parsed from tmux session names
 */
export type SessionType = "issue"

/**
 * AI session prefixes used for tmux sessions
 */
export const AI_SESSION_PREFIXES = ["claude-", "opencode-", "codex-"]

const ISSUE_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]*$/
const LINEAR_IDENTIFIER_PATTERN = /^[A-Za-z][A-Za-z0-9]*-[0-9]+$/

const decodeLegacyNormalizedIssueId = (sessionName: string): string | undefined => {
	if (sessionName.includes(".") || !sessionName.includes("_")) {
		return undefined
	}

	const legacyDotIssueId = sessionName.replaceAll("_", ".")
	if (ISSUE_ID_PATTERN.test(legacyDotIssueId)) {
		return legacyDotIssueId
	}

	return undefined
}

/**
 * Parse a session name to extract type and issueId
 *
 * Returns undefined if the session name doesn't match the expected format.
 */
export function parseIssueSessionName(
	sessionName: string,
): { type: SessionType; issueId: string } | undefined {
	const aiPrefix = AI_SESSION_PREFIXES.find((prefix) => sessionName.startsWith(prefix))
	const withoutPrefix = aiPrefix ? sessionName.slice(aiPrefix.length) : sessionName

	if (!withoutPrefix) {
		return undefined
	}

	const issueId = decodeIssueSessionName(withoutPrefix)
	const legacyNormalizedIssueId = decodeLegacyNormalizedIssueId(issueId)
	if (legacyNormalizedIssueId) {
		return { type: "issue", issueId: legacyNormalizedIssueId }
	}

	if (ISSUE_ID_PATTERN.test(issueId)) {
		return { type: "issue", issueId }
	}

	return undefined
}

/**
 * Normalize issue IDs for lookup comparisons.
 *
 * Linear identifiers are case-insensitive in practice (`AZE-123`), so we
 * normalize just that shape. Other issue ID formats preserve original case.
 */
export function normalizeIssueIdForLookup(issueId: string): string {
	if (LINEAR_IDENTIFIER_PATTERN.test(issueId)) {
		return issueId.toUpperCase()
	}

	return issueId
}

/**
 * Compare issue IDs in a lookup-safe way.
 */
export function issueIdsEqualForLookup(left: string, right: string): boolean {
	return normalizeIssueIdForLookup(left) === normalizeIssueIdForLookup(right)
}

/**
 * Compute the worktree path for an issue
 *
 * Worktrees are created as siblings to the project directory:
 * ../ProjectName-issueId/
 *
 * @param projectPath - Absolute path to the project directory
 * @param issueId - The issue ID
 * @returns Absolute path to the worktree directory
 */
export function getWorktreePath(projectPath: string, issueId: string): string {
	const lastSlash = projectPath.lastIndexOf("/")
	const parentDir = projectPath.slice(0, lastSlash)
	const projectName = projectPath.slice(lastSlash + 1)
	return `${parentDir}/${projectName}-${issueId}`
}
