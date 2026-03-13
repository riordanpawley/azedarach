import { Effect, Ref } from "effect"

export type BackendDaemonRuntimePhase = "starting" | "running" | "recovering"

export interface BackendDaemonClientState {
	readonly clientId: string
	readonly connectedAtMs: number
	readonly lastHeartbeatAtMs: number
	readonly lastReconnectAtMs: number | null
	readonly lastSeenRevision: number | null
}

export interface BackendDaemonState {
	readonly protocolVersion: number
	readonly runtimePhase: BackendDaemonRuntimePhase
	readonly authoritativeRuntime: true
	readonly startedAtMs: number
	readonly updatedAtMs: number
	readonly revision: number
	readonly clients: Readonly<Record<string, BackendDaemonClientState>>
}

export interface BackendDaemonSnapshot {
	readonly protocolVersion: number
	readonly runtimePhase: BackendDaemonRuntimePhase
	readonly authoritativeRuntime: true
	readonly revision: number
	readonly capturedAtMs: number
	readonly clients: Readonly<Record<string, BackendDaemonClientState>>
}

export interface BackendDaemonAttachRequest {
	readonly clientId: string
	readonly requestedAtMs?: number
}

export interface BackendDaemonReconnectRequest {
	readonly clientId: string
	readonly lastSeenRevision?: number
	readonly requestedAtMs?: number
}

export interface BackendDaemonAttachResponse {
	readonly clientId: string
	readonly acceptedAtMs: number
	readonly resumeToken: string
	readonly snapshot: BackendDaemonSnapshot
}

export interface BackendDaemonServiceApi {
	readonly getState: () => Effect.Effect<BackendDaemonState>
	readonly snapshot: () => Effect.Effect<BackendDaemonSnapshot>
	readonly registerClientAttach: (
		request: BackendDaemonAttachRequest,
	) => Effect.Effect<BackendDaemonAttachResponse>
	readonly registerClientHeartbeat: (
		clientId: string,
		observedAtMs?: number,
	) => Effect.Effect<BackendDaemonClientState>
	readonly markClientReconnect: (
		request: BackendDaemonReconnectRequest,
	) => Effect.Effect<BackendDaemonAttachResponse>
}

interface BackendDaemonMutableState {
	readonly protocolVersion: number
	readonly runtimePhase: BackendDaemonRuntimePhase
	readonly authoritativeRuntime: true
	readonly startedAtMs: number
	readonly updatedAtMs: number
	readonly revision: number
	readonly clients: Map<string, BackendDaemonClientState>
}

const PROTOCOL_VERSION = 1

const toClientsRecord = (
	clients: ReadonlyMap<string, BackendDaemonClientState>,
): Readonly<Record<string, BackendDaemonClientState>> =>
	Object.fromEntries(
		[...clients.entries()].sort(([leftId], [rightId]) => leftId.localeCompare(rightId)),
	)

const toState = (state: BackendDaemonMutableState): BackendDaemonState => ({
	protocolVersion: state.protocolVersion,
	runtimePhase: state.runtimePhase,
	authoritativeRuntime: state.authoritativeRuntime,
	startedAtMs: state.startedAtMs,
	updatedAtMs: state.updatedAtMs,
	revision: state.revision,
	clients: toClientsRecord(state.clients),
})

const toSnapshot = (state: BackendDaemonMutableState): BackendDaemonSnapshot => ({
	protocolVersion: state.protocolVersion,
	runtimePhase: state.runtimePhase,
	authoritativeRuntime: state.authoritativeRuntime,
	revision: state.revision,
	capturedAtMs: state.updatedAtMs,
	clients: toClientsRecord(state.clients),
})

const nextResumeToken = (clientId: string, revision: number): string =>
	`${clientId}:${String(revision)}`

