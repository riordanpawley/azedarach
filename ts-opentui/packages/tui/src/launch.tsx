/**
 * TUI launcher - initializes OpenTUI and renders the app
 */
import { GlobalDaemonBootstrap } from "@azedarach/daemon-control"
import { createCliRenderer } from "@opentui/core"
import { createRoot } from "@opentui/react"
import { Cause, Duration, Effect, Layer } from "effect"
import { App } from "./App.js"
import { configureTuiDaemonRpcClient } from "./atoms/runtime.js"
import { AZ_SESSION_NAME } from "./lib/tmux-wrap.js"
import { truncateAzLogOnStartup } from "./logMaintenance.js"
import { clearShutdownHandler, registerShutdownHandler, requestShutdown } from "./runtimeControl.js"
import { killActivePopup } from "./utils/popupCleanup.js"

const AZ_RETURN_KEY = process.env.AZ_RETURN_KEY?.trim() || "g"
const RESET_TERMINAL_MODES_SEQUENCE =
	"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l\x1b[?25h"
const SHUTDOWN_PHASE_WARN_MS = 250
const SHUTDOWN_COMPLETE_TIMEOUT_MS = 2000

type TeardownDiagnostic = {
	readonly level: "info" | "warn"
	readonly phase: string
	readonly message: string
	readonly elapsedMs?: number
}

type TeardownDiagnosticSink = (event: TeardownDiagnostic) => void

const defaultTeardownDiagnosticSink: TeardownDiagnosticSink = (event) => {
	if (event.level === "warn") {
		console.error(
			`[tui-shutdown] warn phase=${event.phase} message=${event.message}${event.elapsedMs === undefined ? "" : ` elapsed_ms=${event.elapsedMs}`}`,
		)
		return
	}
	console.error(
		`[tui-shutdown] info phase=${event.phase} message=${event.message}${event.elapsedMs === undefined ? "" : ` elapsed_ms=${event.elapsedMs}`}`,
	)
}

export const TUI_QUIT_PRESERVES_DAEMON = true

export const runBoundedTeardownPhase = (params: {
	readonly phase: string
	readonly effect: () => void
	readonly warnAfterMs?: number
	readonly diagnostics?: TeardownDiagnosticSink
}): void => {
	const warnAfterMs = params.warnAfterMs ?? SHUTDOWN_PHASE_WARN_MS
	const diagnostics = params.diagnostics ?? defaultTeardownDiagnosticSink
	const startedAt = Date.now()
	try {
		params.effect()
		const elapsedMs = Date.now() - startedAt
		if (elapsedMs > warnAfterMs) {
			diagnostics({
				level: "warn",
				phase: params.phase,
				message: "teardown phase exceeded warning budget",
				elapsedMs,
			})
			return
		}
		diagnostics({
			level: "info",
			phase: params.phase,
			message: "teardown phase completed",
			elapsedMs,
		})
	} catch {
		diagnostics({
			level: "warn",
			phase: params.phase,
			message: "teardown phase threw",
		})
	}
}

export const waitForShutdownCompletion = (params: {
	readonly completion: Effect.Effect<void>
	readonly timeoutMs?: number
	readonly diagnostics?: TeardownDiagnosticSink
}): Effect.Effect<"completed" | "timed_out"> => {
	const diagnostics = params.diagnostics ?? defaultTeardownDiagnosticSink
	const timeoutMs = params.timeoutMs ?? SHUTDOWN_COMPLETE_TIMEOUT_MS
	return Effect.raceFirst(
		params.completion.pipe(Effect.as("completed" as const)),
		Effect.sleep(Duration.millis(timeoutMs)).pipe(
			Effect.map(() => {
				diagnostics({
					level: "warn",
					phase: "shutdown-wait",
					message: `shutdown did not complete within ${timeoutMs}ms`,
					elapsedMs: timeoutMs,
				})
				return "timed_out" as const
			}),
		),
	)
}

function resetTerminalModesOnExit(): void {
	if (!process.stdout.isTTY) return
	try {
		process.stdout.write(RESET_TERMINAL_MODES_SEQUENCE)
	} catch {
		// Best-effort cleanup only.
	}
}

/**
 * Register a global tmux keybinding to return to the az session.
 * Binds Ctrl-a g (by default) to switch back to the main az TUI session.
 * This works from any Claude session spawned by az.
 */
