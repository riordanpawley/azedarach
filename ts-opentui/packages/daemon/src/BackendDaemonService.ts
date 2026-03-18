import { Effect, Ref } from "effect"
import {
	BACKEND_CLIENT_SESSION_NEGOTIATED_CAPABILITIES,
	BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
	BACKEND_DEFAULT_CLIENT_AUTH_CONTEXT,
	BACKEND_SYSTEM_AUTH_CONTEXT,
	type BackendClientAuthContext,
	type BackendClientCapability,
	type BackendClientProtocolAuditEvent,
	type BackendClientProtocolCompatibilityDecision,
	type BackendClientProtocolHandshakeMetadata,
	type BackendClientProtocolOperation,
	type BackendClientSessionNegotiatedCapabilities,
	createBackendClientAttachIntent,
	createBackendClientProtocolAuditEvent,
	createBackendClientReconnectIntent,
	createBackendClientResumeToken,
	hasBackendClientCapability,
	negotiateBackendClientProtocolHandshake,
	requireBackendClientCapability,
} from "../../../src/core/BackendClientSessionProtocol.js"
import {
	type DaemonLifecycleEvent,
	type DaemonLifecycleState,
	resolveDaemonLifecycleTransition,
} from "./DaemonLifecycle.js"

export type BackendDaemonRuntimePhase = DaemonLifecycleState

export interface BackendDaemonClientState {
	readonly clientId: string
	readonly auth?: BackendClientAuthContext
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
	readonly auditEvents?: ReadonlyArray<BackendClientProtocolAuditEvent>
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
	readonly auditEvents?: ReadonlyArray<BackendClientProtocolAuditEvent>
}

export interface BackendDaemonAttachRequest {
	readonly clientId: string
	readonly auth?: BackendClientAuthContext
	readonly protocolVersion?: number
	readonly requestedAtMs?: number
}

export interface BackendDaemonReconnectRequest {
	readonly clientId: string
	readonly auth?: BackendClientAuthContext
	readonly protocolVersion?: number
	readonly lastSeenRevision?: number
	readonly lastSeenLifecycleGeneration?: number
	readonly requestedAtMs?: number
}

export type BackendDaemonProtocolOperation = BackendClientProtocolOperation

export type BackendDaemonProtocolCompatibilityDecision = BackendClientProtocolCompatibilityDecision

export type BackendDaemonNegotiatedCapabilities = BackendClientSessionNegotiatedCapabilities

export type BackendDaemonHandshakeMetadata = BackendClientProtocolHandshakeMetadata

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
	readonly markRuntimeRestart: (
		observedAtMs?: number,
		actor?: BackendClientAuthContext,
	) => Effect.Effect<BackendDaemonSnapshot>
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
	readonly auditEvents: ReadonlyArray<BackendClientProtocolAuditEvent>
}

export const BACKEND_DAEMON_PROTOCOL_VERSION = BACKEND_CLIENT_SESSION_PROTOCOL_VERSION
const BACKEND_DAEMON_NEGOTIATED_CAPABILITIES: BackendDaemonNegotiatedCapabilities =
	BACKEND_CLIENT_SESSION_NEGOTIATED_CAPABILITIES

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
	auditEvents: state.auditEvents,
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
	auditEvents: state.auditEvents,
})

const MAX_AUDIT_EVENTS = 128

const appendAuditEvent = (
	state: BackendDaemonMutableState,
	event: BackendClientProtocolAuditEvent,
): BackendDaemonMutableState => {
	const nextAuditEvents = [...state.auditEvents, event]
	const boundedAuditEvents =
		nextAuditEvents.length > MAX_AUDIT_EVENTS
			? nextAuditEvents.slice(nextAuditEvents.length - MAX_AUDIT_EVENTS)
			: nextAuditEvents
	return {
		...state,
		auditEvents: boundedAuditEvents,
	}
}

