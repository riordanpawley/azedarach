import { Database } from "bun:sqlite"
import { describe, expect, it } from "bun:test"
import { existsSync, mkdirSync, mkdtempSync, rmSync } from "node:fs"
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

describe("sqlite storage path resolution", () => {
	it("creates new project databases at .azedarach/azedarach.db", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-db-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			await Effect.runPromise(
				Effect.gen(function* () {
					const localIssueStore = yield* LocalIssueStore
					yield* localIssueStore.create(
						{
							title: "SQLite source of truth",
						},
						undefined,
						projectPath,
					)
				}).pipe(Effect.provide(testLayer)),
			)

			expect(existsSync(join(projectPath, ".azedarach", "azedarach.db"))).toBe(true)
			expect(existsSync(join(projectPath, ".azedarach", "issues.db"))).toBe(false)
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
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

	it("suppresses reimport for external refs deleted locally", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-delete-suppress-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore
					const created = yield* store.create({ title: "Delete me" }, undefined, projectPath)
					yield* store.upsertExternalRef(
						{
							issueId: created.id,
							target: "linear",
							externalId: "lin-delete-1",
							externalKey: "AZE-999",
						},
						projectPath,
					)
					yield* store.delete(created.id, "linear", projectPath)

					const importedCount = yield* store.importExternalSnapshot(
						"linear",
						[
							{
								localId: "AZE-999",
								externalId: "lin-delete-1",
								externalKey: "AZE-999",
								title: "Should not resurrect",
								status: "closed",
								priority: 2,
								issueType: "task",
								createdAt: created.created_at,
								updatedAt: created.updated_at,
								labels: [],
							},
						],
						projectPath,
					)

					const originalIssue = yield* store.show(created.id, projectPath)
					const mirroredIssue = yield* store.show("AZE-999", projectPath)
					return { importedCount, originalIssue, mirroredIssue }
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.importedCount).toBe(0)
			expect(result.originalIssue).toBeUndefined()
			expect(result.mirroredIssue).toBeUndefined()
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

describe("implementation registry", () => {
	it("exposes a built-in default implementation before any explicit setup", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-impl-default-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const registry = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore
					return yield* store.getImplementationRegistry(projectPath)
				}).pipe(Effect.provide(testLayer)),
			)

			expect(registry.default_implementation).toBe("default")
			expect(registry.implicit_default_allowed).toBe(true)
			expect(registry.implementations).toEqual([
				{
					name: "default",
					description: undefined,
					created_at: "1970-01-01T00:00:00.000Z",
					updated_at: "1970-01-01T00:00:00.000Z",
					is_default: true,
					is_builtin: true,
				},
			])
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("supports add, update, delete, and set-default operations for named implementations", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-impl-ops-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore

					const added = yield* store.createImplementation(
						{
							name: "ts-opentui",
							description: "TypeScript UI",
						},
						projectPath,
					)
					const updated = yield* store.updateImplementation(
						"ts-opentui",
						{
							name: "go-bubbletea",
							description: "Go UI",
							setDefault: true,
						},
						projectPath,
					)
					const afterUpdate = yield* store.getImplementationRegistry(projectPath)
					const deleted = yield* store.deleteImplementation("go-bubbletea", projectPath)
					const afterDelete = yield* store.getImplementationRegistry(projectPath)

					return {
						added,
						updated,
						afterUpdate,
						deleted,
						afterDelete,
					}
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.added.name).toBe("ts-opentui")
			expect(result.added.is_default).toBe(false)

			expect(result.updated.name).toBe("go-bubbletea")
			expect(result.updated.description).toBe("Go UI")
			expect(result.updated.is_default).toBe(true)

			expect(result.afterUpdate.default_implementation).toBe("go-bubbletea")
			expect(result.afterUpdate.implicit_default_allowed).toBe(false)
			expect(
				result.afterUpdate.implementations.map((implementation) => implementation.name),
			).toEqual(["go-bubbletea", "default"])

			expect(result.deleted).toBe(true)
			expect(result.afterDelete.default_implementation).toBe("default")
			expect(result.afterDelete.implicit_default_allowed).toBe(true)
			expect(result.afterDelete.implementations).toHaveLength(1)
			expect(result.afterDelete.implementations[0]?.name).toBe("default")
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})
})