const upsertClient = (params: {
	readonly state: BackendDaemonMutableState
	readonly clientId: string
	readonly observedAtMs: number
	readonly reconnect: boolean
	readonly lastSeenRevision: number | null
}): BackendDaemonClientState => {
	const existing = params.state.clients.get(params.clientId)
	const baseConnectedAtMs = existing?.connectedAtMs ?? params.observedAtMs
	const clientState: BackendDaemonClientState = {
		clientId: params.clientId,
		connectedAtMs: baseConnectedAtMs,
		lastHeartbeatAtMs: params.observedAtMs,
		lastReconnectAtMs: params.reconnect
			? params.observedAtMs
			: (existing?.lastReconnectAtMs ?? null),
		lastSeenRevision:
			params.lastSeenRevision ??
			(params.reconnect
				? (existing?.lastSeenRevision ?? null)
				: (existing?.lastSeenRevision ?? null)),
	}
	params.state.clients.set(params.clientId, clientState)
	return clientState
}

const withClientMutation = (
	stateRef: Ref.Ref<BackendDaemonMutableState>,
	params: {
		readonly clientId: string
		readonly observedAtMs: number
		readonly reconnect: boolean
		readonly lastSeenRevision: number | null
	},
): Effect.Effect<{
	readonly state: BackendDaemonMutableState
	readonly clientState: BackendDaemonClientState
}> =>
	Ref.modify(stateRef, (state) => {
		const updatedClients = new Map(state.clients)
		const updatedPhase: BackendDaemonRuntimePhase = params.reconnect ? "recovering" : "running"
		const updatedState: BackendDaemonMutableState = {
			...state,
			runtimePhase: updatedPhase,
			updatedAtMs: params.observedAtMs,
			revision: state.revision + 1,
			clients: updatedClients,
		}
		const clientState = upsertClient({
			state: updatedState,
			clientId: params.clientId,
			observedAtMs: params.observedAtMs,
			reconnect: params.reconnect,
			lastSeenRevision: params.lastSeenRevision,
		})
		const finalizedState: BackendDaemonMutableState = {
			...updatedState,
			runtimePhase: "running",
		}
		return [{ state: finalizedState, clientState }, finalizedState] as const
	})

export class BackendDaemonService extends Effect.Service<BackendDaemonService>()(
	"BackendDaemonService",
	{
		effect: Effect.gen(function* () {
			const startedAtMs = Date.now()
			const stateRef = yield* Ref.make<BackendDaemonMutableState>({
				protocolVersion: PROTOCOL_VERSION,
				runtimePhase: "starting",
				authoritativeRuntime: true,
				startedAtMs,
				updatedAtMs: startedAtMs,
				revision: 0,
				clients: new Map(),
			})

			return {
				getState: () => Ref.get(stateRef).pipe(Effect.map(toState)),
				snapshot: () => Ref.get(stateRef).pipe(Effect.map(toSnapshot)),
				registerClientAttach: (request: BackendDaemonAttachRequest) =>
					Effect.gen(function* () {
						const observedAtMs = request.requestedAtMs ?? Date.now()
						const { state } = yield* withClientMutation(stateRef, {
							clientId: request.clientId,
							observedAtMs,
							reconnect: false,
							lastSeenRevision: null,
						})
						return {
							clientId: request.clientId,
							acceptedAtMs: observedAtMs,
							resumeToken: nextResumeToken(request.clientId, state.revision),
							snapshot: toSnapshot(state),
						}
					}),
				registerClientHeartbeat: (clientId: string, observedAtMs?: number) =>
					Effect.gen(function* () {
						const timestamp = observedAtMs ?? Date.now()
						const { clientState } = yield* withClientMutation(stateRef, {
							clientId,
							observedAtMs: timestamp,
							reconnect: false,
							lastSeenRevision: null,
						})
						return clientState
					}),
				markClientReconnect: (request: BackendDaemonReconnectRequest) =>
					Effect.gen(function* () {
						const observedAtMs = request.requestedAtMs ?? Date.now()
						const { state } = yield* withClientMutation(stateRef, {
							clientId: request.clientId,
							observedAtMs,
							reconnect: true,
							lastSeenRevision: request.lastSeenRevision ?? null,
						})
						return {
							clientId: request.clientId,
							acceptedAtMs: observedAtMs,
							resumeToken: nextResumeToken(request.clientId, state.revision),
							snapshot: toSnapshot(state),
						}
					}),
			} satisfies BackendDaemonServiceApi
		}),
	},
) {}
