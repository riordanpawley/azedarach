import { Database } from "bun:sqlite"
import { describe, expect, it } from "bun:test"
import { mkdirSync, mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { BunContext } from "@effect/platform-bun"
import { DateTime, Effect, Layer } from "effect"
import {
	allocateNextAlphaIssueId,
	encodeAlphaIssueIndex,
	LocalIssueStore,
	parseBackupTimestampFromFilename,
	resolveLocalIssueStorageRoot,
	selectBackupFilesToPrune,
} from "./LocalIssueStore.js"
import type { SpecPublishConfig, SpecPublishOutcome } from "./specTypes.js"

const LINEAR_DUPLICATE_CLEANUP_FORCE_ENV = "AZEDARACH_LINEAR_DUPLICATE_CLEANUP_FORCE"

const withEnv = async <A>(
	key: string,
	value: string | undefined,
	run: () => Promise<A>,
): Promise<A> => {
	const previous = process.env[key]
	if (value === undefined) {
		delete process.env[key]
	} else {
		process.env[key] = value
	}
	try {
		return await run()
	} finally {
		if (previous === undefined) {
			delete process.env[key]
		} else {
			process.env[key] = previous
		}
	}
}

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

	it("skips reserved issue ID az", () => {
		expect(allocateNextAlphaIssueId(51, new Set())).toEqual({ issueId: "ba", nextIndex: 53 })
	})
})

