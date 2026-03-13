import { Data, Effect, Ref } from "effect"
import {
	type DaemonLifecycleEvent,
	type DaemonLifecycleState,
	resolveDaemonLifecycleTransition,
} from "./DaemonLifecycle.js"

export type BackendDaemonRuntimePhase = DaemonLifecycleState

export interface BackendDaemonClientState {
	readonly clientId: string
	readonly connectedAtMs: number
	readonly lastHeartbeatAtMs: number
	readonly lastReconnectAtMs: number | null
	readonly lastSeenRevision: number | null
	readonly lastSeenLifecycleGeneration: number | null
	readonly lastRecoveryGeneration: number | null
}

export interface BackendDaemonState {
	readonly protocolVersion: number
	readonly runtimePhase: BackendDaemonRuntimePhase
	readonly authoritativeRuntime: true
	readonly startedAtMs: number
	readonly updatedAtMs: number
	readonly revision: number
	readonly lifecycleGeneration: number
	readonly lifecycleReason: string
	readonly recoveryGeneration: number
	readonly clients: Readonly<Record<string, BackendDaemonClientState>>
}

export interface BackendDaemonSnapshot {
	readonly protocolVersion: number
	readonly runtimePhase: BackendDaemonRuntimePhase
	readonly authoritativeRuntime: true
	readonly revision: number
	readonly lifecycleGeneration: number
	readonly lifecycleReason: string
	readonly recoveryGeneration: number
	readonly capturedAtMs: number
	readonly clients: Readonly<Record<string, BackendDaemonClientState>>
}

export interface BackendDaemonAttachRequest {
	readonly clientId: string
	readonly protocolVersion?: number
	readonly requestedAtMs?: number
}

export interface BackendDaemonReconnectRequest {
	readonly clientId: string
	readonly protocolVersion?: number
	readonly lastSeenRevision?: number
	readonly lastSeenLifecycleGeneration?: number
	readonly requestedAtMs?: number
}

export type BackendDaemonProtocolOperation = "attach" | "reconnect"

export type BackendDaemonProtocolCompatibilityDecision =
	| "exact-match"
	| "client-older-compatible"
	| "server-older-compatible"
	| "incompatible"

export interface BackendDaemonNegotiatedCapabilities {
	readonly authoritativeRuntime: true
	readonly lifecycleGenerationTracking: true
	readonly recoveryGenerationTracking: true
	readonly resumeToken: true
}

export interface BackendDaemonHandshakeMetadata {
	readonly operation: BackendDaemonProtocolOperation
	readonly requestedAtMs: number
	readonly negotiatedAtMs: number
	readonly requestedProtocolVersion: number
	readonly negotiatedProtocolVersion: number
	readonly serverSupportedProtocolVersions: ReadonlyArray<number>
	readonly compatibilityDecision: Exclude<
		BackendDaemonProtocolCompatibilityDecision,
		"incompatible"
	>
}

export interface BackendDaemonAttachResponse {
	readonly clientId: string
	readonly acceptedAtMs: number
	readonly resumeToken: string
	readonly negotiatedCapabilities: BackendDaemonNegotiatedCapabilities
	readonly handshake: BackendDaemonHandshakeMetadata
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
	readonly markRuntimeRestart: (observedAtMs?: number) => Effect.Effect<BackendDaemonSnapshot>
}

interface BackendDaemonMutableState {
	readonly protocolVersion: number
	readonly runtimePhase: BackendDaemonRuntimePhase
	readonly authoritativeRuntime: true
	readonly startedAtMs: number
	readonly updatedAtMs: number
	readonly revision: number
	readonly lifecycleGeneration: number
	readonly lifecycleReason: string
	readonly recoveryGeneration: number
	readonly clients: Map<string, BackendDaemonClientState>
}

export const BACKEND_DAEMON_PROTOCOL_VERSION = 1
const BACKEND_DAEMON_SUPPORTED_PROTOCOL_VERSIONS: ReadonlyArray<number> = [
	BACKEND_DAEMON_PROTOCOL_VERSION,
]

const BACKEND_DAEMON_NEGOTIATED_CAPABILITIES: BackendDaemonNegotiatedCapabilities = {
	authoritativeRuntime: true,
	lifecycleGenerationTracking: true,
	recoveryGenerationTracking: true,
	resumeToken: true,
}

