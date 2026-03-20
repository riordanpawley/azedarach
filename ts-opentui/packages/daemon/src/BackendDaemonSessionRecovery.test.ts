import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import { BackendDaemonSessionRecovery } from "./BackendDaemonSessionRecovery.js"

const withSessionRecovery = <A, E>(
	effect: Effect.Effect<A, E, BackendDaemonSessionRecovery>,
): Promise<A> =>
	Effect.runPromise(effect.pipe(Effect.provide(BackendDaemonSessionRecovery.Default)))

describe("BackendDaemonSessionRecovery", () => {
	it("updateState persists metadata for listActive", async () => {
		const sessions = await withSessionRecovery(
			Effect.gen(function* () {
				const recovery = yield* BackendDaemonSessionRecovery
				yield* recovery.updateState({
					issueId: "az-1",
					state: "busy",
					projectPath: "/tmp/project",
					tmuxSessionName: "az-1",
					worktreePath: "/tmp/project/.worktrees/az-1",
					startedAt: "2026-03-20T02:00:00.000Z",
				})
				return yield* recovery.listActive("/tmp/project")
			}),
		)

		expect(sessions).toEqual([
			{
				issueId: "az-1",
				state: "busy",
				projectPath: "/tmp/project",
				tmuxSessionName: "az-1",
				worktreePath: "/tmp/project/.worktrees/az-1",
				startedAt: "2026-03-20T02:00:00.000Z",
			},
		])
	})

	it("preserves prior metadata when update fields are omitted", async () => {
		const session = await withSessionRecovery(
			Effect.gen(function* () {
				const recovery = yield* BackendDaemonSessionRecovery
				yield* recovery.updateState({
					issueId: "az-1",
					state: "busy",
					projectPath: "/tmp/project",
					tmuxSessionName: "az-1",
					worktreePath: "/tmp/project/.worktrees/az-1",
					startedAt: "2026-03-20T02:00:00.000Z",
				})
				return yield* recovery.updateState({
					issueId: "az-1",
					state: "waiting",
					projectPath: "/tmp/project",
				})
			}),
		)

		expect(session).toEqual({
			issueId: "az-1",
			state: "waiting",
			projectPath: "/tmp/project",
			tmuxSessionName: "az-1",
			worktreePath: "/tmp/project/.worktrees/az-1",
			startedAt: "2026-03-20T02:00:00.000Z",
		})
	})

	it("rejects first non-idle update when tmuxSessionName is missing", async () => {
		const exit = await Effect.runPromiseExit(
			Effect.gen(function* () {
				const recovery = yield* BackendDaemonSessionRecovery
				return yield* recovery.updateState({
					issueId: "az-1",
					state: "busy",
					projectPath: "/tmp/project",
				})
			}).pipe(Effect.provide(BackendDaemonSessionRecovery.Default)),
		)

		expect(exit._tag).toBe("Failure")
		if (exit._tag === "Failure" && exit.cause._tag === "Fail") {
			expect(exit.cause.error._tag).toBe("BackendDaemonSessionRecoveryError")
			expect(exit.cause.error.reason).toBe("missing-session-metadata")
		}
	})

	it("removes idle sessions from listActive", async () => {
		const sessions = await withSessionRecovery(
			Effect.gen(function* () {
				const recovery = yield* BackendDaemonSessionRecovery
				yield* recovery.updateState({
					issueId: "az-1",
					state: "busy",
					projectPath: "/tmp/project",
					tmuxSessionName: "az-1",
				})
				yield* recovery.updateState({
					issueId: "az-1",
					state: "idle",
					projectPath: "/tmp/project",
				})
				return yield* recovery.listActive("/tmp/project")
			}),
		)

		expect(sessions).toEqual([])
	})

	it("sorts listActive by issueId", async () => {
		const sessions = await withSessionRecovery(
			Effect.gen(function* () {
				const recovery = yield* BackendDaemonSessionRecovery
				yield* recovery.updateState({
					issueId: "az-b",
					state: "busy",
					projectPath: "/tmp/project",
					tmuxSessionName: "az-b",
				})
				yield* recovery.updateState({
					issueId: "az-a",
					state: "busy",
					projectPath: "/tmp/project",
					tmuxSessionName: "az-a",
				})
				yield* recovery.updateState({
					issueId: "az-c",
					state: "waiting",
					projectPath: "/tmp/project",
					tmuxSessionName: "az-c",
				})
				return yield* recovery.listActive("/tmp/project")
			}),
		)

		expect(sessions.map((session) => session.issueId)).toEqual(["az-a", "az-b", "az-c"])
	})
})