const upsertClient = (params: {
	readonly state: BackendDaemonMutableState
	readonly clientId: string
	readonly auth: BackendClientAuthContext
	readonly observedAtMs: number
	readonly reconnect: boolean
	readonly lastSeenRevision: number | null
	readonly lastSeenLifecycleGeneration: number | null
}): BackendDaemonClientState => {
	const existing = params.state.clients.get(params.clientId)
	const baseConnectedAtMs = existing?.connectedAtMs ?? params.observedAtMs
	const mergedCapabilities = Array.from(
		new Set([...(existing?.auth?.capabilities ?? []), ...params.auth.capabilities]),
	).sort((left, right) => left.localeCompare(right))
	const mergedAuth: BackendClientAuthContext = {
		actorId: params.auth.actorId,
		trustLevel: params.auth.trustLevel,
		capabilities: mergedCapabilities,
	}
	const reconnectLastSeenRevision =
		params.lastSeenRevision !== null
			? params.lastSeenRevision
			: (existing?.lastSeenRevision ?? null)
	const reconnectLastSeenLifecycleGeneration =
		params.lastSeenLifecycleGeneration !== null
			? params.lastSeenLifecycleGeneration
			: (existing?.lastSeenLifecycleGeneration ?? null)
	const clientState: BackendDaemonClientState = {
		clientId: params.clientId,
		auth: mergedAuth,
		connectedAtMs: baseConnectedAtMs,
		lastHeartbeatAtMs: params.observedAtMs,
		lastReconnectAtMs: params.reconnect
			? params.observedAtMs
			: (existing?.lastReconnectAtMs ?? null),
		lastSeenRevision: params.reconnect
			? reconnectLastSeenRevision
			: (existing?.lastSeenRevision ?? null),
		lastSeenLifecycleGeneration: params.reconnect
			? reconnectLastSeenLifecycleGeneration
			: (existing?.lastSeenLifecycleGeneration ?? null),
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
		readonly auth: BackendClientAuthContext
		readonly observedAtMs: number
		readonly reconnect: boolean
		readonly lastSeenRevision: number | null
		readonly lastSeenLifecycleGeneration: number | null
		readonly auditOperation: "client.attach" | "client.reconnect" | "client.heartbeat"
	},
): Effect.Effect<{
	readonly state: BackendDaemonMutableState
	readonly clientState: BackendDaemonClientState
}> =>
	Ref.modify(stateRef, (state) => {
		const updatedClients = new Map(state.clients)
		const transitionedState = transitionForClientMutation(state, params)
		const auditedState = appendAuditEvent(
			transitionedState,
			createBackendClientProtocolAuditEvent({
				occurredAtMs: params.observedAtMs,
				operation: params.auditOperation,
				auth: params.auth,
				capability:
					params.auditOperation === "client.attach"
						? "session:attach"
						: params.auditOperation === "client.reconnect"
							? "session:reconnect"
							: "session:heartbeat",
				outcome: "allowed",
				reason: "capability granted",
			}),
		)
		const updatedState: BackendDaemonMutableState = {
			...auditedState,
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
			auth: params.auth,
			observedAtMs: params.observedAtMs,
			reconnect: params.reconnect,
			lastSeenRevision: params.lastSeenRevision,
			lastSeenLifecycleGeneration: params.lastSeenLifecycleGeneration,
		})
		return [{ state: updatedState, clientState }, updatedState] as const
	}).pipe(Effect.map((result) => result))

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
				auditEvents: [],
			})

			return {
				getState: () => Ref.get(stateRef).pipe(Effect.map(toState)),
				snapshot: () => Ref.get(stateRef).pipe(Effect.map(toSnapshot)),
				registerClientAttach: (request: BackendDaemonAttachRequest) =>
					Effect.gen(function* () {
						const observedAtMs = request.requestedAtMs ?? Date.now()
						const intent = createBackendClientAttachIntent({
							clientId: request.clientId,
							auth: request.auth,
							requestedAtMs: observedAtMs,
							requestedProtocolVersion: request.protocolVersion,
						})
						if (!hasBackendClientCapability(intent.auth, "session:attach")) {
							yield* Ref.update(stateRef, (state) =>
								appendAuditEvent(
									state,
									createBackendClientProtocolAuditEvent({
										occurredAtMs: observedAtMs,
										operation: "client.attach",
										auth: intent.auth,
										capability: "session:attach",
										outcome: "denied",
										reason: "missing session:attach capability",
									}),
								),
							)
							yield* requireBackendClientCapability({
								auth: intent.auth,
								capability: "session:attach",
								operation: "client.attach",
							})
						}
						const handshake = yield* negotiateBackendClientProtocolHandshake(intent)
						const { state } = yield* withClientMutation(stateRef, {
							clientId: intent.identity.clientId,
							auth: intent.auth,
							observedAtMs,
							reconnect: false,
							lastSeenRevision: null,
							lastSeenLifecycleGeneration: null,
							auditOperation: "client.attach",
						})
						return {
							clientId: intent.identity.clientId,
							acceptedAtMs: observedAtMs,
							resumeToken: createBackendClientResumeToken(intent.identity, state.revision),
							negotiatedCapabilities: BACKEND_DAEMON_NEGOTIATED_CAPABILITIES,
							handshake,
							snapshot: toSnapshot(state),
						}
					}),
				registerClientHeartbeat: (clientId: string, observedAtMs?: number) =>
					Effect.gen(function* () {
						const timestamp = observedAtMs ?? Date.now()
						const stateBefore = yield* Ref.get(stateRef)
						const auth =
							stateBefore.clients.get(clientId)?.auth ?? BACKEND_DEFAULT_CLIENT_AUTH_CONTEXT
						if (!hasBackendClientCapability(auth, "session:heartbeat")) {
							yield* Ref.update(stateRef, (state) =>
								appendAuditEvent(
									state,
									createBackendClientProtocolAuditEvent({
										occurredAtMs: timestamp,
										operation: "client.heartbeat",
										auth,
										capability: "session:heartbeat",
										outcome: "denied",
										reason: "missing session:heartbeat capability",
									}),
								),
							)
							yield* requireBackendClientCapability({
								auth,
								capability: "session:heartbeat",
								operation: "client.heartbeat",
							})
						}
						const { clientState } = yield* withClientMutation(stateRef, {
							clientId,
							auth,
							observedAtMs: timestamp,
							reconnect: false,
							lastSeenRevision: null,
							lastSeenLifecycleGeneration: null,
							auditOperation: "client.heartbeat",
						})
						return clientState
					}),
				markClientReconnect: (request: BackendDaemonReconnectRequest) =>
					Effect.gen(function* () {
						const observedAtMs = request.requestedAtMs ?? Date.now()
						const intent = createBackendClientReconnectIntent({
							clientId: request.clientId,
							auth: request.auth,
							requestedAtMs: observedAtMs,
							requestedProtocolVersion: request.protocolVersion,
							lastSeenRevision: request.lastSeenRevision,
							lastSeenLifecycleGeneration: request.lastSeenLifecycleGeneration,
						})
						if (!hasBackendClientCapability(intent.auth, "session:reconnect")) {
							yield* Ref.update(stateRef, (state) =>
								appendAuditEvent(
									state,
									createBackendClientProtocolAuditEvent({
										occurredAtMs: observedAtMs,
										operation: "client.reconnect",
										auth: intent.auth,
										capability: "session:reconnect",
										outcome: "denied",
										reason: "missing session:reconnect capability",
									}),
								),
							)
							yield* requireBackendClientCapability({
								auth: intent.auth,
								capability: "session:reconnect",
								operation: "client.reconnect",
							})
						}
						const handshake = yield* negotiateBackendClientProtocolHandshake(intent)
						const { state } = yield* withClientMutation(stateRef, {
							clientId: intent.identity.clientId,
							auth: intent.auth,
							observedAtMs,
							reconnect: true,
							lastSeenRevision: intent.lastSeenRevision,
							lastSeenLifecycleGeneration: intent.lastSeenLifecycleGeneration,
							auditOperation: "client.reconnect",
						})
						return {
							clientId: intent.identity.clientId,
							acceptedAtMs: observedAtMs,
							resumeToken: createBackendClientResumeToken(intent.identity, state.revision),
							negotiatedCapabilities: BACKEND_DAEMON_NEGOTIATED_CAPABILITIES,
							handshake,
							snapshot: toSnapshot(state),
						}
					}),
				markRuntimeRestart: (observedAtMs?: number, actor?: BackendClientAuthContext) =>
					Ref.modify(stateRef, (state) => {
						const restartAtMs = observedAtMs ?? Date.now()
						const auth = actor ?? BACKEND_SYSTEM_AUTH_CONTEXT
						const allowed = hasBackendClientCapability(auth, "runtime:restart")
						const auditedState = appendAuditEvent(
							state,
							createBackendClientProtocolAuditEvent({
								occurredAtMs: restartAtMs,
								operation: "runtime.restart",
								auth,
								capability: "runtime:restart",
								outcome: allowed ? "allowed" : "denied",
								reason: allowed ? "capability granted" : "missing runtime:restart capability",
							}),
						)
						if (!allowed) {
							const deniedSnapshot = toSnapshot({
								...auditedState,
								updatedAtMs: restartAtMs,
							})
							return [deniedSnapshot, auditedState] as const
						}
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
							auditEvents: auditedState.auditEvents,
							updatedAtMs: restartAtMs,
							revision: state.revision + 1,
						}
						return [toSnapshot(finalizedState), finalizedState] as const
					}).pipe(
						Effect.tap((snapshot) =>
							requireBackendClientCapability({
								auth: actor ?? BACKEND_SYSTEM_AUTH_CONTEXT,
								capability: "runtime:restart",
								operation: "runtime.restart",
							}).pipe(Effect.as(snapshot)),
						),
					),
			} satisfies BackendDaemonServiceApi
		}),
	},
) {}
