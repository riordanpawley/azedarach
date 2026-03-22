import {
	type DaemonPullRequest,
	DaemonRpcClient,
	type DaemonRpcClientError,
	daemonRpcMethodUnavailableError,
	invokeOptionalDaemonRpcMethod,
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
	readonly checkMergeConflicts: (options: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<
		{
			readonly hasConflictRisk: boolean
			readonly conflictingFiles: ReadonlyArray<string>
			readonly baseBranch: string
			readonly issueBranch: string
		},
		PrWorkflowError
	>
	readonly checkUncommittedChanges: (options: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<
		{
			readonly hasUncommittedChanges: boolean
			readonly changedFiles: ReadonlyArray<string>
		},
		PrWorkflowError
	>
	readonly checkBranchBehindBase: (options: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<
		{
			readonly behind: number
			readonly ahead: number
			readonly baseBranch: string
		},
		PrWorkflowError
	>
	readonly getEffectiveBaseBranchForIssue: (options: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<
		{
			readonly baseBranch: string
			readonly parentEpicId: string | undefined
		},
		PrWorkflowError
	>
	readonly mergeIssueIntoIssue: (options: {
		readonly sourceIssueId: string
		readonly targetIssueId: string
		readonly projectPath: string
	}) => Effect.Effect<void, PrWorkflowError>
	readonly getTargetBranch: (
		issueId: string,
		projectPath: string,
	) => Effect.Effect<
		{
			readonly targetBranch: string
			readonly isEpicChild: boolean
		},
		PrWorkflowError
	>
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

const missingDaemonPrRpcMethodError = (methodName: string): PrWorkflowError =>
	new PrWorkflowError({
		reason: "unknown",
		message: daemonRpcMethodUnavailableError(methodName).message,
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
			checkMergeConflicts: ({ issueId, projectPath }) =>
				invokeOptionalDaemonRpcMethod({
					method: daemonRpcClient.prCheckMergeConflicts,
					methodName: "prCheckMergeConflicts",
					request: {
						issueId,
						projectPath,
					},
					onUnavailable: missingDaemonPrRpcMethodError,
					onError: mapRpcError,
				}).pipe(
					Effect.map((result) => ({
						hasConflictRisk: result.hasConflictRisk,
						conflictingFiles: result.conflictingFiles,
						baseBranch: result.baseBranch,
						issueBranch: result.issueBranch,
					})),
				),
			checkUncommittedChanges: ({ issueId, projectPath }) =>
				invokeOptionalDaemonRpcMethod({
					method: daemonRpcClient.prCheckUncommittedChanges,
					methodName: "prCheckUncommittedChanges",
					request: {
						issueId,
						projectPath,
					},
					onUnavailable: missingDaemonPrRpcMethodError,
					onError: mapRpcError,
				}).pipe(
					Effect.map((result) => ({
						hasUncommittedChanges: result.hasUncommittedChanges,
						changedFiles: result.changedFiles,
					})),
				),
			checkBranchBehindBase: ({ issueId, projectPath }) =>
				invokeOptionalDaemonRpcMethod({
					method: daemonRpcClient.prCheckBranchBehindBase,
					methodName: "prCheckBranchBehindBase",
					request: {
						issueId,
						projectPath,
					},
					onUnavailable: missingDaemonPrRpcMethodError,
					onError: mapRpcError,
				}).pipe(
					Effect.map((result) => ({
						behind: result.behind,
						ahead: result.ahead,
						baseBranch: result.baseBranch,
					})),
				),
			getEffectiveBaseBranchForIssue: ({ issueId, projectPath }) =>
				invokeOptionalDaemonRpcMethod({
					method: daemonRpcClient.prGetEffectiveBaseBranch,
					methodName: "prGetEffectiveBaseBranch",
					request: {
						issueId,
						projectPath,
					},
					onUnavailable: missingDaemonPrRpcMethodError,
					onError: mapRpcError,
				}).pipe(
					Effect.map((result) => ({
						baseBranch: result.baseBranch,
						parentEpicId: result.parentEpicId,
					})),
				),
			mergeIssueIntoIssue: ({ sourceIssueId, targetIssueId, projectPath }) =>
				invokeOptionalDaemonRpcMethod({
					method: daemonRpcClient.prMergeIssueIntoIssue,
					methodName: "prMergeIssueIntoIssue",
					request: {
						sourceIssueId,
						targetIssueId,
						projectPath,
					},
					onUnavailable: missingDaemonPrRpcMethodError,
					onError: mapRpcError,
				}).pipe(Effect.asVoid),
			getTargetBranch: (issueId, projectPath) =>
				invokeOptionalDaemonRpcMethod({
					method: daemonRpcClient.prGetTargetBranch,
					methodName: "prGetTargetBranch",
					request: {
						issueId,
						projectPath,
					},
					onUnavailable: missingDaemonPrRpcMethodError,
					onError: mapRpcError,
				}).pipe(
					Effect.map((result) => ({
						targetBranch: result.targetBranch,
						isEpicChild: result.isEpicChild,
					})),
				),
			checkGHCLI: () =>
				invokeOptionalDaemonRpcMethod({
					method: daemonRpcClient.prCheckGhCli,
					methodName: "prCheckGhCli",
					request: undefined,
					onUnavailable: missingDaemonPrRpcMethodError,
					onError: mapRpcError,
				}).pipe(Effect.map((result) => result.available)),
		} satisfies PrWorkflowApi
	}),
}) {}
