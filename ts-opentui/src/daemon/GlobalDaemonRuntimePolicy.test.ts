import { describe, expect, it } from "bun:test"
import type { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Effect } from "effect"
import { makeGlobalDaemonServerRuntime } from "./GlobalDaemonServer.js"

const runWithBunContext = <A, E>(effect: Effect.Effect<A, E, FileSystem.FileSystem | Path.Path>) =>
	Effect.runPromise(effect.pipe(Effect.provide(BunContext.layer)))

const createRuntime = (params?: {
	readonly idleTimeoutMs?: number
	readonly nowMs?: number
	readonly recordIdleEvictionEvents?: boolean
}) =>
	runWithBunContext(
		makeGlobalDaemonServerRuntime({
			socketPath: "/tmp/az-global-daemon-runtime-policy.sock",
			idleTimeoutMs: params?.idleTimeoutMs ?? 100,
			nowMs: params?.nowMs ?? 1_000,
			recordIdleEvictionEvents: params?.recordIdleEvictionEvents,
		}),
	)

const measureTouchBatch = async (params: {
	readonly mode: "cold" | "hot_switch"
	readonly sampleCount: number
	readonly startMs: number
}): Promise<{ readonly elapsedMs: number; readonly perTouchMs: number }> => {
	const runtime = await createRuntime({
		idleTimeoutMs: 60_000,
		nowMs: params.startMs,
	})

	if (params.mode === "hot_switch") {
		await runWithBunContext(runtime.touchProjectRuntime("/tmp/perf-hot-a", params.startMs - 2))
		await runWithBunContext(runtime.touchProjectRuntime("/tmp/perf-hot-b", params.startMs - 1))
	}

	const startedAt = performance.now()
	await runWithBunContext(
		Effect.gen(function* () {
			for (let index = 0; index < params.sampleCount; index += 1) {
				const projectPath =
					params.mode === "cold"
						? `/tmp/perf-cold-${index}`
						: index % 2 === 0
							? "/tmp/perf-hot-a"
							: "/tmp/perf-hot-b"
				yield* runtime.touchProjectRuntime(projectPath, params.startMs + index)
			}
		}),
	)
	const elapsedMs = performance.now() - startedAt
	return {
		elapsedMs,
		perTouchMs: elapsedMs / params.sampleCount,
	}
}

const median = (values: ReadonlyArray<number>): number => {
	const sorted = [...values].sort((left, right) => left - right)
	const middle = Math.floor(sorted.length / 2)
	if (sorted.length % 2 === 0) {
		return (sorted[middle - 1] + sorted[middle]) / 2
	}
	return sorted[middle]
}

describe("GlobalDaemonRuntimePolicy", () => {
	it("uses deterministic idle eviction order and optional idle eviction bookkeeping", async () => {
		const runtime = await createRuntime({
			idleTimeoutMs: 100,
			nowMs: 10_000,
			recordIdleEvictionEvents: false,
		})

		await runWithBunContext(runtime.touchProjectRuntime("/tmp/project-b", 10_010))
		await runWithBunContext(runtime.touchProjectRuntime("/tmp/project-a", 10_010))
		await runWithBunContext(runtime.touchProjectRuntime("/tmp/project-c", 10_080))

		const evicted = await runWithBunContext(runtime.sweepIdleRuntimes(10_120))
		expect(evicted).toEqual(["/tmp/project-a", "/tmp/project-b"])

		const state = await runWithBunContext(runtime.getState())
		expect(state.runtimeCount).toBe(1)
		expect(Object.keys(state.runtimes)).toEqual(["/tmp/project-c"])
		expect(state.events.some((event) => event.event === "runtime_evicted_idle")).toBe(false)
	})

	it("preserves multi-project isolation while reusing and evicting runtimes", async () => {
		const runtime = await createRuntime({
			idleTimeoutMs: 100,
			nowMs: 20_000,
		})

		await runWithBunContext(runtime.touchProjectRuntime("/tmp/project-a", 20_010))
		await runWithBunContext(runtime.touchProjectRuntime("/tmp/project-b", 20_020))
		await runWithBunContext(runtime.touchProjectRuntime("/tmp/project-a", 20_030))

		const stateBeforeSweep = await runWithBunContext(runtime.getState())
		expect(stateBeforeSweep.runtimes["/tmp/project-a"]?.requestCount).toBe(2)
		expect(stateBeforeSweep.runtimes["/tmp/project-a"]?.createdAtMs).toBe(20_010)
		expect(stateBeforeSweep.runtimes["/tmp/project-a"]?.lastTouchedAtMs).toBe(20_030)
		expect(stateBeforeSweep.runtimes["/tmp/project-b"]?.requestCount).toBe(1)
		expect(stateBeforeSweep.runtimes["/tmp/project-b"]?.lastTouchedAtMs).toBe(20_020)

		const evicted = await runWithBunContext(runtime.sweepIdleRuntimes(20_121))
		expect(evicted).toEqual(["/tmp/project-b"])

		const stateAfterSweep = await runWithBunContext(runtime.getState())
		expect(Object.keys(stateAfterSweep.runtimes)).toEqual(["/tmp/project-a"])
		expect(stateAfterSweep.runtimes["/tmp/project-a"]?.requestCount).toBe(2)
	})

	it("meets executable hot-switch vs cold-activation performance targets", async () => {
		const rounds = 3
		const sampleCount = 2_000
		const coldSamples: number[] = []
		const hotSwitchSamples: number[] = []

		for (let round = 0; round < rounds; round += 1) {
			const baseMs = 30_000 + round * 10_000
			const cold = await measureTouchBatch({
				mode: "cold",
				sampleCount,
				startMs: baseMs,
			})
			const hotSwitch = await measureTouchBatch({
				mode: "hot_switch",
				sampleCount,
				startMs: baseMs + 5_000,
			})
			coldSamples.push(cold.perTouchMs)
			hotSwitchSamples.push(hotSwitch.perTouchMs)
		}

		const coldMedianPerTouchMs = median(coldSamples)
		const hotSwitchMedianPerTouchMs = median(hotSwitchSamples)

		expect(coldMedianPerTouchMs).toBeLessThan(2)
		expect(hotSwitchMedianPerTouchMs).toBeLessThan(1)
		expect(hotSwitchMedianPerTouchMs).toBeLessThan(coldMedianPerTouchMs)
	})
})
