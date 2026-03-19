import { describe, expect, it } from "bun:test"
import type { ChildProcessByStdio } from "node:child_process"
import { spawn } from "node:child_process"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import type { Readable } from "node:stream"
import type { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Effect, Option } from "effect"
import {
	DaemonControlLive,
	GlobalDaemonDiscovery,
	type GlobalDaemonDiscoveryApi,
	type GlobalDaemonLease,
} from "../../packages/daemon/src/index.js"
import {
	GlobalDaemonBootstrap,
	type GlobalDaemonDiscoveryMetadata,
} from "../../packages/daemon-control/src/index.js"

const projectRoot = process.cwd()
const daemonMainPath = join(projectRoot, "src/daemon/GlobalDaemonMain.ts")
type SpawnedDaemon = ChildProcessByStdio<null, Readable, Readable>
const COLD_ACTIVATION_TARGET_MS = 1_500
const HOT_SWITCH_TARGET_MS = 100
const ATTACH_FANOUT_CLIENT_COUNT = 8

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

const runWithBunContext = <A, E>(
	effect: Effect.Effect<
		A,
		E,
		GlobalDaemonDiscoveryApi | GlobalDaemonBootstrap | FileSystem.FileSystem | Path.Path
	>,
) =>
	Effect.runPromise(
		effect.pipe(
			Effect.provide(GlobalDaemonDiscovery.Default),
			Effect.provide(DaemonControlLive),
			Effect.provide(BunContext.layer),
		),
	)

const bootstrapDaemonRpcClient = (params: {
	readonly autoStart: boolean
	readonly timeoutMs?: number
	readonly attachRetryBackoffMs?: ReadonlyArray<number>
	readonly onAttachAttempt?: (observation: {
		readonly attempt: number
		readonly delayMs: number
		readonly timeoutRemainingMs: number
		readonly socketPath: string | null
		readonly socketUrl: string | null
	}) => void
}) =>
	Effect.gen(function* () {
		const bootstrap = yield* GlobalDaemonBootstrap
		return yield* bootstrap.bootstrapDaemonRpcClient(params)
	})

const acquireLease = (homeDirectory: string) =>
	Effect.gen(function* () {
		const discovery = yield* GlobalDaemonDiscovery
		return yield* discovery.acquireLease({ homeDirectory })
	})

const releaseLease = (lease: GlobalDaemonLease) =>
	Effect.gen(function* () {
		const discovery = yield* GlobalDaemonDiscovery
		return yield* discovery.releaseLease(lease)
	})

const readDiscovery = (
	homeDirectory: string,
): Promise<Option.Option<GlobalDaemonDiscoveryMetadata>> =>
	runWithBunContext(
		Effect.gen(function* () {
			const discovery = yield* GlobalDaemonDiscovery
			return yield* discovery.readDiscovery({ homeDirectory })
		}),
	)

