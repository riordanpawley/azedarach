import { Data, Effect } from "effect"

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
}

export class BackendDaemonSessionRecoveryError extends Data.TaggedError(
	"BackendDaemonSessionRecoveryError",
)<{
	readonly message: string
}> {}

export interface BackendDaemonSessionRecoveryApi {
	readonly listActive: (
		projectPath: string,
	) => Effect.Effect<ReadonlyArray<BackendDaemonSessionSnapshot>, BackendDaemonSessionRecoveryError>
	readonly recoverSession: (
		issueId: string,
	) => Effect.Effect<void, BackendDaemonSessionRecoveryError>
	readonly updateState: (
		issueId: string,
		newState: BackendDaemonSessionState,
	) => Effect.Effect<void, BackendDaemonSessionRecoveryError>
}

export class BackendDaemonSessionRecovery extends Effect.Service<BackendDaemonSessionRecovery>()(
	"BackendDaemonSessionRecovery",
	{
		effect: Effect.succeed({
			listActive: (_projectPath: string) =>
				Effect.succeed<ReadonlyArray<BackendDaemonSessionSnapshot>>([]),
			recoverSession: (_issueId: string) => Effect.void,
			updateState: (_issueId: string, _newState: BackendDaemonSessionState) => Effect.void,
		} satisfies BackendDaemonSessionRecoveryApi),
	},
) {}
