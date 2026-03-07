import { afterEach, beforeEach, describe, expect, it } from "bun:test"
import fs from "node:fs/promises"
import os from "node:os"
import path from "node:path"
import { invalidateWorkspaceCache, searchWorkspaceEntries } from "./workspaceSearch.js"

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function makeTmpDir(): Promise<string> {
	return fs.mkdtemp(path.join(os.tmpdir(), "az-workspace-test-"))
}

async function writeFile(dir: string, relPath: string, content = ""): Promise<void> {
	const abs = path.join(dir, relPath)
	await fs.mkdir(path.dirname(abs), { recursive: true })
	await fs.writeFile(abs, content)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("searchWorkspaceEntries (filesystem fallback)", () => {
	let tmpDir: string

	beforeEach(async () => {
		tmpDir = await makeTmpDir()
		// Create a simple workspace structure
		await writeFile(tmpDir, "src/main.ts")
		await writeFile(tmpDir, "src/utils/helpers.ts")
		await writeFile(tmpDir, "tsconfig.json")
		await writeFile(tmpDir, "package.json")
		await writeFile(tmpDir, "README.md")
		// Should be ignored
		await writeFile(tmpDir, "node_modules/dep/index.js")
		await writeFile(tmpDir, "dist/bundle.js")
	})

	afterEach(async () => {
		invalidateWorkspaceCache(tmpDir)
		await fs.rm(tmpDir, { recursive: true, force: true })
	})

	it("returns files matching a query substring", async () => {
		const { entries } = await searchWorkspaceEntries({
			cwd: tmpDir,
			query: "ts",
			limit: 20,
		})
		const paths = entries.map((e) => e.path)
		expect(paths.some((p) => p.includes("main.ts"))).toBe(true)
		expect(paths.some((p) => p.includes("tsconfig.json"))).toBe(true)
	})

	it("omits entries from ignored directories", async () => {
		const { entries } = await searchWorkspaceEntries({
			cwd: tmpDir,
			query: "",
			limit: 100,
		})
		const paths = entries.map((e) => e.path)
		expect(paths.some((p) => p.startsWith("node_modules"))).toBe(false)
		expect(paths.some((p) => p.startsWith("dist"))).toBe(false)
	})

	it("returns empty array when query matches nothing", async () => {
		const { entries } = await searchWorkspaceEntries({
			cwd: tmpDir,
			query: "xyzzy_no_match_1234",
			limit: 20,
		})
		expect(entries).toHaveLength(0)
	})

	it("strips leading @ from query for @-mention support", async () => {
		const { entries: withAt } = await searchWorkspaceEntries({
			cwd: tmpDir,
			query: "@tsconfig",
			limit: 10,
		})
		const { entries: withoutAt } = await searchWorkspaceEntries({
			cwd: tmpDir,
			query: "tsconfig",
			limit: 10,
		})
		expect(withAt.map((e) => e.path)).toEqual(withoutAt.map((e) => e.path))
	})

	it("each entry has a correct kind field", async () => {
		const { entries } = await searchWorkspaceEntries({
			cwd: tmpDir,
			query: "",
			limit: 100,
		})
		for (const entry of entries) {
			expect(entry.kind === "file" || entry.kind === "directory").toBe(true)
		}
	})

	it("respects the limit parameter", async () => {
		const { entries } = await searchWorkspaceEntries({
			cwd: tmpDir,
			query: "",
			limit: 2,
		})
		expect(entries.length).toBeLessThanOrEqual(2)
	})

	it("ranks exact basename matches above substring matches", async () => {
		// "main.ts" should rank above "utils/helpers.ts" for query "main"
		const { entries } = await searchWorkspaceEntries({
			cwd: tmpDir,
			query: "main",
			limit: 10,
		})
		const paths = entries.map((e) => e.path)
		const mainIdx = paths.findIndex((p) => p.endsWith("main.ts"))
		// main.ts should appear first (or be in results)
		expect(mainIdx).toBeGreaterThanOrEqual(0)
		// all other results should appear after main.ts
		const otherIdx = paths.findIndex((p) => !p.includes("main"))
		if (otherIdx !== -1) {
			expect(mainIdx).toBeLessThan(otherIdx)
		}
	})

	it("sets parentPath correctly for nested files", async () => {
		const { entries } = await searchWorkspaceEntries({
			cwd: tmpDir,
			query: "helpers",
			limit: 10,
		})
		const helpers = entries.find((e) => e.path.includes("helpers.ts"))
		expect(helpers).toBeDefined()
		expect(helpers?.parentPath).toBe("src/utils")
	})

	it("sets parentPath to undefined for top-level entries", async () => {
		const { entries } = await searchWorkspaceEntries({
			cwd: tmpDir,
			query: "README",
			limit: 10,
		})
		const readme = entries.find((e) => e.path === "README.md")
		expect(readme).toBeDefined()
		expect(readme?.parentPath).toBeUndefined()
	})
})

describe("invalidateWorkspaceCache", () => {
	it("does not throw for unknown paths", () => {
		expect(() => invalidateWorkspaceCache("/no/such/directory")).not.toThrow()
	})
})
