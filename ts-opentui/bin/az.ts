#!/usr/bin/env bun

const { BunContext, BunRuntime } = await import("@effect/platform-bun")
const { Effect } = await import("effect")
const { cliRunner, normalizeCliAliases, normalizeIssueOptionOrder, resolveCliExecutionMode } =
	await import("@azedarach/cli")
const { launchTUI } = await import("@azedarach/tui")

export type AzEntrypointMode = "cli" | "tui"

export const resolveAzEntrypointMode = (argv: ReadonlyArray<string>): AzEntrypointMode => {
	const normalizedArgv = normalizeIssueOptionOrder(normalizeCliAliases(argv))
	return resolveCliExecutionMode(normalizedArgv) === "tui" ? "tui" : "cli"
}

export const runAz = (argv: ReadonlyArray<string>) => {
	if (resolveAzEntrypointMode(argv) === "tui") {
		return Effect.promise(() => launchTUI())
	}

	return cliRunner(argv)
}

if (import.meta.main) {
	// Two-level layer provision (idiomatic @effect/cli pattern):
	// 1. Command.provide(cliLayer) - our app services (done in cli/index.ts)
	// 2. Effect.provide(BunContext.layer) - platform services for @effect/cli internals
	runAz(process.argv).pipe(Effect.provide(BunContext.layer), BunRuntime.runMain)
}
