import { Data, Effect, Ref } from "effect"

export type BackendDaemonSessionState =
	| "idle"
	| "initializing"
	| "busy"
	| "waiting"
	| "done"
	| "error"
	| "paused"
	| "warning"
	| "crashed"

export interface BackendDaemonSessionSnapshot {
	readonly issueId: string
	readonly state: BackendDaemonSessionState
	readonly projectPath: string
	readonly tmuxSessionName: string
	readonly worktreePath: string | null
	readonly startedAt: string | null
}

export class BackendDaemonSessionRecoveryError extends Data.TaggedError(
	"BackendDaemonSessionRecoveryError",
)<{
	readonly reason: "missing-session-metadata"
	readonly message: string
}> {}

export interface BackendDaemonSessionUpdate {
	readonly issueId: string
	readonly state: BackendDaemonSessionState
	readonly projectPath: string
	readonly tmuxSessionName?: string
	readonly worktreePath?: string | null
	readonly startedAt?: string | null
}

export interface BackendDaemonSessionRecoveryApi {
	readonly listActive: (
		projectPath: string,
	) => Effect.Effect<ReadonlyArray<BackendDaemonSessionSnapshot>, BackendDaemonSessionRecoveryError>
	readonly recoverSession: (
		issueId: string,
	) => Effect.Effect<void, BackendDaemonSessionRecoveryError>
	readonly updateState: (
		update: BackendDaemonSessionUpdate,
	) => Effect.Effect<BackendDaemonSessionSnapshot, BackendDaemonSessionRecoveryError>
}

type BackendDaemonSessionStore = Map<string, Map<string, BackendDaemonSessionSnapshot>>

const sortSnapshots = (
	snapshots: ReadonlyArray<BackendDaemonSessionSnapshot>,
): ReadonlyArray<BackendDaemonSessionSnapshot> =>
	[...snapshots].sort((left, right) => left.issueId.localeCompare(right.issueId))

const getProjectSessions = (
	store: BackendDaemonSessionStore,
	projectPath: string,
): Map<string, BackendDaemonSessionSnapshot> => store.get(projectPath) ?? new Map()

const setProjectSessions = (
	store: BackendDaemonSessionStore,
	projectPath: string,
	projectSessions: Map<string, BackendDaemonSessionSnapshot>,
): BackendDaemonSessionStore => {
	const next = new Map(store)
	if (projectSessions.size === 0) {
		next.delete(projectPath)
		return next
	}
	next.set(projectPath, projectSessions)
	return next
}

const resolveTmuxSessionName = (params: {
	readonly existing: BackendDaemonSessionSnapshot | undefined
	readonly update: BackendDaemonSessionUpdate
}): Effect.Effect<string, BackendDaemonSessionRecoveryError> => {
	const tmuxSessionName = params.update.tmuxSessionName ?? params.existing?.tmuxSessionName
	return tmuxSessionName === undefined
		? Effect.fail(
				new BackendDaemonSessionRecoveryError({
					reason: "missing-session-metadata",
					message: `Daemon session telemetry missing tmux session name for ${params.update.issueId}`,
				}),
			)
		: Effect.succeed(tmuxSessionName)
}

const materializeSnapshot = (params: {
	readonly existing: BackendDaemonSessionSnapshot | undefined
	readonly update: BackendDaemonSessionUpdate
	readonly tmuxSessionName: string
}): BackendDaemonSessionSnapshot => ({
	issueId: params.update.issueId,
	state: params.update.state,
	projectPath: params.update.projectPath,
	tmuxSessionName: params.tmuxSessionName,
	worktreePath:
		params.update.worktreePath === undefined
			? (params.existing?.worktreePath ?? null)
			: params.update.worktreePath,
	startedAt:
		params.update.startedAt === undefined
			? (params.existing?.startedAt ?? null)
			: params.update.startedAt,
})

export class BackendDaemonSessionRecovery extends Effect.Service<BackendDaemonSessionRecovery>()(
	"BackendDaemonSessionRecovery",
	{
		effect: Effect.gen(function* () {
			const storeRef = yield* Ref.make<BackendDaemonSessionStore>(new Map())

			return {
				listActive: (projectPath) =>
					Ref.get(storeRef).pipe(
						Effect.map((store) =>
							sortSnapshots(
								[...getProjectSessions(store, projectPath).values()].filter(
									(snapshot) => snapshot.state !== "idle",
								),
							),
						),
					),
				recoverSession: (_issueId: string) => Effect.void,
				updateState: (update) =>
					Effect.gen(function* () {
						const store = yield* Ref.get(storeRef)
						const projectSessions = new Map(getProjectSessions(store, update.projectPath))
						const existing = projectSessions.get(update.issueId)
						const tmuxSessionName = yield* resolveTmuxSessionName({ existing, update })
						const snapshot = materializeSnapshot({
							existing,
							update,
							tmuxSessionName,
						})
						if (update.state === "idle") {
							projectSessions.delete(update.issueId)
						} else {
							projectSessions.set(update.issueId, snapshot)
						}
						yield* Ref.set(storeRef, setProjectSessions(store, update.projectPath, projectSessions))
						return snapshot
					}),
			} satisfies BackendDaemonSessionRecoveryApi
		}),
	},
) {}
