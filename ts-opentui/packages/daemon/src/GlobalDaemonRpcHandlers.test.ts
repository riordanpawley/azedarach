import { describe, expect, it } from "bun:test"
import { DAEMON_RPC_PROTOCOL_VERSION, type DaemonBoardTask } from "@azedarach/shared/rpc"
import { Effect } from "effect"
import { makeDaemonBoardReadModelRpcHandler } from "./GlobalDaemonRpcHandlers.js"

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
