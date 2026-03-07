/**
 * terminalLinks - Extract clickable URLs and file paths from terminal output
 *
 * Inspired by t3code's terminal-links.ts (https://github.com/pingdotgg/t3code).
 * Adapted for TUI context where links are displayed visually rather than opened
 * in a browser/editor.
 *
 * Supports:
 * - HTTP/HTTPS URLs
 * - Absolute and relative file paths (POSIX and Windows)
 * - Tilde-expanded home paths (~/...)
 * - File paths with optional line:column suffixes (e.g. src/foo.ts:10:5)
 */

export type TerminalLinkKind = "url" | "path"

export interface TerminalLinkMatch {
	readonly kind: TerminalLinkKind
	readonly text: string
	readonly start: number
	readonly end: number
}

// ============================================================================
// Patterns
// ============================================================================

const URL_PATTERN = /https?:\/\/[^\s"'`<>]+/g
const FILE_PATH_PATTERN =
	/(?:~\/|\.{1,2}\/|\/|[A-Za-z]:\\|\\\\)[^\s"'`<>]+|[A-Za-z0-9._-]+(?:\/[A-Za-z0-9._-]+)+(?::\d+){0,2}/g

const TRAILING_PUNCTUATION_RE = /[.,;!?]+$/

// ============================================================================
// Helpers
// ============================================================================

function trimClosingDelimiters(value: string): string {
	let output = value.replace(TRAILING_PUNCTUATION_RE, "")
	if (output.length === 0) return output

	const trimUnbalanced = (open: string, close: string): void => {
		while (output.endsWith(close)) {
			const opens = output.split(open).length - 1
			const closes = output.split(close).length - 1
			if (opens >= closes) return
			output = output.slice(0, -1)
		}
	}

	trimUnbalanced("(", ")")
	trimUnbalanced("[", "]")
	trimUnbalanced("{", "}")
	return output
}

function overlaps(
	a: { readonly start: number; readonly end: number },
	b: { readonly start: number; readonly end: number },
): boolean {
	return a.start < b.end && b.start < a.end
}

function collectMatches(
	line: string,
	kind: TerminalLinkKind,
	pattern: RegExp,
	existing: TerminalLinkMatch[],
): TerminalLinkMatch[] {
	const matches: TerminalLinkMatch[] = []
	pattern.lastIndex = 0

	for (const rawMatch of line.matchAll(pattern)) {
		const raw = rawMatch[0]
		const start = rawMatch.index ?? -1
		if (start < 0 || raw.length === 0) continue

		const trimmed = trimClosingDelimiters(raw)
		if (trimmed.length === 0) continue
		if (kind === "path" && /^https?:\/\//i.test(trimmed)) continue

		const candidate: TerminalLinkMatch = {
			kind,
			text: trimmed,
			start,
			end: start + trimmed.length,
		}

		const collides = [...existing, ...matches].some((other) => overlaps(candidate, other))
		if (collides) continue

		matches.push(candidate)
	}

	return matches
}

// ============================================================================
// Public API
// ============================================================================

/**
 * Extract all URL and file-path links from a single line of terminal output.
 *
 * Returns matches sorted by their start position. URL matches take priority
 * over path matches when they overlap.
 *
 * @example
 * ```ts
 * extractTerminalLinks("Error in src/core/foo.ts:42")
 * // → [{ kind: "path", text: "src/core/foo.ts:42", start: 9, end: 27 }]
 *
 * extractTerminalLinks("See https://example.com for details.")
 * // → [{ kind: "url", text: "https://example.com", start: 4, end: 23 }]
 * ```
 */
export function extractTerminalLinks(line: string): TerminalLinkMatch[] {
	const urlMatches = collectMatches(line, "url", URL_PATTERN, [])
	const pathMatches = collectMatches(line, "path", FILE_PATH_PATTERN, urlMatches)
	return [...urlMatches, ...pathMatches].toSorted((a, b) => a.start - b.start)
}

/**
 * Parse optional line and column numbers from a file path string.
 *
 * @example
 * ```ts
 * splitPathAndPosition("src/foo.ts:10:5")
 * // → { path: "src/foo.ts", line: "10", column: "5" }
 *
 * splitPathAndPosition("src/foo.ts:10")
 * // → { path: "src/foo.ts", line: "10", column: undefined }
 *
 * splitPathAndPosition("src/foo.ts")
 * // → { path: "src/foo.ts", line: undefined, column: undefined }
 * ```
 */
export function splitPathAndPosition(value: string): {
	readonly path: string
	readonly line: string | undefined
	readonly column: string | undefined
} {
	let path = value
	let column: string | undefined
	let line: string | undefined

	const columnMatch = path.match(/:(\d+)$/)
	if (!columnMatch?.[1]) {
		return { path, line: undefined, column: undefined }
	}

	column = columnMatch[1]
	path = path.slice(0, -columnMatch[0].length)

	const lineMatch = path.match(/:(\d+)$/)
	if (lineMatch?.[1]) {
		line = lineMatch[1]
		path = path.slice(0, -lineMatch[0].length)
	} else {
		line = column
		column = undefined
	}

	return { path, line, column }
}

/**
 * Check whether a terminal link is a Windows-style absolute path.
 */
export function isWindowsAbsolutePath(value: string): boolean {
	return /^[A-Za-z]:[\\/]/.test(value) || value.startsWith("\\\\")
}

/**
 * Check whether a path string is absolute (POSIX or Windows).
 */
export function isAbsolutePath(value: string): boolean {
	return value.startsWith("/") || isWindowsAbsolutePath(value)
}
