/**
 * workspaceSearch - Search project files and directories
 *
 * Inspired by t3code's workspaceEntries.ts (https://github.com/pingdotgg/t3code).
 * Adapted for use in azedarach's Bun/Effect.ts environment.
 *
 * Uses `git ls-files` when inside a git repository to obtain a fast, accurate
 * list of tracked and untracked (non-ignored) files. Falls back to a recursive
 * filesystem scan when git is unavailable.
 *
 * Results are cached per working directory for up to 15 seconds to avoid
 * redundant subprocess spawns during rapid keystrokes (e.g. @-mention search).
 */

import fs from "node:fs/promises"
import type { Dirent } from "node:fs"
import path from "node:path"

// ============================================================================
// Constants
// ============================================================================

const CACHE_TTL_MS = 15_000
const CACHE_MAX_KEYS = 4
const INDEX_MAX_ENTRIES = 25_000
const READDIR_CONCURRENCY = 32
const GIT_IGNORE_MAX_STDIN_BYTES = 256 * 1024

const IGNORED_DIRS = new Set([
	".git",
	".convex",
	"node_modules",
	".next",
	".turbo",
	"dist",
	"build",
	"out",
	".cache",
	".clode", // azedarach worktree directory
])

// ============================================================================
// Types
// ============================================================================

export interface WorkspaceEntry {
	/** Relative POSIX path from the workspace root */
	readonly path: string
	/** Whether the entry is a file or directory */
	readonly kind: "file" | "directory"
	/** Parent directory path, or undefined for top-level entries */
	readonly parentPath: string | undefined
}

export interface WorkspaceSearchInput {
	/** Absolute path to the workspace root */
	readonly cwd: string
	/** Search query (empty string returns all entries up to `limit`) */
	readonly query: string
	/** Maximum number of results to return (default: 50) */
	readonly limit?: number
}

export interface WorkspaceSearchResult {
	readonly entries: WorkspaceEntry[]
	/** True when the workspace index was truncated due to the entry limit */
	readonly truncated: boolean
}

// ============================================================================
// Cache
// ============================================================================

interface WorkspaceIndex {
	readonly scannedAt: number
	readonly entries: WorkspaceEntry[]
	readonly truncated: boolean
}

const indexCache = new Map<string, WorkspaceIndex>()
const inFlightBuilds = new Map<string, Promise<WorkspaceIndex>>()

// ============================================================================
// Path helpers
// ============================================================================

function toPosix(input: string): string {
	return input.split(path.sep).join("/")
}

function parentOf(posixPath: string): string | undefined {
	const idx = posixPath.lastIndexOf("/")
	return idx === -1 ? undefined : posixPath.slice(0, idx)
}

function basenameOf(posixPath: string): string {
	const idx = posixPath.lastIndexOf("/")
	return idx === -1 ? posixPath : posixPath.slice(idx + 1)
}

function isInsideIgnored(relativePath: string): boolean {
	const first = relativePath.split("/")[0]
	return first !== undefined && IGNORED_DIRS.has(first)
}

function directoryAncestors(relativePath: string): string[] {
	const parts = relativePath.split("/").filter((s) => s.length > 0)
	if (parts.length <= 1) return []
	return parts.slice(1).map((_, i) => parts.slice(0, i + 1).join("/"))
}

// ============================================================================
// Git helpers — uses Bun.spawn to match the rest of the codebase
// ============================================================================

async function runGit(
	args: string[],
	cwd: string,
	stdin?: string,
): Promise<{ stdout: string; code: number } | null> {
	try {
		const proc = Bun.spawn(["git", ...args], {
			cwd,
			stdin: stdin !== undefined ? Buffer.from(stdin) : "ignore",
			stdout: "pipe",
			stderr: "ignore",
		})

		const [stdoutBuf, exitCode] = await Promise.all([
			new Response(proc.stdout).text(),
			proc.exited,
		])

		return { stdout: stdoutBuf, code: exitCode }
	} catch {
		return null
	}
}

async function isGitWorkTree(cwd: string): Promise<boolean> {
	const result = await runGit(["rev-parse", "--is-inside-work-tree"], cwd)
	return Boolean(result && result.code === 0 && result.stdout.trim() === "true")
}