describe("importExternalSnapshot", () => {
	it("preserves local optional fields when snapshot omits them", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const issue = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore
					const created = yield* store.create(
						{
							title: "Local issue",
							description: "Local description should survive import",
							design: "Local design should survive import",
							acceptance: "Local acceptance should survive import",
							estimate: 5,
						},
						undefined,
						projectPath,
					)

					yield* store.update(
						created.id,
						{ notes: "Local notes should survive import" },
						undefined,
						projectPath,
					)

					yield* store.importExternalSnapshot(
						"linear",
						[
							{
								localId: created.id,
								externalId: "lin-1",
								externalKey: created.id,
								title: "Remote title update",
								status: created.status,
								priority: created.priority,
								issueType: created.issue_type,
								createdAt: created.created_at,
								updatedAt: created.updated_at,
								labels: [],
							},
						],
						projectPath,
					)

					return yield* store.show(created.id, projectPath)
				}).pipe(Effect.provide(testLayer)),
			)

			expect(issue).toBeDefined()
			expect(issue?.title).toBe("Remote title update")
			expect(issue?.description).toBe("Local description should survive import")
			expect(issue?.design).toBe("Local design should survive import")
			expect(issue?.notes).toBe("Local notes should survive import")
			expect(issue?.acceptance).toBe("Local acceptance should survive import")
			expect(issue?.estimate).toBe(5)
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("reuses existing local issue id mapped by external id", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-external-id-map-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore
					const created = yield* store.create({ title: "Local issue" }, undefined, projectPath)

					yield* store.upsertExternalRef(
						{
							issueId: created.id,
							target: "linear",
							externalId: "lin-dup-1",
							externalKey: "AZE-123",
						},
						projectPath,
					)

					yield* store.importExternalSnapshot(
						"linear",
						[
							{
								localId: "AZE-123",
								externalId: "lin-dup-1",
								externalKey: "AZE-123",
								title: "Remote title update",
								status: created.status,
								priority: created.priority,
								issueType: created.issue_type,
								createdAt: created.created_at,
								updatedAt: created.updated_at,
								labels: [],
							},
						],
						projectPath,
					)

					const canonicalIssue = yield* store.show(created.id, projectPath)
					const duplicateIssue = yield* store.show("AZE-123", projectPath)
					const externalRef = yield* store.getExternalRef(created.id, "linear", projectPath)
					return { createdId: created.id, canonicalIssue, duplicateIssue, externalRef }
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.canonicalIssue?.id).toBe(result.createdId)
			expect(result.canonicalIssue?.title).toBe("Remote title update")
			expect(result.duplicateIssue).toBeUndefined()
			expect(result.externalRef).toEqual({
				externalId: "lin-dup-1",
				externalKey: "AZE-123",
			})
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("remaps parent dependencies to canonical local ids during import", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-parent-remap-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore
					const parent = yield* store.create({ title: "Parent issue" }, undefined, projectPath)
					const child = yield* store.create({ title: "Child issue" }, undefined, projectPath)

					yield* store.upsertExternalRef(
						{
							issueId: parent.id,
							target: "linear",
							externalId: "lin-parent",
							externalKey: "AZE-200",
						},
						projectPath,
					)
					yield* store.upsertExternalRef(
						{
							issueId: child.id,
							target: "linear",
							externalId: "lin-child",
							externalKey: "AZE-201",
						},
						projectPath,
					)

					yield* store.importExternalSnapshot(
						"linear",
						[
							{
								localId: "AZE-200",
								externalId: "lin-parent",
								externalKey: "AZE-200",
								title: "Parent issue (remote)",
								status: parent.status,
								priority: parent.priority,
								issueType: parent.issue_type,
								createdAt: parent.created_at,
								updatedAt: parent.updated_at,
								labels: [],
							},
							{
								localId: "AZE-201",
								externalId: "lin-child",
								externalKey: "AZE-201",
								title: "Child issue (remote)",
								status: child.status,
								priority: child.priority,
								issueType: child.issue_type,
								createdAt: child.created_at,
								updatedAt: child.updated_at,
								labels: [],
								parentLocalId: "AZE-200",
							},
						],
						projectPath,
					)

					const childAfterImport = yield* store.show(child.id, projectPath)
					const duplicateParentIssue = yield* store.show("AZE-200", projectPath)
					const duplicateChildIssue = yield* store.show("AZE-201", projectPath)
					return {
						parentId: parent.id,
						childAfterImport,
						duplicateParentIssue,
						duplicateChildIssue,
					}
				}).pipe(Effect.provide(testLayer)),
			)

			const parentDependency = result.childAfterImport?.dependencies?.find(
				(dependency) => dependency.dependency_type === "parent-child",
			)
			expect(parentDependency?.id).toBe(result.parentId)
			expect(result.duplicateParentIssue).toBeUndefined()
			expect(result.duplicateChildIssue).toBeUndefined()
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("skips legacy duplicate merge after the one-time cleanup marker is set", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-duplicate-cleanup-skip-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore
					const canonical = yield* store.create(
						{ title: "Canonical local issue" },
						undefined,
						projectPath,
					)

					yield* store.importExternalSnapshot(
						"linear",
						[
							{
								localId: "AZE-320",
								externalId: "lin-320",
								externalKey: "AZE-320",
								title: "Duplicate linear-key issue",
								status: canonical.status,
								priority: canonical.priority,
								issueType: canonical.issue_type,
								createdAt: canonical.created_at,
								updatedAt: canonical.updated_at,
								labels: [],
							},
						],
						projectPath,
					)

					yield* store.upsertExternalRef(
						{
							issueId: canonical.id,
							target: "linear",
							externalId: "lin-320",
							externalKey: "AZE-320",
						},
						projectPath,
					)

					yield* store.importExternalSnapshot(
						"linear",
						[
							{
								localId: "AZE-320",
								externalId: "lin-320",
								externalKey: "AZE-320",
								title: "Canonical issue (remote refresh)",
								status: canonical.status,
								priority: canonical.priority,
								issueType: canonical.issue_type,
								createdAt: canonical.created_at,
								updatedAt: canonical.updated_at,
								labels: [],
							},
						],
						projectPath,
					)

					const canonicalIssue = yield* store.show(canonical.id, projectPath)
					const duplicateIssue = yield* store.show("AZE-320", projectPath)
					const canonicalRef = yield* store.getExternalRef(canonical.id, "linear", projectPath)
					return { canonicalId: canonical.id, canonicalIssue, duplicateIssue, canonicalRef }
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.canonicalIssue?.id).toBe(result.canonicalId)
			expect(result.canonicalIssue?.title).toBe("Canonical issue (remote refresh)")
			expect(result.duplicateIssue?.id).toBe("AZE-320")
			expect(result.canonicalRef).toEqual({
				externalId: "lin-320",
				externalKey: "AZE-320",
			})
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("merges preexisting duplicate linear-key issues when cleanup force env is enabled", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-duplicate-cleanup-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await withEnv(LINEAR_DUPLICATE_CLEANUP_FORCE_ENV, "1", () =>
				Effect.runPromise(
					Effect.gen(function* () {
						const store = yield* LocalIssueStore
						const canonical = yield* store.create(
							{ title: "Canonical local issue" },
							undefined,
							projectPath,
						)
						const dependent = yield* store.create(
							{ title: "Dependent issue" },
							undefined,
							projectPath,
						)

						yield* store.importExternalSnapshot(
							"linear",
							[
								{
									localId: "AZE-310",
									externalId: "lin-310",
									externalKey: "AZE-310",
									title: "Duplicate linear-key issue",
									status: canonical.status,
									priority: canonical.priority,
									issueType: canonical.issue_type,
									createdAt: canonical.created_at,
									updatedAt: canonical.updated_at,
									labels: [],
								},
							],
							projectPath,
						)

						yield* store.upsertExternalRef(
							{
								issueId: canonical.id,
								target: "linear",
								externalId: "lin-310",
								externalKey: "AZE-310",
							},
							projectPath,
						)

						yield* store.update(dependent.id, { parent: "AZE-310" }, undefined, projectPath)

						yield* store.importExternalSnapshot(
							"linear",
							[
								{
									localId: "AZE-310",
									externalId: "lin-310",
									externalKey: "AZE-310",
									title: "Canonical issue (remote refresh)",
									status: canonical.status,
									priority: canonical.priority,
									issueType: canonical.issue_type,
									createdAt: canonical.created_at,
									updatedAt: canonical.updated_at,
									labels: [],
								},
							],
							projectPath,
						)

						const canonicalIssue = yield* store.show(canonical.id, projectPath)
						const duplicateIssue = yield* store.show("AZE-310", projectPath)
						const dependentIssue = yield* store.show(dependent.id, projectPath)
						const canonicalRef = yield* store.getExternalRef(canonical.id, "linear", projectPath)
						return {
							canonicalId: canonical.id,
							canonicalIssue,
							duplicateIssue,
							dependentIssue,
							canonicalRef,
						}
					}).pipe(Effect.provide(testLayer)),
				),
			)

			const parentDependency = result.dependentIssue?.dependencies?.find(
				(dependency) => dependency.dependency_type === "parent-child",
			)
			expect(result.canonicalIssue?.id).toBe(result.canonicalId)
			expect(result.canonicalIssue?.title).toBe("Canonical issue (remote refresh)")
			expect(result.duplicateIssue).toBeUndefined()
			expect(parentDependency?.id).toBe(result.canonicalId)
			expect(result.canonicalRef).toEqual({
				externalId: "lin-310",
				externalKey: "AZE-310",
			})
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})
})

