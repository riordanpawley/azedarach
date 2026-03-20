import {
	cliRunner,
	normalizeCliAliases,
	normalizeIssueOptionOrder,
	resolveCliExecutionMode,
} from "@azedarach/cli"
import { AppConfigProjectContext, type AppConfigProjectContextApi } from "@azedarach/config"
import { GlobalDaemonBootstrap } from "@azedarach/daemon-control"
import { DaemonRpcClient } from "@azedarach/shared/rpc"
import { configureTuiDaemonRpcClient, configureTuiKeyboardService, launchTUI } from "@azedarach/tui"
import { BunContext } from "@effect/platform-bun"
import { Effect, Layer, Stream } from "effect"
import { KeyboardService as LegacyKeyboardService } from "../../../src/runtime/appServicesFacade.js"

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
			const daemonBootstrap = yield* GlobalDaemonBootstrap
			const bootstrap = yield* daemonBootstrap.bootstrapDaemonRpcClient({
				autoStart: true,
			})
			const projectContext: AppConfigProjectContextApi = {
				getCurrentPath: () => Effect.succeed(process.cwd()),
				currentProjectPathChanges: Stream.empty,
			}
			const keyboardService = yield* Effect.gen(function* () {
				return yield* LegacyKeyboardService
			}).pipe(
				Effect.provide(
					LegacyKeyboardService.Default.pipe(
						Layer.provideMerge(Layer.succeed(DaemonRpcClient, bootstrap.client)),
						Layer.provideMerge(Layer.succeed(AppConfigProjectContext, projectContext)),
						Layer.provideMerge(BunContext.layer),
					),
				),
			)
			configureTuiDaemonRpcClient(bootstrap.client)
			configureTuiKeyboardService(keyboardService)
			return yield* Effect.promise(() => launchTUI())
		})
	}

	return cliRunner(argv)
}

export const run = (input: EntryRunInput) => runAz(input.argv)
