/**
 * TUI launcher - initializes OpenTUI and renders the app
 */
import { createCliRenderer } from "@opentui/core"
import { createRoot } from "@opentui/react"
import { App } from "./App.js"
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
	} catch (error) {
		diagnostics({
			level: "warn",
			phase: params.phase,
			message: `teardown phase threw: ${error instanceof Error ? error.message : String(error)}`,
		})
	}
}

export const waitForShutdownCompletion = async (params: {
	readonly completion: Promise<void>
	readonly timeoutMs?: number
	readonly diagnostics?: TeardownDiagnosticSink
}): Promise<"completed" | "timed_out"> => {
	const diagnostics = params.diagnostics ?? defaultTeardownDiagnosticSink
	const timeoutMs = params.timeoutMs ?? SHUTDOWN_COMPLETE_TIMEOUT_MS
	const timeoutResult = Symbol("shutdown-timeout")
	const result = await Promise.race([
		params.completion.then(() => "completed" as const),
		new Promise<typeof timeoutResult>((resolve) => {
			setTimeout(() => resolve(timeoutResult), timeoutMs)
		}),
	])
	if (result === timeoutResult) {
		diagnostics({
			level: "warn",
			phase: "shutdown-wait",
			message: `shutdown did not complete within ${timeoutMs}ms`,
			elapsedMs: timeoutMs,
		})
		return "timed_out"
	}
	return "completed"
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
async function registerReturnBinding(): Promise<void> {
	// Only register if we're inside tmux
	if (!process.env.TMUX) return

	try {
		// Preserve native double-prefix behavior for users who rely on send-prefix.
		const prefixProc = Bun.spawn(["tmux", "bind-key", "C-a", "send-prefix"], {
			stdout: "ignore",
			stderr: "ignore",
		})
		await prefixProc.exited

		// Bind a dedicated key for "return to board" navigation.
		const proc = Bun.spawn(
			["tmux", "bind-key", AZ_RETURN_KEY, "switch-client", "-t", AZ_SESSION_NAME],
			{
				stdout: "ignore",
				stderr: "ignore",
			},
		)
		await proc.exited
	} catch {
		// Silently ignore - binding is nice-to-have, not critical
	}
}

/**
 * Launch the TUI application
 *
 * Initializes the OpenTUI renderer and starts the application.
 * Uses React's createRoot pattern for rendering.
 */
export async function launchTUI(): Promise<void> {
	const launchStartedAtMs = Date.now()
	await truncateAzLogOnStartup()
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
	registerReturnBinding()

	const renderer = await createCliRenderer({
		useMouse: true,
	})
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
			clearShutdownHandler()
			process.off("SIGINT", handleSigint)
			finalizeShutdown()
		}
	}

	registerShutdownHandler(shutdown)
	renderer.once("destroy", () => {
		resetTerminalModesOnExit()
		clearShutdownHandler()
		process.off("SIGINT", handleSigint)
		finalizeShutdown()
	})

	root.render(<App launchStartedAtMs={launchStartedAtMs} />)
	void (await waitForShutdownCompletion({
		completion: shutdownComplete,
		timeoutMs: SHUTDOWN_COMPLETE_TIMEOUT_MS,
		diagnostics,
	}))
}