export class BackendDaemonProtocolVersionMismatchError extends Data.TaggedError(
	"BackendDaemonProtocolVersionMismatchError",
)<{
	readonly operation: BackendDaemonProtocolOperation
	readonly compatibilityDecision: "incompatible"
	readonly serverSupportedProtocolVersions: ReadonlyArray<number>
	readonly expectedProtocolVersion: number
	readonly receivedProtocolVersion: number
}> {}

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
	lifecycleGeneration: state.lifecycleGeneration,
	lifecycleReason: state.lifecycleReason,
	recoveryGeneration: state.recoveryGeneration,
	clients: toClientsRecord(state.clients),
})

const toSnapshot = (state: BackendDaemonMutableState): BackendDaemonSnapshot => ({
	protocolVersion: state.protocolVersion,
	runtimePhase: state.runtimePhase,
	authoritativeRuntime: state.authoritativeRuntime,
	revision: state.revision,
	lifecycleGeneration: state.lifecycleGeneration,
	lifecycleReason: state.lifecycleReason,
	recoveryGeneration: state.recoveryGeneration,
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
	readonly lastSeenLifecycleGeneration: number | null
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
		lastSeenLifecycleGeneration:
			params.lastSeenLifecycleGeneration ??
			(params.reconnect
				? (existing?.lastSeenLifecycleGeneration ?? null)
				: (existing?.lastSeenLifecycleGeneration ?? null)),
		lastRecoveryGeneration: params.reconnect
			? params.state.recoveryGeneration
			: (existing?.lastRecoveryGeneration ?? null),
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
		readonly lastSeenLifecycleGeneration: number | null
	},
): Effect.Effect<{
	readonly state: BackendDaemonMutableState
	readonly clientState: BackendDaemonClientState
}> =>
	Ref.modify(stateRef, (state) => {
		const updatedClients = new Map(state.clients)
		const transitionedState = transitionForClientMutation(state, params)
		const updatedState: BackendDaemonMutableState = {
			...transitionedState,
			updatedAtMs: params.observedAtMs,
			revision: state.revision + 1,
			recoveryGeneration: params.reconnect
				? state.recoveryGeneration + 1
				: state.recoveryGeneration,
			clients: updatedClients,
		}
		const clientState = upsertClient({
			state: updatedState,
			clientId: params.clientId,
			observedAtMs: params.observedAtMs,
			reconnect: params.reconnect,
			lastSeenRevision: params.lastSeenRevision,
			lastSeenLifecycleGeneration: params.lastSeenLifecycleGeneration,
		})
		return [{ state: updatedState, clientState }, updatedState] as const
	})

const transitionState = (
	state: BackendDaemonMutableState,
	event: DaemonLifecycleEvent,
): BackendDaemonMutableState => {
	const transition = resolveDaemonLifecycleTransition(state.runtimePhase, event)
	return {
		...state,
		runtimePhase: transition.to,
		lifecycleReason: transition.reason,
		lifecycleGeneration: state.lifecycleGeneration + 1,
	}
}

const transitionForClientMutation = (
	state: BackendDaemonMutableState,
	params: { readonly reconnect: boolean },
): BackendDaemonMutableState => {
	let nextState = state
	if (nextState.runtimePhase === "starting") {
		nextState = transitionState(nextState, "bootstrap_succeeded")
	}
	if (params.reconnect) {
		if (nextState.runtimePhase === "ready" || nextState.runtimePhase === "degraded") {
			nextState = transitionState(nextState, "restart_requested")
		}
		if (nextState.runtimePhase === "recovering") {
			nextState = transitionState(nextState, "health_check_recovered")
		}
		return nextState
	}
	if (nextState.runtimePhase === "degraded" || nextState.runtimePhase === "recovering") {
		return transitionState(nextState, "health_check_recovered")
	}
	return nextState
}