describe("list", () => {
	it("filters issues by parent dependency id", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-list-parent-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore
					const parent = yield* store.create({ title: "Parent issue" }, undefined, projectPath)
					const matchingChild = yield* store.create(
						{ title: "Matching child" },
						undefined,
						projectPath,
					)
					const otherParent = yield* store.create({ title: "Other parent" }, undefined, projectPath)
					const otherChild = yield* store.create({ title: "Other child" }, undefined, projectPath)

					yield* store.update(matchingChild.id, { parent: parent.id }, undefined, projectPath)
					yield* store.update(otherChild.id, { parent: otherParent.id }, undefined, projectPath)

					return yield* store.list({ parent: parent.id.toLowerCase() }, projectPath, {
						limit: 10,
						pageSize: 10,
					})
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.map((issue) => issue.id)).toEqual(["b"])
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})
})

describe("legacy issue schema migration", () => {
	it("adds missing issues columns before update writes notes", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-legacy-issues-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const dbDir = join(projectPath, ".azedarach")
			const dbPath = join(dbDir, "issues.db")
			mkdirSync(dbDir, { recursive: true })
			const db = new Database(dbPath)
			try {
				db.exec(`
					CREATE TABLE issues (
						id TEXT PRIMARY KEY,
						title TEXT NOT NULL,
						description TEXT,
						status TEXT NOT NULL,
						priority INTEGER NOT NULL,
						issue_type TEXT NOT NULL,
						created_at TEXT NOT NULL,
						updated_at TEXT NOT NULL
					)
				`)

				db.prepare(
					"INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
				).run(
					"legacy-1",
					"Legacy issue",
					"Legacy description",
					"open",
					3,
					"task",
					"2026-03-01T00:00:00.000Z",
					"2026-03-01T00:00:00.000Z",
				)
			} finally {
				db.close()
			}

			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore
					const updated = yield* store.update(
						"legacy-1",
						{ notes: "Updated from legacy schema" },
						undefined,
						projectPath,
					)
					const issue = yield* store.show("legacy-1", projectPath)
					return { updated, issue }
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.updated).toBe(true)
			expect(result.issue?.notes).toBe("Updated from legacy schema")
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})
})

