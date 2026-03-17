import type { DaemonRpcClientApi } from "../DaemonRpcClient.js"
import type { DaemonBoardReadModelRpcClient } from "./DaemonBoardReadModelRpcClient.js"
import type { DaemonControlRpcClient } from "./DaemonControlRpcClient.js"
import type { DaemonDevServerRpcClient } from "./DaemonDevServerRpcClient.js"
import type { DaemonQueueRpcClient } from "./DaemonQueueRpcClient.js"
import type { DaemonTaskSessionRpcClient } from "./DaemonTaskSessionRpcClient.js"

export interface DaemonDomainRpcClients {
	readonly control: DaemonControlRpcClient
	readonly board: DaemonBoardReadModelRpcClient
	readonly taskSession: DaemonTaskSessionRpcClient
	readonly devServer: DaemonDevServerRpcClient
	readonly queue: DaemonQueueRpcClient
}

export const composeDaemonDomainRpcClients = (
	client: DaemonRpcClientApi,
): DaemonDomainRpcClients => ({
	control: {
		status: client.status,
		health: client.health,
		logs: client.logs,
		stop: client.stop,
		restart: client.restart,
		attach: client.attach,
		reconnect: client.reconnect,
		heartbeat: client.heartbeat,
	},
	board: {
		eventStream: client.eventStream,
		sessionSnapshot: client.sessionSnapshot,
		boardReadModel: client.boardReadModel,
	},
	taskSession: {
		sessionStart: client.sessionStart,
		sessionStop: client.sessionStop,
		sessionPause: client.sessionPause,
		sessionResume: client.sessionResume,
		sessionRecover: client.sessionRecover,
		sessionUpdateState: client.sessionUpdateState,
	},
	devServer: {
		devServerStatus: client.devServerStatus,
		devServerList: client.devServerList,
		devServerStart: client.devServerStart,
		devServerStop: client.devServerStop,
	},
	queue: {
		queueEnqueue: client.queueEnqueue,
		queueQuery: client.queueQuery,
		queueCancel: client.queueCancel,
	},
})
