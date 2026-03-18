import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import type { DaemonRpcClientApi } from "../DaemonRpcClient.js"
import { composeDaemonDomainRpcClients } from "./DaemonDomainRpcClients.js"

const buildRpcClientStub = (
	missingMethods: ReadonlySet<keyof DaemonRpcClientApi> = new Set(),
): DaemonRpcClientApi => ({
	status: () => Effect.dieMessage("stub"),
	health: () => Effect.dieMessage("stub"),
	logs: () => Effect.dieMessage("stub"),
	stop: () => Effect.dieMessage("stub"),
	restart: () => Effect.dieMessage("stub"),
	attach: () => Effect.dieMessage("stub"),
	reconnect: () => Effect.dieMessage("stub"),
	heartbeat: () => Effect.dieMessage("stub"),
	...(missingMethods.has("eventStream") ? {} : { eventStream: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("sessionSnapshot")
		? {}
		: { sessionSnapshot: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("boardReadModel")
		? {}
		: { boardReadModel: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("sessionStart") ? {} : { sessionStart: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("sessionStop") ? {} : { sessionStop: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("sessionPause") ? {} : { sessionPause: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("sessionResume")
		? {}
		: { sessionResume: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("sessionRecover")
		? {}
		: { sessionRecover: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("sessionUpdateState")
		? {}
		: { sessionUpdateState: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("devServerStatus")
		? {}
		: { devServerStatus: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("devServerList")
		? {}
		: { devServerList: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("devServerStart")
		? {}
		: { devServerStart: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("devServerStop")
		? {}
		: { devServerStop: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("queueEnqueue") ? {} : { queueEnqueue: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("queueQuery") ? {} : { queueQuery: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("queueCancel") ? {} : { queueCancel: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("issueCreate") ? {} : { issueCreate: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("issueUpdate") ? {} : { issueUpdate: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("issueDelete") ? {} : { issueDelete: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("issueShow") ? {} : { issueShow: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("issueEpicChildren")
		? {}
		: { issueEpicChildren: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("issueEpicWithChildren")
		? {}
		: { issueEpicWithChildren: () => Effect.dieMessage("stub") }),
	...(missingMethods.has("issueParentEpic")
		? {}
		: { issueParentEpic: () => Effect.dieMessage("stub") }),
	issueImplementationRegistry: () => Effect.dieMessage("stub"),
})

const requireRpcMethod = <TMethod>(value: TMethod | undefined, method: string): TMethod => {
	if (value === undefined) {
		throw new Error(`Test stub is missing method: ${method}`)
	}
	return value!
}

describe("composeDaemonDomainRpcClients", () => {
	it("projects monolithic client methods into bounded domain clients", () => {
		const client = buildRpcClientStub()
		const domains = composeDaemonDomainRpcClients(client)
		const eventStream = requireRpcMethod(client.eventStream, "eventStream")
		const sessionSnapshot = requireRpcMethod(client.sessionSnapshot, "sessionSnapshot")
		const boardReadModel = requireRpcMethod(client.boardReadModel, "boardReadModel")
		const sessionStart = requireRpcMethod(client.sessionStart, "sessionStart")
		const sessionUpdateState = requireRpcMethod(client.sessionUpdateState, "sessionUpdateState")
		const devServerList = requireRpcMethod(client.devServerList, "devServerList")
		const queueCancel = requireRpcMethod(client.queueCancel, "queueCancel")
		const issueCreate = requireRpcMethod(client.issueCreate, "issueCreate")
		const issueEpicWithChildren = requireRpcMethod(
			client.issueEpicWithChildren,
			"issueEpicWithChildren",
		)

		expect(domains.control.status).toBe(client.status)
		expect(domains.control.restart).toBe(client.restart)
		expect(domains.board.boardReadModel).toBe(boardReadModel)
		expect(domains.board.eventStream).toBe(eventStream)
		expect(domains.board.sessionSnapshot).toBe(sessionSnapshot)
		expect(domains.taskSession.sessionStart).toBe(sessionStart)
		expect(domains.taskSession.sessionUpdateState).toBe(sessionUpdateState)
		expect(domains.devServer.devServerList).toBe(devServerList)
		expect(domains.queue.queueCancel).toBe(queueCancel)
		expect(domains.issueTask.issueCreate).toBe(issueCreate)
		expect(domains.issueTask.issueEpicWithChildren).toBe(issueEpicWithChildren)
		expect(Object.keys(domains).sort()).toEqual([
			"board",
			"control",
			"devServer",
			"issueTask",
			"queue",
			"taskSession",
		])
	})

	it("fails fast when a required domain method is missing", () => {
		const client = buildRpcClientStub(new Set(["issueCreate"]))

		expect(() => composeDaemonDomainRpcClients(client)).toThrow(
			"Daemon RPC client is missing required method: issueCreate",
		)
	})

	it("fails fast for missing methods in each TUI domain", () => {
		const cases: ReadonlyArray<keyof DaemonRpcClientApi> = [
			"boardReadModel",
			"sessionStart",
			"devServerStatus",
			"queueEnqueue",
			"issueEpicChildren",
		]

		for (const missingMethod of cases) {
			const client = buildRpcClientStub(new Set([missingMethod]))

			expect(() => composeDaemonDomainRpcClients(client)).toThrow(
				`Daemon RPC client is missing required method: ${missingMethod}`,
			)
		}
	})
})
