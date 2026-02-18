/**
 * Pure path utility functions that don't require Path service
 *
 * Session naming convention: canonical tmux-safe encoding of beadId
 * - Each bead has exactly one tmux session (with backwards-compatible parsing)
 * - Windows within the session handle different concerns (code, dev, etc.)
 */

/**
 * Standard window names for bead sessions
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
 * Generate canonical tmux session name for a bead.
 *
 * Tmux session names must be shell-safe and cannot rely on raw bead IDs
 * containing punctuation (for example, dots). We keep the mapping bijective
 * by escaping unsupported characters as _x<hex>_ tokens.
 */
export function getBeadSessionName(beadId: string): string {
	return [...beadId].map(encodeSessionChar).join("")
}

/**
 * Decode a canonical tmux bead session name back to the original bead ID.
 */
export function decodeBeadSessionName(sessionName: string): string {
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
export type SessionType = "bead"

/**
 * AI session prefixes used for tmux sessions
 */
export const AI_SESSION_PREFIXES = ["claude-", "opencode-"]

const BEAD_ID_PATTERN = /^[A-Za-z0-9]+[.-][A-Za-z0-9._-]+$/

const decodeLegacyNormalizedBeadId = (sessionName: string): string | undefined => {
	if (sessionName.includes(".") || !sessionName.includes("_")) {
		return undefined
	}

	const legacyDotBeadId = sessionName.replaceAll("_", ".")
	if (BEAD_ID_PATTERN.test(legacyDotBeadId)) {
		return legacyDotBeadId
	}

	return undefined
}

/**
 * Parse a session name to extract type and beadId
 *
 * Returns undefined if the session name doesn't match the expected format.
 */
export function parseSessionName(
	sessionName: string,
): { type: SessionType; beadId: string } | undefined {
	const aiPrefix = AI_SESSION_PREFIXES.find((prefix) => sessionName.startsWith(prefix))
	const withoutPrefix = aiPrefix ? sessionName.slice(aiPrefix.length) : sessionName

	if (!withoutPrefix) {
		return undefined
	}

	const beadId = decodeBeadSessionName(withoutPrefix)
	const legacyNormalizedBeadId = decodeLegacyNormalizedBeadId(beadId)
	if (legacyNormalizedBeadId) {
		return { type: "bead", beadId: legacyNormalizedBeadId }
	}

	if (BEAD_ID_PATTERN.test(beadId)) {
		return { type: "bead", beadId }
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
