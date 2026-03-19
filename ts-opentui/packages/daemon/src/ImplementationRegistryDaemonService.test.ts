import { Database } from "bun:sqlite"
import { describe, expect, it } from "bun:test"
import { FileSystem } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { ImplementationRegistryDaemonService } from "./ImplementationRegistryDaemonService.js"

const makeProjectPath = (suffix: string): string =>
	`/tmp/az-impl-daemon-${suffix}-${crypto.randomUUID()}`

const withTempProject = async <A>(
	suffix: string,
	run: (projectPath: string) => Promise<A>,
): Promise<A> => {
	const projectPath = makeProjectPath(suffix)
	await Effect.runPromise(
		Effect.gen(function* () {
			const fs = yield* FileSystem.FileSystem
			yield* fs.makeDirectory(projectPath, { recursive: true })
		}).pipe(Effect.provide(BunContext.layer)),
	)

	try {
		return await run(projectPath)
	} finally {
		await Effect.runPromise(
			Effect.gen(function* () {
				const fs = yield* FileSystem.FileSystem
				yield* fs.remove(projectPath, { recursive: true, force: true })
			}).pipe(Effect.provide(BunContext.layer)),
		)
	}
}

const registryLayer = ImplementationRegistryDaemonService.Default

describe("ImplementationRegistryDaemonService", () => {
	it("exposes the built-in default implementation for empty projects", async () => {
		await withTempProject("default", async (projectPath) => {
			const registry = await Effect.runPromise(
				Effect.gen(function* () {
					const service = yield* ImplementationRegistryDaemonService
					return yield* service.getRegistry(projectPath)
				}).pipe(Effect.provide(registryLayer)),
			)

			expect(registry.default_implementation).toBe("default")
			expect(registry.implicit_default_allowed).toBe(true)
			expect(registry.implementations).toEqual([
				{
					name: "default",
					description: undefined,
					directory: undefined,
					created_at: "1970-01-01T00:00:00.000Z",
					updated_at: "1970-01-01T00:00:00.000Z",
					is_default: true,
					is_builtin: true,
				},
			])
		})
	})

	it("renames implementation references in issues and spec links", async () => {
		await withTempProject("rename", async (projectPath) => {
			await Effect.runPromise(
				Effect.gen(function* () {
					const service = yield* ImplementationRegistryDaemonService
					yield* service.create(
						{
							name: "ts-opentui",
						},
						projectPath,
					)
				}).pipe(Effect.provide(registryLayer)),
			)

			const dbPath = `${projectPath}/.azedarach/azedarach.db`
			const database = new Database(dbPath)
			try {
				database.run(
					"INSERT INTO issues (id, implementations_json, updated_at, deleted_at) VALUES (?, ?, ?, NULL)",
					"qc",
					'["ts-opentui"]',
					"2026-03-20T00:00:00.000Z",
				)
				database.run(
					"INSERT INTO spec_issue_links (issue_id, requirement_id, link_type, implementations_json, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, NULL)",
					"qc",
					"fr0001",
					"implements",
					'["ts-opentui"]',
					"2026-03-20T00:00:00.000Z",
				)
			} finally {
				database.close()
			}

			await Effect.runPromise(
				Effect.gen(function* () {
					const service = yield* ImplementationRegistryDaemonService
					yield* service.update(
						"ts-opentui",
						{
							name: "go-bubbletea",
						},
						projectPath,
					)
				}).pipe(Effect.provide(registryLayer)),
			)

			const verifyDb = new Database(dbPath, { readonly: true })
			try {
				const issueRow = verifyDb
					.query<{ readonly implementations_json: string | null }, []>(
						"SELECT implementations_json FROM issues WHERE id = 'qc'",
					)
					.get()
				const linkRow = verifyDb
					.query<{ readonly implementations_json: string | null }, []>(
						"SELECT implementations_json FROM spec_issue_links WHERE issue_id = 'qc' AND requirement_id = 'fr0001' AND link_type = 'implements'",
					)
					.get()
				expect(issueRow?.implementations_json).toBe('["go-bubbletea"]')
				expect(linkRow?.implementations_json).toBe('["go-bubbletea"]')
			} finally {
				verifyDb.close()
			}
		})
	})

	it("blocks delete while the implementation is still referenced", async () => {
		await withTempProject("delete-guard", async (projectPath) => {
			await Effect.runPromise(
				Effect.gen(function* () {
					const service = yield* ImplementationRegistryDaemonService
					yield* service.create(
						{
							name: "ts-opentui",
						},
						projectPath,
					)
				}).pipe(Effect.provide(registryLayer)),
			)

			const dbPath = `${projectPath}/.azedarach/azedarach.db`
			const database = new Database(dbPath)
			try {
				database.run(
					"INSERT INTO issues (id, implementations_json, updated_at, deleted_at) VALUES (?, ?, ?, NULL)",
					"qc",
					'["ts-opentui"]',
					"2026-03-20T00:00:00.000Z",
				)
			} finally {
				database.close()
			}

			await expect(
				Effect.runPromise(
					Effect.gen(function* () {
						const service = yield* ImplementationRegistryDaemonService
						yield* service.delete("ts-opentui", projectPath)
					}).pipe(Effect.provide(registryLayer)),
				),
			).rejects.toThrow("Implementation ts-opentui is still assigned to one or more issues")
		})
	})
})
