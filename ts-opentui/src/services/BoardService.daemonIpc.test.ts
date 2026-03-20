import { describe, expect, it } from "bun:test"
import type { DaemonRpcClientApi, DaemonRpcClientError } from "@azedarach/shared/rpc"
import { DAEMON_RPC_PROTOCOL_VERSION } from "@azedarach/shared/rpc"
import { RpcClientError } from "@effect/rpc/RpcClientError"
import { Effect, Option, Ref } from "effect"
import {
	makeBoardDaemonIpcSignals,
	resolveDaemonAuthoritativeProjectPath,
	resolveDaemonBoardReadModelRpc,
} from "./BoardService.js"

const makeDaemonRpcClientStub = (params: {
	readonly onAttach?: () => Effect.Effect<void, DaemonRpcClientError>
	readonly onReconnect?: () => Effect.Effect<void, DaemonRpcClientError>
	readonly onHeartbeat?: () => Effect.Effect<void, DaemonRpcClientError>
	readonly onSessionSnapshotRequest?: (projectPath: string | undefined) => Effect.Effect<void>
	readonly onSessionSnapshot?: () => Effect.Effect<void, DaemonRpcClientError>
	readonly onEventStream?: () => Effect.Effect<void, DaemonRpcClientError>
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
	issueGet: () => Effect.dieMessage("Unexpected issueGet RPC in BoardService daemon IPC test"),
	issueList: () => Effect.dieMessage("Unexpected issueList RPC in BoardService daemon IPC test"),
	issueCreate: () =>
		Effect.dieMessage("Unexpected issueCreate RPC in BoardService daemon IPC test"),
	issueUpdate: () =>
		Effect.dieMessage("Unexpected issueUpdate RPC in BoardService daemon IPC test"),
	implementationGetRegistry: () =>
		Effect.dieMessage("Unexpected implementationGetRegistry RPC in BoardService daemon IPC test"),
	implementationCreate: () =>
		Effect.dieMessage("Unexpected implementationCreate RPC in BoardService daemon IPC test"),
	implementationUpdate: () =>
		Effect.dieMessage("Unexpected implementationUpdate RPC in BoardService daemon IPC test"),
	implementationDelete: () =>
		Effect.dieMessage("Unexpected implementationDelete RPC in BoardService daemon IPC test"),
	implementationSetDefault: () =>
		Effect.dieMessage("Unexpected implementationSetDefault RPC in BoardService daemon IPC test"),
	issueAddDependency: () =>
		Effect.dieMessage("Unexpected issueAddDependency RPC in BoardService daemon IPC test"),
	issueRemoveDependency: () =>
		Effect.dieMessage("Unexpected issueRemoveDependency RPC in BoardService daemon IPC test"),
	issueClose: () => Effect.dieMessage("Unexpected issueClose RPC in BoardService daemon IPC test"),
	issueDelete: () =>
		Effect.dieMessage("Unexpected issueDelete RPC in BoardService daemon IPC test"),
	issueSync: () => Effect.dieMessage("Unexpected issueSync RPC in BoardService daemon IPC test"),
	attachmentList: () =>
		Effect.dieMessage("Unexpected attachmentList RPC in BoardService daemon IPC test"),
	attachmentCountBatch: () =>
		Effect.dieMessage("Unexpected attachmentCountBatch RPC in BoardService daemon IPC test"),
	attachmentAttachFile: () =>
		Effect.dieMessage("Unexpected attachmentAttachFile RPC in BoardService daemon IPC test"),
	attachmentAttachClipboard: () =>
		Effect.dieMessage("Unexpected attachmentAttachClipboard RPC in BoardService daemon IPC test"),
	attachmentRemove: () =>
		Effect.dieMessage("Unexpected attachmentRemove RPC in BoardService daemon IPC test"),
	attachmentMaterializePath: () =>
		Effect.dieMessage("Unexpected attachmentMaterializePath RPC in BoardService daemon IPC test"),
	planningGenerate: () =>
		Effect.dieMessage("Unexpected planningGenerate RPC in BoardService daemon IPC test"),
	planningReview: () =>
		Effect.dieMessage("Unexpected planningReview RPC in BoardService daemon IPC test"),
	planningRefine: () =>
		Effect.dieMessage("Unexpected planningRefine RPC in BoardService daemon IPC test"),
	planningCreateIssues: () =>
		Effect.dieMessage("Unexpected planningCreateIssues RPC in BoardService daemon IPC test"),
	prCreate: () => Effect.dieMessage("Unexpected prCreate RPC in BoardService daemon IPC test"),
	prCleanup: () => Effect.dieMessage("Unexpected prCleanup RPC in BoardService daemon IPC test"),
	prMergeToMain: () =>
		Effect.dieMessage("Unexpected prMergeToMain RPC in BoardService daemon IPC test"),
	prCheckGhCli: () =>
		Effect.dieMessage("Unexpected prCheckGhCli RPC in BoardService daemon IPC test"),
	specRequirementList: () =>
		Effect.dieMessage("Unexpected specRequirementList RPC in BoardService daemon IPC test"),
	specRequirementGet: () =>
		Effect.dieMessage("Unexpected specRequirementGet RPC in BoardService daemon IPC test"),
	specRequirementCreate: () =>
		Effect.dieMessage("Unexpected specRequirementCreate RPC in BoardService daemon IPC test"),
	specRequirementUpdate: () =>
		Effect.dieMessage("Unexpected specRequirementUpdate RPC in BoardService daemon IPC test"),
	specRequirementDelete: () =>
		Effect.dieMessage("Unexpected specRequirementDelete RPC in BoardService daemon IPC test"),
	specRead: () => Effect.dieMessage("Unexpected specRead RPC in BoardService daemon IPC test"),
	specLint: () => Effect.dieMessage("Unexpected specLint RPC in BoardService daemon IPC test"),
	specParity: () => Effect.dieMessage("Unexpected specParity RPC in BoardService daemon IPC test"),
	specLinkList: () =>
		Effect.dieMessage("Unexpected specLinkList RPC in BoardService daemon IPC test"),
	specLinkAdd: () =>
		Effect.dieMessage("Unexpected specLinkAdd RPC in BoardService daemon IPC test"),
	specLinkRemove: () =>
		Effect.dieMessage("Unexpected specLinkRemove RPC in BoardService daemon IPC test"),
	specLinkUpdate: () =>
		Effect.dieMessage("Unexpected specLinkUpdate RPC in BoardService daemon IPC test"),
	specIssueLinks: () =>
		Effect.dieMessage("Unexpected specIssueLinks RPC in BoardService daemon IPC test"),
	specRequirementIssues: () =>
		Effect.dieMessage("Unexpected specRequirementIssues RPC in BoardService daemon IPC test"),
	specPublishConfigGet: () =>
		Effect.dieMessage("Unexpected specPublishConfigGet RPC in BoardService daemon IPC test"),
	specPublishConfigSet: () =>
		Effect.dieMessage("Unexpected specPublishConfigSet RPC in BoardService daemon IPC test"),
	specPublishOutcomeGet: () =>
		Effect.dieMessage("Unexpected specPublishOutcomeGet RPC in BoardService daemon IPC test"),
	specSyncMarkdown: () =>
		Effect.dieMessage("Unexpected specSyncMarkdown RPC in BoardService daemon IPC test"),
	specPublish: () =>
		Effect.dieMessage("Unexpected specPublish RPC in BoardService daemon IPC test"),
	eventStream: () =>
		(params.onEventStream ?? (() => Effect.void))().pipe(
			Effect.zipRight(
				Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					polledAtMs: 5,
					nextCursor: 42,
					events: [
						{
							cursor: 41,
							emittedAtMs: 4,
							event: {
								_tag: "DaemonEventStreamSessionSnapshotEvent",
								capturedAtMs: 4,
								sessions: [],
							},
						},
					],
				}),
			),
		),
	sessionSnapshot: (request) =>
		(params.onSessionSnapshotRequest ?? (() => Effect.void))(request?.projectPath).pipe(
			Effect.zipRight((params.onSessionSnapshot ?? (() => Effect.void))()),
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
		const cursor = await Effect.runPromise(signals.consumeStreamBatch(undefined))
		expect(cursor).toBeUndefined()
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
									new RpcClientError({
										reason: "Protocol",
										message: "daemon restarted",
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
		const snapshotProjectPathRef = await Effect.runPromise(
			Ref.make<ReadonlyArray<string | undefined>>([]),
		)
		const signals = makeBoardDaemonIpcSignals({
			daemonRpcClient: Option.some(
				makeDaemonRpcClientStub({
					onSessionSnapshotRequest: (projectPath) =>
						Ref.update(snapshotProjectPathRef, (paths) => [...paths, projectPath]),
					onSessionSnapshot: () => Ref.update(snapshotCallsRef, (count) => count + 1),
				}),
			),
			daemonFrontendClientId: "board-ui:test",
			nowMs: () => 1000,
			getProjectPath: () => Effect.succeed("/tmp/project-switched"),
		})

		await Effect.runPromise(signals.signalAttach())
		await Effect.runPromise(signals.signalHeartbeat())
		const snapshotCalls = await Effect.runPromise(Ref.get(snapshotCallsRef))
		const snapshotProjectPaths = await Effect.runPromise(Ref.get(snapshotProjectPathRef))
		expect(snapshotCalls).toBe(2)
		expect(snapshotProjectPaths).toEqual(["/tmp/project-switched", "/tmp/project-switched"])
	})

	it("consumes daemon event stream batches and advances cursor", async () => {
		const streamCallsRef = await Effect.runPromise(Ref.make(0))
		const observedBatchCursorRef = await Effect.runPromise(Ref.make<number | null>(null))
		const signals = makeBoardDaemonIpcSignals({
			daemonRpcClient: Option.some(
				makeDaemonRpcClientStub({
					onEventStream: () => Ref.update(streamCallsRef, (count) => count + 1),
				}),
			),
			daemonFrontendClientId: "board-ui:test",
			nowMs: () => 1000,
			onDaemonStreamBatch: (batch) => Ref.set(observedBatchCursorRef, batch.nextCursor),
		})

		const nextCursor = await Effect.runPromise(signals.consumeStreamBatch(40))
		const streamCalls = await Effect.runPromise(Ref.get(streamCallsRef))
		const observedBatchCursor = await Effect.runPromise(Ref.get(observedBatchCursorRef))
		expect(nextCursor).toBe(42)
		expect(streamCalls).toBe(1)
		expect(observedBatchCursor).toBe(42)
	})
})

