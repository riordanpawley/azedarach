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
	acquireGlobalDaemonLease,
	type GlobalDaemonDiscovery,
	readGlobalDaemonDiscovery,
	releaseGlobalDaemonLease,
} from "../core/GlobalDaemonRegistry.js"

const projectRoot = process.cwd()
const daemonMainPath = join(projectRoot, "src/daemon/GlobalDaemonMain.ts")
type SpawnedDaemon = ChildProcessByStdio<null, Readable, Readable>

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

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

describe("GlobalDaemonIpc integration", () => {
	it("scaffolds singleton reuse and reconnect behavior across spawned daemon processes", async () => {
		const homeDirectory = mkdtempSync(join(tmpdir(), "az-global-daemon-ipc-"))
		const lease = await runWithBunContext(acquireGlobalDaemonLease({ homeDirectory }))
		let daemonB: SpawnedDaemon | null = null
		let daemonC: SpawnedDaemon | null = null

		try {
			const firstDiscovery = await waitForDiscovery(homeDirectory, 3_000)
			expect(firstDiscovery.pid).toBe(process.pid)
			expect(firstDiscovery.lockId).toBe(lease.lockId)

			daemonB = spawnGlobalDaemon(homeDirectory)
			const secondExit = await waitForExit(daemonB, 8_000)
			expect(secondExit.code).not.toBeNull()

			const discoveryAfterSecondStart = await waitForDiscovery(homeDirectory, 3_000)
			expect(discoveryAfterSecondStart.pid).toBe(firstDiscovery.pid)
			expect(discoveryAfterSecondStart.lockId).toBe(firstDiscovery.lockId)
			expect(discoveryAfterSecondStart.socketPath).toBe(firstDiscovery.socketPath)

			await runWithBunContext(releaseGlobalDaemonLease(lease))
			await waitForNoDiscovery(homeDirectory, 8_000)

			daemonC = spawnGlobalDaemon(homeDirectory)
			const recoveredDiscovery = await waitForDiscovery(homeDirectory, 8_000)
			expect(recoveredDiscovery.pid).not.toBe(firstDiscovery.pid)
			expect(recoveredDiscovery.socketPath).toBe(firstDiscovery.socketPath)
		} finally {
			await runWithBunContext(releaseGlobalDaemonLease(lease).pipe(Effect.ignore))
			if (daemonB !== null) {
				await terminateChild(daemonB)
			}
			if (daemonC !== null) {
				await terminateChild(daemonC)
			}
			rmSync(homeDirectory, { recursive: true, force: true })
		}
	})

	it("scaffolds CLI and TUI convergence by validating shared discovery endpoint semantics", async () => {
		const homeDirectory = mkdtempSync(join(tmpdir(), "az-global-daemon-converge-"))
		const daemon = spawnGlobalDaemon(homeDirectory)
		try {
			const cliView = await waitForDiscovery(homeDirectory, 8_000)
			const tuiView = await waitForDiscovery(homeDirectory, 8_000)

			expect(cliView.pid).toBe(tuiView.pid)
			expect(cliView.lockId).toBe(tuiView.lockId)
			expect(cliView.socketPath).toBe(tuiView.socketPath)
		} finally {
			await terminateChild(daemon)
			rmSync(homeDirectory, { recursive: true, force: true })
		}
	})
})
