import { BunContext } from "@effect/platform-bun"
import { Console, Effect, Layer } from "effect"
import { GlobalDaemonDiscovery } from "./GlobalDaemonDiscovery.js"
import { startGlobalDaemonServer, stopGlobalDaemonServer } from "./GlobalDaemonServer.js"

const awaitShutdownSignal = (): Effect.Effect<"SIGINT" | "SIGTERM", never> =>
	Effect.async<"SIGINT" | "SIGTERM">((resume) => {
		let settled = false
		const cleanup = () => {
			process.off("SIGINT", onSigint)
			process.off("SIGTERM", onSigterm)
		}
		const settle = (signal: "SIGINT" | "SIGTERM") => {
			if (settled) return
			settled = true
			cleanup()
			resume(Effect.succeed(signal))
		}
		const onSigint = () => settle("SIGINT")
		const onSigterm = () => settle("SIGTERM")
		process.on("SIGINT", onSigint)
		process.on("SIGTERM", onSigterm)
		return Effect.sync(cleanup)
	})

export const runGlobalDaemonMain = Effect.gen(function* () {
	const handle = yield* startGlobalDaemonServer()
	yield* Console.log(`Global daemon listening on ${handle.lease.paths.socketPath}`)
	const signal = yield* awaitShutdownSignal()
	yield* Console.log(`Received ${signal}; shutting down global daemon`)
	yield* stopGlobalDaemonServer(handle, `signal:${signal}`)
}).pipe(
	Effect.catchAllCause((cause) =>
		Console.error(`Global daemon exited with failure: ${String(cause)}`),
	),
)

const GlobalDaemonMainLayer = Layer.mergeAll(
	BunContext.layer,
	GlobalDaemonDiscovery.Default.pipe(Layer.provide(BunContext.layer)),
)

if (import.meta.main) {
	Effect.runPromise(runGlobalDaemonMain.pipe(Effect.provide(GlobalDaemonMainLayer))).catch(() => {
		process.exitCode = 1
	})
}
