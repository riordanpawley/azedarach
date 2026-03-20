import { PlatformLogger } from "@effect/platform"
import { BunContext, BunFileSystem } from "@effect/platform-bun"
import { Effect, Layer, Logger } from "effect"
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

const runGlobalDaemonProgram = Effect.gen(function* () {
	const handle = yield* startGlobalDaemonServer()
	yield* Effect.logInfo(`Global daemon listening on ${handle.lease.paths.socketPath}`)
	const signal = yield* awaitShutdownSignal()
	yield* Effect.logInfo(`Received ${signal}; shutting down global daemon`)
	yield* stopGlobalDaemonServer(handle, `signal:${signal}`)
})

const daemonFileLogger = Logger.logfmtLogger.pipe(
	PlatformLogger.toFile("az-daemon.log", { flag: "a" }),
)
const daemonLoggerLayer = Logger.replaceScoped(Logger.defaultLogger, daemonFileLogger)
const daemonLoggingLayer = Layer.mergeAll(BunFileSystem.layer, daemonLoggerLayer)

export const runGlobalDaemonMain = runGlobalDaemonProgram.pipe(
	Effect.provide(daemonLoggingLayer),
	Effect.catchAllCause((cause) =>
		Effect.logError(`Global daemon exited with failure: ${String(cause)}`),
	),
)

const GlobalDaemonMainLayer = Layer.mergeAll(BunContext.layer, GlobalDaemonDiscovery.Default)

if (import.meta.main) {
	Effect.runPromise(runGlobalDaemonMain.pipe(Effect.provide(GlobalDaemonMainLayer))).catch(() => {
		process.exitCode = 1
	})
}
