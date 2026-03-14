import { describe, expect, it } from "bun:test"
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import type { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Effect, Option } from "effect"
import {
	acquireGlobalDaemonLease,
	GlobalDaemonAlreadyRunningError,
	readGlobalDaemonDiscovery,
	releaseGlobalDaemonLease,
	resolveGlobalDaemonRegistryPaths,
} from "./GlobalDaemonRegistry.js"

const runWithBunContext = <A, E>(effect: Effect.Effect<A, E, FileSystem.FileSystem | Path.Path>) =>
	Effect.runPromise(effect.pipe(Effect.provide(BunContext.layer)))

describe("GlobalDaemonRegistry", () => {
	it("acquires and releases a global daemon lease with discovery metadata", async () => {
		const homeDirectory = mkdtempSync(join(tmpdir(), "az-global-daemon-home-"))

		try {
			const lease = await runWithBunContext(acquireGlobalDaemonLease({ homeDirectory }))
			const discovery = await runWithBunContext(readGlobalDaemonDiscovery({ homeDirectory }))
			expect(Option.isSome(discovery)).toBe(true)
			if (Option.isSome(discovery)) {
				expect(discovery.value.pid).toBe(process.pid)
				expect(discovery.value.lockId).toBe(lease.lockId)
				expect(discovery.value.socketPath.endsWith("global.sock")).toBe(true)
			}

			await runWithBunContext(releaseGlobalDaemonLease(lease))
			const afterRelease = await runWithBunContext(readGlobalDaemonDiscovery({ homeDirectory }))
			expect(Option.isNone(afterRelease)).toBe(true)
		} finally {
			rmSync(homeDirectory, { recursive: true, force: true })
		}
	})

	it("rejects acquisition when a live owner already holds the global lock", async () => {
		const homeDirectory = mkdtempSync(join(tmpdir(), "az-global-daemon-live-"))
		try {
			const first = await runWithBunContext(acquireGlobalDaemonLease({ homeDirectory }))
			const secondAttempt = await runWithBunContext(
				acquireGlobalDaemonLease({ homeDirectory }).pipe(
					Effect.catchTag("GlobalDaemonAlreadyRunningError", (error) => Effect.succeed(error)),
				),
			)
			expect(secondAttempt instanceof GlobalDaemonAlreadyRunningError).toBe(true)
			if (secondAttempt instanceof GlobalDaemonAlreadyRunningError) {
				expect(secondAttempt.discovery.pid).toBe(process.pid)
			}

			await runWithBunContext(releaseGlobalDaemonLease(first))
		} finally {
			rmSync(homeDirectory, { recursive: true, force: true })
		}
	})

	it("recovers stale lock ownership when existing pid is dead", async () => {
		const homeDirectory = mkdtempSync(join(tmpdir(), "az-global-daemon-stale-"))
		try {
			const paths = await runWithBunContext(
				resolveGlobalDaemonRegistryPaths({
					homeDirectory,
					pathOps: { join },
				}),
			)
			mkdirSync(paths.lockDir, { recursive: true })
			writeFileSync(
				paths.discoveryPath,
				JSON.stringify({
					schemaVersion: 1,
					pid: 999_999,
					lockId: "stale-lock",
					socketPath: paths.socketPath,
					startedAtMs: 1,
				}),
				"utf8",
			)

			const lease = await runWithBunContext(acquireGlobalDaemonLease({ homeDirectory }))
			const discovery = await runWithBunContext(readGlobalDaemonDiscovery({ homeDirectory }))
			expect(Option.isSome(discovery)).toBe(true)
			if (Option.isSome(discovery)) {
				expect(discovery.value.lockId).toBe(lease.lockId)
				expect(discovery.value.pid).toBe(process.pid)
			}
			await runWithBunContext(releaseGlobalDaemonLease(lease))
		} finally {
			rmSync(homeDirectory, { recursive: true, force: true })
		}
	})
})