const negotiateProtocolHandshake = (
	operation: BackendDaemonProtocolOperation,
	requestedAtMs: number,
	protocolVersion: number | undefined,
): Effect.Effect<BackendDaemonHandshakeMetadata> => {
	const requestedProtocolVersion = protocolVersion ?? BACKEND_DAEMON_PROTOCOL_VERSION
	if (requestedProtocolVersion !== BACKEND_DAEMON_PROTOCOL_VERSION) {
		return Effect.die(
			new BackendDaemonProtocolVersionMismatchError({
				operation,
				compatibilityDecision: "incompatible",
				serverSupportedProtocolVersions: BACKEND_DAEMON_SUPPORTED_PROTOCOL_VERSIONS,
				expectedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
				receivedProtocolVersion: requestedProtocolVersion,
			}),
		)
	}
	return Effect.succeed({
		operation,
		requestedAtMs,
		negotiatedAtMs: requestedAtMs,
		requestedProtocolVersion,
		negotiatedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
		serverSupportedProtocolVersions: BACKEND_DAEMON_SUPPORTED_PROTOCOL_VERSIONS,
		compatibilityDecision: "exact-match",
	})
}

export class BackendDaemonService extends Effect.Service<BackendDaemonService>()(
	"BackendDaemonService",
	{
		effect: Effect.gen(function* () {
			const startedAtMs = Date.now()
			const stateRef = yield* Ref.make<BackendDaemonMutableState>({
				protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
				runtimePhase: "starting",
				authoritativeRuntime: true,
				startedAtMs,
				updatedAtMs: startedAtMs,
				revision: 0,
				lifecycleGeneration: 0,
				lifecycleReason: "daemon bootstrapping",
				recoveryGeneration: 0,
				clients: new Map(),
			})

			return {
				getState: () => Ref.get(stateRef).pipe(Effect.map(toState)),
				snapshot: () => Ref.get(stateRef).pipe(Effect.map(toSnapshot)),
				registerClientAttach: (request: BackendDaemonAttachRequest) =>
					Effect.gen(function* () {
						const observedAtMs = request.requestedAtMs ?? Date.now()
						const handshake = yield* negotiateProtocolHandshake(
							"attach",
							observedAtMs,
							request.protocolVersion,
						)
						const { state } = yield* withClientMutation(stateRef, {
							clientId: request.clientId,
							observedAtMs,
							reconnect: false,
							lastSeenRevision: null,
							lastSeenLifecycleGeneration: null,
						})
						return {
							clientId: request.clientId,
							acceptedAtMs: observedAtMs,
							resumeToken: nextResumeToken(request.clientId, state.revision),
							negotiatedCapabilities: BACKEND_DAEMON_NEGOTIATED_CAPABILITIES,
							handshake,
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
							lastSeenLifecycleGeneration: null,
						})
						return clientState
					}),
				markClientReconnect: (request: BackendDaemonReconnectRequest) =>
					Effect.gen(function* () {
						const observedAtMs = request.requestedAtMs ?? Date.now()
						const handshake = yield* negotiateProtocolHandshake(
							"reconnect",
							observedAtMs,
							request.protocolVersion,
						)
						const { state } = yield* withClientMutation(stateRef, {
							clientId: request.clientId,
							observedAtMs,
							reconnect: true,
							lastSeenRevision: request.lastSeenRevision ?? null,
							lastSeenLifecycleGeneration: request.lastSeenLifecycleGeneration ?? null,
						})
						return {
							clientId: request.clientId,
							acceptedAtMs: observedAtMs,
							resumeToken: nextResumeToken(request.clientId, state.revision),
							negotiatedCapabilities: BACKEND_DAEMON_NEGOTIATED_CAPABILITIES,
							handshake,
							snapshot: toSnapshot(state),
						}
					}),
				markRuntimeRestart: (observedAtMs?: number) =>
					Ref.modify(stateRef, (state) => {
						const restartAtMs = observedAtMs ?? Date.now()
						let nextState = state
						if (nextState.runtimePhase === "starting") {
							nextState = transitionState(nextState, "bootstrap_succeeded")
						}
						if (nextState.runtimePhase === "ready" || nextState.runtimePhase === "degraded") {
							nextState = transitionState(nextState, "restart_requested")
						}
						if (nextState.runtimePhase === "recovering") {
							nextState = transitionState(nextState, "health_check_recovered")
						}
						const finalizedState: BackendDaemonMutableState = {
							...nextState,
							updatedAtMs: restartAtMs,
							revision: state.revision + 1,
						}
						return [toSnapshot(finalizedState), finalizedState] as const
					}),
			} satisfies BackendDaemonServiceApi
		}),
	},
) {}
