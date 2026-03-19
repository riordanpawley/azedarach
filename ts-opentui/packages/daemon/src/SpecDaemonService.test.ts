import { Database } from "bun:sqlite"
import { describe, expect, it } from "bun:test"
import { FileSystem } from "@effect/platform"
import { BunContext, BunFileSystem, BunPath } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { SpecDaemonService } from "./SpecDaemonService.js"

const makeProjectPath = (suffix: string): string =>
	`/tmp/az-spec-daemon-${suffix}-${crypto.randomUUID()}`

const platformLayer = Layer.mergeAll(BunContext.layer, BunFileSystem.layer, BunPath.layer)

const withTempProject = async <A>(
	suffix: string,
	run: (projectPath: string) => Promise<A>,
): Promise<A> => {
	const projectPath = makeProjectPath(suffix)
	await Effect.runPromise(
		Effect.gen(function* () {
			const fs = yield* FileSystem.FileSystem
			yield* fs.makeDirectory(projectPath, { recursive: true })
		}).pipe(Effect.provide(platformLayer)),
	)

	try {
		return await run(projectPath)
	} finally {
		await Effect.runPromise(
			Effect.gen(function* () {
				const fs = yield* FileSystem.FileSystem
				yield* fs.remove(projectPath, { recursive: true, force: true })
			}).pipe(Effect.provide(platformLayer)),
		)
	}
}

const specLayer = SpecDaemonService.Default.pipe(Layer.provide(platformLayer))

const runSpecProgram = <A, E>(program: Effect.Effect<A, E, SpecDaemonService>) =>
	Effect.runPromise(program.pipe(Effect.provide(specLayer)))

describe("SpecDaemonService", () => {
	it("reads requirement, issue link, parity, and lint state from daemon-local storage", async () => {
		await withTempProject("read-model", async (projectPath) => {
			await runSpecProgram(
				Effect.gen(function* () {
					const service = yield* SpecDaemonService
					return yield* service.listRequirements(projectPath)
				}),
			)

			const dbPath = `${projectPath}/.azedarach/azedarach.db`
			const database = new Database(dbPath)
			try {
				database.run(
					"INSERT INTO issues (id, title, status, issue_type, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, NULL)",
					["te-123", "Track board state", "in_progress", "task", "2026-03-20T00:00:00.000Z"],
				)
				database.run(
					"INSERT INTO spec_requirements (id, local_id, external_code, title, body_md, kind, status, priority, created_at, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)",
					[
						"req-1",
						"s1",
						"AZ-FR-0001",
						"Track board state",
						"Keep the board synchronized.",
						"functional",
						"draft",
						1,
						"2026-03-20T00:00:00.000Z",
						"2026-03-20T00:01:00.000Z",
					],
				)
				database.run(
					"INSERT INTO spec_issue_links (issue_id, requirement_id, link_type, implementations_json, fulfillment_status, fulfillment_percent, evidence_note, created_at, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)",
					[
						"te-123",
						"req-1",
						"implements",
						'["default"]',
						"complete",
						100,
						"covered by daemon rpc",
						"2026-03-20T00:02:00.000Z",
						"2026-03-20T00:03:00.000Z",
					],
				)
			} finally {
				database.close()
			}

			const result = await runSpecProgram(
				Effect.gen(function* () {
					const service = yield* SpecDaemonService
					const [requirements, linkedRequirements, linkedIssues, parity, lint, snapshot] =
						yield* Effect.all([
							service.listRequirements(projectPath),
							service.listIssueRequirements("te-123", projectPath),
							service.listRequirementIssues("s1", projectPath, "local_id"),
							service.getParityReport("default", projectPath),
							service.lint(projectPath),
							service.readSnapshot(projectPath),
						])
					return {
						requirements,
						linkedRequirements,
						linkedIssues,
						parity,
						lint,
						snapshot,
					}
				}),
			)

			expect(result.requirements).toHaveLength(1)
			expect(result.requirements[0]?.external_code).toBe("AZ-FR-0001")
			expect(result.linkedRequirements).toHaveLength(1)
			expect(result.linkedRequirements[0]?.id).toBe("req-1")
			expect(result.linkedIssues).toHaveLength(1)
			expect(result.linkedIssues[0]?.id).toBe("te-123")
			expect(result.linkedIssues[0]?.status).toBe("in_progress")
			expect(result.parity.implemented_requirement_ids).toEqual(["s1"])
			expect(result.lint.ok).toBe(true)
			expect(result.snapshot.links[0]?.issue_id).toBe("te-123")
			expect(result.snapshot.coverage.fully_implemented_requirement_ids).toEqual(["s1"])
		})
	})
})
