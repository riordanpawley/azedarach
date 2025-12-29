#!/usr/bin/env bun

/**
 * Azedarach CLI Entry Point
 *
 * Auto-wraps in tmux ONLY for the bare TUI command (no subcommands), then
 * parses command-line arguments and executes the appropriate command.
 *
 * Environment variables:
 * - AZ_NO_TMUX=1      Skip auto-wrap (for debugging, CI)
 * - AZ_TMUX_SESSION   Custom tmux session name (default: "azedarach")
 */

import { execInTmux, shouldWrapInTmux } from "../src/lib/tmux-wrap.js"

/**
 * Known subcommands that should NOT trigger tmux wrapping.
 * These commands run in the current terminal and don't need the TUI.
 */
const CLI_SUBCOMMANDS = new Set([
	"add",
	"list",
	"start",
	"attach",
	"pause",
	"kill",
	"status",
	"sync",
	"gate",
	"dev",
	"notify",
	"hooks",
	"project",
	"--help",
	"-h",
	"--version",
])

/**
 * Check if this invocation is a CLI subcommand (not the TUI).
 * Returns true if we should skip tmux wrapping.
 */
function isCliSubcommand(): boolean {
	// argv[0] = bun, argv[1] = script path, argv[2] = first user arg
	const firstArg = process.argv[2]
	if (!firstArg) return false

	// Check if it's a known subcommand
	return CLI_SUBCOMMANDS.has(firstArg)
}

// CRITICAL: Check tmux BEFORE any Effect initialization
// This must happen early to avoid loading heavy modules we'll discard when exec'ing
// Only wrap in tmux for the bare TUI command, NOT for CLI subcommands
if (shouldWrapInTmux() && !isCliSubcommand()) {
	// This never returns - it execs into tmux and exits with that process's code
	await execInTmux(process.argv)
} else {
	// Normal startup path - dynamic imports keep the fast path minimal
	const { BunRuntime } = await import("@effect/platform-bun")
	const { Effect } = await import("effect")
	const { run } = await import("../src/cli/index.js")

	// Note: @effect/cli expects full process.argv (it handles stripping binary/script path)
	// Effect.suspend ensures lazy evaluation of CLI parsing
	Effect.suspend(() => run(process.argv)).pipe(BunRuntime.runMain)
}
