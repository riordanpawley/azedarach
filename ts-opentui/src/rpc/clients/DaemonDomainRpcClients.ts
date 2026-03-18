import type { DaemonRpcClientApi } from "../DaemonRpcClient.js"
import type { DaemonBoardReadModelRpcClient } from "./DaemonBoardReadModelRpcClient.js"
import type { DaemonControlRpcClient } from "./DaemonControlRpcClient.js"
import type { DaemonDevServerRpcClient } from "./DaemonDevServerRpcClient.js"
import type { DaemonIssueTaskRpcClient } from "./DaemonIssueTaskRpcClient.js"
import type { DaemonQueueRpcClient } from "./DaemonQueueRpcClient.js"
import type { DaemonTaskSessionRpcClient } from "./DaemonTaskSessionRpcClient.js"

type RequiredDaemonControlRpcClient = Required<DaemonControlRpcClient>
type RequiredDaemonBoardReadModelRpcClient = Required<DaemonBoardReadModelRpcClient>
type RequiredDaemonTaskSessionRpcClient = Required<DaemonTaskSessionRpcClient>
type RequiredDaemonDevServerRpcClient = Required<DaemonDevServerRpcClient>
type RequiredDaemonQueueRpcClient = Required<DaemonQueueRpcClient>
type RequiredDaemonIssueTaskRpcClient = Required<DaemonIssueTaskRpcClient>

export interface DaemonDomainRpcClients {
	readonly control: RequiredDaemonControlRpcClient
	readonly board: RequiredDaemonBoardReadModelRpcClient
	readonly taskSession: RequiredDaemonTaskSessionRpcClient
	readonly devServer: RequiredDaemonDevServerRpcClient
	readonly queue: RequiredDaemonQueueRpcClient
	readonly issueTask: RequiredDaemonIssueTaskRpcClient
}

const requireDaemonRpcMethod = <TMethod extends keyof DaemonRpcClientApi>(
	client: DaemonRpcClientApi,
	method: TMethod,
): NonNullable<DaemonRpcClientApi[TMethod]> => {
	const value = client[method]
	if (value === undefined) {
		throw new Error(`Daemon RPC client is missing required method: ${String(method)}`)
	}
	return value
}

export const composeDaemonDomainRpcClients = (
	client: DaemonRpcClientApi,
): DaemonDomainRpcClients => ({
	control: {
		status: requireDaemonRpcMethod(client, "status"),
		health: requireDaemonRpcMethod(client, "health"),
		logs: requireDaemonRpcMethod(client, "logs"),
		stop: requireDaemonRpcMethod(client, "stop"),
		restart: requireDaemonRpcMethod(client, "restart"),
		attach: requireDaemonRpcMethod(client, "attach"),
		reconnect: requireDaemonRpcMethod(client, "reconnect"),
		heartbeat: requireDaemonRpcMethod(client, "heartbeat"),
	},
	board: {
		eventStream: requireDaemonRpcMethod(client, "eventStream"),
		sessionSnapshot: requireDaemonRpcMethod(client, "sessionSnapshot"),
		boardReadModel: requireDaemonRpcMethod(client, "boardReadModel"),
	},
	taskSession: {
		sessionStart: requireDaemonRpcMethod(client, "sessionStart"),
		sessionStop: requireDaemonRpcMethod(client, "sessionStop"),
		sessionPause: requireDaemonRpcMethod(client, "sessionPause"),
		sessionResume: requireDaemonRpcMethod(client, "sessionResume"),
		sessionRecover: requireDaemonRpcMethod(client, "sessionRecover"),
		sessionUpdateState: requireDaemonRpcMethod(client, "sessionUpdateState"),
	},
	devServer: {
		devServerStatus: requireDaemonRpcMethod(client, "devServerStatus"),
		devServerList: requireDaemonRpcMethod(client, "devServerList"),
		devServerStart: requireDaemonRpcMethod(client, "devServerStart"),
		devServerStop: requireDaemonRpcMethod(client, "devServerStop"),
	},
	queue: {
		queueEnqueue: requireDaemonRpcMethod(client, "queueEnqueue"),
		queueQuery: requireDaemonRpcMethod(client, "queueQuery"),
		queueCancel: requireDaemonRpcMethod(client, "queueCancel"),
	},
	issueTask: {
		issueCreate: requireDaemonRpcMethod(client, "issueCreate"),
		issueUpdate: requireDaemonRpcMethod(client, "issueUpdate"),
		issueDelete: requireDaemonRpcMethod(client, "issueDelete"),
		issueShow: requireDaemonRpcMethod(client, "issueShow"),
		issueEpicChildren: requireDaemonRpcMethod(client, "issueEpicChildren"),
		issueEpicWithChildren: requireDaemonRpcMethod(client, "issueEpicWithChildren"),
		issueParentEpic: requireDaemonRpcMethod(client, "issueParentEpic"),
		issueImplementationRegistry: requireDaemonRpcMethod(client, "issueImplementationRegistry"),
	},
})
