import { describe, expect, it } from "bun:test"
import type { TrackedIssue } from "@azedarach/shared/rpc"
import { Effect, Layer } from "effect"
import { BackendDaemonBoardStore, BackendDaemonBoardStoreError } from "./BackendDaemonBoardStore.js"
import { BackendDaemonSessionRecovery } from "./BackendDaemonSessionRecovery.js"
import { DevServerDaemonService, type DevServerDaemonServiceApi } from "./DevServerDaemonService.js"
import {
	TrackerIssueDaemonError,
	TrackerIssueDaemonService,
	type TrackerIssueDaemonServiceApi,
} from "./TrackerIssueDaemonService.js"

const issue: TrackedIssue = {
	id: "task-1",
	title: "Board task",
	status: "in_progress",
	priority: 2,
	issue_type: "task",
	created_at: "2026-03-20T00:00:00.000Z",
	updated_at: "2026-03-20T01:00:00.000Z",
	implementations: ["ts-opentui"],
}

const unexpectedTrackerError = (): Effect.Effect<never, TrackerIssueDaemonError> =>
	Effect.fail(
		new TrackerIssueDaemonError({
			reason: "command-failed",
			message: "unexpected tracker issue call in BackendDaemonBoardStore test",
		}),
	)

const makeTrackerIssuesStub = (
	list: TrackerIssueDaemonServiceApi["list"],
): TrackerIssueDaemonServiceApi => ({
	get: () => unexpectedTrackerError(),
	list,
	create: () => unexpectedTrackerError(),
	update: () => unexpectedTrackerError(),
	addDependency: () => unexpectedTrackerError(),
	removeDependency: () => unexpectedTrackerError(),
	close: () => unexpectedTrackerError(),
	delete: () => unexpectedTrackerError(),
	sync: () => unexpectedTrackerError(),
})

const makeSessionRecoveryStub = (
	listActive: Parameters<typeof BackendDaemonSessionRecovery.make>[0]["listActive"],
): Parameters<typeof BackendDaemonSessionRecovery.make>[0] => ({
	listActive,
	recoverSession: () => Effect.void,
	updateState: () =>
		Effect.dieMessage(
			"unexpected session recovery updateState call in BackendDaemonBoardStore test",
		),
})

const makeDevServerStub = (list: DevServerDaemonServiceApi["list"]): DevServerDaemonServiceApi => ({
	status: () =>
		Effect.dieMessage("unexpected dev-server status call in BackendDaemonBoardStore test"),
	list,
	start: () =>
		Effect.dieMessage("unexpected dev-server start call in BackendDaemonBoardStore test"),
	stop: () => Effect.dieMessage("unexpected dev-server stop call in BackendDaemonBoardStore test"),
})

const makeBoardStoreTestLayer = (params: {
	readonly trackerIssues: Parameters<typeof TrackerIssueDaemonService.make>[0]
	readonly sessionRecovery: Parameters<typeof BackendDaemonSessionRecovery.make>[0]
	readonly devServers: Parameters<typeof DevServerDaemonService.make>[0]
}) =>
	Layer.provide(
		BackendDaemonBoardStore.DefaultWithoutDependencies,
		Layer.mergeAll(
			Layer.succeed(
				TrackerIssueDaemonService,
				TrackerIssueDaemonService.make(params.trackerIssues),
			),
			Layer.succeed(
				BackendDaemonSessionRecovery,
				BackendDaemonSessionRecovery.make(params.sessionRecovery),
			),
			Layer.succeed(DevServerDaemonService, DevServerDaemonService.make(params.devServers)),
		),
	)

describe("BackendDaemonBoardStore", () => {
	it("combines issues, active sessions, and running dev servers into board tasks", async () => {
		const layer = makeBoardStoreTestLayer({
			trackerIssues: makeTrackerIssuesStub((_filters, projectPath) => {
				expect(projectPath).toBe("/tmp/project")
				return Effect.succeed([issue])
			}),
			sessionRecovery: makeSessionRecoveryStub((projectPath) => {
				expect(projectPath).toBe("/tmp/project")
				return Effect.succeed([
					{
						issueId: "task-1",
						state: "busy",
						projectPath: "/tmp/project",
						tmuxSessionName: "az-task-1",
						worktreePath: "/tmp/project/.worktrees/task-1",
						startedAt: "2026-03-20T01:30:00.000Z",
					},
				])
			}),
			devServers: makeDevServerStub((request) => {
				expect(request?.projectPath).toBe("/tmp/project")
				return Effect.succeed({
					capturedAtMs: 1,
					servers: [
						{
							issueId: "task-1",
							serverName: "default",
							status: "running",
							port: 4100,
							windowName: "dev-default",
							tmuxSession: "az-task-1",
							worktreePath: "/tmp/project/.worktrees/task-1",
							projectPath: "/tmp/project",
							startedAt: "2026-03-20T01:00:00.000Z",
							error: null,
						},
						{
							issueId: "task-1",
							serverName: "preview",
							status: "stopped",
							port: null,
							windowName: null,
							tmuxSession: null,
							worktreePath: null,
							projectPath: "/tmp/project",
							startedAt: null,
							error: null,
						},
					],
				})
			}),
		})

		const tasks = await Effect.runPromise(
			Effect.gen(function* () {
				const store = yield* BackendDaemonBoardStore
				return yield* store.listBoardTasks("/tmp/project")
			}).pipe(Effect.provide(layer)),
		)

		expect(tasks).toHaveLength(1)
		expect(tasks[0]?.id).toBe("task-1")
		expect(tasks[0]?.sessionState).toBe("busy")
		expect(tasks[0]?.sessionStartedAt).toBe("2026-03-20T01:30:00.000Z")
		expect(tasks[0]?.hasTmuxSession).toBe(true)
		expect(tasks[0]?.hasWorktree).toBe(true)
		expect(tasks[0]?.hasDevServer).toBe(true)
	})

	it("wraps dependency failures as BackendDaemonBoardStoreError", async () => {
		const layer = makeBoardStoreTestLayer({
			trackerIssues: makeTrackerIssuesStub(() =>
				Effect.fail(
					new TrackerIssueDaemonError({
						reason: "command-failed",
						message: "tracker list failed",
					}),
				),
			),
			sessionRecovery: makeSessionRecoveryStub(() => Effect.succeed([])),
			devServers: makeDevServerStub(() =>
				Effect.succeed({
					capturedAtMs: 1,
					servers: [],
				}),
			),
		})

		const exit = await Effect.runPromiseExit(
			Effect.gen(function* () {
				const store = yield* BackendDaemonBoardStore
				return yield* store.listBoardTasks("/tmp/project")
			}).pipe(Effect.provide(layer)),
		)

		expect(exit._tag).toBe("Failure")
		if (exit._tag === "Failure") {
			const failure = exit.cause
			expect(failure._tag).toBe("Fail")
			if (failure._tag === "Fail") {
				expect(failure.error).toBeInstanceOf(BackendDaemonBoardStoreError)
				expect(failure.error.message).toContain("Failed to build daemon board read model")
			}
		}
	})
})
