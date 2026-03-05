import { describe, expect, it } from "bun:test"
import {
	allocateNextAlphaIssueId,
	encodeAlphaIssueIndex,
	parseBackupTimestampFromFilename,
	resolveLocalIssueStorageRoot,
	selectBackupFilesToPrune,
} from "./LocalIssueStore.js"

describe("resolveLocalIssueStorageRoot", () => {
	it("prefers explicit project path over current project path", () => {
		const resolved = resolveLocalIssueStorageRoot({
			explicitProjectPath: "/repos/selected-project",
			currentProjectPath: "/repos/stale-project",
			fallbackCwd: "/repos/fallback",
		})

		expect(resolved).toBe("/repos/selected-project")
	})

	it("uses current project path when explicit path is not provided", () => {
		const resolved = resolveLocalIssueStorageRoot({
			currentProjectPath: "/repos/current-project",
			fallbackCwd: "/repos/fallback",
		})

		expect(resolved).toBe("/repos/current-project")
	})

	it("falls back to process cwd when no project path is available", () => {
		const resolved = resolveLocalIssueStorageRoot({
			fallbackCwd: "/repos/fallback",
		})

		expect(resolved).toBe("/repos/fallback")
	})
})

describe("parseBackupTimestampFromFilename", () => {
	it("parses valid backup names", () => {
		const parsed = parseBackupTimestampFromFilename("issues-20260305T120001Z.db")
		expect(parsed).toBeDefined()
	})

	it("ignores non-backup filenames", () => {
		expect(parseBackupTimestampFromFilename("issues.db")).toBeUndefined()
		expect(parseBackupTimestampFromFilename("issues-20260305T120001Z.tmp")).toBeUndefined()
	})
})

describe("selectBackupFilesToPrune", () => {
	it("prunes oldest backups beyond maxBackups", () => {
		const toPrune = selectBackupFilesToPrune(
			[
				"issues-20260301T120000Z.db",
				"issues-20260302T120000Z.db",
				"issues-20260303T120000Z.db",
				"issues-20260304T120000Z.db",
			],
			2,
		)

		expect(toPrune).toEqual(["issues-20260302T120000Z.db", "issues-20260301T120000Z.db"])
	})

	it("ignores non-matching files while pruning", () => {
		const toPrune = selectBackupFilesToPrune(
			["issues-20260301T120000Z.db", "issues-20260302T120000Z.db", "readme.txt"],
			1,
		)

		expect(toPrune).toEqual(["issues-20260301T120000Z.db"])
	})
})

describe("encodeAlphaIssueIndex", () => {
	it("encodes expected bijective base-26 values", () => {
		expect(encodeAlphaIssueIndex(0)).toBe("a")
		expect(encodeAlphaIssueIndex(25)).toBe("z")
		expect(encodeAlphaIssueIndex(26)).toBe("aa")
		expect(encodeAlphaIssueIndex(27)).toBe("ab")
		expect(encodeAlphaIssueIndex(701)).toBe("zz")
		expect(encodeAlphaIssueIndex(702)).toBe("aaa")
	})
})

describe("allocateNextAlphaIssueId", () => {
	it("allocates sequential IDs when no collisions exist", () => {
		expect(allocateNextAlphaIssueId(0, new Set())).toEqual({ issueId: "a", nextIndex: 1 })
		expect(allocateNextAlphaIssueId(1, new Set(["a"]))).toEqual({ issueId: "b", nextIndex: 2 })
	})

	it("skips occupied candidates and advances index", () => {
		const existing = new Set(["a", "b", "c"])
		expect(allocateNextAlphaIssueId(0, existing)).toEqual({ issueId: "d", nextIndex: 4 })
	})
})
