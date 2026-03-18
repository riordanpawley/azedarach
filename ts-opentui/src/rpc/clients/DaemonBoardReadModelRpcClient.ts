import type { DaemonRpcClientApi } from "../DaemonRpcClient.js"

export type DaemonBoardReadModelRpcClient = Pick<
	DaemonRpcClientApi,
	"eventStream" | "sessionSnapshot" | "boardReadModel"
>
