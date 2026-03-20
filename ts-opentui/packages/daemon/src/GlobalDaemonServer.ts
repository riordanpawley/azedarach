import { DaemonAppRpcGroup } from "@azedarach/shared/rpc"
import { FileSystem } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import * as BunSocketServer from "@effect/platform-bun/BunSocketServer"
import { RpcSerialization, RpcServer } from "@effect/rpc"
import { Cause, Data, Effect, Fiber, Layer, Ref } from "effect"
import { BackendDaemonControlService } from "./BackendDaemonControlService.js"
import { BackendDaemonService } from "./BackendDaemonService.js"
import { BackendDaemonSessionRecovery } from "./BackendDaemonSessionRecovery.js"
import { DaemonAttachmentService } from "./DaemonAttachmentService.js"
import { DaemonPlanningService } from "./DaemonPlanningService.js"
import { DaemonPrService } from "./DaemonPrService.js"
import { DaemonSessionService } from "./DaemonSessionService.js"
import { GlobalDaemonDiscovery, type GlobalDaemonLease } from "./GlobalDaemonDiscovery.js"
import { GlobalDaemonRpcHandlersLive } from "./GlobalDaemonRpcHandlers.js"
import { ImplementationRegistryDaemonService } from "./ImplementationRegistryDaemonService.js"
import { SpecDaemonService } from "./SpecDaemonService.js"
import { TrackerIssueDaemonService } from "./TrackerIssueDaemonService.js"

export interface GlobalProjectRuntime {
	readonly projectPath: string
	readonly createdAtMs: number
	readonly lastTouchedAtMs: number
	readonly requestCount: number
}

export interface GlobalDaemonRuntimeObservation {
	readonly observedAtMs: number
	readonly runtimeCount: number
	readonly idleForMs: number
	readonly nextIdleSweepInMs: number
}

interface GlobalDaemonRuntimePolicy {
	readonly recordIdleEvictionEvents: boolean
}

export type GlobalDaemonLifecycleEvent =
	| "runtime_touched"
	| "runtime_evicted_idle"
	| "runtime_released"
	| "shutdown_requested"

export interface GlobalDaemonEvent {
	readonly event: GlobalDaemonLifecycleEvent
	readonly observedAtMs: number
	readonly projectPath: string | null
	readonly reason: string
}

export interface GlobalDaemonServerState {
	readonly socketPath: string
	readonly startedAtMs: number
	readonly lastActivityAtMs: number
	readonly idleTimeoutMs: number
	readonly runtimeCount: number
	readonly shuttingDown: boolean
	readonly shutdownReason: string | null
	readonly runtimes: Readonly<Record<string, GlobalProjectRuntime>>
	readonly events: ReadonlyArray<GlobalDaemonEvent>
}

interface GlobalDaemonMutableState {
	readonly socketPath: string
	readonly startedAtMs: number
	readonly lastActivityAtMs: number
	readonly idleTimeoutMs: number
	readonly policy: GlobalDaemonRuntimePolicy
	readonly shuttingDown: boolean
	readonly shutdownReason: string | null
	readonly runtimes: Map<string, GlobalProjectRuntime>
	readonly events: ReadonlyArray<GlobalDaemonEvent>
}

export interface GlobalDaemonServerRuntime {
	readonly getState: () => Effect.Effect<GlobalDaemonServerState>
	readonly observeIdleState: (
		observedAtMs?: number,
	) => Effect.Effect<GlobalDaemonRuntimeObservation>
	readonly touchProjectRuntime: (
		projectPath: string,
		observedAtMs?: number,
	) => Effect.Effect<GlobalProjectRuntime>
	readonly sweepIdleRuntimes: (observedAtMs?: number) => Effect.Effect<ReadonlyArray<string>>
	readonly releaseProjectRuntime: (
		projectPath: string,
		observedAtMs?: number,
	) => Effect.Effect<boolean>
	readonly requestShutdown: (
		reason: string,
		observedAtMs?: number,
	) => Effect.Effect<GlobalDaemonServerState>
}

export interface GlobalDaemonServerHandle {
	readonly lease: GlobalDaemonLease
	readonly runtime: GlobalDaemonServerRuntime
	readonly protocolFiber: Fiber.RuntimeFiber<void, never>
}

export class GlobalDaemonServerError extends Data.TaggedError("GlobalDaemonServerError")<{
	readonly message: string
	readonly cause: string
}> {}

const MAX_EVENTS = 256
const DEFAULT_RUNTIME_POLICY: GlobalDaemonRuntimePolicy = {
	recordIdleEvictionEvents: true,
}

const RUNTIME_CREATED_REASON = "runtime created (cold)"
const RUNTIME_REUSED_REASON = "runtime reused (hot)"
const IDLE_EVICTION_REASON = "idle timeout exceeded"