describe("spec requirements and links", () => {
	it("accepts suffixed spec requirement external codes", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-spec-suffix-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const requirement = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore
					return yield* store.createSpecRequirement(
						{
							external_code: "AZ-FR-0802a",
							title: "Suffixed requirement ID support",
							body: "Allow optional single-letter suffix in requirement IDs.",
						},
						projectPath,
					)
				}).pipe(Effect.provide(testLayer)),
			)

			expect(requirement.id.startsWith("sr_")).toBe(true)
			expect(requirement.local_id).toBe("fr0802a")
			expect(requirement.external_code).toBe("AZ-FR-0802A")
			expect(requirement.kind).toBe("functional")
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("supports requirement CRUD, bidirectional links, and coverage gaps", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-spec-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore

					const issue = yield* store.create({ title: "Implement feature" }, undefined, projectPath)
					const requirement = yield* store.createSpecRequirement(
						{
							external_code: "AZ-FR-4201",
							title: "Track requirement records",
							body: "The store must persist requirements.",
						},
						projectPath,
					)
					const acceptanceRequirement = yield* store.createSpecRequirement(
						{
							external_code: "AZ-AT-2901",
							title: "Acceptance coverage",
							body: "Acceptance requirement with no links yet.",
						},
						projectPath,
					)

					yield* store.addSpecIssueLink(issue.id, requirement.id, "implements", projectPath)

					const issueRequirements = yield* store.listIssueSpecRequirements(issue.id, projectPath)
					const requirementIssues = yield* store.listRequirementLinkedIssues(
						requirement.id,
						projectPath,
					)
					const coverage = yield* store.getSpecCoverageReport(projectPath)

					return {
						requirement,
						acceptanceRequirement,
						issueRequirements,
						requirementIssues,
						coverage,
					}
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.issueRequirements).toHaveLength(1)
			expect(result.issueRequirements[0]?.id).toBe(result.requirement.id)
			expect(result.issueRequirements[0]?.local_id).toBe("fr4201")
			expect(result.issueRequirements[0]?.external_code).toBe("AZ-FR-4201")
			expect(result.issueRequirements[0]?.link_type).toBe("implements")

			expect(result.requirementIssues).toHaveLength(1)
			expect(result.requirementIssues[0]?.link_type).toBe("implements")

			expect(result.coverage.requirements).toHaveLength(2)
			expect(result.coverage.unlinked_requirement_ids).toContain(
				result.acceptanceRequirement.local_id,
			)
			expect(
				result.coverage.integrity_gaps.some(
					(gap) =>
						gap.kind === "unlinked_requirement" &&
						gap.requirement_id === result.acceptanceRequirement.local_id,
				),
			).toBe(true)
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("persists publish config and last publish outcome metadata", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-spec-publish-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore

					const config: SpecPublishConfig = {
						enabled: true,
						debounce_ms: 1500,
						target_project: "AZE",
						documents: {
							overview: "Spec Overview",
							requirements: "Requirements Index",
							acceptance: "Acceptance Index",
							change_log: "Change Log",
						},
					}
					yield* store.setSpecPublishConfig(config, projectPath)

					const outcome: SpecPublishOutcome = {
						started_at: DateTime.unsafeFromDate(new Date("2026-03-07T00:00:00.000Z")),
						finished_at: DateTime.unsafeFromDate(new Date("2026-03-07T00:00:01.000Z")),
						status: "partial",
						total_requirements: 2,
						total_links: 1,
						outcomes: [
							{
								document_key: "overview",
								title: "Spec Overview",
								status: "success",
								message: "Updated",
								requirement_count: 2,
								link_count: 1,
							},
							{
								document_key: "requirements",
								title: "Requirements Index",
								status: "failed",
								message: "permission denied",
								requirement_count: 2,
								link_count: 1,
							},
						],
					}
					yield* store.setSpecPublishOutcome(outcome, projectPath)

					const savedConfig = yield* store.getSpecPublishConfig(projectPath)
					const savedOutcome = yield* store.getSpecPublishOutcome(projectPath)

					return {
						savedConfig,
						savedOutcome,
					}
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.savedConfig.enabled).toBe(true)
			expect(result.savedConfig.debounce_ms).toBe(1500)
			expect(result.savedConfig.target_project).toBe("AZE")
			expect(result.savedOutcome?.status).toBe("partial")
			expect(result.savedOutcome && DateTime.formatIso(result.savedOutcome.started_at)).toBe(
				"2026-03-07T00:00:00.000Z",
			)
			expect(result.savedOutcome && DateTime.formatIso(result.savedOutcome.finished_at)).toBe(
				"2026-03-07T00:00:01.000Z",
			)
			expect(result.savedOutcome?.outcomes).toHaveLength(2)
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})
})

