import type { DaemonRpcClientApi } from "../DaemonRpcClient.js"

export type DaemonControlRpcClient = Pick<
	DaemonRpcClientApi,
	"status" | "health" | "logs" | "stop" | "restart" | "attach" | "reconnect" | "heartbeat"
>
