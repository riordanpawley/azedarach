import { describe, expect, it } from "bun:test"
import type { TrackedIssue } from "@azedarach/shared/rpc"
import { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { DaemonAttachmentService } from "./DaemonAttachmentService.js"
import {
	TrackerIssueDaemonError,
	TrackerIssueDaemonService,
	type TrackerIssueDaemonServiceApi,
} from "./TrackerIssueDaemonService.js"

const makeTrackedIssue = (issueId: string, notes = ""): TrackedIssue => ({
	id: issueId,
	title: `Issue ${issueId}`,
	status: "open",
	priority: 2,
	issue_type: "task",
	created_at: "2026-03-20T00:00:00.000Z",
	updated_at: "2026-03-20T00:00:00.000Z",
	closed_at: null,
	assignee: null,
	description: undefined,
	design: undefined,
	acceptance: undefined,
	notes,
	estimate: undefined,
	labels: undefined,
	implementations: [],
	dependencies: undefined,
	dependents: undefined,
	dependency_count: undefined,
	dependent_count: undefined,
})

const unexpectedTrackerError = (message: string): Effect.Effect<never, TrackerIssueDaemonError> =>
	Effect.fail(
		new TrackerIssueDaemonError({
			reason: "command-failed",
			message,
		}),
	)

const makeTrackerStub = () => {
	const issues = new Map<string, TrackedIssue>([["AZ-1", makeTrackedIssue("AZ-1")]])

	const service: TrackerIssueDaemonServiceApi = {
		get: (issueId) =>
			Effect.gen(function* () {
				const issue = issues.get(issueId)
				if (issue === undefined) {
					return yield* unexpectedTrackerError(`unknown issue ${issueId}`)
				}
				return issue
			}),
		list: () => unexpectedTrackerError("unexpected list call"),
		create: () => unexpectedTrackerError("unexpected create call"),
		update: (issueId, patch) =>
			Effect.sync(() => {
				const issue = issues.get(issueId)
				if (issue === undefined) {
					throw new Error(`unknown issue ${issueId}`)
				}
				issues.set(issueId, {
					...issue,
					notes: patch.notes ?? issue.notes,
					updated_at: "2026-03-20T01:00:00.000Z",
				})
			}).pipe(Effect.catchAll(() => unexpectedTrackerError(`failed to update ${issueId}`))),
		addDependency: () => unexpectedTrackerError("unexpected addDependency call"),
		removeDependency: () => unexpectedTrackerError("unexpected removeDependency call"),
		close: () => unexpectedTrackerError("unexpected close call"),
		delete: () => unexpectedTrackerError("unexpected delete call"),
		sync: () => unexpectedTrackerError("unexpected sync call"),
	}

	return { issues, service }
}

const makeLayer = (trackerIssues: TrackerIssueDaemonServiceApi) =>
	Layer.mergeAll(
		Layer.provide(
			DaemonAttachmentService.DefaultWithoutDependencies,
			Layer.mergeAll(
				Layer.succeed(TrackerIssueDaemonService, TrackerIssueDaemonService.make(trackerIssues)),
				BunContext.layer,
			),
		),
		BunContext.layer,
	)

describe("DaemonAttachmentService", () => {
	it("stores, lists, materializes, and removes attachments while syncing issue notes", async () => {
		const tracker = makeTrackerStub()
		const projectPath = `/tmp/azedarach-daemon-attachment-${crypto.randomUUID()}`

		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const fs = yield* FileSystem.FileSystem
				const path = yield* Path.Path
				const service = yield* DaemonAttachmentService

				const fixturesDir = path.join(projectPath, "fixtures")
				yield* fs.makeDirectory(fixturesDir, { recursive: true })

				const sourcePath = path.join(fixturesDir, "example.png")
				const sourceContent = Uint8Array.from([0x89, 0x50, 0x4e, 0x47])
				yield* fs.writeFile(sourcePath, sourceContent)

				const attachment = yield* service.attachFile({
					issueId: "AZ-1",
					filePath: sourcePath,
					projectPath,
				})
				const listed = yield* service.list("AZ-1", projectPath)
				const counts = yield* service.countBatch(["AZ-1"], projectPath)
				const notesAfterAttach = tracker.issues.get("AZ-1")?.notes ?? ""

				const materializedPath = yield* service.materializePath({
					issueId: "AZ-1",
					attachmentId: attachment.id,
					projectPath,
				})
				const materializedContent = yield* fs.readFile(materializedPath)

				yield* service.remove({
					issueId: "AZ-1",
					attachmentId: attachment.id,
					projectPath,
				})
				const remaining = yield* service.list("AZ-1", projectPath)
				const notesAfterRemove = tracker.issues.get("AZ-1")?.notes ?? ""

				yield* fs.remove(projectPath, { recursive: true }).pipe(Effect.ignore)

				return {
					attachment,
					listed,
					counts,
					notesAfterAttach,
					materializedPath,
					materializedContent,
					remaining,
					notesAfterRemove,
				}
			}).pipe(Effect.provide(makeLayer(tracker.service))),
		)

		expect(result.listed).toHaveLength(1)
		expect(result.listed[0]?.id).toBe(result.attachment.id)
		expect(result.counts).toEqual({ "AZ-1": 1 })
		expect(result.notesAfterAttach).toContain(result.attachment.filename)
		expect(result.materializedPath).toContain(`/${result.attachment.id}/`)
		expect(Array.from(result.materializedContent)).toEqual([0x89, 0x50, 0x4e, 0x47])
		expect(result.remaining).toEqual([])
		expect(result.notesAfterRemove).toBe("")
	})
})
