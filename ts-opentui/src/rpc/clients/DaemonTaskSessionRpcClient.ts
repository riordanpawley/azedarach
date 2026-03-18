import type { DaemonRpcClientApi } from "../DaemonRpcClient.js"

export type DaemonTaskSessionRpcClient = Pick<
	DaemonRpcClientApi,
	| "sessionStart"
	| "sessionStop"
	| "sessionPause"
	| "sessionResume"
	| "sessionRecover"
	| "sessionUpdateState"
>