const waitForDiscovery = async (
	homeDirectory: string,
	timeoutMs: number,
): Promise<GlobalDaemonDiscoveryMetadata> => {
	const startedAtMs = Date.now()
	while (Date.now() - startedAtMs <= timeoutMs) {
		const discovery = await readDiscovery(homeDirectory)
		if (Option.isSome(discovery)) {
			return discovery.value
		}
		await sleep(30)
	}
	throw new Error("Timed out waiting for global daemon discovery metadata")
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

const spawnGlobalDaemon = (homeDirectory: string): SpawnedDaemon =>
	spawn(process.execPath, ["run", daemonMainPath], {
		cwd: projectRoot,
		env: {
			...process.env,
			HOME: homeDirectory,
		},
		stdio: ["ignore", "pipe", "pipe"],
	})

const waitForExit = async (
	child: SpawnedDaemon,
	timeoutMs: number,
): Promise<{ readonly code: number | null; readonly signal: NodeJS.Signals | null }> => {
	return await new Promise((resolve, reject) => {
		const timeout = setTimeout(() => {
			reject(new Error("Timed out waiting for child process exit"))
		}, timeoutMs)
		child.once("exit", (code, signal) => {
			clearTimeout(timeout)
			resolve({ code, signal })
		})
	})
}

const terminateChild = async (child: SpawnedDaemon): Promise<void> => {
	if (child.killed || child.exitCode !== null) {
		return
	}
	child.kill("SIGTERM")
	try {
		await waitForExit(child, 3_000)
	} catch {
		child.kill("SIGKILL")
		await waitForExit(child, 3_000)
	}
}

const withIsolatedHome = async <A>(fn: (homeDirectory: string) => Promise<A>): Promise<A> => {
	const homeDirectory = mkdtempSync(join(tmpdir(), "az-global-daemon-ipc-"))
	const previousHome = process.env.HOME
	process.env.HOME = homeDirectory
	try {
		return await fn(homeDirectory)
	} finally {
		process.env.HOME = previousHome
		rmSync(homeDirectory, { recursive: true, force: true })
	}
}

describe("GlobalDaemonIpc integration", () => {
	it("verifies singleton reuse across independent process invocations", async () => {
		await withIsolatedHome(async (homeDirectory) => {
			let lease: GlobalDaemonLease | null = null
			let daemonB: SpawnedDaemon | null = null
			try {
				lease = await runWithBunContext(acquireLease(homeDirectory))
				const firstDiscovery = await waitForDiscovery(homeDirectory, 8_000)
				expect(firstDiscovery.pid).toBe(process.pid)
				expect(firstDiscovery.lockId).toBe(lease.lockId)

				daemonB = spawnGlobalDaemon(homeDirectory)
				const secondExit = await waitForExit(daemonB, 8_000)
				expect(secondExit.code).not.toBeNull()

				const discoveryAfterSecondStart = await waitForDiscovery(homeDirectory, 3_000)
				expect(discoveryAfterSecondStart.pid).toBe(firstDiscovery.pid)
				expect(discoveryAfterSecondStart.lockId).toBe(firstDiscovery.lockId)
			} finally {
				if (daemonB !== null) {
					await terminateChild(daemonB)
				}
				if (lease !== null) {
					await runWithBunContext(releaseLease(lease).pipe(Effect.ignore))
				}
				await waitForNoDiscovery(homeDirectory, 8_000).catch(() => undefined)
			}
		})
	}, 30_000)

	it("verifies real CLI and TUI convergence on the same daemon endpoint with performance gates", async () => {
		await withIsolatedHome(async (homeDirectory) => {
			let lease: GlobalDaemonLease | null = null
			const coldStartBegin = Date.now()
			lease = await runWithBunContext(acquireLease(homeDirectory))
			try {
				const discovery = await waitForDiscovery(homeDirectory, 8_000)
				const coldActivationMs = Date.now() - coldStartBegin
				const hotSwitchBegin = Date.now()
				const cliAttach = await runWithBunContext(
					bootstrapDaemonRpcClient({
						autoStart: false,
						timeoutMs: 8_000,
					}),
				)
				const tuiAttach = await runWithBunContext(
					bootstrapDaemonRpcClient({
						autoStart: false,
						timeoutMs: 8_000,
					}),
				)
				const hotSwitchMs = Date.now() - hotSwitchBegin

				expect(discovery.pid).toBeGreaterThan(0)
				expect(discovery.pid).toBe(process.pid)
				expect(cliAttach.startedDaemon).toBe(false)
				expect(tuiAttach.startedDaemon).toBe(false)
				expect(cliAttach.attachAttemptCount).toBeGreaterThanOrEqual(1)
				expect(tuiAttach.attachAttemptCount).toBeGreaterThanOrEqual(1)
				expect(cliAttach.discovery.pid).toBe(tuiAttach.discovery.pid)
				expect(cliAttach.discovery.lockId).toBe(tuiAttach.discovery.lockId)
				expect(cliAttach.socketUrl).toBe(tuiAttach.socketUrl)
				expect(coldActivationMs).toBeLessThan(COLD_ACTIVATION_TARGET_MS)
				expect(hotSwitchMs).toBeLessThan(HOT_SWITCH_TARGET_MS)
			} finally {
				if (lease !== null) {
					await runWithBunContext(releaseLease(lease).pipe(Effect.ignore))
				}
				await waitForNoDiscovery(homeDirectory, 8_000).catch(() => undefined)
			}
		})
	}, 30_000)

	it("verifies daemon stop/restart with client reconnect semantics", async () => {
		await withIsolatedHome(async (homeDirectory) => {
			let leaseA: GlobalDaemonLease | null = null
			let leaseB: GlobalDaemonLease | null = null
			leaseA = await runWithBunContext(acquireLease(homeDirectory))
			const firstDiscovery = await waitForDiscovery(homeDirectory, 8_000)
			const firstAttach = await runWithBunContext(
				bootstrapDaemonRpcClient({
					autoStart: false,
					timeoutMs: 8_000,
				}),
			)
			try {
				expect(firstAttach.startedDaemon).toBe(false)
				expect(firstAttach.attachAttemptCount).toBeGreaterThanOrEqual(1)
				await runWithBunContext(releaseLease(leaseA))
				leaseA = null
				await waitForNoDiscovery(homeDirectory, 8_000)

				leaseB = await runWithBunContext(acquireLease(homeDirectory))
				const restartedDiscovery = await waitForDiscovery(homeDirectory, 8_000)
				const restartedAttach = await runWithBunContext(
					bootstrapDaemonRpcClient({
						autoStart: false,
						timeoutMs: 8_000,
					}),
				)
				const reconnectAttach = await runWithBunContext(
					bootstrapDaemonRpcClient({
						autoStart: false,
						timeoutMs: 8_000,
					}),
				)

				expect(restartedAttach.startedDaemon).toBe(false)
				expect(reconnectAttach.startedDaemon).toBe(false)
				expect(restartedAttach.attachAttemptCount).toBeGreaterThanOrEqual(1)
				expect(reconnectAttach.attachAttemptCount).toBeGreaterThanOrEqual(1)
				expect(restartedDiscovery.lockId).not.toBe(firstDiscovery.lockId)
				expect(restartedDiscovery.socketPath).toBe(firstDiscovery.socketPath)
				expect(restartedAttach.discovery.pid).toBe(restartedDiscovery.pid)
				expect(restartedAttach.discovery.lockId).toBe(restartedDiscovery.lockId)
				expect(reconnectAttach.discovery.pid).toBe(restartedDiscovery.pid)
				expect(reconnectAttach.discovery.lockId).toBe(restartedDiscovery.lockId)
				expect(reconnectAttach.socketUrl).toBe(restartedAttach.socketUrl)
			} finally {
				if (leaseA !== null) {
					await runWithBunContext(releaseLease(leaseA).pipe(Effect.ignore))
				}
				if (leaseB !== null) {
					await runWithBunContext(releaseLease(leaseB).pipe(Effect.ignore))
				}
				await waitForNoDiscovery(homeDirectory, 8_000).catch(() => undefined)
			}
		})
	}, 30_000)

	it("retries reconnect attach deterministically after interruption until daemon is back", async () => {
		await withIsolatedHome(async (homeDirectory) => {
			let leaseA: GlobalDaemonLease | null = null
			let leaseB: GlobalDaemonLease | null = null
			leaseA = await runWithBunContext(acquireLease(homeDirectory))
			await waitForDiscovery(homeDirectory, 8_000)
			try {
				await runWithBunContext(
					bootstrapDaemonRpcClient({
						autoStart: false,
						timeoutMs: 8_000,
					}),
				)

				await runWithBunContext(releaseLease(leaseA))
				leaseA = null
				await waitForNoDiscovery(homeDirectory, 8_000)

				const attempts: Array<{
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
							attempts.push({
								attempt: observation.attempt,
								delayMs: observation.delayMs,
								timeoutRemainingMs: observation.timeoutRemainingMs,
								socketPath: observation.socketPath,
							})
						},
					}),
				)

				await sleep(90)
				leaseB = await runWithBunContext(acquireLease(homeDirectory))
				const restartedDiscovery = await waitForDiscovery(homeDirectory, 8_000)

				const reconnectAttach = await reconnectPromise
				expect(reconnectAttach.startedDaemon).toBe(false)
				expect(reconnectAttach.discovery.lockId).toBe(restartedDiscovery.lockId)
				expect(reconnectAttach.discovery.pid).toBe(restartedDiscovery.pid)
				expect(reconnectAttach.attachAttemptCount).toBe(attempts.length)
				expect(reconnectAttach.attachAttemptCount).toBeGreaterThan(1)
				expect(attempts[0]?.delayMs).toBe(0)
				expect(attempts.some((attempt) => attempt.socketPath === null)).toBe(true)
				expect(
					attempts.every((attempt, index) =>
						index === 0
							? attempt.attempt === 1
							: attempt.attempt === attempts[index - 1]!.attempt + 1,
					),
				).toBe(true)
			} finally {
				if (leaseA !== null) {
					await runWithBunContext(releaseLease(leaseA).pipe(Effect.ignore))
				}
				if (leaseB !== null) {
					await runWithBunContext(releaseLease(leaseB).pipe(Effect.ignore))
				}
				await waitForNoDiscovery(homeDirectory, 8_000).catch(() => undefined)
			}
		})
	}, 30_000)

	it("allows multi-client attach fanout on one daemon endpoint without a fixed cap", async () => {
		await withIsolatedHome(async (homeDirectory) => {
			let lease: GlobalDaemonLease | null = null
			lease = await runWithBunContext(acquireLease(homeDirectory))
			try {
				const discovery = await waitForDiscovery(homeDirectory, 8_000)
				const attaches = await Promise.all(
					Array.from({ length: ATTACH_FANOUT_CLIENT_COUNT }, () =>
						runWithBunContext(
							bootstrapDaemonRpcClient({
								autoStart: false,
								timeoutMs: 8_000,
							}),
						),
					),
				)

				expect(attaches.length).toBe(ATTACH_FANOUT_CLIENT_COUNT)
				for (const attach of attaches) {
					expect(attach.startedDaemon).toBe(false)
					expect(attach.attachAttemptCount).toBeGreaterThanOrEqual(1)
					expect(attach.discovery.pid).toBe(discovery.pid)
					expect(attach.discovery.lockId).toBe(discovery.lockId)
					expect(attach.socketUrl).toBe(`ws+unix://${discovery.socketPath}:/`)
				}
			} finally {
				if (lease !== null) {
					await runWithBunContext(releaseLease(lease).pipe(Effect.ignore))
				}
				await waitForNoDiscovery(homeDirectory, 8_000).catch(() => undefined)
			}
		})
	}, 30_000)
})