describe("issue implementations", () => {
	it("requires explicit implementations on new issue writes once multiple implementations exist", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-issue-impl-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const store = await Effect.runPromise(
				Effect.gen(function* () {
					const resolvedStore = yield* LocalIssueStore
					yield* resolvedStore.createImplementation({ name: "ts-opentui" }, projectPath)
					return resolvedStore
				}).pipe(Effect.provide(testLayer)),
			)

			await expect(
				Effect.runPromise(store.create({ title: "Implicit impl issue" }, undefined, projectPath)),
			).rejects.toThrow("Multiple implementations are configured")

			const explicitIssue = await Effect.runPromise(
				store.create(
					{
						title: "TS issue",
						implementations: ["ts-opentui"],
					},
					undefined,
					projectPath,
				),
			)
			const sharedIssue = await Effect.runPromise(
				store.create(
					{
						title: "Shared issue",
						implementations: ["default", "ts-opentui"],
					},
					undefined,
					projectPath,
				),
			)
			await Effect.runPromise(
				store.update(
					explicitIssue.id,
					{ notes: "keep current impl assignment" },
					undefined,
					projectPath,
				),
			)
			const persistedExplicitIssue = await Effect.runPromise(
				store.show(explicitIssue.id, projectPath),
			)
			const tsScopedIssues = await Effect.runPromise(
				store.list({ implementations: ["ts-opentui"] }, projectPath),
			)

			expect(explicitIssue.implementations).toEqual(["ts-opentui"])
			expect(sharedIssue.implementations).toEqual(["default", "ts-opentui"])
			expect(persistedExplicitIssue?.implementations).toEqual(["ts-opentui"])
			expect(tsScopedIssues.map((issue) => issue.id).sort()).toEqual(
				[explicitIssue.id, sharedIssue.id].sort(),
			)
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

	it("supports impl-scoped spec links, bidirectional lookups, and parity reporting", async () => {
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
					yield* store.createImplementation({ name: "ts-opentui" }, projectPath)
					yield* store.createImplementation({ name: "go-bubbletea" }, projectPath)
					yield* store.addSpecIssueLink(
						issue.id,
						requirement.id,
						"implements",
						"complete",
						100,
						"Shipped in default",
						projectPath,
						"auto",
						["default"],
					)
					yield* store.addSpecIssueLink(
						issue.id,
						requirement.id,
						"implements",
						"complete",
						100,
						"Shipped in ts-opentui",
						projectPath,
						"auto",
						["ts-opentui"],
					)
					yield* store.addSpecIssueLink(
						issue.id,
						requirement.id,
						"tests",
						"verified",
						100,
						"Tested in ts-opentui",
						projectPath,
						"auto",
						["ts-opentui"],
					)
					yield* store.addSpecIssueLink(
						issue.id,
						requirement.id,
						"relates",
						"planned",
						null,
						null,
						projectPath,
						"auto",
						["go-bubbletea"],
					)

					const links = yield* store.listSpecIssueLinks(undefined, projectPath)
					const tsOpenTuiLinks = yield* store.listSpecIssueLinks(
						{ implementation: "ts-opentui" },
						projectPath,
					)
					const issueRequirements = yield* store.listIssueSpecRequirements(issue.id, projectPath)
					const requirementIssues = yield* store.listRequirementLinkedIssues(
						requirement.id,
						projectPath,
					)
					const coverage = yield* store.getSpecCoverageReport(projectPath)
					const defaultParity = yield* store.getSpecParityReport("default", projectPath)
					const tsOpenTuiParity = yield* store.getSpecParityReport("ts-opentui", projectPath)
					const goBubbleteaParity = yield* store.getSpecParityReport("go-bubbletea", projectPath)

					return {
						links,
						tsOpenTuiLinks,
						requirement,
						acceptanceRequirement,
						issueRequirements,
						requirementIssues,
						coverage,
						defaultParity,
						tsOpenTuiParity,
						goBubbleteaParity,
					}
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.links).toHaveLength(3)
			expect(result.tsOpenTuiLinks).toHaveLength(2)
			expect(result.links.find((link) => link.link_type === "implements")?.implementations).toEqual(
				["default", "ts-opentui"],
			)

			expect(result.issueRequirements).toHaveLength(3)
			expect(result.issueRequirements[0]?.id).toBe(result.requirement.id)
			expect(result.issueRequirements[0]?.local_id).toBe("fr4201")
			expect(result.issueRequirements[0]?.external_code).toBe("AZ-FR-4201")
			expect(result.issueRequirements[0]?.link_type).toBe("implements")
			expect(result.issueRequirements[0]?.implementations).toEqual(["default", "ts-opentui"])
			expect(result.issueRequirements[0]?.implementations).toEqual(["default", "ts-opentui"])
			expect(result.issueRequirements[0]?.fulfillment_status).toBe("complete")
			expect(result.issueRequirements[0]?.fulfillment_percent).toBe(100)
			expect(result.issueRequirements[0]?.evidence_note).toBe("Shipped in ts-opentui")

			expect(result.requirementIssues).toHaveLength(3)
			const implementsLink = result.requirementIssues.find(
				(item) => item.link_type === "implements",
			)
			const testsLink = result.requirementIssues.find((item) => item.link_type === "tests")
			const relatesLink = result.requirementIssues.find((item) => item.link_type === "relates")
			expect(implementsLink?.implementations).toEqual(["default", "ts-opentui"])
			expect(implementsLink?.fulfillment_status).toBe("complete")
			expect(testsLink?.implementations).toEqual(["ts-opentui"])
			expect(testsLink?.fulfillment_status).toBe("verified")
			expect(relatesLink?.implementations).toEqual(["go-bubbletea"])
			expect(relatesLink?.fulfillment_status).toBe("planned")

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

			expect(result.defaultParity.implementation).toBe("default")
			expect(result.defaultParity.implemented_requirement_ids).toContain(
				result.requirement.local_id,
			)
			expect(result.defaultParity.tested_requirement_ids).not.toContain(result.requirement.local_id)

			expect(result.tsOpenTuiParity.implementation).toBe("ts-opentui")
			expect(result.tsOpenTuiParity.implemented_requirement_ids).toContain(
				result.requirement.local_id,
			)
			expect(result.tsOpenTuiParity.tested_requirement_ids).toContain(result.requirement.local_id)
			expect(result.tsOpenTuiParity.uncovered_requirement_ids).toContain(
				result.acceptanceRequirement.local_id,
			)

			expect(result.goBubbleteaParity.implementation).toBe("go-bubbletea")
			expect(result.goBubbleteaParity.implemented_requirement_ids).not.toContain(
				result.requirement.local_id,
			)
			expect(result.goBubbleteaParity.related_only_requirement_ids).toContain(
				result.requirement.local_id,
			)
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("stores and lists explicit link fulfillment metadata", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-spec-link-fulfillment-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore
					const issue = yield* store.create(
						{ title: "Implement part of feature" },
						undefined,
						projectPath,
					)
					const requirement = yield* store.createSpecRequirement(
						{
							external_code: "AZ-FR-4202",
							title: "Partial delivery support",
							body: "Link metadata should carry progress context.",
						},
						projectPath,
					)

					yield* store.addSpecIssueLink(
						issue.id,
						requirement.id,
						"implements",
						"partial",
						45,
						"Backend complete; UI pending",
						projectPath,
					)

					const links = yield* store.listSpecIssueLinks(undefined, projectPath)
					const linkedRequirements = yield* store.listIssueSpecRequirements(issue.id, projectPath)
					const linkedIssues = yield* store.listRequirementLinkedIssues(requirement.id, projectPath)
					const parity = yield* store.getSpecParityReport("default", projectPath)

					return {
						links,
						linkedRequirements,
						linkedIssues,
						parity,
					}
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.links).toHaveLength(1)
			expect(result.links[0]?.fulfillment_status).toBe("partial")
			expect(result.links[0]?.fulfillment_percent).toBe(45)
			expect(result.links[0]?.evidence_note).toBe("Backend complete; UI pending")

			expect(result.linkedRequirements[0]?.fulfillment_status).toBe("partial")
			expect(result.linkedRequirements[0]?.fulfillment_percent).toBe(45)
			expect(result.linkedRequirements[0]?.evidence_note).toBe("Backend complete; UI pending")

			expect(result.linkedIssues[0]?.fulfillment_status).toBe("partial")
			expect(result.linkedIssues[0]?.fulfillment_percent).toBe(45)
			expect(result.linkedIssues[0]?.evidence_note).toBe("Backend complete; UI pending")

			expect(result.parity.partially_implemented_requirement_ids).toContain("fr4202")
			expect(result.parity.implemented_requirement_ids).not.toContain("fr4202")
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("filters spec requirements by query, kind, status, and priority with deterministic ordering", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-spec-filter-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore

					yield* store.createSpecRequirement(
						{
							external_code: "AZ-FR-5101",
							title: "Sync metadata for markdown snapshots",
							body: "Generates read-only markdown outputs for review.",
							status: "active",
							priority: 1,
						},
						projectPath,
					)
					yield* store.createSpecRequirement(
						{
							external_code: "AZ-AT-5101",
							title: "Search CLI output remains deterministic",
							body: "Query includes local_id and external_code matches.",
							status: "draft",
							priority: 3,
						},
						projectPath,
					)
					yield* store.createSpecRequirement(
						{
							external_code: "AZ-FR-5102",
							title: "Publish config handling",
							body: "Ensure config updates remain stable.",
							status: "active",
							priority: 3,
						},
						projectPath,
					)

					const queryFiltered = yield* store.listSpecRequirements(projectPath, {
						query: "markdown",
					})
					const kindFiltered = yield* store.listSpecRequirements(projectPath, {
						kind: "acceptance",
					})
					const statusPriorityFiltered = yield* store.listSpecRequirements(projectPath, {
						status: "active",
						priority: 3,
					})
					const externalCodeQuery = yield* store.listSpecRequirements(projectPath, {
						query: "AZ-FR-5102",
					})
					const noMatches = yield* store.listSpecRequirements(projectPath, {
						query: "does-not-exist",
					})

					return {
						queryFiltered,
						kindFiltered,
						statusPriorityFiltered,
						externalCodeQuery,
						noMatches,
					}
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.queryFiltered.map((requirement) => requirement.local_id)).toEqual(["fr5101"])
			expect(result.kindFiltered.map((requirement) => requirement.local_id)).toEqual(["at5101"])
			expect(result.statusPriorityFiltered.map((requirement) => requirement.local_id)).toEqual([
				"fr5102",
			])
			expect(result.externalCodeQuery.map((requirement) => requirement.local_id)).toEqual([
				"fr5102",
			])
			expect(result.noMatches).toEqual([])
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

describe("linear webhook runtime lease metadata", () => {
	it("stores, reads, and clears the runtime lease in sqlite meta", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-local-store-webhook-lease-"))
		const testLayer = Layer.provide(LocalIssueStore.Default, BunContext.layer)

		try {
			const result = await Effect.runPromise(
				Effect.gen(function* () {
					const store = yield* LocalIssueStore
					yield* store.setLinearWebhookRuntimeLease(
						{
							webhookId: "wh_123",
							webhookUrl: "https://example.ts.net/linear/webhook",
							teamId: "team_123",
							resourceTypes: ["Issue"],
							webhookSecret: "whsec_123",
						},
						projectPath,
					)
					const saved = yield* store.getLinearWebhookRuntimeLease(projectPath)
					yield* store.clearLinearWebhookRuntimeLease(projectPath)
					const cleared = yield* store.getLinearWebhookRuntimeLease(projectPath)
					return {
						saved,
						cleared,
					}
				}).pipe(Effect.provide(testLayer)),
			)

			expect(result.saved).toEqual({
				webhookId: "wh_123",
				webhookUrl: "https://example.ts.net/linear/webhook",
				teamId: "team_123",
				resourceTypes: ["Issue"],
				webhookSecret: "whsec_123",
			})
			expect(result.cleared).toBeUndefined()
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
