import type { Effect } from "effect"
import type { DaemonRpcClientError } from "../DaemonRpcClient.js"
import type {
	DaemonImplementationRegistryResult,
	DaemonIssueCreateRequest,
	DaemonIssueCreateResult,
	DaemonIssueDeleteRequest,
	DaemonIssueDeleteResult,
	DaemonIssueEpicChildrenRequest,
	DaemonIssueEpicChildrenResult,
	DaemonIssueEpicWithChildrenResult,
	DaemonIssueParentEpicRequest,
	DaemonIssueParentEpicResult,
	DaemonIssueShowRequest,
	DaemonIssueShowResult,
	DaemonIssueUpdateRequest,
	DaemonIssueUpdateResult,
} from "../DaemonRpcSchemas.js"

export interface DaemonIssueTaskRpcClient {
	readonly issueCreate?: (
		request: DaemonIssueCreateRequest,
	) => Effect.Effect<DaemonIssueCreateResult, DaemonRpcClientError>
	readonly issueUpdate?: (
		request: DaemonIssueUpdateRequest,
	) => Effect.Effect<DaemonIssueUpdateResult, DaemonRpcClientError>
	readonly issueDelete?: (
		request: DaemonIssueDeleteRequest,
	) => Effect.Effect<DaemonIssueDeleteResult, DaemonRpcClientError>
	readonly issueShow?: (
		request: DaemonIssueShowRequest,
	) => Effect.Effect<DaemonIssueShowResult, DaemonRpcClientError>
	readonly issueEpicChildren?: (
		request: DaemonIssueEpicChildrenRequest,
	) => Effect.Effect<DaemonIssueEpicChildrenResult, DaemonRpcClientError>
	readonly issueEpicWithChildren?: (
		request: DaemonIssueEpicChildrenRequest,
	) => Effect.Effect<DaemonIssueEpicWithChildrenResult, DaemonRpcClientError>
	readonly issueParentEpic?: (
		request: DaemonIssueParentEpicRequest,
	) => Effect.Effect<DaemonIssueParentEpicResult, DaemonRpcClientError>
	readonly issueImplementationRegistry?: () => Effect.Effect<
		DaemonImplementationRegistryResult,
		DaemonRpcClientError
	>
}