describe("getSyncQueueSummary", () => {
	it("reports delayed and failed queue states for diagnostics", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-sync-summary-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const summary = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore

					const first = yield* store.create({ title: "First sync item" }, "linear", projectPath)
					const firstClaims = yield* store.listPendingSync("linear", 10, projectPath)
					yield* store.markSyncRetriable(
						{
							claims: firstClaims.map((item) => ({
								id: item.id,
								attemptToken: item.attemptToken,
							})),
							errorMessage: "retry later",
							delaySeconds: 60,
							nextAttempts: 1,
						},
						projectPath,
					)

					yield* store.create({ title: "Second sync item" }, "linear", projectPath)
					const secondClaims = yield* store.listPendingSync("linear", 10, projectPath)
					yield* store.markSyncTerminalFailure(
						{
							claims: secondClaims.map((item) => ({
								id: item.id,
								attemptToken: item.attemptToken,
							})),
							errorMessage: "terminal failure",
							nextAttempts: 5,
						},
						projectPath,
					)

					const queueSummary = yield* store.getSyncQueueSummary("linear", projectPath)
					const firstIssueClaims = yield* store.listPendingSync("linear", 10, projectPath)
					expect(firstIssueClaims).toHaveLength(0)
					expect(first.id.length).toBeGreaterThan(0)
					return queueSummary
				}).pipe(Effect.provide(testLayer)),
			)

			expect(summary.total).toBe(2)
			expect(summary.pendingReady).toBe(0)
			expect(summary.pendingDelayed).toBe(1)
			expect(summary.processingActive).toBe(0)
			expect(summary.processingStale).toBe(0)
			expect(summary.failed).toBe(1)
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})
})
