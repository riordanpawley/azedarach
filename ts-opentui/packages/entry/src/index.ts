import {
	cliRunner,
	normalizeCliAliases,
	normalizeIssueOptionOrder,
	resolveCliExecutionMode,
} from "@azedarach/cli"
import { bootstrapDaemonRpcClient } from "@azedarach/shared"
import { launchTUI } from "@azedarach/tui"
import { Effect } from "effect"

export type AzEntrypointMode = "cli" | "tui"

export type EntryRoute = Readonly<{
	readonly name: string
	readonly path: string
}>

export type EntryRunInput = Readonly<{
	readonly route: EntryRoute
	readonly argv: ReadonlyArray<string>
}>

export type EntryRunResult = Readonly<{
	readonly exitCode: number
}>

export const route = (definition: EntryRoute): EntryRoute => ({ ...definition })

export const resolveAzEntrypointMode = (argv: ReadonlyArray<string>): AzEntrypointMode => {
	const normalizedArgv = normalizeIssueOptionOrder(normalizeCliAliases(argv))
	return resolveCliExecutionMode(normalizedArgv) === "tui" ? "tui" : "cli"
}

export const runAz = (argv: ReadonlyArray<string>) => {
	if (resolveAzEntrypointMode(argv) === "tui") {
		return Effect.gen(function* () {
			yield* bootstrapDaemonRpcClient({
				autoStart: true,
			})
			return yield* Effect.promise(() => launchTUI())
		})
	}

	return cliRunner(argv)
}

export const run = (input: EntryRunInput) => runAz(input.argv)
