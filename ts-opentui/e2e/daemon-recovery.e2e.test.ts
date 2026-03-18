import { describe, expect, it } from "bun:test"
import { mkdtempSync, rmSync } from "node:fs"
import type { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Registry, Result } from "@effect-atom/atom"
import { Effect, Option } from "effect"
import { bootstrapDaemonRpcClient } from "../src/cli/daemonClientBootstrap.js"
import {
	acquireGlobalDaemonLease,
	type GlobalDaemonDiscovery,
	type GlobalDaemonLease,
	readGlobalDaemonDiscovery,
	releaseGlobalDaemonLease,
} from "../src/core/GlobalDaemonRegistry.js"
import { boardRenderStateAtom, filteredTasksByColumnAtom } from "../src/ui/atoms/board.js"
import { drillDownChildIdsAtom } from "../src/ui/atoms/navigation.js"
import type { TaskWithSession } from "../src/ui/types.js"

const sleep = (ms: number): Promise<void> =>
	new Promise((resolve) => {
		setTimeout(resolve, ms)
	})

const runWithBunContext = <A, E>(effect: Effect.Effect<A, E, FileSystem.FileSystem | Path.Path>) =>
	Effect.runPromise(effect.pipe(Effect.provide(BunContext.layer)))

const readDiscovery = (homeDirectory: string): Promise<Option.Option<GlobalDaemonDiscovery>> =>
	runWithBunContext(
		readGlobalDaemonDiscovery({
			homeDirectory,
		}),
	)

const waitForDiscovery = async (
	homeDirectory: string,
	timeoutMs: number,
): Promise<GlobalDaemonDiscovery> => {
	const startedAtMs = Date.now()
	while (Date.now() - startedAtMs <= timeoutMs) {
		const discovery = await readDiscovery(homeDirectory)
		if (Option.isSome(discovery)) {
			return discovery.value
		}
		await sleep(30)
	}
	throw new Error("Timed out waiting for global daemon discovery")
}

const waitForNoDiscovery = async (homeDirectory: string, timeoutMs: number): Promise<void> => {
	const startedAtMs = Date.now()
	while (Date.now() - startedAtMs <= timeoutMs) {
		const discovery = await readDiscovery(homeDirectory)
		if (Option.isNone(discovery)) {
			return
		}
		await sleep(30)
	}
	throw new Error("Timed out waiting for global daemon discovery to clear")
}

const withIsolatedHome = async <A>(fn: (homeDirectory: string) => Promise<A>): Promise<A> => {
	const homeDirectory = mkdtempSync("/tmp/az-global-daemon-recovery-")
	const previousHome = process.env.HOME
	process.env.HOME = homeDirectory
	try {
		return await fn(homeDirectory)
	} finally {
		process.env.HOME = previousHome
		rmSync(homeDirectory, { recursive: true, force: true })
	}
}

const makeBoardRegistry = (
	tasksResult: Result.Result<readonly (readonly TaskWithSession[])[], unknown>,
) =>
	Registry.make({
		initialValues: [
			[drillDownChildIdsAtom, Result.success(new Set<string>())],
			[filteredTasksByColumnAtom, tasksResult],
		],
	})

