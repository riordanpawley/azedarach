import type { DaemonRpcClientApi } from "../DaemonRpcClient.js"

export type DaemonDevServerRpcClient = Pick<
	DaemonRpcClientApi,
	"devServerStatus" | "devServerList" | "devServerStart" | "devServerStop"
>
