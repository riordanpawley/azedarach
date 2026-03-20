import {
	type DaemonPullRequest,
	DaemonRpcClient,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import { Data, Effect } from "effect"

export class PrWorkflowError extends Data.TaggedError("PrWorkflowError")<{
	readonly message: string
}> {}

export interface PrWorkflowApi {
	readonly createPR: (options: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<DaemonPullRequest, PrWorkflowError>
	readonly cleanup: (options: {
		readonly issueId: string
		readonly projectPath: string
		readonly closeIssue?: boolean
	}) => Effect.Effect<void, PrWorkflowError>
	readonly mergeToMain: (options: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<void, PrWorkflowError>
	readonly checkGHCLI: () => Effect.Effect<boolean, PrWorkflowError>
}

const mapRpcError = (error: DaemonRpcClientError): PrWorkflowError =>
	new PrWorkflowError({
		message: error.message,
	})

export class PrWorkflowService extends Effect.Service<PrWorkflowService>()("PrWorkflowService", {
	effect: Effect.gen(function* () {
		const daemonRpcClient = yield* DaemonRpcClient

		return {
			createPR: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prCreate({
						issueId,
						projectPath,
					})
					.pipe(
						Effect.map((result) => result.pullRequest),
						Effect.mapError(mapRpcError),
					),
			cleanup: ({ issueId, projectPath, closeIssue }) =>
				daemonRpcClient
					.prCleanup({
						issueId,
						projectPath,
						closeIssue,
					})
					.pipe(Effect.asVoid, Effect.mapError(mapRpcError)),
			mergeToMain: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prMergeToMain({
						issueId,
						projectPath,
					})
					.pipe(Effect.asVoid, Effect.mapError(mapRpcError)),
			checkGHCLI: () =>
				daemonRpcClient.prCheckGhCli().pipe(
					Effect.map((result) => result.available),
					Effect.mapError(mapRpcError),
				),
		} satisfies PrWorkflowApi
	}),
}) {}
