import { describe, expect, it } from "bun:test"
import { Effect, Option, Ref } from "effect"
import {
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
	DaemonRpcTransportError,
} from "../rpc/DaemonRpcClient.js"
import { DAEMON_RPC_PROTOCOL_VERSION } from "../rpc/DaemonRpcSchemas.js"
import { makeBoardDaemonIpcSignals } from "./BoardService.js"

const makeDaemonRpcClientStub = (params: {
	readonly onAttach?: () => Effect.Effect<void, DaemonRpcClientError>
	readonly onReconnect?: () => Effect.Effect<void, DaemonRpcClientError>
	readonly onHeartbeat?: () => Effect.Effect<void, DaemonRpcClientError>
	readonly onSessionSnapshot?: () => Effect.Effect<void, DaemonRpcClientError>
}): DaemonRpcClientApi => ({
	status: () =>
		Effect.succeed({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			checkedAtMs: 0,
			runtime: {
				protocolVersion: 1,
				runtimePhase: "ready",
				authoritativeRuntime: true,
				revision: 0,
				lifecycleGeneration: 0,
				lifecycleReason: "ready",
				recoveryGeneration: 0,
				capturedAtMs: 0,
				clients: {},
			},
			sync: {
				state: "running",
				generation: 1,
				projectPath: "/tmp/project",
				intervalMs: 5000,
				startedAtMs: 0,
				runCount: 0,
				successCount: 0,
				failureCount: 0,
				failureStreak: 0,
				restartStreak: 0,
				lastBackoffMs: null,
				lastSuccessfulRunAtMs: null,
				lastRun: null,
				lastError: null,
			},
		}),
	health: () =>
		Effect.succeed({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			checkedAtMs: 0,
			state: "healthy",
			reason: "daemon runtime is healthy",
			status: {
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				checkedAtMs: 0,
				runtime: {
					protocolVersion: 1,
					runtimePhase: "ready",
					authoritativeRuntime: true,
					revision: 0,
					lifecycleGeneration: 0,
					lifecycleReason: "ready",
					recoveryGeneration: 0,
					capturedAtMs: 0,
					clients: {},
				},
				sync: {
					state: "running",
					generation: 1,
					projectPath: "/tmp/project",
					intervalMs: 5000,
					startedAtMs: 0,
					runCount: 0,
					successCount: 0,
					failureCount: 0,
					failureStreak: 0,
					restartStreak: 0,
					lastBackoffMs: null,
					lastSuccessfulRunAtMs: null,
					lastRun: null,
					lastError: null,
				},
			},
		}),
	logs: () =>
		Effect.succeed({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			logPath: "/tmp/project/az-cli.log",
			totalLines: 0,
			lines: [],
		}),
	stop: () =>
		Effect.succeed({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			checkedAtMs: 0,
			runtime: {
				protocolVersion: 1,
				runtimePhase: "ready",
				authoritativeRuntime: true,
				revision: 0,
				lifecycleGeneration: 0,
				lifecycleReason: "ready",
				recoveryGeneration: 0,
				capturedAtMs: 0,
				clients: {},
			},
			sync: {
				state: "stopped",
				generation: 1,
				projectPath: null,
				intervalMs: null,
				startedAtMs: null,
				runCount: 0,
				successCount: 0,
				failureCount: 0,
				failureStreak: 0,
				restartStreak: 0,
				lastBackoffMs: null,
				lastSuccessfulRunAtMs: null,
				lastRun: null,
				lastError: null,
			},
		}),
	restart: () =>
		Effect.succeed({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			checkedAtMs: 0,
			runtime: {
				protocolVersion: 1,
				runtimePhase: "ready",
				authoritativeRuntime: true,
				revision: 1,
				lifecycleGeneration: 1,
				lifecycleReason: "restart requested",
				recoveryGeneration: 0,
				capturedAtMs: 0,
				clients: {},
			},
			sync: {
				state: "running",
				generation: 2,
				projectPath: "/tmp/project",
				intervalMs: 5000,
				startedAtMs: 0,
				runCount: 0,
				successCount: 0,
				failureCount: 0,
				failureStreak: 0,
				restartStreak: 0,
				lastBackoffMs: null,
				lastSuccessfulRunAtMs: null,
				lastRun: null,
				lastError: null,
			},
		}),
	attach: () =>
		(params.onAttach ?? (() => Effect.void))().pipe(
			Effect.zipRight(
				Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					clientId: "board-ui:test",
					acceptedAtMs: 1,
					resumeToken: "board-ui:test:1",
					negotiatedCapabilities: {
						authoritativeRuntime: true,
						lifecycleGenerationTracking: true,
						recoveryGenerationTracking: true,
						resumeToken: true,
					},
					handshake: {
						operation: "attach",
						requestedAtMs: 1,
						negotiatedAtMs: 1,
						requestedProtocolVersion: 1,
						negotiatedProtocolVersion: 1,
						serverSupportedProtocolVersions: [1],
						compatibilityDecision: "exact-match",
					},
					snapshot: {
						protocolVersion: 1,
						runtimePhase: "ready",
						authoritativeRuntime: true,
						revision: 1,
						lifecycleGeneration: 1,
						lifecycleReason: "ready",
						recoveryGeneration: 0,
						capturedAtMs: 1,
						clients: {},
					},
				}),
			),
		),
	reconnect: () =>
		(params.onReconnect ?? (() => Effect.void))().pipe(
			Effect.zipRight(
				Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					clientId: "board-ui:test",
					acceptedAtMs: 2,
					resumeToken: "board-ui:test:2",
					negotiatedCapabilities: {
						authoritativeRuntime: true,
						lifecycleGenerationTracking: true,
						recoveryGenerationTracking: true,
						resumeToken: true,
					},
					handshake: {
						operation: "reconnect",
						requestedAtMs: 2,
						negotiatedAtMs: 2,
						requestedProtocolVersion: 1,
						negotiatedProtocolVersion: 1,
						serverSupportedProtocolVersions: [1],
						compatibilityDecision: "exact-match",
					},
					snapshot: {
						protocolVersion: 1,
						runtimePhase: "ready",
						authoritativeRuntime: true,
						revision: 2,
						lifecycleGeneration: 2,
						lifecycleReason: "recovery succeeded",
						recoveryGeneration: 1,
						capturedAtMs: 2,
						clients: {},
					},
				}),
			),
		),
	heartbeat: () =>
		(params.onHeartbeat ?? (() => Effect.void))().pipe(
			Effect.zipRight(
				Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					client: {
						clientId: "board-ui:test",
						connectedAtMs: 1,
						lastHeartbeatAtMs: 3,
						lastReconnectAtMs: null,
						lastSeenRevision: null,
						lastSeenLifecycleGeneration: null,
						lastRecoveryGeneration: null,
					},
				}),
			),
		),
	sessionSnapshot: () =>
		(params.onSessionSnapshot ?? (() => Effect.void))().pipe(
			Effect.zipRight(
				Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					capturedAtMs: 4,
					sessions: [
						{
							issueId: "AZE-1",
							worktreePath: "/tmp/project/.worktrees/AZE-1",
							tmuxSessionName: "az-AZE-1",
							state: "busy",
							startedAt: "2026-03-14T00:00:00.000Z",
							projectPath: "/tmp/project",
						},
					],
				}),
			),
		),
})