const registerReturnBinding = Effect.gen(function* () {
	if (!process.env.TMUX) return

	yield* Effect.promise(() =>
		Bun.spawn(["tmux", "bind-key", "C-a", "send-prefix"], {
			stdout: "ignore",
			stderr: "ignore",
		}).exited.then(() => undefined),
	)
	yield* Effect.promise(() =>
		Bun.spawn(["tmux", "bind-key", AZ_RETURN_KEY, "switch-client", "-t", AZ_SESSION_NAME], {
			stdout: "ignore",
			stderr: "ignore",
		}).exited.then(() => undefined),
	)
}).pipe(Effect.catchAll(() => Effect.void))

export const launchTUI = Effect.gen(function* () {
	const daemonBootstrap = yield* GlobalDaemonBootstrap
	const bootstrap = yield* daemonBootstrap.bootstrapDaemonRpcClient({
		autoStart: true,
	})
	configureTuiDaemonRpcClient(bootstrap.client, { socketUrl: bootstrap.socketUrl })

	const launchStartedAtMs = Date.now()
	yield* Effect.promise(() => truncateAzLogOnStartup())
	const diagnostics: TeardownDiagnosticSink = defaultTeardownDiagnosticSink

	const handleSigint = () => {
		killActivePopup()
		requestShutdown()
	}

	// Register SIGINT handler to clean up any active tmux popup.
	// Avoid forcing process.exit here: hard-exiting from a signal handler during
	// renderer lifecycle can trigger OpenTUI/React teardown failures.
	process.on("SIGINT", handleSigint)

	// Register return-to-board tmux keybinding (fire-and-forget)
	yield* Effect.forkScoped(registerReturnBinding)

	const renderer = yield* Effect.promise(() =>
		createCliRenderer({
			useMouse: true,
		}),
	)
	const root = createRoot(renderer)
	let shuttingDown = false
	let resolveShutdown: (() => void) | null = null
	let shutdownWatchdog: ReturnType<typeof setTimeout> | null = null

	const shutdownComplete = new Promise<void>((resolve) => {
		resolveShutdown = resolve
	})
	const finalizeShutdown = () => {
		if (shutdownWatchdog !== null) {
			clearTimeout(shutdownWatchdog)
			shutdownWatchdog = null
		}
		const resolve = resolveShutdown
		resolveShutdown = null
		resolve?.()
	}

	const clearResources = () => {
		clearShutdownHandler()
		process.off("SIGINT", handleSigint)
	}

	const shutdown = () => {
		if (shuttingDown) return
		shuttingDown = true
		diagnostics({
			level: "info",
			phase: "shutdown",
			message: `shutdown started (daemon_preserved=${String(TUI_QUIT_PRESERVES_DAEMON)})`,
		})
		shutdownWatchdog = setTimeout(() => {
			diagnostics({
				level: "warn",
				phase: "shutdown-watchdog",
				message: `forcing shutdown completion after ${SHUTDOWN_COMPLETE_TIMEOUT_MS}ms`,
				elapsedMs: SHUTDOWN_COMPLETE_TIMEOUT_MS,
			})
			finalizeShutdown()
		}, SHUTDOWN_COMPLETE_TIMEOUT_MS)
		try {
			runBoundedTeardownPhase({
				phase: "terminal-reset",
				effect: resetTerminalModesOnExit,
				diagnostics,
			})
			runBoundedTeardownPhase({
				phase: "root-unmount",
				effect: () => {
					root.unmount()
				},
				diagnostics,
			})
			runBoundedTeardownPhase({
				phase: "renderer-destroy",
				effect: () => {
					renderer.destroy()
				},
				diagnostics,
			})
		} finally {
			clearResources()
			finalizeShutdown()
		}
	}

	registerShutdownHandler(shutdown)
	renderer.once("destroy", () => {
		resetTerminalModesOnExit()
		clearResources()
		finalizeShutdown()
	})

	yield* Effect.addFinalizer(() =>
		Effect.sync(() => {
			shutdown()
		}),
	)

	root.render(<App launchStartedAtMs={launchStartedAtMs} />)
	yield* waitForShutdownCompletion({
		completion: Effect.promise(() => shutdownComplete),
		timeoutMs: SHUTDOWN_COMPLETE_TIMEOUT_MS,
		diagnostics,
	}).pipe(
		Effect.catchAllCause((cause) => Effect.logWarning(Cause.pretty(cause)).pipe(Effect.asVoid)),
	)
})

export const TuiRuntimeLayer = Layer.scopedDiscard(launchTUI)
