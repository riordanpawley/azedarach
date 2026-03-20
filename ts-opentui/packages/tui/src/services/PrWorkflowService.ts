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

const missingDaemonPrRpcMethodError = (methodName: string): PrWorkflowError =>
	new PrWorkflowError({
		reason: "unknown",
		message: `Daemon RPC method is unavailable: ${methodName}`,
	})

const mapWorkflowError = (error: DaemonRpcClientError | PrWorkflowError): PrWorkflowError =>
	error._tag === "PrWorkflowError" ? error : mapRpcError(error)

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
						Effect.mapError(mapWorkflowError),
					),
			cleanup: ({ issueId, projectPath, closeIssue }) =>
				daemonRpcClient
					.prCleanup({
						issueId,
						projectPath,
						closeIssue,
					})
					.pipe(Effect.asVoid, Effect.mapError(mapWorkflowError)),
			mergeToMain: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prMergeToMain({
						issueId,
						projectPath,
					})
					.pipe(Effect.asVoid, Effect.mapError(mapWorkflowError)),
			updateFromBase: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prUpdateFromBase({
						issueId,
						projectPath,
					})
					.pipe(Effect.asVoid, Effect.mapError(mapWorkflowError)),
			mergeBaseIntoBranch: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prMergeBaseIntoBranch({
						issueId,
						projectPath,
					})
					.pipe(Effect.asVoid, Effect.mapError(mapWorkflowError)),
			abortMerge: ({ issueId, projectPath }) =>
				daemonRpcClient
					.prAbortMerge({
						issueId,
						projectPath,
					})
					.pipe(Effect.asVoid, Effect.mapError(mapWorkflowError)),
			checkMergeConflicts: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const method = daemonRpcClient.prCheckMergeConflicts
					if (method === undefined) {
						return yield* Effect.fail(missingDaemonPrRpcMethodError("prCheckMergeConflicts"))
					}
					return yield* method({
						issueId,
						projectPath,
					})
				}).pipe(
					Effect.map((result) => ({
						hasConflictRisk: result.hasConflictRisk,
						conflictingFiles: result.conflictingFiles,
						baseBranch: result.baseBranch,
						issueBranch: result.issueBranch,
					})),
					Effect.mapError(mapWorkflowError),
				),
			checkUncommittedChanges: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const method = daemonRpcClient.prCheckUncommittedChanges
					if (method === undefined) {
						return yield* Effect.fail(missingDaemonPrRpcMethodError("prCheckUncommittedChanges"))
					}
					return yield* method({
						issueId,
						projectPath,
					})
				}).pipe(
					Effect.map((result) => ({
						hasUncommittedChanges: result.hasUncommittedChanges,
						changedFiles: result.changedFiles,
					})),
					Effect.mapError(mapWorkflowError),
				),
			checkBranchBehindBase: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const method = daemonRpcClient.prCheckBranchBehindBase
					if (method === undefined) {
						return yield* Effect.fail(missingDaemonPrRpcMethodError("prCheckBranchBehindBase"))
					}
					return yield* method({
						issueId,
						projectPath,
					})
				}).pipe(
					Effect.map((result) => ({
						behind: result.behind,
						ahead: result.ahead,
						baseBranch: result.baseBranch,
					})),
					Effect.mapError(mapWorkflowError),
				),
			getEffectiveBaseBranchForIssue: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const method = daemonRpcClient.prGetEffectiveBaseBranch
					if (method === undefined) {
						return yield* Effect.fail(missingDaemonPrRpcMethodError("prGetEffectiveBaseBranch"))
					}
					return yield* method({
						issueId,
						projectPath,
					})
				}).pipe(
					Effect.map((result) => ({
						baseBranch: result.baseBranch,
						parentEpicId: result.parentEpicId,
					})),
					Effect.mapError(mapWorkflowError),
				),
			mergeIssueIntoIssue: ({ sourceIssueId, targetIssueId, projectPath }) =>
				Effect.gen(function* () {
					const method = daemonRpcClient.prMergeIssueIntoIssue
					if (method === undefined) {
						return yield* Effect.fail(missingDaemonPrRpcMethodError("prMergeIssueIntoIssue"))
					}
					return yield* method({
						sourceIssueId,
						targetIssueId,
						projectPath,
					})
				}).pipe(Effect.asVoid, Effect.mapError(mapWorkflowError)),
			getTargetBranch: (issueId, projectPath) =>
				Effect.gen(function* () {
					const method = daemonRpcClient.prGetTargetBranch
					if (method === undefined) {
						return yield* Effect.fail(missingDaemonPrRpcMethodError("prGetTargetBranch"))
					}
					return yield* method({
						issueId,
						projectPath,
					})
				}).pipe(
					Effect.map((result) => ({
						targetBranch: result.targetBranch,
						isEpicChild: result.isEpicChild,
					})),
					Effect.mapError(mapWorkflowError),
				),
			checkGHCLI: () =>
				daemonRpcClient.prCheckGhCli().pipe(
					Effect.map((result) => result.available),
					Effect.mapError(mapWorkflowError),
				),
		} satisfies PrWorkflowApi
	}),
}) {}