const compareRuntimeEvictionOrder = (
	left: GlobalProjectRuntime,
	right: GlobalProjectRuntime,
): number => {
	if (left.lastTouchedAtMs !== right.lastTouchedAtMs) {
		return left.lastTouchedAtMs - right.lastTouchedAtMs
	}
	return left.projectPath.localeCompare(right.projectPath)
}

const shouldEvictRuntimeForIdleTimeout = (
	runtime: GlobalProjectRuntime,
	nowMs: number,
	idleTimeoutMs: number,
): boolean => nowMs - runtime.lastTouchedAtMs >= idleTimeoutMs

const toRuntimeRecord = (
	runtimes: ReadonlyMap<string, GlobalProjectRuntime>,
): Readonly<Record<string, GlobalProjectRuntime>> =>
	Object.fromEntries([...runtimes.entries()].sort(([left], [right]) => left.localeCompare(right)))

const toState = (state: GlobalDaemonMutableState): GlobalDaemonServerState => ({
	socketPath: state.socketPath,
	startedAtMs: state.startedAtMs,
	lastActivityAtMs: state.lastActivityAtMs,
	idleTimeoutMs: state.idleTimeoutMs,
	runtimeCount: state.runtimes.size,
	shuttingDown: state.shuttingDown,
	shutdownReason: state.shutdownReason,
	runtimes: toRuntimeRecord(state.runtimes),
	events: state.events,
})

const appendEvent = (
	state: GlobalDaemonMutableState,
	event: GlobalDaemonEvent,
): GlobalDaemonMutableState => {
	const events = [...state.events, event]
	return {
		...state,
		events: events.length > MAX_EVENTS ? events.slice(events.length - MAX_EVENTS) : events,
	}
}

export const makeGlobalDaemonTransportLayer = (
	socketPath: string,
): Layer.Layer<
	RpcServer.Protocol,
	import("@effect/platform/SocketServer").SocketServerError,
	never
> =>
	Layer.provide(
		RpcServer.layerProtocolSocketServer,
		Layer.mergeAll(RpcSerialization.layerJson, BunSocketServer.layer({ path: socketPath })),
	)

const makeGlobalDaemonRpcServerLayer = (socketPath: string) => {
	const daemonServicesLayer = Layer.mergeAll(
		BunContext.layer,
		BackendDaemonControlService.Default,
		BackendDaemonService.Default,
		BackendDaemonSessionRecovery.Default,
		DaemonAttachmentService.Default,
		DaemonSessionService.Default,
		ImplementationRegistryDaemonService.Default,
		SpecDaemonService.Default,
		TrackerIssueDaemonService.Default,
	)

	const daemonServicesWithPlanningLayer = Layer.mergeAll(
		daemonServicesLayer,
		DaemonPlanningService.Default,
		DaemonPrService.Default,
	)

	const handlersLayer = Layer.provide(GlobalDaemonRpcHandlersLive, daemonServicesWithPlanningLayer)
	const rpcDependenciesLayer = Layer.mergeAll(
		makeGlobalDaemonTransportLayer(socketPath),
		handlersLayer,
	)

	return Layer.provide(RpcServer.layer(DaemonAppRpcGroup), rpcDependenciesLayer)
}

