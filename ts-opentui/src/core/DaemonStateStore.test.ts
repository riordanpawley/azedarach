import { describe, expect, it } from "bun:test"
import { mkdtempSync, readdirSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Effect, Option } from "effect"
import type { BackendSyncDaemonStatus } from "./BackendSyncDaemonService.js"
import {
	makeDaemonStateStore,
	resolveDaemonStateStorePaths,
	toDaemonStatus,
} from "./DaemonStateStore.js"

const runWithBunContext = <A, E>(effect: Effect.Effect<A, E, FileSystem.FileSystem | Path.Path>) =>
	Effect.runPromise(effect.pipe(Effect.provide(BunContext.layer)))

const makeStatus = (projectPath: string): BackendSyncDaemonStatus => ({
	state: "crashed",
	generation: 4,
	projectPath,
	intervalMs: 50,
	startedAtMs: 3_000,
	runCount: 12,
	successCount: 8,
	failureCount: 4,
	failureStreak: 4,
	restartStreak: 4,
	lastBackoffMs: 200,
	lastSuccessfulRunAtMs: 2_500,
	lastRun: {
		runAtMs: 2_800,
		result: "failed",
		pushed: 0,
		pulled: 0,
		message: "boom",
	},
	lastError: "boom",
})

describe("DaemonStateStore", () => {
	it("persists and reloads versioned daemon state", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-daemon-state-store-"))
		try {
			const persisted = await runWithBunContext(
				Effect.gen(function* () {
					const fs = yield* FileSystem.FileSystem
					const path = yield* Path.Path
					const runtimeStore = makeDaemonStateStore({ fs, path })
					const status = makeStatus(projectPath)
					yield* runtimeStore.persist(projectPath, status)
					return yield* runtimeStore.load(projectPath)
				}),
			)

			expect(Option.isSome(persisted)).toBe(true)
			if (Option.isSome(persisted)) {
				const recoveredStatus = toDaemonStatus(persisted.value)
				expect(recoveredStatus.runCount).toBe(12)
				expect(recoveredStatus.failureStreak).toBe(4)
				expect(recoveredStatus.lastBackoffMs).toBe(200)
				expect(recoveredStatus.lastRun?.result).toBe("failed")
				expect(recoveredStatus.lastError).toBe("boom")
			}
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("treats corrupted persisted state as recoverable-none", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-daemon-state-store-corrupt-"))
		try {
			const persisted = await runWithBunContext(
				Effect.gen(function* () {
					const fs = yield* FileSystem.FileSystem
					const path = yield* Path.Path
					const runtimeStore = makeDaemonStateStore({ fs, path })
					const paths = resolveDaemonStateStorePaths(projectPath, path)
					yield* fs.makeDirectory(paths.daemonDirectory, { recursive: true })
					yield* fs.writeFileString(paths.statePath, "{invalid-json")
					return yield* runtimeStore.load(projectPath)
				}),
			)

			expect(Option.isNone(persisted)).toBe(true)
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("uses temp-file rename persistence without leaving temp artifacts", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-daemon-state-store-atomic-"))
		try {
			const paths = await runWithBunContext(
				Effect.gen(function* () {
					const fs = yield* FileSystem.FileSystem
					const path = yield* Path.Path
					const runtimeStore = makeDaemonStateStore({ fs, path })
					const resolved = resolveDaemonStateStorePaths(projectPath, path)
					yield* runtimeStore.persist(projectPath, makeStatus(projectPath))
					return resolved
				}),
			)

			const files = readdirSync(paths.daemonDirectory)
			expect(files).toContain("backend-sync-state.json")
			expect(files.some((name) => name.endsWith(".tmp"))).toBe(false)
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})
})
