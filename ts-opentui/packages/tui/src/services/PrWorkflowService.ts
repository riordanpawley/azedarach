import {
	type DaemonPullRequest,
	DaemonRpcClient,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import { Data, Effect } from "effect"

export class PrWorkflowError extends Data.TaggedError("PrWorkflowError")<{
	readonly reason:
		| "command-failed"
		| "config-error"
		| "issue-tracker-error"
		| "merge-conflict"
		| "pr-disabled"
		| "validation-failed"
		| "worktree-missing"
		| "unknown"
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
	readonly updateFromBase: (options: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<void, PrWorkflowError>
	readonly mergeBaseIntoBranch: (options: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<void, PrWorkflowError>
	readonly abortMerge: (options: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<void, PrWorkflowError>
	readonly checkGHCLI: () => Effect.Effect<boolean, PrWorkflowError>
}

const mapRpcError = (error: DaemonRpcClientError): PrWorkflowError =>
	new PrWorkflowError({
		reason:
			error._tag === "DaemonRpcActionError" &&
			(error.code === "command-failed" ||
				error.code === "config-error" ||
				error.code === "issue-tracker-error" ||
				error.code === "merge-conflict" ||
				error.code === "pr-disabled" ||
				error.code === "validation-failed" ||
				error.code === "worktree-missing")
				? error.code
				: "unknown",
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
			updateFromBase: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prUpdateFromBase({
						issueId,
						projectPath,
					})
					.pipe(Effect.asVoid, Effect.mapError(mapRpcError)),
			mergeBaseIntoBranch: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prMergeBaseIntoBranch({
						issueId,
						projectPath,
					})
					.pipe(Effect.asVoid, Effect.mapError(mapRpcError)),
			abortMerge: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prAbortMerge({
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
