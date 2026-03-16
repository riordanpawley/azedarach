import { describe, expect, it } from "bun:test"
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import type { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Effect, Option } from "effect"
import {
	acquireDaemonSyncInstanceLease,
	checkDaemonOwnerLiveness,
	DaemonInstanceAlreadyRunningError,
	formatDaemonInstanceAlreadyRunningMessage,
	readDaemonSyncDiscovery,
	releaseDaemonSyncInstanceLease,
	resolveDaemonSyncLockPaths,
} from "./DaemonInstanceRegistry.js"

const runWithBunContext = <A, E>(effect: Effect.Effect<A, E, FileSystem.FileSystem | Path.Path>) =>
	Effect.runPromise(effect.pipe(Effect.provide(BunContext.layer)))

describe("DaemonInstanceRegistry", () => {
	it("writes typed discovery metadata and releases lock ownership", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-daemon-registry-"))

		try {
			const lease = await runWithBunContext(
				acquireDaemonSyncInstanceLease({
					projectPath,
					endpoint: {
						protocol: "local-sync",
						address: projectPath,
					},
				}),
			)

			const discovery = await runWithBunContext(readDaemonSyncDiscovery(projectPath))
			expect(Option.isSome(discovery)).toBe(true)
			if (Option.isSome(discovery)) {
				expect(discovery.value.pid).toBe(process.pid)
				expect(discovery.value.projectPath).toBe(projectPath)
				expect(discovery.value.lockId).toBe(lease.lockId)

				const liveness = await runWithBunContext(checkDaemonOwnerLiveness(discovery.value))
				expect(liveness).toBe("alive")
			}

			await runWithBunContext(releaseDaemonSyncInstanceLease(lease))

			const afterRelease = await runWithBunContext(readDaemonSyncDiscovery(projectPath))
			expect(Option.isNone(afterRelease)).toBe(true)
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("rejects startup when a live owner already holds the lock", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-daemon-registry-live-"))

		try {
			const firstLease = await runWithBunContext(
				acquireDaemonSyncInstanceLease({
					projectPath,
					endpoint: {
						protocol: "local-sync",
						address: projectPath,
					},
				}),
			)

			const secondAcquire = await runWithBunContext(
				acquireDaemonSyncInstanceLease({
					projectPath,
					endpoint: {
						protocol: "local-sync",
						address: projectPath,
					},
				}).pipe(
					Effect.catchTag("DaemonInstanceAlreadyRunningError", (error) => Effect.succeed(error)),
				),
			)

			expect(secondAcquire instanceof DaemonInstanceAlreadyRunningError).toBe(true)
			if (secondAcquire instanceof DaemonInstanceAlreadyRunningError) {
				expect(secondAcquire.owner.pid).toBe(process.pid)
				const message = formatDaemonInstanceAlreadyRunningMessage(secondAcquire)
				expect(message.includes(projectPath)).toBe(true)
				expect(message.includes("already running")).toBe(true)
			}

			await runWithBunContext(releaseDaemonSyncInstanceLease(firstLease))
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("recovers stale lock ownership when recorded pid is dead", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-daemon-registry-stale-"))

		try {
			const paths = resolveDaemonSyncLockPaths(projectPath, { join })
			mkdirSync(paths.lockDirectory, { recursive: true })
			writeFileSync(
				paths.ownerMetadataPath,
				JSON.stringify({
					schemaVersion: 1,
					daemonKind: "backend-sync",
					projectPath,
					pid: 999_999,
					lockId: "stale-lock-id",
					acquiredAtMs: 1,
					endpoint: {
						protocol: "local-sync",
						address: projectPath,
					},
				}),
				"utf8",
			)

			const lease = await runWithBunContext(
				acquireDaemonSyncInstanceLease({
					projectPath,
					endpoint: {
						protocol: "local-sync",
						address: projectPath,
					},
				}),
			)

			const discovery = await runWithBunContext(readDaemonSyncDiscovery(projectPath))
			expect(Option.isSome(discovery)).toBe(true)
			if (Option.isSome(discovery)) {
				expect(discovery.value.pid).toBe(process.pid)
				expect(discovery.value.lockId).toBe(lease.lockId)
			}

			await runWithBunContext(releaseDaemonSyncInstanceLease(lease))
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})
})
