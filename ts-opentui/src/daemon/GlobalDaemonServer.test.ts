import { describe, expect, it } from "bun:test"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import type { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Effect } from "effect"
import {
	GlobalDaemonDiscovery,
	type GlobalDaemonDiscoveryApi,
} from "../../packages/daemon/src/index.js"
import {
	makeGlobalDaemonServerRuntime,
	startGlobalDaemonServer,
	stopGlobalDaemonServer,
} from "./GlobalDaemonServer.js"

const runWithBunContext = <A, E>(
	effect: Effect.Effect<A, E, GlobalDaemonDiscoveryApi | FileSystem.FileSystem | Path.Path>,
) =>
	Effect.runPromise(
		effect.pipe(Effect.provide(GlobalDaemonDiscovery.Default), Effect.provide(BunContext.layer)),
	)

describe("GlobalDaemonServer", () => {
	it("tracks project runtime map, idle sweep, and shutdown observability", async () => {
		const runtime = await runWithBunContext(
			makeGlobalDaemonServerRuntime({
				socketPath: "/tmp/az-global-daemon-test.sock",
				idleTimeoutMs: 100,
				nowMs: 1_000,
			}),
		)

		const touchedA = await runWithBunContext(runtime.touchProjectRuntime("/tmp/project-a", 1_010))
		const touchedB = await runWithBunContext(runtime.touchProjectRuntime("/tmp/project-b", 1_020))
		expect(touchedA.requestCount).toBe(1)
		expect(touchedB.requestCount).toBe(1)

		await runWithBunContext(runtime.touchProjectRuntime("/tmp/project-a", 1_030))
		const evicted = await runWithBunContext(runtime.sweepIdleRuntimes(1_140))
		expect(evicted).toEqual(["/tmp/project-b", "/tmp/project-a"])

		const observation = await runWithBunContext(runtime.observeIdleState(1_150))
		expect(observation.runtimeCount).toBe(0)
		expect(observation.idleForMs).toBeGreaterThanOrEqual(100)

		const shutdown = await runWithBunContext(runtime.requestShutdown("test_shutdown", 1_160))
		expect(shutdown.shuttingDown).toBe(true)
		expect(shutdown.shutdownReason).toBe("test_shutdown")
		expect((shutdown.events.at(-1) ?? null)?.event).toBe("shutdown_requested")
		expect(shutdown.events[0]?.reason).toBe("runtime created (cold)")
		expect(shutdown.events[2]?.reason).toBe("runtime reused (hot)")
	})

	it("starts global daemon server with lease and shuts down cleanly", async () => {
		const homeDirectory = mkdtempSync(join(tmpdir(), "az-global-daemon-server-"))
		try {
			const handle = await runWithBunContext(
				startGlobalDaemonServer({
					homeDirectory,
					idleTimeoutMs: 250,
				}),
			)

			expect(handle.lease.paths.socketPath.endsWith("global.sock")).toBe(true)
			await runWithBunContext(handle.runtime.touchProjectRuntime("/tmp/project-a", 2_000))
			const state = await runWithBunContext(handle.runtime.getState())
			expect(state.runtimeCount).toBe(1)
			expect(state.events.at(-1)?.event).toBe("runtime_touched")

			await runWithBunContext(stopGlobalDaemonServer(handle, "test_complete"))
		} finally {
			rmSync(homeDirectory, { recursive: true, force: true })
		}
	})
})