function splitNulSeparated(input: string): string[] {
	return input.split("\0").filter((s) => s.length > 0)
}

async function filterIgnoredPaths(cwd: string, relativePaths: string[]): Promise<string[]> {
	if (relativePaths.length === 0) return relativePaths

	const ignoredSet = new Set<string>()
	let chunk: string[] = []
	let chunkBytes = 0

	const flush = async (): Promise<boolean> => {
		if (chunk.length === 0) return true
		const result = await runGit(
			["check-ignore", "--no-index", "-z", "--stdin"],
			cwd,
			`${chunk.join("\0")}\0`,
		)
		chunk = []
		chunkBytes = 0

		if (!result) return false
		if (result.code !== 0 && result.code !== 1) return false

		for (const p of splitNulSeparated(result.stdout)) {
			ignoredSet.add(p)
		}
		return true
	}

	for (const p of relativePaths) {
		const bytes = Buffer.byteLength(p) + 1
		if (chunk.length > 0 && chunkBytes + bytes > GIT_IGNORE_MAX_STDIN_BYTES) {
			if (!(await flush())) return relativePaths
		}
		chunk.push(p)
		chunkBytes += bytes
		if (chunkBytes >= GIT_IGNORE_MAX_STDIN_BYTES) {
			if (!(await flush())) return relativePaths
		}
	}

	if (!(await flush())) return relativePaths
	return ignoredSet.size === 0 ? relativePaths : relativePaths.filter((p) => !ignoredSet.has(p))
}

// ============================================================================
// Index builders
// ============================================================================

async function buildIndexFromGit(cwd: string): Promise<WorkspaceIndex | null> {
	if (!(await isGitWorkTree(cwd))) return null

	const listed = await runGit(
		["ls-files", "--cached", "--others", "--exclude-standard", "-z"],
		cwd,
	)
	if (!listed || listed.code !== 0) return null

	const rawPaths = splitNulSeparated(listed.stdout)
		.map((p) => toPosix(p))
		.filter((p) => p.length > 0 && !isInsideIgnored(p))

	const filePaths = await filterIgnoredPaths(cwd, rawPaths)

	const dirSet = new Set<string>()
	for (const fp of filePaths) {
		for (const dir of directoryAncestors(fp)) {
			if (!isInsideIgnored(dir)) dirSet.add(dir)
		}
	}

	const dirEntries: WorkspaceEntry[] = [...dirSet]
		.toSorted((a, b) => a.localeCompare(b))
		.map((p) => ({ path: p, kind: "directory", parentPath: parentOf(p) }))

	const fileEntries: WorkspaceEntry[] = [...new Set(filePaths)]
		.toSorted((a, b) => a.localeCompare(b))
		.map((p) => ({ path: p, kind: "file", parentPath: parentOf(p) }))

	const all = [...dirEntries, ...fileEntries]
	return {
		scannedAt: Date.now(),
		entries: all.slice(0, INDEX_MAX_ENTRIES),
		truncated: all.length > INDEX_MAX_ENTRIES,
	}
}

async function mapConcurrent<T, R>(
	items: T[],
	concurrency: number,
	fn: (item: T) => Promise<R>,
): Promise<R[]> {
	if (items.length === 0) return []
	const results: R[] = new Array(items.length)
	let next = 0
	await Promise.all(
		Array.from({ length: Math.min(concurrency, items.length) }, async () => {
			while (next < items.length) {
				const i = next++
				// biome-ignore lint/style/noNonNullAssertion: index is within bounds (next < items.length)
				results[i] = await fn(items[i]!)
			}
		}),
	)
	return results
}

