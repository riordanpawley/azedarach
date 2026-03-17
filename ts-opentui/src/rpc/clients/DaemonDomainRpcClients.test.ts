import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import type { DaemonRpcClientApi } from "../DaemonRpcClient.js"
import { composeDaemonDomainRpcClients } from "./DaemonDomainRpcClients.js"

const buildRpcClientStub = (): DaemonRpcClientApi => ({
	status: () => Effect.dieMessage("stub"),
	health: () => Effect.dieMessage("stub"),
	logs: () => Effect.dieMessage("stub"),
	stop: () => Effect.dieMessage("stub"),
	restart: () => Effect.dieMessage("stub"),
	attach: () => Effect.dieMessage("stub"),
	reconnect: () => Effect.dieMessage("stub"),
	heartbeat: () => Effect.dieMessage("stub"),
	eventStream: () => Effect.dieMessage("stub"),
	sessionSnapshot: () => Effect.dieMessage("stub"),
	boardReadModel: () => Effect.dieMessage("stub"),
	sessionStart: () => Effect.dieMessage("stub"),
	sessionStop: () => Effect.dieMessage("stub"),
	sessionPause: () => Effect.dieMessage("stub"),
	sessionResume: () => Effect.dieMessage("stub"),
	sessionRecover: () => Effect.dieMessage("stub"),
	sessionUpdateState: () => Effect.dieMessage("stub"),
	devServerStatus: () => Effect.dieMessage("stub"),
	devServerList: () => Effect.dieMessage("stub"),
	devServerStart: () => Effect.dieMessage("stub"),
	devServerStop: () => Effect.dieMessage("stub"),
	queueEnqueue: () => Effect.dieMessage("stub"),
	queueQuery: () => Effect.dieMessage("stub"),
	queueCancel: () => Effect.dieMessage("stub"),
	issueCreate: () => Effect.dieMessage("stub"),
	issueUpdate: () => Effect.dieMessage("stub"),
	issueDelete: () => Effect.dieMessage("stub"),
	issueShow: () => Effect.dieMessage("stub"),
	issueEpicChildren: () => Effect.dieMessage("stub"),
	issueEpicWithChildren: () => Effect.dieMessage("stub"),
	issueParentEpic: () => Effect.dieMessage("stub"),
	issueImplementationRegistry: () => Effect.dieMessage("stub"),
})

describe("composeDaemonDomainRpcClients", () => {
	it("projects monolithic client methods into bounded domain clients", () => {
		const client = buildRpcClientStub()
		const domains = composeDaemonDomainRpcClients(client)

		expect(domains.control.status).toBe(client.status)
		expect(domains.control.restart).toBe(client.restart)
		expect(domains.board.boardReadModel).toBe(client.boardReadModel)
		expect(domains.board.eventStream).toBe(client.eventStream)
		expect(domains.taskSession.sessionStart).toBe(client.sessionStart)
		expect(domains.taskSession.sessionUpdateState).toBe(client.sessionUpdateState)
		expect(domains.devServer.devServerList).toBe(client.devServerList)
		expect(domains.queue.queueCancel).toBe(client.queueCancel)
		expect(domains.issueTask.issueCreate).toBe(client.issueCreate)
		expect(domains.issueTask.issueEpicWithChildren).toBe(client.issueEpicWithChildren)
	})
})
