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
				daemonRpcClient
					.prCheckMergeConflicts({
						issueId,
						projectPath,
					})
					.pipe(
						Effect.map((result) => ({
							hasConflictRisk: result.hasConflictRisk,
							conflictingFiles: result.conflictingFiles,
							baseBranch: result.baseBranch,
							issueBranch: result.issueBranch,
						})),
						Effect.mapError(mapRpcError),
					),
			checkUncommittedChanges: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prCheckUncommittedChanges({
						issueId,
						projectPath,
					})
					.pipe(
						Effect.map((result) => ({
							hasUncommittedChanges: result.hasUncommittedChanges,
							changedFiles: result.changedFiles,
						})),
						Effect.mapError(mapRpcError),
					),
			checkBranchBehindBase: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prCheckBranchBehindBase({
						issueId,
						projectPath,
					})
					.pipe(
						Effect.map((result) => ({
							behind: result.behind,
							ahead: result.ahead,
							baseBranch: result.baseBranch,
						})),
						Effect.mapError(mapRpcError),
					),
			getEffectiveBaseBranchForIssue: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prGetEffectiveBaseBranch({
						issueId,
						projectPath,
					})
					.pipe(
						Effect.map((result) => ({
							baseBranch: result.baseBranch,
							parentEpicId: result.parentEpicId,
						})),
						Effect.mapError(mapRpcError),
					),
			mergeIssueIntoIssue: ({ sourceIssueId, targetIssueId, projectPath }) =>
				daemonRpcClient
					.prMergeIssueIntoIssue({
						sourceIssueId,
						targetIssueId,
						projectPath,
					})
					.pipe(Effect.asVoid, Effect.mapError(mapRpcError)),
			getTargetBranch: (issueId, projectPath) =>
				daemonRpcClient
					.prGetTargetBranch({
						issueId,
						projectPath,
					})
					.pipe(
						Effect.map((result) => ({
							targetBranch: result.targetBranch,
							isEpicChild: result.isEpicChild,
						})),
						Effect.mapError(mapRpcError),
					),
			checkGHCLI: () =>
				daemonRpcClient.prCheckGhCli().pipe(
					Effect.map((result) => result.available),
					Effect.mapError(mapRpcError),
				),
		} satisfies PrWorkflowApi
	}),
}) {}