export const makeGlobalDaemonServerRuntime = (params: {
	readonly socketPath: string
	readonly idleTimeoutMs: number
	readonly nowMs?: number
	readonly recordIdleEvictionEvents?: boolean
}): Effect.Effect<GlobalDaemonServerRuntime> =>
	Effect.gen(function* () {
		const startedAtMs = params.nowMs ?? Date.now()
		const policy: GlobalDaemonRuntimePolicy = {
			...DEFAULT_RUNTIME_POLICY,
			recordIdleEvictionEvents:
				params.recordIdleEvictionEvents ?? DEFAULT_RUNTIME_POLICY.recordIdleEvictionEvents,
		}
		const stateRef = yield* Ref.make<GlobalDaemonMutableState>({
			socketPath: params.socketPath,
			startedAtMs,
			lastActivityAtMs: startedAtMs,
			idleTimeoutMs: params.idleTimeoutMs,
			policy,
			shuttingDown: false,
			shutdownReason: null,
			runtimes: new Map(),
			events: [],
		})

		return {
			getState: () => Ref.get(stateRef).pipe(Effect.map(toState)),
			observeIdleState: (observedAtMs?: number) =>
				Ref.get(stateRef).pipe(
					Effect.map((state) => {
						const nowMs = observedAtMs ?? Date.now()
						const idleForMs = Math.max(0, nowMs - state.lastActivityAtMs)
						return {
							observedAtMs: nowMs,
							runtimeCount: state.runtimes.size,
							idleForMs,
							nextIdleSweepInMs: Math.max(0, state.idleTimeoutMs - idleForMs),
						}
					}),
				),
			touchProjectRuntime: (projectPath: string, observedAtMs?: number) =>
				Ref.modify(stateRef, (state) => {
					const nowMs = observedAtMs ?? Date.now()
					const runtimes = new Map(state.runtimes)
					const existing = runtimes.get(projectPath)
					const nextRuntime: GlobalProjectRuntime = existing
						? {
								...existing,
								lastTouchedAtMs: nowMs,
								requestCount: existing.requestCount + 1,
							}
						: {
								projectPath,
								createdAtMs: nowMs,
								lastTouchedAtMs: nowMs,
								requestCount: 1,
							}
					runtimes.set(projectPath, nextRuntime)
					const eventState = appendEvent(state, {
						event: "runtime_touched",
						observedAtMs: nowMs,
						projectPath,
						reason: existing ? RUNTIME_REUSED_REASON : RUNTIME_CREATED_REASON,
					})
					const nextState: GlobalDaemonMutableState = {
						...eventState,
						runtimes,
						lastActivityAtMs: nowMs,
					}
					return [nextRuntime, nextState] as const
				}),
			sweepIdleRuntimes: (observedAtMs?: number) =>
				Ref.modify(stateRef, (state) => {
					const nowMs = observedAtMs ?? Date.now()
					const runtimes = new Map(state.runtimes)
					const evictedRuntimes = [...runtimes.values()]
						.filter((runtime) =>
							shouldEvictRuntimeForIdleTimeout(runtime, nowMs, state.idleTimeoutMs),
						)
						.sort(compareRuntimeEvictionOrder)

					for (const runtime of evictedRuntimes) {
						runtimes.delete(runtime.projectPath)
					}
					const evicted = evictedRuntimes.map((runtime) => runtime.projectPath)

					let eventState = state
					if (state.policy.recordIdleEvictionEvents) {
						for (const projectPath of evicted) {
							eventState = appendEvent(eventState, {
								event: "runtime_evicted_idle",
								observedAtMs: nowMs,
								projectPath,
								reason: IDLE_EVICTION_REASON,
							})
						}
					}
					const nextState: GlobalDaemonMutableState = {
						...eventState,
						runtimes,
					}
					return [evicted as ReadonlyArray<string>, nextState] as const
				}),
			releaseProjectRuntime: (projectPath: string, observedAtMs?: number) =>
				Ref.modify(stateRef, (state) => {
					const nowMs = observedAtMs ?? Date.now()
					if (!state.runtimes.has(projectPath)) {
						return [false, state] as const
					}
					const runtimes = new Map(state.runtimes)
					runtimes.delete(projectPath)
					const eventState = appendEvent(state, {
						event: "runtime_released",
						observedAtMs: nowMs,
						projectPath,
						reason: "runtime released",
					})
					const nextState: GlobalDaemonMutableState = {
						...eventState,
						runtimes,
						lastActivityAtMs: nowMs,
					}
					return [true, nextState] as const
				}),
			requestShutdown: (reason: string, observedAtMs?: number) =>
				Ref.modify(stateRef, (state) => {
					const nowMs = observedAtMs ?? Date.now()
					const eventState = appendEvent(state, {
						event: "shutdown_requested",
						observedAtMs: nowMs,
						projectPath: null,
						reason,
					})
					const nextState: GlobalDaemonMutableState = {
						...eventState,
						shuttingDown: true,
						shutdownReason: reason,
						lastActivityAtMs: nowMs,
					}
					return [toState(nextState), nextState] as const
				}),
		} satisfies GlobalDaemonServerRuntime
	})

export const startGlobalDaemonServer = (params?: {
	readonly homeDirectory?: string
	readonly idleTimeoutMs?: number
}) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const discoveryService = yield* GlobalDaemonDiscovery
		const lease = yield* discoveryService.acquireLease({ homeDirectory: params?.homeDirectory })
		yield* fs.remove(lease.paths.socketPath, { force: true }).pipe(Effect.ignore)

		const runtime = yield* makeGlobalDaemonServerRuntime({
			socketPath: lease.paths.socketPath,
			idleTimeoutMs: params?.idleTimeoutMs ?? 5 * 60 * 1000,
		})

		const protocolLoop = Layer.launch(makeGlobalDaemonRpcServerLayer(lease.paths.socketPath))

		const protocolFiber = yield* Effect.forkDaemon(
			protocolLoop.pipe(
				Effect.catchAllCause((cause) =>
					Effect.fail(
						new GlobalDaemonServerError({
							message: "Global daemon protocol loop failed",
							cause: Cause.pretty(cause),
						}),
					),
				),
				Effect.catchAll(() => Effect.void),
			),
		)

		return {
			lease,
			runtime,
			protocolFiber,
		} satisfies GlobalDaemonServerHandle
	})

export const stopGlobalDaemonServer = (handle: GlobalDaemonServerHandle, reason: string) =>
	Effect.gen(function* () {
		const discoveryService = yield* GlobalDaemonDiscovery
		yield* handle.runtime.requestShutdown(reason).pipe(Effect.ignore)
		yield* Fiber.interrupt(handle.protocolFiber).pipe(Effect.ignore)
		yield* discoveryService.releaseLease(handle.lease)
	})
