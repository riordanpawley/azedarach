import { describe, expect, it } from "bun:test"
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import {
	GlobalDaemonAlreadyRunningError,
	GlobalDaemonDiscovery,
	type GlobalDaemonDiscoveryApi,
	type GlobalDaemonLease,
} from "@azedarach/shared"
import type { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Effect, Option } from "effect"

const runWithBunContext = <A, E>(
	effect: Effect.Effect<A, E, GlobalDaemonDiscoveryApi | FileSystem.FileSystem | Path.Path>,
) =>
	Effect.runPromise(
		effect.pipe(Effect.provide(GlobalDaemonDiscovery.Default), Effect.provide(BunContext.layer)),
	)

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

const readDiscovery = (homeDirectory: string) =>
	Effect.gen(function* () {
		const discovery = yield* GlobalDaemonDiscovery
		return yield* discovery.readDiscovery({ homeDirectory })
	})

const resolvePaths = (homeDirectory: string) =>
	Effect.gen(function* () {
		const discovery = yield* GlobalDaemonDiscovery
		return yield* discovery.resolvePaths({ homeDirectory })
	})

describe("GlobalDaemonDiscovery", () => {
	it("acquires and releases a global daemon lease with discovery metadata", async () => {
		const homeDirectory = mkdtempSync(join(tmpdir(), "az-global-daemon-home-"))

		try {
			const lease = await runWithBunContext(acquireLease(homeDirectory))
			const discovery = await runWithBunContext(readDiscovery(homeDirectory))
			expect(Option.isSome(discovery)).toBe(true)
			if (Option.isSome(discovery)) {
				expect(discovery.value.pid).toBe(process.pid)
				expect(discovery.value.lockId).toBe(lease.lockId)
				expect(discovery.value.socketPath.endsWith("global.sock")).toBe(true)
			}

			await runWithBunContext(releaseLease(lease))
			const afterRelease = await runWithBunContext(readDiscovery(homeDirectory))
			expect(Option.isNone(afterRelease)).toBe(true)
		} finally {
			rmSync(homeDirectory, { recursive: true, force: true })
		}
	})

	it("rejects acquisition when a live owner already holds the global lock", async () => {
		const homeDirectory = mkdtempSync(join(tmpdir(), "az-global-daemon-live-"))
		try {
			const first = await runWithBunContext(acquireLease(homeDirectory))
			const secondAttempt = await runWithBunContext(
				acquireLease(homeDirectory).pipe(
					Effect.catchTag("GlobalDaemonAlreadyRunningError", (error) => Effect.succeed(error)),
				),
			)
			expect(secondAttempt instanceof GlobalDaemonAlreadyRunningError).toBe(true)
			if (secondAttempt instanceof GlobalDaemonAlreadyRunningError) {
				expect(secondAttempt.discovery.pid).toBe(process.pid)
			}

			await runWithBunContext(releaseLease(first))
		} finally {
			rmSync(homeDirectory, { recursive: true, force: true })
		}
	})

	it("recovers stale lock ownership when existing pid is dead", async () => {
		const homeDirectory = mkdtempSync(join(tmpdir(), "az-global-daemon-stale-"))
		try {
			const paths = await runWithBunContext(resolvePaths(homeDirectory))
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

			const lease = await runWithBunContext(acquireLease(homeDirectory))
			const discovery = await runWithBunContext(readDiscovery(homeDirectory))
			expect(Option.isSome(discovery)).toBe(true)
			if (Option.isSome(discovery)) {
				expect(discovery.value.lockId).toBe(lease.lockId)
				expect(discovery.value.pid).toBe(process.pid)
			}
			await runWithBunContext(releaseLease(lease))
		} finally {
			rmSync(homeDirectory, { recursive: true, force: true })
		}
	})

	it("recovers stale lock without discovery and clears stale socket path", async () => {
		const homeDirectory = mkdtempSync(join(tmpdir(), "az-global-daemon-missing-discovery-"))
		try {
			const paths = await runWithBunContext(resolvePaths(homeDirectory))
			mkdirSync(paths.lockDir, { recursive: true })
			mkdirSync(paths.daemonDir, { recursive: true })
			writeFileSync(paths.socketPath, "stale-socket-file", "utf8")

			const lease = await runWithBunContext(acquireLease(homeDirectory))
			const discovery = await runWithBunContext(readDiscovery(homeDirectory))
			expect(Option.isSome(discovery)).toBe(true)
			expect(existsSync(paths.socketPath)).toBe(false)
			if (Option.isSome(discovery)) {
				expect(discovery.value.lockId).toBe(lease.lockId)
				expect(discovery.value.pid).toBe(process.pid)
			}
			await runWithBunContext(releaseLease(lease))
		} finally {
			rmSync(homeDirectory, { recursive: true, force: true })
		}
	})
})
