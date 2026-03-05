import { describe, expect, it } from "bun:test"
import {
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

		expect(toPrune).toEqual([
			"issues-20260302T120000Z.db",
			"issues-20260301T120000Z.db",
		])
	})

	it("ignores non-matching files while pruning", () => {
		const toPrune = selectBackupFilesToPrune(
			[
				"issues-20260301T120000Z.db",
				"issues-20260302T120000Z.db",
				"readme.txt",
			],
			1,
		)

		expect(toPrune).toEqual(["issues-20260301T120000Z.db"])
	})
})
