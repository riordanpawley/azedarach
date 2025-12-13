/**
 * StateDetector Usage Examples
 *
 * Demonstrates how to use the StateDetector service for detecting
 * Claude session state from PTY output.
 */

import { Effect } from "effect"
import {
	createDetector,
	detectFromChunk,
	type SessionState,
	StateDetector,
	StateDetectorLive,
} from "./StateDetector"

// ============================================================================
// Example 1: Basic state detection from a single chunk
// ============================================================================

const example1 = Effect.gen(function* () {
	console.log("=== Example 1: Single chunk detection ===\n")

	const detector = yield* StateDetector

	// Test various output patterns
	const testCases = [
		"Building project...",
		"Error: File not found",
		"Do you want to continue? [y/n]",
		"Task completed successfully!",
		"   ",
		"Running tests... Still running...",
	]

	for (const chunk of testCases) {
		const state = yield* detector.detectFromChunk(chunk)
		console.log(`Input: "${chunk}"`)
		console.log(`State: ${state}\n`)
	}
}).pipe(Effect.provide(StateDetectorLive))

// ============================================================================
// Example 2: Stateful detector with debouncing
// ============================================================================

const example2 = Effect.gen(function* () {
	console.log("=== Example 2: Stateful detector with debouncing ===\n")

	const detector = yield* StateDetector
	const detect = yield* detector.createDetector()

	// Simulate a stream of output chunks
	const outputStream = [
		"Starting build...",
		"Compiling TypeScript...",
		"Compiling TypeScript...", // Still busy (debounced)
		"Compiling TypeScript...", // Still busy (debounced)
		"Do you want to deploy? [y/n]", // Transition to waiting
		"Deploying...", // Back to busy
		"Done.", // Completed
	]

	console.log("Processing output stream:\n")
	for (const chunk of outputStream) {
		const state = detect(chunk)
		console.log(`Chunk: "${chunk}"`)
		console.log(`State: ${state ?? "(no change)"}\n`)

		// Simulate time passing between chunks
		yield* Effect.sleep("50 millis")
	}
}).pipe(Effect.provide(StateDetectorLive))

// ============================================================================
// Example 3: Using convenience functions
// ============================================================================

const example3 = Effect.gen(function* () {
	console.log("=== Example 3: Convenience functions ===\n")

	// Direct detection
	const errorState = yield* detectFromChunk("Error: ENOENT: file not found")
	console.log(`Error detection: ${errorState}`)

	const waitingState = yield* detectFromChunk("Press Enter to continue...")
	console.log(`Waiting detection: ${waitingState}`)

	const doneState = yield* detectFromChunk("Successfully completed all tasks")
	console.log(`Done detection: ${doneState}\n`)
}).pipe(Effect.provide(StateDetectorLive))

// ============================================================================
// Example 4: Practical PTY stream processing
// ============================================================================

const example4 = Effect.gen(function* () {
	console.log("=== Example 4: PTY stream processing ===\n")

	const detect = yield* createDetector()

	// Simulate processing PTY output in a real session
	const simulatePtyOutput = (chunks: string[]): SessionState[] => {
		const states: SessionState[] = []

		for (const chunk of chunks) {
			const state = detect(chunk)
			if (state !== null) {
				states.push(state)
				console.log(`State transition: ${state}`)
				console.log(`  Triggered by: "${chunk.substring(0, 50)}..."\n`)
			}
		}

		return states
	}

	const ptyOutput = [
		"$ claude-code\n",
		"Loading project...\n",
		"Analyzing codebase...\n",
		"Found 42 files\n",
		"\n",
		"I'll help you implement the feature.\n",
		"\n",
		"Error: Missing configuration file\n",
		"Please create .config.json\n",
	]

	const states = simulatePtyOutput(ptyOutput)
	console.log(`Total state transitions: ${states.length}`)
}).pipe(Effect.provide(StateDetectorLive))

// ============================================================================
// Run Examples
// ============================================================================

const runAll = Effect.gen(function* () {
	yield* example1
	yield* example2
	yield* example3
	yield* example4
})

// Execute if run directly
if (import.meta.main) {
	Effect.runPromise(runAll)
}

export { example1, example2, example3, example4, runAll }
