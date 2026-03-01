/**
 * PTYMonitor - Effect service for monitoring Claude session PTY output
 *
 * Continuously monitors tmux pane output for active sessions and:
 * - Detects session state (busy, error, done) via pattern matching
 * - Extracts session metrics (tokens, agent phase, recent output)
 * - Reports state changes to ClaudeSessionManager
 *
 * Works in tandem with TmuxSessionMonitor:
 * - PTY provides: busy detection, error detection, done detection, metrics
 * - TmuxSessionMonitor provides: waiting, idle (authoritative)
 * - TmuxSessionMonitor signals always take priority over PTY (2s priority window)
 *
 * State aggregation flow:
 * 1. PTYMonitor polls tmux panes every 500ms
 * 2. Output is fed to StateDetector for pattern matching
 * 3. Detected state is compared against hook priority window
 * 4. If hooks haven't fired recently, PTY state updates ClaudeSessionManager
 */

import { Effect, HashMap, Ref, Schedule, SubscriptionRef } from "effect"
import { stripAnsi } from "../lib/ansi.js"
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import type { AgentPhase, SessionState } from "../ui/types.js"
import { ClaudeSessionManager } from "./ClaudeSessionManager.js"
import { type DetectionResult, StateDetector } from "./StateDetector.js"
import { TmuxService } from "./TmuxService.js"

// ============================================================================
// Types
// ============================================================================

/**
 * Extracted metrics from PTY output
 */
export interface ExtractedMetrics {
	/** Estimated token count parsed from Claude status line */
	readonly estimatedTokens?: number
	/** Recent meaningful output line (truncated) */
	readonly recentOutput?: string
	/** Detected agent phase (planning/action/verification) */
	readonly agentPhase?: AgentPhase
	/** Checklist progress: [completed, total] task counts from terminal output */
	readonly checklistProgress?: readonly [number, number]
}

/**
 * Per-session monitoring state
 */
interface SessionMonitor {
	readonly beadId: string
	readonly tmuxSessionName: string
	readonly detector: (chunk: string) => DetectionResult
	readonly lastOutput: string
	readonly lastForegroundCmd: string | null
	readonly lastStateFromHook: SessionState | null
	readonly lastHookTime: number
}

// ============================================================================
// Constants
// ============================================================================

/** Polling interval for PTY capture (matches TmuxSessionMonitor) */
const POLL_INTERVAL_MS = 1000

/** Number of lines to capture from tmux pane */
const CAPTURE_LINES = 50

/** Time window during which hook signals take priority */
const HOOK_PRIORITY_WINDOW_MS = 2000

// ============================================================================
// Metrics Extraction Helpers
// ============================================================================

/**
 * Extract token count from Claude's status line
 *
 * Patterns:
 * - "↓ 41 tokens" → 41
 * - "↑ 1.5k tokens" → 1500
 * - "3.2K tokens" → 3200
 */
const extractTokenCount = (output: string): number | undefined => {
	// Look for token count patterns in status line
	const patterns = [
		/[↓↑]\s*(\d+(?:\.\d+)?)\s*([kK])?\s*tokens?/i,
		/(\d+(?:\.\d+)?)\s*([kK])\s*tokens?/i,
	]

	for (const pattern of patterns) {
		const match = output.match(pattern)
		if (match) {
			let value = parseFloat(match[1])
			if (match[2]?.toLowerCase() === "k") {
				value *= 1000
			}
			return Math.floor(value)
		}
	}
	return undefined
}

/**
 * Extract recent output snippet (last meaningful line)
 *
 * Skips status bars and control characters to find actual content.
 */
const extractRecentOutput = (output: string): string | undefined => {
	const lines = output
		.trim()
		.split("\n")
		.filter((line) => line.trim().length > 0)

	// Look for the last non-status line
	for (let i = lines.length - 1; i >= 0; i--) {
		const line = lines[i].trim()
		// Skip status bar patterns
		if (!line.match(/^[·✶⏺]\s|esc to interrupt|-- INSERT|-- NORMAL/)) {
			return line.slice(0, 100) // Truncate to reasonable length
		}
	}
	return undefined
}

