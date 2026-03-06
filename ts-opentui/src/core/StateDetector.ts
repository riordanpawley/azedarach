/**
 * StateDetector - Effect service for detecting Claude session state from PTY output
 *
 * Analyzes output chunks from Claude Code sessions to determine current state:
 * - waiting: Claude is waiting for user input
 * - error: An error occurred
 * - done: Task completed successfully
 * - busy: Claude is actively working
 * - idle: No output (initial state)
 *
 * Pattern matching uses priority ordering - first match wins.
 */

import { Data, Effect } from "effect"
import { stripAnsi } from "../lib/ansi.js"

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * Session state types
 */
export type SessionState = "idle" | "initializing" | "busy" | "waiting" | "done" | "error"

/**
 * Agent workflow phase types
 *
 * Tracks where Claude is in its typical workflow:
 * - planning: Analyzing, reading code, formulating approach
 * - action: Writing code, making edits, running commands
 * - verification: Running tests, type checks, validating results
 * - planMode: Claude Code's formal plan mode (read-only permission state)
 */
export type AgentPhase = "idle" | "planning" | "action" | "verification" | "planMode"

/**
 * State detection patterns with priority levels
 */
export interface StatePattern {
	readonly state: SessionState
	readonly patterns: readonly RegExp[]
	readonly priority: number
}

/**
 * Phase detection patterns with priority levels
 */
export interface PhasePattern {
	readonly phase: AgentPhase
	readonly patterns: readonly RegExp[]
	readonly priority: number
}

// ============================================================================
// Error Types
// ============================================================================

/**
 * Error when state detection fails unexpectedly
 */
export class StateDetectionError extends Data.TaggedError("StateDetectionError")<{
	readonly message: string
	readonly chunk?: string
}> {}

// ============================================================================
// Configuration
// ============================================================================

// ============================================================================
// Pre-compiled Pattern Constants
// ============================================================================

/**
 * Braille spinner characters used by Claude Code, OpenCode, and many other
 * terminal-based AI tools during active processing.
 *
 * Frames: ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏ (10-frame braille animation)
 * Plus: ◐◓◑◒ (quarter-circle spinner)
 * And: ⣾⣽⣻⢿⡿⣟⣯⣷ (8-frame braille block animation)
 *
 * These characters are extremely reliable "busy" indicators — they only appear
 * when an agent is actively running and animating its loading state.
 */
const BRAILLE_SPINNERS = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏◐◓◑◒⣾⣽⣻⢿⡿⣟⣯⣷"

/**
 * Dingbat characters used by Claude Code as working-indicator prefixes.
 *
 * Claude Code shows messages like "✻ Sketching…", "✶ Thinking...", "❃ Analyzing…"
 * when actively processing. The dingbat is a status bullet; the verb+ing+ellipsis
 * combination uniquely identifies an active work status message.
 *
 * Sources: Claude Code source, empirical observation, Grove's WORKING_INDICATORS pattern.
 */
const WORKING_INDICATOR_DINGBATS = "✢✣✤✥✦✧✨✩✪✫✬✭✮✯✰✱✲✳✴✵✶✷✸✹✺✻✼✽✾✿❀❁❂❃❄❅❆❇❈❉❊❋✡✥★☆"

/**
 * Regex that matches braille spinner characters in output.
 * Used to detect actively running agents.
 */
const SPINNER_RE = new RegExp(`[${BRAILLE_SPINNERS}]`, "u")

/**
 * Regex that matches Claude Code working-indicator status messages.
 * Pattern: dingbat + optional whitespace + verb-ing word + ellipsis characters
 * e.g. "✻ Sketching…", "✶ Thinking...", "❃ Analyzing…"
 */
const WORKING_INDICATOR_RE = new RegExp(`[${WORKING_INDICATOR_DINGBATS}]\\s*\\w+ing[.…]+`, "u")

/**
 * Pattern definitions ordered by priority (highest to lowest)
 *
 * Patterns are checked in order, first match wins.
 * This ensures higher-priority states (like "waiting") take precedence
 * over lower-priority states (like "busy").
 */
