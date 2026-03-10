/**
 * Schema Migration Tests
 *
 * Tests for the config schema migration system that automatically
 * upgrades old config formats to the current version.
 */

import { describe, expect, it } from "bun:test"
import { Schema } from "effect"
import { mergeWithDefaults } from "./defaults.js"
import {
	AZEDARACH_CONFIG_JSON_SCHEMA_URI,
	AzedarachConfigSchema,
	CURRENT_CONFIG_VERSION,
} from "./schema.js"

/**
 * Helper to decode a raw config through the schema
 */
const decodeConfig = (raw: unknown) => Schema.decodeUnknownSync(AzedarachConfigSchema)(raw)
const encodeConfig = (decoded: ReturnType<typeof decodeConfig>) =>
	Schema.encodeSync(AzedarachConfigSchema)(decoded)

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
			expect(resolved.sessionRecovery.retryBaseDelayMs).toBe(1000)
			expect(resolved.sessionRecovery.retryMaxDelayMs).toBe(60000)
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

		it("accepts editor metadata schema URI with explicit $version", () => {
			const result = decodeConfig({
				$schema: AZEDARACH_CONFIG_JSON_SCHEMA_URI,
				$version: CURRENT_CONFIG_VERSION,
				session: { command: "claude" },
			})
			expect(result.$schema).toBe(CURRENT_CONFIG_VERSION)
			expect(result.session?.command).toBe("claude")
		})
	})

	describe("session recovery config", () => {
		it("accepts retry delay overrides", () => {
			const result = decodeConfig({
				sessionRecovery: {
					mode: "auto",
					autoRecoveryDelayMs: 500,
					retryBaseDelayMs: 1500,
					retryMaxDelayMs: 45000,
				},
			})
			const resolved = mergeWithDefaults(result)

			expect(resolved.sessionRecovery.mode).toBe("auto")
			expect(resolved.sessionRecovery.autoRecoveryDelayMs).toBe(500)
			expect(resolved.sessionRecovery.retryBaseDelayMs).toBe(1500)
			expect(resolved.sessionRecovery.retryMaxDelayMs).toBe(45000)
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
			expect(result.pr).toEqual({
				autoDraft: true,
				autoMerge: undefined,
				enabled: undefined,
				aiModel: undefined,
			})
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
		it("migrates legacy tracker.issueTracker=tracker to issueTracker.azedarach", () => {
			const result = decodeConfig({
				$schema: 2,
				tracker: {
					syncEnabled: false,
					issueTracker: "tracker",
				},
			})

			expect(result.issueTracker?.tracker?.syncEnabled).toBe(false)
		})

		it("migrates legacy tracker.issueTracker=legacy to issueTracker.legacy", () => {
			const result = decodeConfig({
				$schema: 2,
				tracker: {
					syncEnabled: false,
					issueTracker: "legacy",
				},
			})

			expect(result.issueTracker?.legacy?.syncEnabled).toBe(false)
		})

		it("migrates v3 flat linear config to nested issueTracker.linear", () => {
			const result = decodeConfig({
				$schema: 3,
				issueTracker: "linear",
				linear: {
					syncEnabled: true,
					command: "linear-cli",
					team: "ENG",
					syncThrottle: {
						maxPerMinute: 90,
						burst: 10,
					},
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
			expect(result.issueTracker?.linear?.syncThrottle?.maxPerMinute).toBe(90)
			expect(result.issueTracker?.linear?.syncThrottle?.burst).toBe(10)
			expect(result.issueTracker?.linear?.webhooks?.url).toBe("https://example.ngrok.app")
		})

		it("preserves nested linear syncThrottle during v2 migration path", () => {
			const result = decodeConfig({
				$schema: 2,
				issueTracker: {
					linear: {
						syncEnabled: true,
						syncThrottle: {
							maxPerMinute: 120,
							burst: 20,
						},
					},
				},
			})

			expect(result.issueTracker?.linear?.syncEnabled).toBe(true)
			expect(result.issueTracker?.linear?.syncThrottle?.maxPerMinute).toBe(120)
			expect(result.issueTracker?.linear?.syncThrottle?.burst).toBe(20)
		})
	})

	describe("v4 → v5 migration: merge.startClaudeOnFailure rename", () => {
		it("migrates merge.startClaudeOnFailure to merge.startAiSessionOnFailure", () => {
			const result = decodeConfig({
				$schema: 4,
				merge: {
					validateCommands: ["bun run type-check"],
					startClaudeOnFailure: false,
				},
			})

			expect(result.$schema).toBe(CURRENT_CONFIG_VERSION)
			expect(result.merge?.startAiSessionOnFailure).toBe(false)

			const encoded = Schema.encodeSync(AzedarachConfigSchema)(result)
			expect(encoded.merge?.startAiSessionOnFailure).toBe(false)
		})

		it("prefers merge.startAiSessionOnFailure when both keys are present", () => {
			const result = decodeConfig({
				$schema: 4,
				merge: {
					startClaudeOnFailure: false,
					startAiSessionOnFailure: true,
				},
			})

			expect(result.merge?.startAiSessionOnFailure).toBe(true)
		})
	})

	describe("v5 → v6 migration: git-scoped workflow aliases", () => {
		it("migrates git.pr and git.merge to canonical workflow config", () => {
			const result = decodeConfig({
				$schema: 5,
				git: {
					baseBranch: "main",
					pr: {
						enabled: true,
						autoDraft: false,
						aiModel: "gpt-5.3-codex-spark",
					},
					merge: {
						maxFixAttempts: 3,
						startClaudeOnFailure: false,
					},
				},
			})

			expect(result.$schema).toBe(CURRENT_CONFIG_VERSION)
			expect(result.pr?.enabled).toBe(true)
			expect(result.pr?.autoDraft).toBe(false)
			expect(result.pr?.aiModel).toBe("gpt-5.3-codex-spark")
			expect(result.merge?.maxFixAttempts).toBe(3)
			expect(result.merge?.startAiSessionOnFailure).toBe(false)
		})

		it("preserves top-level pr/merge when both top-level and git-scoped values exist", () => {
			const result = decodeConfig({
				$schema: 5,
				pr: {
					enabled: false,
				},
				merge: {
					maxFixAttempts: 1,
				},
				git: {
					pr: {
						enabled: true,
						aiModel: "gpt-5.3-codex-spark",
					},
					merge: {
						maxFixAttempts: 9,
					},
				},
			})

			expect(result.pr?.enabled).toBe(false)
			expect(result.pr?.aiModel).toBe("gpt-5.3-codex-spark")
			expect(result.merge?.maxFixAttempts).toBe(1)
		})

		it("preserves git-scoped pr.aiModel when re-encoding canonical config", () => {
			const decoded = decodeConfig({
				$schema: 5,
				git: {
					pr: {
						aiModel: "gpt-5.3-codex-spark",
					},
				},
			})
			const encoded = encodeConfig(decoded)

			expect(encoded.pr?.aiModel).toBe("gpt-5.3-codex-spark")
		})

		it("preserves git-scoped fallback fields when schema is already v6", () => {
			const decoded = decodeConfig({
				$schema: "./config.schema.json",
				$version: CURRENT_CONFIG_VERSION,
				pr: {
					enabled: true,
				},
				merge: {
					validateCommands: ["bun run type-check"],
				},
				git: {
					pr: {
						aiModel: "gpt-5.3-codex-spark",
					},
					merge: {
						startAiSessionOnFailure: false,
					},
				},
			})
			const encoded = encodeConfig(decoded)

			expect(decoded.pr?.enabled).toBe(true)
			expect(decoded.pr?.aiModel).toBe("gpt-5.3-codex-spark")
			expect(decoded.merge?.startAiSessionOnFailure).toBe(false)
			expect(encoded.pr?.aiModel).toBe("gpt-5.3-codex-spark")
			expect(encoded.git?.pr).toBeUndefined()
			expect(encoded.git?.merge).toBeUndefined()
		})
	})

	describe("v6 → v7 migration: optional spec feature gating", () => {
		it("migrates v6 config forward and defaults spec to enabled", () => {
			const result = decodeConfig({
				$schema: 6,
				git: {
					baseBranch: "main",
				},
			})
			const resolved = mergeWithDefaults(result)

			expect(result.$schema).toBe(CURRENT_CONFIG_VERSION)
			expect(resolved.spec.enabled).toBe(true)
		})

		it("accepts explicit spec.enabled false and preserves it when encoding", () => {
			const decoded = decodeConfig({
				spec: {
					enabled: false,
				},
			})
			const encoded = encodeConfig(decoded)

			expect(decoded.spec?.enabled).toBe(false)
			expect(encoded.spec?.enabled).toBe(false)
		})
	})

	describe("v4 issueTracker shape", () => {
		it("accepts nested tracker config", () => {
			const result = decodeConfig({
				issueTracker: {
					tracker: { syncEnabled: false },
				},
			})

			expect(result.issueTracker?.tracker?.syncEnabled).toBe(false)
		})

		it("accepts nested legacy config", () => {
			const result = decodeConfig({
				issueTracker: {
					legacy: { syncEnabled: true },
				},
			})

			expect(result.issueTracker?.legacy?.syncEnabled).toBe(true)
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
						tracker: { syncEnabled: true },
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
					issueTracker: "tracker",
					legacy: { syncEnabled: true },
				}),
			).toThrow(/issueTracker='tracker' does not match backend block/)
		})

		it("rejects multiple backend blocks", () => {
			expect(() =>
				decodeConfig({
					issueTracker: "legacy",
					legacy: { syncEnabled: true },
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
						default: "gpt-5.3-codex-spark",
						chat: "gpt-5-mini",
					},
				},
			})
			const resolved = mergeWithDefaults(result)

			expect(resolved.cliTool).toBe("codex")
			expect(resolved.model.codex.default).toBe("gpt-5.3-codex-spark")
			expect(resolved.model.codex.chat).toBe("gpt-5-mini")
		})

		it("accepts pr.aiModel override", () => {
			const result = decodeConfig({
				pr: {
					enabled: true,
					aiModel: "gpt-5.3-codex-spark",
				},
			})
			const resolved = mergeWithDefaults(result)

			expect(resolved.pr.aiModel).toBe("gpt-5.3-codex-spark")
		})

		it("preserves pr.aiModel when re-encoding config for disk", () => {
			const decoded = decodeConfig({
				pr: {
					enabled: true,
					aiModel: "gpt-5.3-codex-spark",
				},
			})
			const encoded = encodeConfig(decoded)

			expect(encoded.pr?.aiModel).toBe("gpt-5.3-codex-spark")
			expect(encoded.pr?.enabled).toBe(true)
		})

		it("accepts gpt-5.4 model literal", () => {
			const result = decodeConfig({
				model: {
					default: "gpt-5.4",
				},
			})
			const resolved = mergeWithDefaults(result)

			expect(resolved.model.default).toBe("gpt-5.4")
		})

		it("preserves worktree config", () => {
			const result = decodeConfig({
				worktree: {
					initCommands: ["direnv allow", "bun install"],
					continueOnFailure: false,
				},
				issueTracker: {
					legacy: { syncEnabled: true },
				},
			})

			expect(result.worktree?.initCommands).toEqual(["direnv allow", "bun install"])
			expect(result.worktree?.continueOnFailure).toBe(false)
		})

		it("preserves projects array", () => {
			const result = decodeConfig({
				projects: [
					{ name: "project1", path: "/path/to/project1" },
					{ name: "project2", path: "/path/to/project2", issueStorePath: "/custom/tracker" },
				],
				defaultProject: "project1",
				issueTracker: {
					tracker: { syncEnabled: true },
				},
			})

			expect(result.projects).toHaveLength(2)
			expect(result.projects?.[0]).toEqual({ name: "project1", path: "/path/to/project1" })
			expect(result.projects?.[1]?.issueStorePath).toBe("/custom/tracker")
			expect(result.defaultProject).toBe("project1")
		})
	})

	describe("encoding", () => {
		it("encodes current config with editor metadata and explicit version", () => {
			const config = {
				$schema: CURRENT_CONFIG_VERSION,
				issueTracker: {
					legacy: { syncEnabled: true },
				},
				git: { baseBranch: "main" },
				pr: { autoDraft: true },
			}

			const encoded = Schema.encodeSync(AzedarachConfigSchema)(config)
			const encodedIssueTracker = encoded.issueTracker

			expect(encoded.$schema).toBe(AZEDARACH_CONFIG_JSON_SCHEMA_URI)
			expect(encoded.$version).toBe(CURRENT_CONFIG_VERSION)
			if (
				encodedIssueTracker === undefined ||
				typeof encodedIssueTracker !== "object" ||
				encodedIssueTracker === null
			) {
				throw new Error("Expected encoded issueTracker object")
			}
			expect(encodedIssueTracker.legacy?.syncEnabled).toBe(true)
			expect(encoded.git?.baseBranch).toBe("main")
			expect(encoded.pr?.autoDraft).toBe(true)
		})

		it("encodes migrated config from resolved defaults", () => {
			const decoded = decodeConfig({
				pr: {
					baseBranch: "develop",
				},
			})
			const resolved = mergeWithDefaults(decoded)
			const encoded = encodeConfig(resolved)

			expect(encoded.$schema).toBe(AZEDARACH_CONFIG_JSON_SCHEMA_URI)
			expect(encoded.$version).toBe(CURRENT_CONFIG_VERSION)
			expect(encoded.git?.baseBranch).toBe("develop")
			expect(encoded.session?.shell).toBeDefined()
			const encodedIssueTracker = encoded.issueTracker
			if (
				encodedIssueTracker === undefined ||
				typeof encodedIssueTracker !== "object" ||
				encodedIssueTracker === null
			) {
				throw new Error("Expected encoded issueTracker object")
			}
			expect(encodedIssueTracker.local?.syncEnabled).toBe(false)
		})
	})
})
