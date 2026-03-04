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
		it("sets $schema to current for empty config", () => {
			const result = decodeConfig({})
			expect(result.$schema).toBe(CURRENT_CONFIG_VERSION)
			expect(result.issueTracker).toBe("br")
			expect(result.beads_rust?.syncEnabled).toBe(true)
		})

		it("sets $schema to current for v1 config (legacy)", () => {
			const result = decodeConfig({ $schema: 1 })
			expect(result.$schema).toBe(CURRENT_CONFIG_VERSION)
		})

		it("handles config with no version field (legacy)", () => {
			const result = decodeConfig({
				session: { command: "claude" },
			})
			expect(result.$schema).toBe(CURRENT_CONFIG_VERSION)
			expect(result.session?.command).toBe("claude")
		})
	})

	describe("v1 → v2 migration: pr.baseBranch → git.baseBranch", () => {
		it("migrates pr.baseBranch to git.baseBranch", () => {
			const result = decodeConfig({
				pr: {
					baseBranch: "develop",
					autoDraft: true,
				},
			})

			expect(result.git?.baseBranch).toBe("develop")
			expect(result.pr).toEqual({ autoDraft: true, autoMerge: undefined, enabled: undefined })
		})

		it("does not overwrite existing git.baseBranch", () => {
			const result = decodeConfig({
				git: { baseBranch: "main" },
				pr: { baseBranch: "develop" },
			})

			expect(result.git?.baseBranch).toBe("main")
		})
	})

	describe("v2 → v3 migration: top-level issueTracker", () => {
		it("migrates legacy beads.issueTracker=bd to top-level issueTracker + beads block", () => {
			const result = decodeConfig({
				$schema: 2,
				beads: {
					syncEnabled: false,
					issueTracker: "bd",
				},
			})

			expect(result.issueTracker).toBe("bd")
			expect(result.beads?.syncEnabled).toBe(false)
			expect(result.beads_rust).toBeUndefined()
			expect(result.linear).toBeUndefined()
		})

		it("migrates legacy beads.issueTracker=br to top-level issueTracker + beads_rust block", () => {
			const result = decodeConfig({
				$schema: 2,
				beads: {
					syncEnabled: false,
					issueTracker: "br",
				},
			})

			expect(result.issueTracker).toBe("br")
			expect(result.beads_rust?.syncEnabled).toBe(false)
			expect(result.beads).toBeUndefined()
			expect(result.linear).toBeUndefined()
		})

		it("accepts new bd config shape", () => {
			const result = decodeConfig({
				issueTracker: "bd",
				beads: { syncEnabled: false },
			})

			expect(result.issueTracker).toBe("bd")
			expect(result.beads?.syncEnabled).toBe(false)
		})

		it("accepts new br config shape", () => {
			const result = decodeConfig({
				issueTracker: "br",
				beads_rust: { syncEnabled: true },
			})

			expect(result.issueTracker).toBe("br")
			expect(result.beads_rust?.syncEnabled).toBe(true)
		})

		it("accepts new linear config shape", () => {
			const result = decodeConfig({
				issueTracker: "linear",
				linear: {
					syncEnabled: true,
					command: "linear-cli",
					team: "ENG",
				},
			})

			expect(result.issueTracker).toBe("linear")
			expect(result.linear?.team).toBe("ENG")
			expect(result.linear?.command).toBe("linear-cli")
		})

		it("rejects mismatched issueTracker/backend block", () => {
			expect(() =>
				decodeConfig({
					issueTracker: "bd",
					beads_rust: { syncEnabled: true },
				}),
			).toThrow(/issueTracker='bd' does not match backend block/)
		})

		it("rejects multiple backend blocks", () => {
			expect(() =>
				decodeConfig({
					issueTracker: "br",
					beads_rust: { syncEnabled: true },
					linear: { syncEnabled: true },
				}),
			).toThrow(/only one issue backend block is allowed/)
		})

		it("rejects issueTracker without backend block", () => {
			expect(() =>
				decodeConfig({
					issueTracker: "linear",
				}),
			).toThrow(/requires matching backend block/)
		})
	})

	describe("passthrough of other config sections", () => {
		it("preserves worktree config", () => {
			const result = decodeConfig({
				worktree: {
					initCommands: ["direnv allow", "bun install"],
					continueOnFailure: false,
				},
				issueTracker: "br",
				beads_rust: { syncEnabled: true },
			})

			expect(result.worktree?.initCommands).toEqual(["direnv allow", "bun install"])
			expect(result.worktree?.continueOnFailure).toBe(false)
		})

		it("preserves projects array", () => {
			const result = decodeConfig({
				projects: [
					{ name: "project1", path: "/path/to/project1" },
					{ name: "project2", path: "/path/to/project2", beadsPath: "/custom/beads" },
				],
				defaultProject: "project1",
				issueTracker: "bd",
				beads: { syncEnabled: true },
			})

			expect(result.projects).toHaveLength(2)
			expect(result.projects?.[0]).toEqual({ name: "project1", path: "/path/to/project1" })
			expect(result.projects?.[1]?.beadsPath).toBe("/custom/beads")
			expect(result.defaultProject).toBe("project1")
		})
	})

	describe("encoding", () => {
		it("encodes current config with version", () => {
			const issueTracker: "br" = "br"
			const config = {
				$schema: CURRENT_CONFIG_VERSION,
				issueTracker,
				beads_rust: { syncEnabled: true },
				git: { baseBranch: "main" },
				pr: { autoDraft: true },
			}

			const encoded = Schema.encodeSync(AzedarachConfigSchema)(config)

			expect(encoded.$schema).toBe(CURRENT_CONFIG_VERSION)
			expect(encoded.issueTracker).toBe("br")
			expect(encoded.git?.baseBranch).toBe("main")
		})
	})
})