const STATE_PATTERNS: readonly StatePattern[] = [
	{
		state: "waiting",
		priority: 100,
		patterns: [
			/\[y\/n\]/i,
			/Do you want to/i,
			/Press Enter/i,
			/waiting for input/i,
			/Continue\?/i,
			/Proceed\?/i,
			// AskUserQuestion tool - numbered choices and "Other" option
			/^\s*\d+\.\s+Other\b/im, // "1. Other" or "  2. Other" etc.
			/\bOther\s*\(describe\)/i, // "Other (describe)"
			/select.*option/i, // "Select an option"
			/choose.*option/i, // "Choose an option"
			/enter.*number/i, // "Enter a number"
			/type.*number.*select/i, // "Type a number to select"
			// Grove-inspired patterns: bash confirmation and permission prompts
			/Run this command\?/i,
			/Execute\?/i,
			/Ready to implement\?/i,
			/Proceed with/i,
			/Allow\s*(this|once|always)?\s*\?/i,
			// Numbered selection indicator (❯ 1. Option)
			/❯\s*\d+\./,
			// Lettered option choices (Option A:, Option B:)
			/Option\s+[A-Z]:/,
			// Clarification and disambiguation requests
			/Could you clarify/i,
			/Which one (?:are you looking for|do you want)/i,
			// Keyboard hint patterns seen in Claude permission dialogs
			/Enter\s+to\s+confirm/i,
			/Esc\s+to\s+cancel/i,
			// OpenCode-specific waiting patterns
			/permission required/i, // OpenCode permission panel
			/type your own answer/i, // OpenCode question panel (plan mode)
			/esc dismiss/i, // OpenCode dismiss hint in question panel
			/asked.*question/i, // OpenCode question panel header
			// Gemini-specific waiting patterns
			/action\s+required/i, // Gemini "Action Required" banner
			/waiting\s+for\s+confirmation/i, // Gemini confirmation dialog
			/answer\s+questions/i, // Gemini question panel title
			/enter\s+to\s+select.*esc\s+to\s+cancel/i, // Gemini keyboard hints in question panel
			/^\s*\d+\.\s+.+\?$/m, // Gemini numbered questions ("   1. Do you want to...?")
		],
	},
	{
		state: "error",
		priority: 90,
		patterns: [
			/Error:/i,
			/Exception:/i,
			/Failed:/i,
			/ENOENT/i,
			/EACCES/i,
			/command not found/i,
			/permission denied/i,
			// Grove-inspired: visual error indicators and Rust-style errors
			/[✗✘❌]\s/u,
			/^error\[E\d+\]/im, // Rust compiler errors: "error[E0382]"
			/panicked at/i, // Rust panics
			/FAILED$/m, // Test runner failures
		],
	},
	{
		// Explicit "busy" signals placed BETWEEN error and done.
		//
		// Grove's key insight: braille spinners and working indicators appear when the
		// AI agent is actively processing. Checking them BEFORE "done" prevents false
		// "done" detection when completion text (e.g., "All tests pass") sits in
		// scrollback while spinners are visible in the most recent lines.
		state: "busy",
		priority: 85,
		patterns: [
			// Braille spinner characters (Claude Code, OpenCode, and other AI tools)
			// These are extremely reliable — only appear while an agent is actively running
			SPINNER_RE,
			// Working indicator: dingbat + verb-ing + ellipsis
			// e.g. "✻ Sketching…", "✶ Thinking...", "❃ Analyzing…"
			WORKING_INDICATOR_RE,
			// Claude Code tool execution: ⏺ + tool name (most reliable busy signal)
			/⏺\s*(?:Read|Write|Edit|Bash|Glob|Grep|Task|WebFetch|WebSearch)/u,
			// OpenCode progress animation: 4+ consecutive dots (e.g. "....  esc interrupt")
			/\.{4,}/,
			// Imperative-verb active-voice lines at the start of a line.
			// Grove's second TOOL_PATTERN: agent status lines like "Reading file...",
			// "Building project...", "Installing dependencies..." are reliable busy indicators.
			// Applied to the most recent captured output (last CAPTURE_LINES lines), so the risk
			// of matching unrelated program output is bounded to the last few screen-fuls.
			/^(?:reading|writing|editing|searching|running|executing|thinking|analyzing|processing|fetching|installing|building|compiling|testing)\b/im,
		],
	},
	{
		state: "done",
		priority: 80,
		patterns: [
			/Task completed/i,
			/Successfully/i,
			/Done\./i,
			/Finished/i,
			/All tasks complete/i,
			// Grove-inspired: visual completion checkmarks at line start
			/^[✓✔☑✅]\s/mu,
			/completed successfully/i,
		],
	},
	// "busy" is the final fallback when output is flowing but no pattern matches
	// "idle" is the default/initial state with no output
]

const getPatternsForState = (state: SessionState): readonly RegExp[] => {
	const match = STATE_PATTERNS.find((entry) => entry.state === state)
	return match ? match.patterns : []
}

