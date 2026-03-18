import type { DaemonRpcClientApi } from "../DaemonRpcClient.js"

export type DaemonQueueRpcClient = Pick<
	DaemonRpcClientApi,
	"queueEnqueue" | "queueQuery" | "queueCancel"
>