/**
 * Extract checklist task progress from terminal output.
 *
 * Inspired by Grove's checklist detection. Parses Claude Code output for:
 * - Authoritative summary: "11 tasks (9 done, 1 in progress, 1 open)"
 * - Individual checkboxes: [✓]/[✔]/[✅] = done, [•]/[○]/[ ] = pending
 * - Collapsed counts: "... +3 completed"
 *
 * @returns [completed, total] tuple or undefined if no progress found
 */
const extractChecklistProgress = (output: string): readonly [number, number] | undefined => {
	const clean = stripAnsi(output)

	// First: try the authoritative task summary line
	// e.g., "11 tasks (9 done, 1 in progress, 1 open)"
	const summaryMatch = clean.match(/(\d+)\s+tasks?\s*\((\d+)\s+done/)
	if (summaryMatch) {
		const total = parseInt(summaryMatch[1], 10)
		const done = parseInt(summaryMatch[2], 10)
		if (!Number.isNaN(total) && !Number.isNaN(done) && total > 0) {
			return [done, total] as const
		}
	}

	let completed = 0
	let total = 0

	for (const line of clean.split("\n")) {
		const trimmed = line.trim()

		// Collapsed tasks: "... +3 completed"
		const collapsedMatch = trimmed.match(/\+(\d+)\s+completed/)
		if (collapsedMatch) {
			const count = parseInt(collapsedMatch[1], 10)
			if (!Number.isNaN(count)) {
				completed += count
				total += count
				continue
			}
		}

		// Strip tree decorators (│ ├ └ ─) before checking checkboxes
		const checkPart = trimmed.replace(/^[│├└─\s]+/, "")

		if (
			checkPart.startsWith("[✓]") ||
			checkPart.startsWith("[✔]") ||
			checkPart.startsWith("[✅]")
		) {
			completed++
			total++
		} else if (
			checkPart.startsWith("[•]") ||
			checkPart.startsWith("[○]") ||
			checkPart.startsWith("[ ]")
		) {
			total++
		}
		// Standalone checkmarks at line start (without brackets)
		else if (
			checkPart.startsWith("✓") ||
			checkPart.startsWith("✔") ||
			checkPart.startsWith("☑") ||
			checkPart.startsWith("✅")
		) {
			completed++
			total++
		}
		// Pending shapes
		else if (
			checkPart.startsWith("○") ||
			checkPart.startsWith("□") ||
			checkPart.startsWith("☐")
		) {
			total++
		}
	}

	return total > 0 ? ([completed, total] as const) : undefined
}

/**
 * Classify a foreground process command name.
 *
 * Inspired by Grove's ForegroundProcess enum for ground-truth state detection.
 * When the AI agent is no longer the foreground process and a shell has taken
 * over, that is a strong indicator the agent has finished its current task.
 *
 * @param cmd - process name from `#{pane_current_command}`, or null if unavailable
 * @returns
 *   - "agent"     — AI agent is active (node/claude/npx/opencode/codex/gemini)
 *   - "shell"     — shell is foreground; agent has likely finished (bash/zsh/sh/fish/dash)
 *   - "subprocess"— agent launched a subprocess that is still running
 *   - "unknown"   — cmd is null/empty (tmux unavailable or pane just created)
 */
const classifyForegroundProcess = (
	cmd: string | null,
): "agent" | "shell" | "subprocess" | "unknown" => {
	if (!cmd) return "unknown"
	const lower = cmd.toLowerCase()
	// AI agent process names (node = Claude Code/Opencode, claude, npx, opencode, codex, gemini)
	if (
		lower === "node" ||
		lower === "claude" ||
		lower === "npx" ||
		lower === "opencode" ||
		lower === "codex" ||
		lower === "gemini"
	) {
		return "agent"
	}
	// Shell process names
	if (
		lower === "bash" ||
		lower === "zsh" ||
		lower === "sh" ||
		lower === "fish" ||
		lower === "dash"
	) {
		return "shell"
	}
	return "subprocess"
}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * PTYMonitor service
 *
 * Polls tmux panes for active sessions and detects state/metrics.
 * Uses a scoped effect so the polling fiber is automatically interrupted
 * when the service is disposed.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const monitor = yield* PTYMonitor
 *
 *   // Register a session for monitoring
 *   yield* monitor.registerSession("az-123", "az-123")
 *
 *   // Get current metrics
 *   const metrics = yield* monitor.getMetrics("az-123")
 *
 *   // Unregister when done
 *   yield* monitor.unregisterSession("az-123")
 * }).pipe(Effect.provide(PTYMonitor.Default))
 * ```
 */
export class PTYMonitor extends Effect.Service<PTYMonitor>()("PTYMonitor", {
	dependencies: [
		TmuxService.Default,
		ClaudeSessionManager.Default,
		StateDetector.Default,
		DiagnosticsService.Default,
	],
	scoped: Effect.gen(function* () {
		const tmux = yield* TmuxService
		const sessionManager = yield* ClaudeSessionManager
		const stateDetector = yield* StateDetector
		const diagnostics = yield* DiagnosticsService

		// Register with diagnostics - will mark unhealthy when scope closes
		yield* diagnostics.trackService("PTYMonitor", "Polling tmux panes every 1s")

		// Per-session monitoring state
		const monitors = yield* Ref.make<HashMap.HashMap<string, SessionMonitor>>(HashMap.empty())

		// Metrics output (published per-session via SubscriptionRef)
		const metricsRef = yield* SubscriptionRef.make<HashMap.HashMap<string, ExtractedMetrics>>(
			HashMap.empty(),
		)

		// ========================================================================
		// Session Registration
		// ========================================================================

		/**
		 * Register a session for PTY monitoring
		 *
		 * Creates a stateful detector for the session and starts monitoring.
		 */
		const registerSession = (beadId: string, tmuxSessionName: string) =>
			Effect.gen(function* () {
				const detector = yield* stateDetector.createCombinedDetector()
				const monitor: SessionMonitor = {
					beadId,
					tmuxSessionName,
					detector,
					lastOutput: "",
					lastForegroundCmd: null,
					lastStateFromHook: null,
					lastHookTime: 0,
				}
				yield* Ref.update(monitors, (m) => HashMap.set(m, beadId, monitor))
				yield* Effect.log(`PTYMonitor: Registered session ${beadId}`)
			})

		/**
		 * Unregister a session from PTY monitoring
		 *
		 * Cleans up state and metrics for the session.
		 */
		const unregisterSession = (beadId: string) =>
			Effect.gen(function* () {
				yield* Ref.update(monitors, (m) => HashMap.remove(m, beadId))
				yield* SubscriptionRef.update(metricsRef, (m) => HashMap.remove(m, beadId))
				yield* Effect.log(`PTYMonitor: Unregistered session ${beadId}`)
			})

		/**
		 * Record a hook signal for priority handling
		 *
		 * Called by TmuxSessionMonitor integration to notify PTYMonitor of authoritative
		 * state changes. The priority window ensures hooks always take precedence.
		 */
		const recordHookSignal = (beadId: string, state: SessionState) =>
			Ref.update(monitors, (m) => {
				const existing = HashMap.get(m, beadId)
				if (existing._tag === "Some") {
					return HashMap.set(m, beadId, {
						...existing.value,
						lastStateFromHook: state,
						lastHookTime: Date.now(),
					})
				}
				return m
			})

		// ========================================================================
		// Polling Logic
		// ========================================================================

		/**
		 * Poll a single session for state and metrics
		 *
		 * Uses foreground process detection (Grove-inspired) as a supplemental
		 * signal: if the AI agent is no longer the foreground process and a shell
		 * has taken over, that is a strong indicator the agent has finished its
		 * current task.
		 */
		const pollSession = (beadId: string, monitor: SessionMonitor) =>
			Effect.gen(function* () {
				// Capture recent output from tmux pane
				const output = yield* tmux.capturePane(monitor.tmuxSessionName, CAPTURE_LINES)

				// In parallel, check what process is currently in the foreground
				const foregroundCmd = yield* tmux.getPaneCurrentCommand(monitor.tmuxSessionName)
				const foregroundKind = classifyForegroundProcess(foregroundCmd)

				// Skip output-based detection if output hasn't changed AND foreground process is the same
				if (output === monitor.lastOutput && foregroundCmd === monitor.lastForegroundCmd) {
					return
				}

				// Run detection on new output
				const { state: detectedState, phase: detectedPhase } = monitor.detector(output)

				// Extract metrics (includes checklist progress)
				const metrics: ExtractedMetrics = {
					estimatedTokens: extractTokenCount(output),
					recentOutput: extractRecentOutput(output),
					agentPhase: detectedPhase ?? undefined,
					checklistProgress: extractChecklistProgress(output),
				}

				// Update metrics SubscriptionRef
				yield* SubscriptionRef.update(metricsRef, (m) => HashMap.set(m, beadId, metrics))

				// State aggregation: respect hook priority window
				const hookRecency = Date.now() - monitor.lastHookTime
				const hookHasPriority = hookRecency < HOOK_PRIORITY_WINDOW_MS

				if (!hookHasPriority) {
					// Get current state from ClaudeSessionManager
					const currentState = yield* sessionManager
						.getState(beadId)
						.pipe(Effect.catchAll(() => Effect.succeed("idle" as SessionState)))

					// Foreground process ground-truth (Grove-inspired):
					// If the pane now has a shell as the foreground process and the session
					// was busy/initializing, Claude has finished its current task. With
					// remain-on-exit=on the pane stays alive (a shell takes over), which is
					// exactly this case. Promote to "done" so the user knows to review output.
					if (
						foregroundKind === "shell" &&
						(currentState === "busy" || currentState === "initializing")
					) {
						yield* sessionManager.updateState(beadId, "done")
						yield* Effect.log(
							`PTYMonitor: ${beadId} state ${currentState} → done (shell is foreground process)`,
						)
					} else if (detectedState) {
						// Pattern-based detection (original logic)
						const shouldUpdate =
							(currentState === "idle" && detectedState === "busy") ||
							(currentState === "initializing" &&
								(detectedState === "error" ||
									detectedState === "done" ||
									detectedState === "busy")) ||
							(currentState === "busy" &&
								(detectedState === "error" || detectedState === "done"))

						if (shouldUpdate) {
							yield* sessionManager.updateState(beadId, detectedState)
							yield* Effect.log(
								`PTYMonitor: ${beadId} state ${currentState} → ${detectedState} (PTY detected)`,
							)
						}
					}
				}

				// Update monitor state with new output and foreground command
				yield* Ref.update(monitors, (m) =>
					HashMap.set(m, beadId, {
						...monitor,
						lastOutput: output,
						lastForegroundCmd: foregroundCmd,
					}),
				)
			}).pipe(
				Effect.catchAll((e) =>
					Effect.logWarning(`PTYMonitor: Error polling ${beadId}: ${e}`).pipe(Effect.asVoid),
				),
			)

		/**
		 * Poll all registered sessions
		 */
		const pollAll = () =>
			diagnostics.measure(
				{
					source: "PTYMonitor",
					name: "pollAll",
					thresholdMs: 200,
				},
				Effect.gen(function* () {
					const allMonitors = yield* Ref.get(monitors)
					yield* Effect.all(
						Array.from(HashMap.entries(allMonitors)).map(([beadId, monitor]) =>
							pollSession(beadId, monitor),
						),
						{ concurrency: 4 },
					)
				}).pipe(Effect.withSpan("pty.pollAll")),
			)

		// ========================================================================
		// Start Polling Loop
		// ========================================================================

		// Start the polling fiber - scoped by service lifetime (auto-interrupted on dispose)
		yield* Effect.scheduleForked(Schedule.spaced(`${POLL_INTERVAL_MS} millis`))(
			pollAll().pipe(
				Effect.catchAll((e) =>
					Effect.logWarning(`PTYMonitor: Poll cycle error: ${e}`).pipe(Effect.asVoid),
				),
			),
		)

		yield* Effect.log("PTYMonitor: Started polling for PTY output")

		// ========================================================================
		// Service Interface
		// ========================================================================

		return {
			/** Register a session for PTY monitoring */
			registerSession,

			/** Unregister a session from monitoring */
			unregisterSession,

			/** Record a hook signal for priority handling */
			recordHookSignal,

			/** Metrics SubscriptionRef for reactive UI updates */
			metrics: metricsRef,

			/** Get metrics for a specific session */
			getMetrics: (beadId: string) =>
				Effect.gen(function* () {
					const all = yield* SubscriptionRef.get(metricsRef)
					const found = HashMap.get(all, beadId)
					return found._tag === "Some" ? found.value : undefined
				}),
		}
	}),
}) {}