const WAITING_PATTERNS = getPatternsForState("waiting")
const ERROR_PATTERNS = getPatternsForState("error")
const BUSY_PATTERNS = getPatternsForState("busy")
const DONE_PATTERNS = getPatternsForState("done")

/**
 * Phase detection patterns ordered by priority (highest to lowest)
 *
 * Verification takes highest priority (tests/checks are explicit signals)
 * Action is next (tool usage, code writes)
 * Planning is lowest (intent statements are common)
 */
const PHASE_PATTERNS: readonly PhasePattern[] = [
	{
		phase: "planMode",
		priority: 110, // Highest priority - plan mode is a distinct operational state
		patterns: [
			// Plan mode entry/active indicators
			/plan mode/i,
			/entering plan mode/i,
			/in plan mode/i,
			/ExitPlanMode/i, // Tool name indicates plan mode is active
			/exit plan mode/i,
			/read-only mode/i,
			// Plan mode prompts
			/would you like (?:me )?to enter plan mode/i,
			/enter plan mode\?/i,
			// Plan mode status indicators (Claude Code terminal output)
			/\[plan\]/i, // Status bar indicator
			/mode:\s*plan/i,
			/permission.?mode.*plan/i,
		],
	},
	{
		phase: "verification",
		priority: 100,
		patterns: [
			// Test execution
			/running tests?/i,
			/bun test/i,
			/npm test/i,
			/pnpm test/i,
			/jest/i,
			/vitest/i,
			/pytest/i,
			/cargo test/i,
			/go test/i,
			// Type checking
			/type[- ]?check/i,
			/tsc/i,
			/typecheck/i,
			// Build verification
			/bun run build/i,
			/npm run build/i,
			/pnpm build/i,
			/cargo build/i,
			// Linting
			/eslint/i,
			/biome/i,
			/prettier/i,
			// Validation signals
			/verifying/i,
			/validating/i,
			/checking/i,
			/tests? pass/i,
			/all tests/i,
		],
	},
	{
		phase: "action",
		priority: 80,
		patterns: [
			// Tool usage indicators (Claude Code output patterns)
			/\bEdit\b.*tool/i,
			/\bWrite\b.*tool/i,
			/\bBash\b.*tool/i,
			/\bRead\b.*tool/i,
			// Claude Code tool execution: ⏺ + tool name (authoritative action signal)
			/⏺\s*(?:Read|Write|Edit|Bash|Glob|Grep|Task|WebFetch|WebSearch)/u,
			// File operations
			/writing to/i,
			/creating file/i,
			/editing file/i,
			/modifying/i,
			// Code output signals
			/```[\w]*\n/i, // Code block start
			/implementing/i,
			/adding/i,
			/updating/i,
			/ Fixing/i,
			/refactoring/i,
			/⏺/u,
			// Working indicators (Claude Code "✻ Sketching…" style status messages)
			WORKING_INDICATOR_RE,
			// Command execution
			/running command/i,
			/executing/i,
			/\$ /i, // Shell prompt
		],
	},
	{
		phase: "planning",
		priority: 60,
		patterns: [
			// Intent statements
			/I'll /i,
			/Let me /i,
			/I will /i,
			/I need to /i,
			/I should /i,
			/First,? I/i,
			/Now I/i,
			/Next,? I/i,
			// Analysis signals
			/looking at/i,
			/analyzing/i,
			/understanding/i,
			/exploring/i,
			/searching/i,
			/reading/i,
			/checking/i,
			/investigating/i,
			// Planning language
			/my plan/i,
			/the approach/i,
			/strategy/i,
			/to understand/i,
			/to figure out/i,
			/✶/u,
			/·/u,
		],
	},

	// "idle" is the default when no phase patterns match
]

// ============================================================================
// Service Definition
// ============================================================================

/**
 * Combined state and phase detection result
 */
export interface DetectionResult {
	readonly state: SessionState | null
	readonly phase: AgentPhase | null
}

/**
 * StateDetector service interface
 *
 * Provides pattern-matching capabilities for detecting Claude session state
 * and workflow phase from PTY output chunks.
 */
export interface StateDetectorService {
	/**
	 * Detect session state from a single output chunk
	 *
	 * Returns null if no state transition is detected (output doesn't match patterns).
	 * Returns SessionState if a pattern matches.
	 *
	 * @example
	 * ```ts
	 * const detector = yield* StateDetector
	 * const state = yield* detector.detectFromChunk("Error: File not found")
	 * // state === "error"
	 * ```
	 */
	readonly detectFromChunk: (chunk: string) => Effect.Effect<SessionState | null, never>

	/**
	 * Detect agent phase from a single output chunk
	 *
	 * Returns null if no phase is detected.
	 * Returns AgentPhase if a pattern matches.
	 *
	 * @example
	 * ```ts
	 * const detector = yield* StateDetector
	 * const phase = yield* detector.detectPhaseFromChunk("I'll start by reading the file")
	 * // phase === "planning"
	 * ```
	 */
	readonly detectPhaseFromChunk: (chunk: string) => Effect.Effect<AgentPhase | null, never>

	/**
	 * Create a stateful detector function
	 *
	 * Returns a pure function that can be called repeatedly with output chunks.
	 * The function maintains internal state for debouncing and pattern matching.
	 *
	 * @example
	 * ```ts
	 * const detector = yield* StateDetector
	 * const detect = yield* detector.createDetector()
	 *
	 * // Use in a stream
	 * const state1 = detect("Building...")  // "busy"
	 * const state2 = detect("Still building...")  // "busy"
	 * const state3 = detect("Done.")  // "done"
	 * ```
	 */
	readonly createDetector: () => Effect.Effect<(chunk: string) => SessionState | null, never>

	/**
	 * Create a combined stateful detector for both state and phase
	 *
	 * Returns a function that detects both session state and agent phase.
	 * Phase detection uses a longer debounce window to avoid rapid transitions.
	 *
	 * @example
	 * ```ts
	 * const detector = yield* StateDetector
	 * const detect = yield* detector.createCombinedDetector()
	 *
	 * const result = detect("I'll read the file first")
	 * // result === { state: "busy", phase: "planning" }
	 * ```
	 */
	readonly createCombinedDetector: () => Effect.Effect<(chunk: string) => DetectionResult, never>
}

// ============================================================================
// Implementation Helpers
// ============================================================================

/**
 * Check if a chunk matches any pattern for a given state
 */
const matchesPattern = (chunk: string, patterns: readonly RegExp[]): boolean => {
	return patterns.some((pattern) => pattern.test(chunk))
}

const getRecentNonEmptyChunk = (chunk: string, maxLines: number): string => {
	const lines = chunk
		.split("\n")
		.map((line) => line.trimEnd())
		.filter((line) => line.trim().length > 0)

	if (lines.length === 0) {
		return ""
	}

	return lines.slice(-maxLines).join("\n")
}

/**
 * Detect state from a chunk using recent-output prioritization.
 *
 * Uses a short recent line window to avoid stale scrollback dominating state:
 * - waiting prompts win immediately
 * - latest-line explicit errors win next
 * - active busy indicators outrank older error text
 * - done is only considered when not actively busy
 *
 * Returns "busy" when output exists but no explicit state matches,
 * or null if the chunk is empty/whitespace only.
 */
const detectState = (chunk: string): SessionState | null => {
	// Ignore empty or whitespace-only chunks
	if (!chunk || chunk.trim().length === 0) {
		return null
	}

	// Strip ANSI codes before pattern matching (Grove-inspired: clean output first)
	const clean = stripAnsi(chunk)
	const recentChunk = getRecentNonEmptyChunk(clean, 12)

	if (recentChunk.length === 0) {
		return null
	}

	const recentLines = recentChunk.split("\n")
	const lastLine = recentLines[recentLines.length - 1] ?? ""

	// Waiting prompts should always win.
	if (matchesPattern(recentChunk, WAITING_PATTERNS)) {
		return "waiting"
	}

	// If the latest line is an explicit error, surface it immediately.
	if (matchesPattern(lastLine, ERROR_PATTERNS)) {
		return "error"
	}

	// Active work indicators are preferred over historical errors in scrollback.
	if (matchesPattern(recentChunk, BUSY_PATTERNS)) {
		return "busy"
	}

	// If recent output still looks like an error and no busy signal is present, mark error.
	if (matchesPattern(recentChunk, ERROR_PATTERNS)) {
		return "error"
	}

	// Done is lower priority than active processing and explicit errors.
	if (matchesPattern(recentChunk, DONE_PATTERNS)) {
		return "done"
	}

	// If we have non-empty output that doesn't match any pattern, it's "busy"
	return "busy"
}

/**
 * Detect agent phase from a chunk by checking phase patterns in priority order
 *
 * Returns the first matching phase, or null if no patterns match.
 * Unlike state detection, no fallback phase is assumed.
 *
 * Strips ANSI escape codes before matching.
 */
const detectPhase = (chunk: string): AgentPhase | null => {
	// Ignore empty or whitespace-only chunks
	if (!chunk || chunk.trim().length === 0) {
		return null
	}

	// Strip ANSI codes before pattern matching
	const clean = stripAnsi(chunk)

	// Check patterns in priority order
	for (const { phase, patterns } of PHASE_PATTERNS) {
		if (matchesPattern(clean, patterns)) {
			return phase
		}
	}

	// No phase detected from this chunk
	return null
}

/**
 * Create a stateful detector with debouncing
 *
 * The detector maintains state across calls:
 * - Rapid "busy" signals are coalesced (debouncing)
 * - High-priority states ("waiting", "error", "done") apply immediately
 * - "waiting" state is detected immediately
 */
const createStatefulDetector = (): ((chunk: string) => SessionState | null) => {
	let lastState: SessionState | null = null
	let lastDetectionTime = Date.now()
	const DEBOUNCE_MS = 100 // Only report state changes after 100ms of consistent state

	return (chunk: string): SessionState | null => {
		const detectedState = detectState(chunk)

		// No output, no change
		if (detectedState === null) {
			return null
		}

		const now = Date.now()
		const timeSinceLastDetection = now - lastDetectionTime

		// High-priority states ("waiting", "error", "done") are reported immediately
		if (detectedState === "waiting" || detectedState === "error" || detectedState === "done") {
			lastState = detectedState
			lastDetectionTime = now
			return detectedState
		}

		// For "busy" state, apply debouncing
		// Only report if we've been consistently busy for DEBOUNCE_MS
		if (detectedState === "busy") {
			if (lastState === "busy" && timeSinceLastDetection < DEBOUNCE_MS) {
				// Still within debounce window, don't report
				return null
			}

			lastState = "busy"
			lastDetectionTime = now
			return "busy"
		}

		// Shouldn't reach here, but handle gracefully
		lastState = detectedState
		lastDetectionTime = now
		return detectedState
	}
}

/**
 * Create a combined stateful detector for both state and phase
 *
 * Phase detection uses a longer debounce window (500ms) than state detection
 * because phases tend to persist longer and we want to avoid rapid flickering.
 */
const createCombinedStatefulDetector = (): ((chunk: string) => DetectionResult) => {
	let lastState: SessionState | null = null
	let lastPhase: AgentPhase | null = null
	let lastStateTime = Date.now()
	let lastPhaseTime = Date.now()
	const STATE_DEBOUNCE_MS = 100
	const PHASE_DEBOUNCE_MS = 500 // Phases persist longer

	return (chunk: string): DetectionResult => {
		const now = Date.now()
		const detectedState = detectState(chunk)
		const detectedPhase = detectPhase(chunk)

		// State detection logic (same as createStatefulDetector)
		let newState: SessionState | null = null
		if (detectedState !== null) {
			// High-priority states report immediately
			if (detectedState === "waiting" || detectedState === "error" || detectedState === "done") {
				lastState = detectedState
				lastStateTime = now
				newState = detectedState
			}
			// Busy state with debouncing
			else if (detectedState === "busy") {
				if (lastState !== "busy" || now - lastStateTime >= STATE_DEBOUNCE_MS) {
					lastState = "busy"
					lastStateTime = now
					newState = "busy"
				}
			} else {
				lastState = detectedState
				lastStateTime = now
				newState = detectedState
			}
		}

		// Phase detection with longer debounce
		let newPhase: AgentPhase | null = null
		if (detectedPhase !== null) {
			// Only update phase if different and past debounce window
			if (detectedPhase !== lastPhase || now - lastPhaseTime >= PHASE_DEBOUNCE_MS) {
				lastPhase = detectedPhase
				lastPhaseTime = now
				newPhase = detectedPhase
			}
		}

		return { state: newState, phase: newPhase }
	}
}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * StateDetector service
 *
 * Provides stateless pattern matching and stateful detector creation.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const detector = yield* StateDetector
 *   const state = yield* detector.detectFromChunk("Error: something went wrong")
 *   return state // "error"
 * }).pipe(Effect.provide(StateDetector.Default))
 * ```
 */
export class StateDetector extends Effect.Service<StateDetector>()("StateDetector", {
	effect: Effect.succeed({
		detectFromChunk: (chunk: string) => Effect.sync(() => detectState(chunk)),

		detectPhaseFromChunk: (chunk: string) => Effect.sync(() => detectPhase(chunk)),

		createDetector: () => Effect.sync(() => createStatefulDetector()),

		createCombinedDetector: () => Effect.sync(() => createCombinedStatefulDetector()),
	}),
}) {}
