/**
 * Session naming helpers for tmux-safe session encoding/decoding and
 * project-aware issue lookup.
 */

const SESSION_ESCAPE_PREFIX = "_x"
const SESSION_ESCAPE_SUFFIX = "_"
const SAFE_SESSION_CHAR_PATTERN = /^[A-Za-z0-9-]$/
const PROJECT_SESSION_PREFIX_PATTERN = /^[a-z]{2}-/

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
 * Generate short project prefix for tmux session names.
 *
 * Examples:
 * - /Users/user/prog/azedarach -> az
 * - /Users/user/prog/Chefy -> ch
 */
export function getProjectSessionPrefix(projectPath: string): string {
	const lastSlash = projectPath.lastIndexOf("/")
	const projectName = (
		lastSlash >= 0 ? projectPath.slice(lastSlash + 1) : projectPath
	).toLowerCase()
	const lettersOnly = projectName.replace(/[^a-z0-9]/g, "")
	if (lettersOnly.length >= 2) {
		return lettersOnly.slice(0, 2)
	}
	if (lettersOnly.length === 1) {
		return `${lettersOnly}x`
	}
	return "az"
}

/**
 * Generate canonical tmux session name for an issue.
 *
 * Tmux session names must be shell-safe and cannot rely on raw issue IDs
 * containing punctuation (for example, dots). We keep the mapping bijective
 * by escaping unsupported characters as _x<hex>_ tokens.
 */
export function getIssueSessionName(issueId: string, projectPath?: string): string {
	const encoded = [...issueId].map(encodeSessionChar).join("")
	if (!projectPath) {
		return encoded
	}
	return `${getProjectSessionPrefix(projectPath)}-${encoded}`
}

/**
 * Decode a canonical tmux issue session name back to the original issue ID.
 */
export function decodeIssueSessionName(sessionName: string): string {
	return sessionName.replace(/_x([0-9a-f]+)_/gi, (full, hex: string) => {
		const codepoint = Number.parseInt(hex, 16)
		if (!Number.isFinite(codepoint)) {
			return full
		}

		try {
			return String.fromCodePoint(codepoint)
		} catch {
			return full
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
const RESERVED_RUNTIME_ISSUE_IDS = new Set(["az"])

const isRecoverableIssueId = (issueId: string): boolean =>
	ISSUE_ID_PATTERN.test(issueId) && !RESERVED_RUNTIME_ISSUE_IDS.has(issueId.toLowerCase())

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
 * Parse a session name to extract type and issueId.
 *
 * Returns undefined if the session name doesn't match the expected format.
 */
export function parseIssueSessionName(
	sessionName: string,
	projectPath?: string,
): { type: SessionType; issueId: string } | undefined {
	const aiPrefix = AI_SESSION_PREFIXES.find((prefix) => sessionName.startsWith(prefix))
	const withoutPrefix = aiPrefix ? sessionName.slice(aiPrefix.length) : sessionName
	const hasProjectStylePrefix = PROJECT_SESSION_PREFIX_PATTERN.test(withoutPrefix)

	if (!withoutPrefix) {
		return undefined
	}

	if (projectPath) {
		const expectedPrefix = `${getProjectSessionPrefix(projectPath)}-`
		if (withoutPrefix.startsWith(expectedPrefix)) {
			const prefixedIssueId = decodeIssueSessionName(withoutPrefix.slice(expectedPrefix.length))
			if (isRecoverableIssueId(prefixedIssueId)) {
				return { type: "issue", issueId: prefixedIssueId }
			}
		}

		// In a known project context, treat mismatched project-style prefixes as
		// out-of-scope session names instead of stripping the prefix and causing
		// cross-project issue ID collisions (for example `ch-f` -> `f`).
		if (hasProjectStylePrefix) {
			return undefined
		}
	}

	if (hasProjectStylePrefix) {
		const prefixedIssueId = decodeIssueSessionName(withoutPrefix.slice(3))
		if (
			isRecoverableIssueId(prefixedIssueId) &&
			(prefixedIssueId.length === 1 || prefixedIssueId.includes("-"))
		) {
			return { type: "issue", issueId: prefixedIssueId }
		}
	}

	const issueId = decodeIssueSessionName(withoutPrefix)
	const legacyNormalizedIssueId = decodeLegacyNormalizedIssueId(issueId)
	if (legacyNormalizedIssueId && isRecoverableIssueId(legacyNormalizedIssueId)) {
		return { type: "issue", issueId: legacyNormalizedIssueId }
	}

	if (isRecoverableIssueId(issueId)) {
		return { type: "issue", issueId }
	}

	return undefined
}

/**
 * Resolve an issue ID from a tmux session name with optional project context.
 *
 * Parsing with explicit projectPath is preferred because project-prefixed
 * session names (for example `az-ak`) are ambiguous without context.
 *
 * If parsing fails, fallbackIssueId is returned when provided.
 */
export function resolveIssueIdFromSessionName(
	sessionName: string,
	options?: {
		readonly projectPath?: string | null
		readonly fallbackIssueId?: string
	},
): string | undefined {
	const projectPath = options?.projectPath ?? undefined
	if (projectPath) {
		const parsedWithProject = parseIssueSessionName(sessionName, projectPath)
		if (parsedWithProject && parsedWithProject.type === "issue") {
			return parsedWithProject.issueId
		}
	}

	const parsed = parseIssueSessionName(sessionName)
	if (parsed && parsed.type === "issue") {
		return parsed.issueId
	}

	return options?.fallbackIssueId
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