describe("resolveDaemonBoardReadModelRpc", () => {
	it("returns none when daemon rpc client is unavailable", () => {
		const resolved = resolveDaemonBoardReadModelRpc({
			daemonRpcClient: Option.none(),
		})
		expect(Option.isNone(resolved)).toBe(true)
	})

	it("returns none when daemon rpc client has no boardReadModel operation", () => {
		const resolved = resolveDaemonBoardReadModelRpc({
			daemonRpcClient: Option.some(makeDaemonRpcClientStub({})),
		})
		expect(Option.isNone(resolved)).toBe(true)
	})

	it("returns boardReadModel rpc when client and project path are available", async () => {
		const daemonRpcClient: DaemonRpcClientApi = {
			...makeDaemonRpcClientStub({}),
			boardReadModel: () =>
				Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					capturedAtMs: 9,
					projectPath: "/tmp/project",
					tasks: [],
				}),
		}
		const resolved = resolveDaemonBoardReadModelRpc({
			daemonRpcClient: Option.some(daemonRpcClient),
		})
		expect(Option.isSome(resolved)).toBe(true)
		if (Option.isNone(resolved)) {
			throw new Error("Expected boardReadModel rpc")
		}
		const result = await Effect.runPromise(resolved.value({ projectPath: "/tmp/project" }))
		expect(result.projectPath).toBe("/tmp/project")
	})
})

describe("resolveDaemonAuthoritativeProjectPath", () => {
	it("uses provided project path for refresh and project switch flows", () => {
		expect(resolveDaemonAuthoritativeProjectPath("/tmp/project-switched")).toBe(
			"/tmp/project-switched",
		)
	})

	it("falls back to cwd when project path is missing", () => {
		expect(resolveDaemonAuthoritativeProjectPath(null)).toBe(process.cwd())
		expect(resolveDaemonAuthoritativeProjectPath(undefined)).toBe(process.cwd())
	})
})
