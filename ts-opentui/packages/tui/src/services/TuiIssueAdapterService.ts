import {
	type DaemonIssueUpdatePatch,
	DaemonRpcClient,
	type TrackedIssue,
	type TrackedIssueRelationshipRef,
} from "@azedarach/shared/rpc"
import { Data, Effect } from "effect"
import type { DependencyRef, Issue } from "../contracts.js"

export class TuiIssueAdapterServiceError extends Data.TaggedError("TuiIssueAdapterServiceError")<{
	readonly reason: "rpc-failed"
	readonly operation:
		| "issueShow"
		| "issueUpdate"
		| "issueAddDependency"
		| "issueRemoveDependency"
		| "issueGetEpicWithChildren"
	readonly message: string
}> {}

export interface TuiIssueAdapterServiceApi {
	readonly show: (
		issueId: string,
		options?: {
			readonly projectPath?: string
			readonly maxSyncWaitMs?: number
		},
	) => Effect.Effect<Issue, TuiIssueAdapterServiceError>
	readonly update: (
		issueId: string,
		patch: DaemonIssueUpdatePatch,
		options?: {
			readonly projectPath?: string
		},
	) => Effect.Effect<void, TuiIssueAdapterServiceError>
	readonly addDependency: (
		issueId: string,
		dependsOnId: string,
		dependencyType: DependencyRef["dependency_type"],
		options?: {
			readonly projectPath?: string
		},
	) => Effect.Effect<void, TuiIssueAdapterServiceError>
	readonly removeDependency: (
		issueId: string,
		dependsOnId: string,
		options?: {
			readonly dependencyType?: DependencyRef["dependency_type"]
			readonly projectPath?: string
		},
	) => Effect.Effect<void, TuiIssueAdapterServiceError>
	readonly getEpicWithChildren: (
		epicId: string,
		options?: {
			readonly projectPath?: string
			readonly maxSyncWaitMs?: number
		},
	) => Effect.Effect<
		{ readonly epic: Issue; readonly children: ReadonlyArray<DependencyRef> },
		TuiIssueAdapterServiceError
	>
	readonly getEpicChildren: (
		epicId: string,
		options?: {
			readonly projectPath?: string
			readonly maxSyncWaitMs?: number
		},
	) => Effect.Effect<ReadonlyArray<DependencyRef>, TuiIssueAdapterServiceError>
}

const toDependencyRef = (dependency: TrackedIssueRelationshipRef): DependencyRef => ({
	id: dependency.id,
	dependency_type: dependency.dependency_type,
	title: dependency.title,
	status: dependency.status,
	issue_type: dependency.issue_type,
})

const toIssue = (issue: TrackedIssue): Issue => ({
	id: issue.id,
	title: issue.title,
	description: issue.description,
	status: issue.status,
	priority: issue.priority,
	issue_type: issue.issue_type,
	created_at: issue.created_at,
	updated_at: issue.updated_at,
	closed_at: issue.closed_at,
	assignee: issue.assignee,
	labels: issue.labels,
	design: issue.design,
	notes: issue.notes,
	acceptance: issue.acceptance,
	estimate: issue.estimate,
	implementations: issue.implementations,
	dependent_count: issue.dependent_count,
	dependency_count: issue.dependency_count,
	dependents: issue.dependents?.map(toDependencyRef),
	dependencies: issue.dependencies?.map(toDependencyRef),
})

const rpcFailure = (
	operation: TuiIssueAdapterServiceError["operation"],
	message: string,
): TuiIssueAdapterServiceError =>
	new TuiIssueAdapterServiceError({
		reason: "rpc-failed",
		operation,
		message,
	})

export class TuiIssueAdapterService extends Effect.Service<TuiIssueAdapterService>()(
	"TuiIssueAdapterService",
	{
		effect: Effect.gen(function* () {
			const daemonRpcClient = yield* DaemonRpcClient

			const service: TuiIssueAdapterServiceApi = {
				show: (issueId, options) => {
					return daemonRpcClient
						.issueGet({
							issueId,
							...(options?.projectPath !== undefined ? { projectPath: options.projectPath } : {}),
							...(options?.maxSyncWaitMs !== undefined
								? { maxSyncWaitMs: options.maxSyncWaitMs }
								: {}),
						})
						.pipe(
							Effect.map((result) => toIssue(result.issue)),
							Effect.mapError((error) => rpcFailure("issueShow", error.message)),
						)
				},
				update: (issueId, patch, options) =>
					daemonRpcClient
						.issueUpdate({
							issueId,
							patch,
							projectPath: options?.projectPath,
						})
						.pipe(
							Effect.asVoid,
							Effect.mapError((error) => rpcFailure("issueUpdate", error.message)),
						),
				addDependency: (issueId, dependsOnId, dependencyType, options) =>
					daemonRpcClient
						.issueAddDependency({
							issueId,
							dependsOnId,
							dependencyType,
							projectPath: options?.projectPath,
						})
						.pipe(
							Effect.asVoid,
							Effect.mapError((error) => rpcFailure("issueAddDependency", error.message)),
						),
				removeDependency: (issueId, dependsOnId, options) =>
					daemonRpcClient
						.issueRemoveDependency({
							issueId,
							dependsOnId,
							dependencyType: options?.dependencyType,
							projectPath: options?.projectPath,
						})
						.pipe(
							Effect.asVoid,
							Effect.mapError((error) => rpcFailure("issueRemoveDependency", error.message)),
						),
				getEpicWithChildren: (epicId, options) => {
					return daemonRpcClient
						.issueGet({
							issueId: epicId,
							...(options?.projectPath !== undefined ? { projectPath: options.projectPath } : {}),
							...(options?.maxSyncWaitMs !== undefined
								? { maxSyncWaitMs: options.maxSyncWaitMs }
								: {}),
						})
						.pipe(
							Effect.map((result) => {
								const epic = toIssue(result.issue)
								const children =
									epic.dependents?.filter(
										(dependency) => dependency.dependency_type === "parent-child",
									) ?? []
								return { epic, children }
							}),
							Effect.mapError((error) => rpcFailure("issueGetEpicWithChildren", error.message)),
						)
				},
				getEpicChildren: (epicId, options) =>
					service.getEpicWithChildren(epicId, options).pipe(Effect.map(({ children }) => children)),
			}

			return service
		}),
	},
) {}
