import type { DaemonRpcClientApi } from "@azedarach/shared/rpc"
import { Context } from "effect"

export class BoardRefreshDaemonRpcClient extends Context.Tag("BoardRefreshDaemonRpcClient")<
	BoardRefreshDaemonRpcClient,
	DaemonRpcClientApi
>() {}