describe("BoardService daemon IPC signaling", () => {
	it("no-ops when daemon rpc client is unavailable", async () => {
		const signals = makeBoardDaemonIpcSignals({
			daemonRpcClient: Option.none(),
			daemonFrontendClientId: "board-ui:test",
			nowMs: () => 1000,
		})

		await Effect.runPromise(signals.signalAttach())
		await Effect.runPromise(signals.signalHeartbeat())
		await Effect.runPromise(signals.signalReconnect())
	})

	it("signals attach through daemon rpc client", async () => {
		const attachCallsRef = await Effect.runPromise(Ref.make(0))
		const signals = makeBoardDaemonIpcSignals({
			daemonRpcClient: Option.some(
				makeDaemonRpcClientStub({
					onAttach: () => Ref.update(attachCallsRef, (count) => count + 1),
				}),
			),
			daemonFrontendClientId: "board-ui:test",
			nowMs: () => 1000,
		})

		await Effect.runPromise(signals.signalAttach())
		const attachCalls = await Effect.runPromise(Ref.get(attachCallsRef))
		expect(attachCalls).toBe(1)
	})

	it("reconnects and retries heartbeat after transient heartbeat failure", async () => {
		const reconnectCallsRef = await Effect.runPromise(Ref.make(0))
		const heartbeatCallsRef = await Effect.runPromise(Ref.make(0))
		const signals = makeBoardDaemonIpcSignals({
			daemonRpcClient: Option.some(
				makeDaemonRpcClientStub({
					onReconnect: () => Ref.update(reconnectCallsRef, (count) => count + 1),
					onHeartbeat: () =>
						Effect.gen(function* () {
							const callCount = yield* Ref.get(heartbeatCallsRef)
							yield* Ref.set(heartbeatCallsRef, callCount + 1)
							if (callCount === 0) {
								return yield* Effect.fail(
									new DaemonRpcTransportError({
										operation: "heartbeat",
										reason: "transport",
										message: "daemon restarted",
										suggestion: "retry",
									}),
								)
							}
						}),
				}),
			),
			daemonFrontendClientId: "board-ui:test",
			nowMs: () => 1000,
		})

		await Effect.runPromise(signals.signalHeartbeat())
		const reconnectCalls = await Effect.runPromise(Ref.get(reconnectCallsRef))
		const heartbeatCalls = await Effect.runPromise(Ref.get(heartbeatCallsRef))
		expect(reconnectCalls).toBe(1)
		expect(heartbeatCalls).toBe(2)
	})

	it("observes daemon session snapshots on attach and heartbeat", async () => {
		const snapshotCallsRef = await Effect.runPromise(Ref.make(0))
		const signals = makeBoardDaemonIpcSignals({
			daemonRpcClient: Option.some(
				makeDaemonRpcClientStub({
					onSessionSnapshot: () => Ref.update(snapshotCallsRef, (count) => count + 1),
				}),
			),
			daemonFrontendClientId: "board-ui:test",
			nowMs: () => 1000,
		})

		await Effect.runPromise(signals.signalAttach())
		await Effect.runPromise(signals.signalHeartbeat())
		const snapshotCalls = await Effect.runPromise(Ref.get(snapshotCallsRef))
		expect(snapshotCalls).toBe(2)
	})
})
