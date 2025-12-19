/**
 * Schema Migration Tests
 *
 * Tests for the config schema migration system that automatically
 * upgrades old config formats to the current version.
 */

import { describe, expect, it } from "bun:test"
import { Schema } from "effect"
import { AzedarachConfigSchema, CURRENT_CONFIG_VERSION } from "./schema.js"

/**
 * Helper to decode a raw config through the schema
 */
const decodeConfig = (raw: unknown) => Schema.decodeUnknownSync(AzedarachConfigSchema)(raw)

describe("AzedarachConfigSchema", () => {
	describe("version handling", () => {
		it("sets configVersion to current for empty config", () => {
			const result = decodeConfig({})
			expect(result.configVersion).toBe(CURRENT_CONFIG_VERSION)
		})

		it("sets configVersion to current for v0 config", () => {
			const result = decodeConfig({ configVersion: 0 })
			expect(result.configVersion).toBe(CURRENT_CONFIG_VERSION)
		})

		it("preserves configVersion for current version config", () => {
			const result = decodeConfig({ configVersion: CURRENT_CONFIG_VERSION })
			expect(result.configVersion).toBe(CURRENT_CONFIG_VERSION)
		})

		it("handles config with no version field (legacy)", () => {
			const result = decodeConfig({
				session: { command: "claude" },
			})
			expect(result.configVersion).toBe(CURRENT_CONFIG_VERSION)
			expect(result.session?.command).toBe("claude")
		})
	})

	describe("v0 → v1 migration: pr.baseBranch → git.baseBranch", () => {
		it("migrates pr.baseBranch to git.baseBranch", () => {
			const result = decodeConfig({
				pr: {
					baseBranch: "develop",
					autoDraft: true,
				},
			})

			// baseBranch should be in git section now
			expect(result.git?.baseBranch).toBe("develop")
			// pr section should NOT have baseBranch
			expect(result.pr).toEqual({ autoDraft: true, autoMerge: undefined })
		})

		it("does not overwrite existing git.baseBranch", () => {
			const result = decodeConfig({
				git: { baseBranch: "main" },
				pr: { baseBranch: "develop" }, // legacy field should be ignored
			})

			// git.baseBranch should win
			expect(result.git?.baseBranch).toBe("main")
		})

		it("handles config with only git.baseBranch (no legacy)", () => {
			const result = decodeConfig({
				configVersion: 1,
				git: { baseBranch: "release" },
			})

			expect(result.git?.baseBranch).toBe("release")
		})

		it("strips baseBranch from pr section in output", () => {
			const result = decodeConfig({
				pr: {
					autoDraft: false,
					autoMerge: true,
					baseBranch: "legacy-value",
				},
			})

			// pr should only have autoDraft and autoMerge
			expect(result.pr).toEqual({ autoDraft: false, autoMerge: true })
			// baseBranch should NOT be in pr
			expect("baseBranch" in (result.pr ?? {})).toBe(false)
		})
	})

	describe("passthrough of other config sections", () => {
		it("preserves worktree config", () => {
			const result = decodeConfig({
				worktree: {
					initCommands: ["direnv allow", "bun install"],
					continueOnFailure: false,
				},
			})

			expect(result.worktree?.initCommands).toEqual(["direnv allow", "bun install"])
			expect(result.worktree?.continueOnFailure).toBe(false)
		})

		it("preserves session config", () => {
			const result = decodeConfig({
				session: {
					command: "claude --model opus",
					shell: "/bin/zsh",
					tmuxPrefix: "C-b",
				},
			})

			expect(result.session?.command).toBe("claude --model opus")
			expect(result.session?.shell).toBe("/bin/zsh")
			expect(result.session?.tmuxPrefix).toBe("C-b")
		})

		it("preserves merge config", () => {
			const result = decodeConfig({
				merge: {
					validateCommands: ["bun run test", "bun run build"],
					fixCommand: "bun run fix",
					maxFixAttempts: 3,
				},
			})

			expect(result.merge?.validateCommands).toEqual(["bun run test", "bun run build"])
			expect(result.merge?.fixCommand).toBe("bun run fix")
			expect(result.merge?.maxFixAttempts).toBe(3)
		})

		it("preserves notifications config", () => {
			const result = decodeConfig({
				notifications: {
					bell: false,
					system: true,
				},
			})

			expect(result.notifications?.bell).toBe(false)
			expect(result.notifications?.system).toBe(true)
		})

		it("preserves projects array", () => {
			const result = decodeConfig({
				projects: [
					{ name: "project1", path: "/path/to/project1" },
					{ name: "project2", path: "/path/to/project2", beadsPath: "/custom/beads" },
				],
				defaultProject: "project1",
			})

			expect(result.projects).toHaveLength(2)
			expect(result.projects?.[0]).toEqual({ name: "project1", path: "/path/to/project1" })
			expect(result.projects?.[1]?.beadsPath).toBe("/custom/beads")
			expect(result.defaultProject).toBe("project1")
		})
	})

	describe("complex migration scenarios", () => {
		it("handles full v0 config with all sections", () => {
			const result = decodeConfig({
				worktree: { initCommands: ["npm install"] },
				session: { command: "claude" },
				git: { remote: "upstream", branchPrefix: "feature-" },
				pr: {
					baseBranch: "develop", // legacy
					autoDraft: false,
					autoMerge: true,
				},
				merge: { validateCommands: ["npm test"] },
				notifications: { bell: true },
			})

			// Migration should move baseBranch to git
			expect(result.git?.baseBranch).toBe("develop")
			expect(result.git?.remote).toBe("upstream")
			expect(result.git?.branchPrefix).toBe("feature-")

			// pr should not have baseBranch
			expect(result.pr).toEqual({ autoDraft: false, autoMerge: true })

			// Other sections should pass through
			expect(result.worktree?.initCommands).toEqual(["npm install"])
			expect(result.session?.command).toBe("claude")
			expect(result.merge?.validateCommands).toEqual(["npm test"])
			expect(result.notifications?.bell).toBe(true)

			// Version should be current
			expect(result.configVersion).toBe(CURRENT_CONFIG_VERSION)
		})

		it("handles already-migrated v1 config cleanly", () => {
			const v1Config = {
				configVersion: 1,
				git: { baseBranch: "main", remote: "origin" },
				pr: { autoDraft: true },
				session: { command: "claude" },
			}

			const result = decodeConfig(v1Config)

			expect(result.configVersion).toBe(1)
			expect(result.git?.baseBranch).toBe("main")
			expect(result.git?.remote).toBe("origin")
			expect(result.pr).toEqual({ autoDraft: true, autoMerge: undefined })
		})
	})

	describe("encoding", () => {
		it("encodes current config with version", () => {
			const config = {
				configVersion: 1,
				git: { baseBranch: "main" },
				pr: { autoDraft: true },
			}

			const encoded = Schema.encodeSync(AzedarachConfigSchema)(config)

			expect(encoded.configVersion).toBe(CURRENT_CONFIG_VERSION)
			expect(encoded.git?.baseBranch).toBe("main")
		})
	})
})