async function buildIndexFromFs(cwd: string): Promise<WorkspaceIndex> {
	const useGitIgnore = await isGitWorkTree(cwd)
	const entries: WorkspaceEntry[] = []
	let pending: string[] = [""]
	let truncated = false

	while (pending.length > 0 && !truncated) {
		const current = pending
		pending = []

		const reads = await mapConcurrent(current, READDIR_CONCURRENCY, async (relDir) => {
			const absDir = relDir ? path.join(cwd, relDir) : cwd
			try {
				const dirents = await fs.readdir(absDir, { withFileTypes: true })
				return { relDir, dirents }
			} catch {
				return { relDir, dirents: null as Dirent[] | null }
			}
		})

		const allCandidates: { dirent: Dirent; relPath: string }[] = []
		for (const { relDir, dirents } of reads) {
			if (!dirents) continue
			for (const d of dirents.sort((a, b) => a.name.localeCompare(b.name))) {
				if (!d.name || d.name === "." || d.name === "..") continue
				if (d.isDirectory() && IGNORED_DIRS.has(d.name)) continue
				if (!d.isDirectory() && !d.isFile()) continue
				const relPath = toPosix(relDir ? path.join(relDir, d.name) : d.name)
				if (isInsideIgnored(relPath)) continue
				allCandidates.push({ dirent: d, relPath })
			}
		}

		const candidatePaths = allCandidates.map((c) => c.relPath)
		const allowed = useGitIgnore
			? new Set(await filterIgnoredPaths(cwd, candidatePaths))
			: null

		for (const { dirent, relPath } of allCandidates) {
			if (allowed && !allowed.has(relPath)) continue
			entries.push({
				path: relPath,
				kind: dirent.isDirectory() ? "directory" : "file",
				parentPath: parentOf(relPath),
			})
			if (dirent.isDirectory()) pending.push(relPath)
			if (entries.length >= INDEX_MAX_ENTRIES) {
				truncated = true
				break
			}
		}
	}

	return { scannedAt: Date.now(), entries, truncated }
}

async function getIndex(cwd: string): Promise<WorkspaceIndex> {
	const cached = indexCache.get(cwd)
	if (cached && Date.now() - cached.scannedAt < CACHE_TTL_MS) return cached

	const inFlight = inFlightBuilds.get(cwd)
	if (inFlight) return inFlight

	const promise = (async () => {
		const idx = (await buildIndexFromGit(cwd)) ?? (await buildIndexFromFs(cwd))
		indexCache.set(cwd, idx)
		while (indexCache.size > CACHE_MAX_KEYS) {
			const oldest = indexCache.keys().next().value
			if (oldest) indexCache.delete(oldest)
		}
		return idx
	})().finally(() => inFlightBuilds.delete(cwd))

	inFlightBuilds.set(cwd, promise)
	return promise
}

// ============================================================================
// Scoring
// ============================================================================

function normalizeQuery(raw: string): string {
	return raw
		.trim()
		.replace(/^[@./]+/, "")
		.toLowerCase()
}

function scoreEntry(entry: WorkspaceEntry, query: string): number {
	if (!query) return entry.kind === "directory" ? 0 : 1

	const lp = entry.path.toLowerCase()
	const ln = basenameOf(lp)

	if (ln === query) return 0
	if (lp === query) return 1
	if (ln.startsWith(query)) return 2
	if (lp.startsWith(query)) return 3
	if (lp.includes(`/${query}`)) return 4
	return 5
}

// ============================================================================
// Public API
// ============================================================================

/**
 * Search workspace files and directories.
 *
 * Uses `git ls-files` when inside a git repository for speed and accuracy,
 * otherwise falls back to a recursive filesystem scan. Results are cached
 * per `cwd` for {@link CACHE_TTL_MS} ms to support fast incremental queries.
 *
 * @example
 * ```ts
 * const { entries } = await searchWorkspaceEntries({
 *   cwd: "/home/user/project",
 *   query: "@tsconfig",
 *   limit: 10,
 * })
 * // entries[0].path → "tsconfig.json"
 * ```
 */
export async function searchWorkspaceEntries(
	input: WorkspaceSearchInput,
): Promise<WorkspaceSearchResult> {
	const index = await getIndex(input.cwd)
	const limit = input.limit ?? 50
	const q = normalizeQuery(input.query)

	const candidates = q
		? index.entries.filter((e) => e.path.toLowerCase().includes(q))
		: index.entries

	const ranked = candidates.toSorted((a, b) => {
		const delta = scoreEntry(a, q) - scoreEntry(b, q)
		return delta !== 0 ? delta : a.path.localeCompare(b.path)
	})

	return {
		entries: ranked.slice(0, limit),
		truncated: index.truncated || ranked.length > limit,
	}
}

/**
 * Invalidate the cached workspace index for a given directory.
 * Call this when files are created/deleted in the workspace.
 */
export function invalidateWorkspaceCache(cwd: string): void {
	indexCache.delete(cwd)
}
