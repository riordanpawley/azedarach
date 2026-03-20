const SESSION_ESCAPE_PREFIX = "_x"
const SESSION_ESCAPE_SUFFIX = "_"
const SAFE_SESSION_CHAR_PATTERN = /^[A-Za-z0-9-]$/
const PROJECT_SESSION_PREFIX_PATTERN = /^[a-z]{2}-/
const AI_SESSION_PREFIXES = ["claude-", "opencode-", "codex-"] as const
const ISSUE_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]*$/
const LINEAR_IDENTIFIER_PATTERN = /^[A-Za-z][A-Za-z0-9]*-[0-9]+$/
const RESERVED_RUNTIME_ISSUE_IDS = new Set(["az"])

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

const isRecoverableIssueId = (issueId: string): boolean =>
	ISSUE_ID_PATTERN.test(issueId) && !RESERVED_RUNTIME_ISSUE_IDS.has(issueId.toLowerCase())

const decodeLegacyNormalizedIssueId = (sessionName: string): string | undefined => {
	if (sessionName.includes(".") || !sessionName.includes("_")) {
		return undefined
	}

	const legacyDotIssueId = sessionName.replaceAll("_", ".")
	return ISSUE_ID_PATTERN.test(legacyDotIssueId) ? legacyDotIssueId : undefined
}

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

export function getIssueSessionName(issueId: string, projectPath?: string): string {
	const encoded = [...issueId].map(encodeSessionChar).join("")
	if (!projectPath) {
		return encoded
	}
	return `${getProjectSessionPrefix(projectPath)}-${encoded}`
}

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

export function parseIssueSessionName(
	sessionName: string,
	projectPath?: string,
): { readonly type: "issue"; readonly issueId: string } | undefined {
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

export function normalizeIssueIdForLookup(issueId: string): string {
	if (LINEAR_IDENTIFIER_PATTERN.test(issueId)) {
		return issueId.toUpperCase()
	}

	return issueId
}

export function issueIdsEqualForLookup(left: string, right: string): boolean {
	return normalizeIssueIdForLookup(left) === normalizeIssueIdForLookup(right)
}
