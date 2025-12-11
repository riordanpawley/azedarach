#!/usr/bin/env bun

/**
 * Azedarach CLI Entry Point
 *
 * Parses command-line arguments and executes the appropriate command.
 * Uses @effect/cli for type-safe argument parsing.
 */

import { BunRuntime } from "@effect/platform-bun"
import { Effect } from "effect"
import { run } from "../src/cli/index.js"

// Run the CLI
// Note: @effect/cli expects full process.argv (it handles stripping binary/script path)
// Effect.suspend ensures lazy evaluation of CLI parsing
Effect.suspend(() => run(process.argv)).pipe(BunRuntime.runMain)
