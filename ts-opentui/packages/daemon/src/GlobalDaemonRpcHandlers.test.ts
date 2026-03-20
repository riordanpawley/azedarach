import { describe, expect, it } from "bun:test"
import { DAEMON_RPC_PROTOCOL_VERSION, type DaemonBoardTask } from "@azedarach/shared/rpc"
import { Effect } from "effect"
import type { BackendDaemonSessionSnapshot } from "./BackendDaemonSessionRecovery.js"
import { DaemonSessionError } from "./DaemonSessionService.js"
import {
	makeDaemonBoardReadModelRpcHandler,
	makeDaemonSessionPauseRpcHandler,
	makeDaemonSessionRecoverRpcHandler,
	makeDaemonSessionResumeRpcHandler,
	makeDaemonSessionSnapshotRpcHandler,
	makeDaemonSessionStartRpcHandler,
	makeDaemonSessionStopRpcHandler,
	makeDaemonSessionUpdateStateRpcHandler,
} from "./GlobalDaemonRpcHandlers.js"

const task: DaemonBoardTask = {
	id: "task-1",
	title: "Board task",
	status: "in_progress",
	priority: 2,
	issue_type: "task",
	created_at: "2026-03-20T00:00:00.000Z",
	updated_at: "2026-03-20T01:00:00.000Z",
	implementations: ["ts-opentui"],
	sessionState: "busy",
}

const sessionSnapshot: BackendDaemonSessionSnapshot = {
	issueId: "task-1",
	state: "busy",
	projectPath: "/tmp/project",
	tmuxSessionName: "az-task-1",
	worktreePath: "/tmp/project/.worktrees/task-1",
	startedAt: "2026-03-20T02:00:00.000Z",
}

describe("makeDaemonBoardReadModelRpcHandler", () => {
	it("maps daemon control board snapshots into the RPC envelope", async () => {
		const handler = makeDaemonBoardReadModelRpcHandler({
			boardReadModel: (request) => {
				expect(request.projectPath).toBe("/tmp/project")
				return Effect.succeed({
					capturedAtMs: 123,
					projectPath: "/tmp/project",
					tasks: [task],
				})
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)

		expect(result).toEqual({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			capturedAtMs: 123,
			projectPath: "/tmp/project",
			tasks: [task],
		})
	})

	it("maps board read model failures into daemon rpc action errors", async () => {
		const handler = makeDaemonBoardReadModelRpcHandler({
			boardReadModel: () => Effect.fail(new Error("board read failed")),
		})

		const exit = await Effect.runPromiseExit(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)

		expect(exit._tag).toBe("Failure")
		if (exit._tag === "Failure" && exit.cause._tag === "Fail") {
			expect(exit.cause.error).toEqual({
				_tag: "DaemonRpcActionError",
				action: "boardReadModel",
				code: "daemon-operation-failed",
				message: "board read failed",
			})
		}
	})
})

describe("makeDaemonSessionSnapshotRpcHandler", () => {
	it("maps daemon session telemetry into the RPC envelope", async () => {
		const handler = makeDaemonSessionSnapshotRpcHandler({
			listActive: (projectPath) => {
				expect(projectPath).toBe("/tmp/project")
				return Effect.succeed([
					{
						issueId: "task-1",
						state: "waiting",
						projectPath,
						tmuxSessionName: "az-task-1",
						worktreePath: "/tmp/project/.worktrees/task-1",
						startedAt: "2026-03-20T02:00:00.000Z",
					},
				])
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)

		expect(result.sessions[0]).toEqual({
			issueId: "task-1",
			state: "waiting",
			projectPath: "/tmp/project",
			tmuxSessionName: "az-task-1",
			worktreePath: "/tmp/project/.worktrees/task-1",
			startedAt: "2026-03-20T02:00:00.000Z",
		})
	})
})

describe("session mutation rpc handlers", () => {
	it("maps session start results into the RPC envelope", async () => {
		const handler = makeDaemonSessionStartRpcHandler({
			start: (request) => {
				expect(request).toEqual({
					issueId: "task-1",
					projectPath: "/tmp/project",
				})
				return Effect.succeed(sessionSnapshot)
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(result.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(result.session).toEqual(sessionSnapshot)
		expect(typeof result.capturedAtMs).toBe("number")
	})

	it("maps session stop results into the RPC envelope", async () => {
		const handler = makeDaemonSessionStopRpcHandler({
			stop: (request) => {
				expect(request.issueId).toBe("task-1")
				expect(request.projectPath).toBe("/tmp/project")
				return Effect.succeed(sessionSnapshot)
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(result.session).toEqual(sessionSnapshot)
		expect(result.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(typeof result.capturedAtMs).toBe("number")
	})

	it("maps session pause results into the RPC envelope", async () => {
		const handler = makeDaemonSessionPauseRpcHandler({
			pause: (request) => {
				expect(request.issueId).toBe("task-1")
				expect(request.projectPath).toBe("/tmp/project")
				return Effect.succeed(sessionSnapshot)
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(result.session.state).toBe("busy")
	})

	it("maps session resume results into the RPC envelope", async () => {
		const handler = makeDaemonSessionResumeRpcHandler({
			resume: (request) => {
				expect(request.issueId).toBe("task-1")
				expect(request.projectPath).toBe("/tmp/project")
				return Effect.succeed(sessionSnapshot)
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(result.session.tmuxSessionName).toBe("az-task-1")
	})

	it("maps session recover results into the RPC envelope", async () => {
		const handler = makeDaemonSessionRecoverRpcHandler({
			recover: (request) => {
				expect(request.issueId).toBe("task-1")
				expect(request.projectPath).toBe("/tmp/project")
				return Effect.succeed(sessionSnapshot)
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(result.session.worktreePath).toBe("/tmp/project/.worktrees/task-1")
	})

	it("maps session errors into daemon rpc action errors", async () => {
		const handler = makeDaemonSessionStartRpcHandler({
			start: () =>
				Effect.fail(
					new DaemonSessionError({
						reason: "session-limit",
						message: "Maximum session limit reached",
					}),
				),
		})

		const exit = await Effect.runPromiseExit(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(exit._tag).toBe("Failure")
		if (exit._tag === "Failure" && exit.cause._tag === "Fail") {
			expect(exit.cause.error).toEqual({
				_tag: "DaemonRpcActionError",
				action: "sessionStart",
				code: "session-limit",
				message: "Maximum session limit reached",
			})
		}
	})
})

describe("makeDaemonSessionUpdateStateRpcHandler", () => {
	it("persists daemon session metadata updates through the recovery service", async () => {
		const handler = makeDaemonSessionUpdateStateRpcHandler({
			updateState: (update) => {
				expect(update.issueId).toBe("task-1")
				expect(update.tmuxSessionName).toBe("az-task-1")
				return Effect.succeed({
					issueId: update.issueId,
					state: update.state,
					projectPath: update.projectPath,
					tmuxSessionName: update.tmuxSessionName ?? "az-task-1",
					worktreePath: update.worktreePath ?? null,
					startedAt: update.startedAt ?? null,
				})
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				state: "busy",
				projectPath: "/tmp/project",
				tmuxSessionName: "az-task-1",
				worktreePath: "/tmp/project/.worktrees/task-1",
				startedAt: "2026-03-20T02:00:00.000Z",
			}),
		)

		expect(result.session).toEqual({
			issueId: "task-1",
			state: "busy",
			projectPath: "/tmp/project",
			tmuxSessionName: "az-task-1",
			worktreePath: "/tmp/project/.worktrees/task-1",
			startedAt: "2026-03-20T02:00:00.000Z",
		})
	})
})
