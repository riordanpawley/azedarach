#!/usr/bin/env bun

/**
 * Azedarach CLI Entry Point
 *
 * Launches in the current terminal context and executes the appropriate
 * command/TUI mode without implicit tmux wrapping.
 */
export {}

const { BunContext, BunRuntime } = await import("@effect/platform-bun")
const { Effect } = await import("effect")
const { cliRunner } = await import("@azedarach/cli")

// Two-level layer provision (idiomatic @effect/cli pattern):
// 1. Command.provide(cliLayer) - our app services (done in cli/index.ts)
// 2. Effect.provide(BunContext.layer) - platform services for @effect/cli internals
cliRunner(process.argv).pipe(Effect.provide(BunContext.layer), BunRuntime.runMain)
