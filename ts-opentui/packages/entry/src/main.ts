#!/usr/bin/env bun

import { BunContext, BunRuntime } from "@effect/platform-bun"
import { Effect } from "effect"
import { runAz } from "./index.js"

export const runAzMain = (argv: ReadonlyArray<string>) =>
	BunRuntime.runMain(Effect.provide(runAz(argv), BunContext.layer))

if (import.meta.main) {
	runAzMain(process.argv)
}