describe("daemon recovery e2e", () => {
	it("recovers when the daemon is unavailable at start and appears later", async () => {
		await withIsolatedHome(async (homeDirectory) => {
			let lease: GlobalDaemonLease | null = null
			try {
				const attachAttempts: Array<{
					readonly attempt: number
					readonly delayMs: number
					readonly timeoutRemainingMs: number
					readonly socketPath: string | null
					readonly socketUrl: string | null
				}> = []

				const bootstrapPromise = runWithBunContext(
					bootstrapDaemonRpcClient({
						autoStart: false,
						timeoutMs: 3_000,
						attachRetryBackoffMs: [15, 25, 40],
						onAttachAttempt: (observation) => {
							attachAttempts.push({
								attempt: observation.attempt,
								delayMs: observation.delayMs,
								timeoutRemainingMs: observation.timeoutRemainingMs,
								socketPath: observation.socketPath,
								socketUrl: observation.socketUrl,
							})
						},
					}),
				)

				await sleep(90)
				lease = await runWithBunContext(acquireGlobalDaemonLease({ homeDirectory }))
				const discovery = await waitForDiscovery(homeDirectory, 8_000)
				const bootstrap = await bootstrapPromise

				expect(bootstrap.startedDaemon).toBe(false)
				expect(bootstrap.discovery.pid).toBe(discovery.pid)
				expect(bootstrap.discovery.lockId).toBe(discovery.lockId)
				expect(bootstrap.attachAttemptCount).toBe(attachAttempts.length)
				expect(bootstrap.attachAttemptCount).toBeGreaterThan(1)
				expect(attachAttempts[0]?.delayMs).toBe(0)
				expect(attachAttempts.some((attempt) => attempt.socketPath === null)).toBe(true)
				expect(
					attachAttempts.every((attempt, index) =>
						index === 0
							? attempt.attempt === 1
							: attempt.attempt === attachAttempts[index - 1]!.attempt + 1,
					),
				).toBe(true)
			} finally {
				if (lease !== null) {
					await runWithBunContext(releaseGlobalDaemonLease(lease).pipe(Effect.ignore))
				}
				await waitForNoDiscovery(homeDirectory, 8_000).catch(() => undefined)
			}
		})
	}, 30_000)

	it("reconnects after runtime disconnects and restarts with the same RPC surface", async () => {
		await withIsolatedHome(async (homeDirectory) => {
			let leaseA: GlobalDaemonLease | null = null
			let leaseB: GlobalDaemonLease | null = null
			try {
				leaseA = await runWithBunContext(acquireGlobalDaemonLease({ homeDirectory }))
				const firstDiscovery = await waitForDiscovery(homeDirectory, 8_000)

				const firstAttach = await runWithBunContext(
					bootstrapDaemonRpcClient({
						autoStart: false,
						timeoutMs: 8_000,
					}),
				)
				expect(firstAttach.startedDaemon).toBe(false)
				expect(firstAttach.discovery.pid).toBe(firstDiscovery.pid)
				expect(firstAttach.discovery.lockId).toBe(firstDiscovery.lockId)

				await runWithBunContext(releaseGlobalDaemonLease(leaseA))
				leaseA = null
				await waitForNoDiscovery(homeDirectory, 8_000)

				const reconnectAttempts: Array<{
					readonly attempt: number
					readonly delayMs: number
					readonly timeoutRemainingMs: number
					readonly socketPath: string | null
				}> = []

				const reconnectPromise = runWithBunContext(
					bootstrapDaemonRpcClient({
						autoStart: false,
						timeoutMs: 3_000,
						attachRetryBackoffMs: [15, 25, 40],
						onAttachAttempt: (observation) => {
							reconnectAttempts.push({
								attempt: observation.attempt,
								delayMs: observation.delayMs,
								timeoutRemainingMs: observation.timeoutRemainingMs,
								socketPath: observation.socketPath,
							})
						},
					}),
				)

				await sleep(90)
				leaseB = await runWithBunContext(acquireGlobalDaemonLease({ homeDirectory }))
				const restartedDiscovery = await waitForDiscovery(homeDirectory, 8_000)
				const reconnectAttach = await reconnectPromise

				expect(reconnectAttach.startedDaemon).toBe(false)
				expect(reconnectAttach.discovery.pid).toBe(restartedDiscovery.pid)
				expect(reconnectAttach.discovery.lockId).toBe(restartedDiscovery.lockId)
				expect(reconnectAttach.socketUrl).toBe(firstAttach.socketUrl)
				expect(reconnectAttach.attachAttemptCount).toBe(reconnectAttempts.length)
				expect(reconnectAttach.attachAttemptCount).toBeGreaterThan(1)
				expect(reconnectAttempts[0]?.delayMs).toBe(0)
				expect(reconnectAttempts.some((attempt) => attempt.socketPath === null)).toBe(true)
			} finally {
				if (leaseA !== null) {
					await runWithBunContext(releaseGlobalDaemonLease(leaseA).pipe(Effect.ignore))
				}
				if (leaseB !== null) {
					await runWithBunContext(releaseGlobalDaemonLease(leaseB).pipe(Effect.ignore))
				}
				await waitForNoDiscovery(homeDirectory, 8_000).catch(() => undefined)
			}
		})
	}, 30_000)

	it("keeps interruption-only board results in loading and recovers to ready on success", async () => {
		const interruptedTasks = Result.fromExit(await Effect.runPromiseExit(Effect.interrupt))
		const loadingRegistry = makeBoardRegistry(interruptedTasks)
		const loadingState = loadingRegistry.get(boardRenderStateAtom)

		expect(loadingState._tag).toBe("loading")

		const emptyTasksByColumn: readonly (readonly TaskWithSession[])[] = [[], [], [], []]
		const readyRegistry = makeBoardRegistry(Result.success(emptyTasksByColumn))
		const readyState = readyRegistry.get(boardRenderStateAtom)

		expect(readyState._tag).toBe("ready")
		if (readyState._tag !== "ready") {
			throw new Error("Expected board render state to recover to ready")
		}
		expect(readyState.tasksByColumn).toEqual(emptyTasksByColumn)
	}, 10_000)
})
