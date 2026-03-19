#!/usr/bin/env bun

import { DaemonControlLive } from "@azedarach/daemon"
import { BunContext, BunRuntime } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { runAz } from "./index.js"

const EntryRuntimeLayer = Layer.mergeAll(BunContext.layer, DaemonControlLive)

export const runAzMain = (argv: ReadonlyArray<string>) =>
	BunRuntime.runMain(runAz(argv).pipe(Effect.provide(EntryRuntimeLayer)))

if (import.meta.main) {
	runAzMain(process.argv)
}
