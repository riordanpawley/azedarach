/**
 * Schema Migration Tests
 *
 * Tests for the config schema migration system that automatically
 * upgrades old config formats to the current version.
 */

import { describe, expect, it } from "bun:test"
import { Schema } from "effect"
import { mergeWithDefaults } from "./defaults.js"
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
			expect(result.issueTracker?.local?.syncEnabled).toBe(false)
			const resolved = mergeWithDefaults(result)
			expect("local" in resolved.issueTracker && resolved.issueTracker.local.backups.enabled).toBe(
				true,
			)
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

		it("preserves pr.enabled while removing legacy baseBranch", () => {
			const result = decodeConfig({
				pr: {
					enabled: false,
					baseBranch: "develop",
				},
			})

			expect(result.git?.baseBranch).toBe("develop")
			expect(result.pr?.enabled).toBe(false)
		})

		it("does not overwrite existing git.baseBranch", () => {
			const result = decodeConfig({
				git: { baseBranch: "main" },
				pr: { baseBranch: "develop" },
			})

			expect(result.git?.baseBranch).toBe("main")
		})
	})

	describe("v2/v3 → v4 migration: nested issueTracker config", () => {
		it("migrates legacy beads.issueTracker=bd to issueTracker.beads", () => {
			const result = decodeConfig({
				$schema: 2,
				beads: {
					syncEnabled: false,
					issueTracker: "bd",
				},
			})

			expect(result.issueTracker?.beads?.syncEnabled).toBe(false)
		})

		it("migrates legacy beads.issueTracker=br to issueTracker.beads_rust", () => {
			const result = decodeConfig({
				$schema: 2,
				beads: {
					syncEnabled: false,
					issueTracker: "br",
				},
			})

			expect(result.issueTracker?.beads_rust?.syncEnabled).toBe(false)
		})

		it("migrates v3 flat linear config to nested issueTracker.linear", () => {
			const result = decodeConfig({
				$schema: 3,
				issueTracker: "linear",
				linear: {
					syncEnabled: true,
					command: "linear-cli",
					team: "ENG",
					webhooks: {
						enabled: true,
						url: "https://example.ngrok.app",
						port: 9000,
						events: ["Issue"],
					},
				},
			})

			expect(result.issueTracker?.linear?.syncEnabled).toBe(true)
			expect(result.issueTracker?.linear?.team).toBe("ENG")
			expect(result.issueTracker?.linear?.command).toBe("linear-cli")
			expect(result.issueTracker?.linear?.webhooks?.url).toBe("https://example.ngrok.app")
		})
	})

	describe("v4 issueTracker shape", () => {
		it("accepts nested beads config", () => {
			const result = decodeConfig({
				issueTracker: {
					beads: { syncEnabled: false },
				},
			})

			expect(result.issueTracker?.beads?.syncEnabled).toBe(false)
		})

		it("accepts nested beads_rust config", () => {
			const result = decodeConfig({
				issueTracker: {
					beads_rust: { syncEnabled: true },
				},
			})

			expect(result.issueTracker?.beads_rust?.syncEnabled).toBe(true)
		})

		it("accepts nested linear config", () => {
			const result = decodeConfig({
				issueTracker: {
					linear: {
						syncEnabled: true,
						command: "linear-cli",
						team: "ENG",
					},
				},
			})

			expect(result.issueTracker?.linear?.team).toBe("ENG")
			expect(result.issueTracker?.linear?.command).toBe("linear-cli")
		})

		it("accepts nested linear webhook config", () => {
			const result = decodeConfig({
				issueTracker: {
					linear: {
						team: "AZE",
						webhooks: {
							enabled: true,
							transport: "cli",
							url: "https://example.ngrok.app",
							port: 9010,
							events: ["Issue", "Comment"],
							secret: "lin_wh_secret",
						},
					},
				},
			})

			expect(result.issueTracker?.linear?.team).toBe("AZE")
			expect(result.issueTracker?.linear?.webhooks?.enabled).toBe(true)
			expect(result.issueTracker?.linear?.webhooks?.transport).toBe("cli")
			expect(result.issueTracker?.linear?.webhooks?.url).toBe("https://example.ngrok.app")
			expect(result.issueTracker?.linear?.webhooks?.port).toBe(9010)
			expect(result.issueTracker?.linear?.webhooks?.events).toEqual(["Issue", "Comment"])
			expect(result.issueTracker?.linear?.webhooks?.secret).toBe("lin_wh_secret")
		})

		it("accepts nested local config", () => {
			const result = decodeConfig({
				issueTracker: {
					local: {
						syncEnabled: false,
					},
				},
			})

			expect(result.issueTracker?.local?.syncEnabled).toBe(false)
		})

		it("accepts nested local backup config", () => {
			const result = decodeConfig({
				issueTracker: {
					local: {
						syncEnabled: false,
						backups: {
							enabled: true,
							intervalMinutes: 15,
							writeCooldownSeconds: 45,
							maxBackups: 12,
							directory: ".azedarach/snapshots",
						},
					},
				},
			})
			const resolved = mergeWithDefaults(result)

			expect("local" in resolved.issueTracker && resolved.issueTracker.local.backups.enabled).toBe(
				true,
			)
			expect(
				"local" in resolved.issueTracker && resolved.issueTracker.local.backups.intervalMinutes,
			).toBe(15)
			expect(
				"local" in resolved.issueTracker &&
					resolved.issueTracker.local.backups.writeCooldownSeconds,
			).toBe(45)
			expect(
				"local" in resolved.issueTracker && resolved.issueTracker.local.backups.maxBackups,
			).toBe(12)
			expect(
				"local" in resolved.issueTracker && resolved.issueTracker.local.backups.directory,
			).toBe(".azedarach/snapshots")
		})

		it("uses sdk webhook transport by default", () => {
			const decoded = decodeConfig({
				issueTracker: {
					linear: {
						team: "AZE",
						webhooks: {
							enabled: true,
						},
					},
				},
			})
			const result = mergeWithDefaults(decoded)

			expect("linear" in result.issueTracker && result.issueTracker.linear.webhooks.transport).toBe(
				"sdk",
			)
		})

		it("rejects nested issueTracker with multiple backend blocks", () => {
			expect(() =>
				decodeConfig({
					issueTracker: {
						beads: { syncEnabled: true },
						linear: { syncEnabled: true },
					},
				}),
			).toThrow(/Predicate refinement failure/)
		})
	})

	describe("legacy v3 shape validation", () => {
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

		it("accepts issueTracker literal without backend block and synthesizes linear defaults", () => {
			const result = decodeConfig({
				$schema: 2,
				issueTracker: "linear",
			})

			expect(result.issueTracker?.linear?.syncEnabled).toBe(true)
		})

		it("accepts issueTracker literal without backend block and synthesizes local defaults", () => {
			const result = decodeConfig({
				$schema: 2,
				issueTracker: "local",
			})

			expect(result.issueTracker?.local?.syncEnabled).toBe(false)
		})
	})

	describe("passthrough of other config sections", () => {
		it("accepts codex as cliTool with codex model overrides", () => {
			const result = decodeConfig({
				cliTool: "codex",
				model: {
					codex: {
						default: "gpt-5-codex",
						chat: "gpt-5-mini",
					},
				},
			})
			const resolved = mergeWithDefaults(result)

			expect(resolved.cliTool).toBe("codex")
			expect(resolved.model.codex.default).toBe("gpt-5-codex")
			expect(resolved.model.codex.chat).toBe("gpt-5-mini")
		})

		it("preserves worktree config", () => {
			const result = decodeConfig({
				worktree: {
					initCommands: ["direnv allow", "bun install"],
					continueOnFailure: false,
				},
				issueTracker: {
					beads_rust: { syncEnabled: true },
				},
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
				issueTracker: {
					beads: { syncEnabled: true },
				},
			})

			expect(result.projects).toHaveLength(2)
			expect(result.projects?.[0]).toEqual({ name: "project1", path: "/path/to/project1" })
			expect(result.projects?.[1]?.beadsPath).toBe("/custom/beads")
			expect(result.defaultProject).toBe("project1")
		})
	})

	describe("encoding", () => {
		it("encodes current config with version", () => {
			const config = {
				$schema: CURRENT_CONFIG_VERSION,
				issueTracker: {
					beads_rust: { syncEnabled: true },
				},
				git: { baseBranch: "main" },
				pr: { autoDraft: true },
			}

			const encoded = Schema.encodeSync(AzedarachConfigSchema)(config)
			const encodedIssueTracker = encoded.issueTracker

			expect(encoded.$schema).toBe(CURRENT_CONFIG_VERSION)
			if (
				encodedIssueTracker === undefined ||
				typeof encodedIssueTracker !== "object" ||
				encodedIssueTracker === null
			) {
				throw new Error("Expected encoded issueTracker object")
			}
			expect(encodedIssueTracker.beads_rust?.syncEnabled).toBe(true)
			expect(encoded.git?.baseBranch).toBe("main")
		})
	})
})
