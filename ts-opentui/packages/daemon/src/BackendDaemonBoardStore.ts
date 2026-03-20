import { Data, Effect } from "effect"
import {
	type BackendDaemonBoardTaskSnapshot,
	buildBoardTaskSnapshots,
} from "./BackendDaemonBoardProjection.js"
import {
	BackendDaemonSessionRecovery,
	type BackendDaemonSessionRecoveryApi,
} from "./BackendDaemonSessionRecovery.js"
import { DevServerDaemonService, type DevServerDaemonServiceApi } from "./DevServerDaemonService.js"
import {
	TrackerIssueDaemonService,
	type TrackerIssueDaemonServiceApi,
} from "./TrackerIssueDaemonService.js"

export type { BackendDaemonBoardTaskSnapshot } from "./BackendDaemonBoardProjection.js"

export class BackendDaemonBoardStoreError extends Data.TaggedError("BackendDaemonBoardStoreError")<{
	readonly message: string
}> {}

export interface BackendDaemonBoardStoreApi {
	readonly listBoardTasks: (
		projectPath: string,
	) => Effect.Effect<ReadonlyArray<BackendDaemonBoardTaskSnapshot>, BackendDaemonBoardStoreError>
}

const makeBackendDaemonBoardStore = (params: {
	readonly trackerIssues: TrackerIssueDaemonServiceApi
	readonly sessionRecovery: BackendDaemonSessionRecoveryApi
	readonly devServers: DevServerDaemonServiceApi
}): BackendDaemonBoardStoreApi => ({
	listBoardTasks: (projectPath) =>
		Effect.gen(function* () {
			const [issues, activeSessions, devServerList] = yield* Effect.all([
				params.trackerIssues.list(undefined, projectPath),
				params.sessionRecovery.listActive(projectPath),
				params.devServers.list({ projectPath }),
			])

			const sessionsByIssueId = new Map(
				activeSessions.map(
					(session) =>
						[
							session.issueId,
							{
								state: session.state,
								startedAt: session.startedAt,
								tmuxSessionName: session.tmuxSessionName,
								worktreePath: session.worktreePath,
							},
						] as const,
				),
			)
			const devServerIssueIds = new Set(
				devServerList.servers
					.filter((server) => server.status === "running")
					.map((server) => server.issueId),
			)

			return buildBoardTaskSnapshots({
				issues,
				sessionsByIssueId,
				devServerIssueIds,
			})
		}).pipe(
			Effect.mapError(
				(error) =>
					new BackendDaemonBoardStoreError({
						message: `Failed to build daemon board read model: ${String(error)}`,
					}),
			),
		),
})

export class BackendDaemonBoardStore extends Effect.Service<BackendDaemonBoardStore>()(
	"BackendDaemonBoardStore",
	{
		dependencies: [
			TrackerIssueDaemonService.Default,
			BackendDaemonSessionRecovery.Default,
			DevServerDaemonService.Default,
		],
		effect: Effect.gen(function* () {
			const trackerIssues = yield* TrackerIssueDaemonService
			const sessionRecovery = yield* BackendDaemonSessionRecovery
			const devServers = yield* DevServerDaemonService
			return makeBackendDaemonBoardStore({
				trackerIssues,
				sessionRecovery,
				devServers,
			})
		}),
	},
) {}
