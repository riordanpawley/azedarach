import { Data, Effect, Option } from "effect"
import {
	composeDaemonDomainRpcClients,
	type DaemonDomainRpcClients,
} from "../rpc/clients/DaemonDomainRpcClients.js"
import { DaemonRpcClient } from "../rpc/DaemonRpcClient.js"

class RequiredDaemonDomainRpcClientError extends Data.TaggedError(
	"RequiredDaemonDomainRpcClientError",
)<{
	readonly message: string
}> {}

const errorMessage = (error: unknown): string =>
	error instanceof Error ? error.message : String(error)

type RequiredDaemonDomainMethods<TMethods> = {
	readonly [K in keyof TMethods]-?: NonNullable<TMethods[K]>
}

export interface RequiredDaemonDomainRpcClients
	extends Omit<DaemonDomainRpcClients, "taskSession" | "issueTask"> {
	readonly taskSession: RequiredDaemonDomainMethods<DaemonDomainRpcClients["taskSession"]>
	readonly issueTask: RequiredDaemonDomainMethods<DaemonDomainRpcClients["issueTask"]>
}

export type RequiredDaemonIssueTaskRpcClient = RequiredDaemonDomainRpcClients["issueTask"]

const requireDomainMethod = <TMethod>(method: TMethod | undefined, methodName: string): TMethod => {
	if (method === undefined) {
		throw new RequiredDaemonDomainRpcClientError({
			message: `Required daemon RPC method is unavailable: ${methodName}`,
		})
	}
	return method
}

const ensureRequiredDaemonDomainRpcClients = (
	domains: DaemonDomainRpcClients,
): RequiredDaemonDomainRpcClients => ({
	...domains,
	taskSession: {
		sessionStart: requireDomainMethod(domains.taskSession.sessionStart, "sessionStart"),
		sessionStop: requireDomainMethod(domains.taskSession.sessionStop, "sessionStop"),
		sessionPause: requireDomainMethod(domains.taskSession.sessionPause, "sessionPause"),
		sessionResume: requireDomainMethod(domains.taskSession.sessionResume, "sessionResume"),
		sessionRecover: requireDomainMethod(domains.taskSession.sessionRecover, "sessionRecover"),
		sessionUpdateState: requireDomainMethod(
			domains.taskSession.sessionUpdateState,
			"sessionUpdateState",
		),
	},
	issueTask: {
		issueCreate: requireDomainMethod(domains.issueTask.issueCreate, "issueCreate"),
		issueUpdate: requireDomainMethod(domains.issueTask.issueUpdate, "issueUpdate"),
		issueDelete: requireDomainMethod(domains.issueTask.issueDelete, "issueDelete"),
		issueShow: requireDomainMethod(domains.issueTask.issueShow, "issueShow"),
		issueEpicChildren: requireDomainMethod(
			domains.issueTask.issueEpicChildren,
			"issueEpicChildren",
		),
		issueEpicWithChildren: requireDomainMethod(
			domains.issueTask.issueEpicWithChildren,
			"issueEpicWithChildren",
		),
		issueParentEpic: requireDomainMethod(domains.issueTask.issueParentEpic, "issueParentEpic"),
		issueImplementationRegistry: requireDomainMethod(
			domains.issueTask.issueImplementationRegistry,
			"issueImplementationRegistry",
		),
	},
})

export const getRequiredDaemonDomainRpcClients = (): Effect.Effect<
	RequiredDaemonDomainRpcClients,
	RequiredDaemonDomainRpcClientError,
	never
> =>
	Effect.serviceOption(DaemonRpcClient).pipe(
		Effect.flatMap(
			Option.match({
				onNone: () =>
					Effect.fail(
						new RequiredDaemonDomainRpcClientError({
							message: "Daemon RPC client is unavailable",
						}),
					),
				onSome: (daemonRpcClient) =>
					Effect.try({
						try: () =>
							ensureRequiredDaemonDomainRpcClients(composeDaemonDomainRpcClients(daemonRpcClient)),
						catch: (error) =>
							new RequiredDaemonDomainRpcClientError({
								message: `Daemon RPC domain composition failed: ${errorMessage(error)}`,
							}),
					}),
			}),
		),
	)
