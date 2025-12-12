#!/usr/bin/env bun

/**
 * Azedarach CLI Entry Point
 *
 * Auto-wraps in tmux if running outside tmux, then
 * parses command-line arguments and executes the appropriate command.
 *
 * Environment variables:
 * - AZ_NO_TMUX=1      Skip auto-wrap (for debugging, CI)
 * - AZ_TMUX_SESSION   Custom tmux session name (default: "azedarach")
 */

import { shouldWrapInTmux, execInTmux } from "../src/lib/tmux-wrap.js"

// CRITICAL: Check tmux BEFORE any Effect initialization
// This must happen early to avoid loading heavy modules we'll discard when exec'ing
if (shouldWrapInTmux()) {
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
