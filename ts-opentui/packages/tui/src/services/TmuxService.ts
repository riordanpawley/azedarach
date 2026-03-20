import { Command } from "@effect/platform"
import * as CommandExecutor from "@effect/platform/CommandExecutor"
import { BunContext } from "@effect/platform-bun"
import { Array as Arr, Data, Effect, Option, Stream } from "effect"

export class TmuxNotFoundError extends Data.TaggedError("TmuxNotFoundError")<{}> {}

export class SessionNotFoundError extends Data.TaggedError("SessionNotFoundError")<{
	readonly session: string
}> {}

export class TmuxError extends Data.TaggedError("TmuxError")<{
	readonly message: string
}> {}

export interface TmuxSession {
	readonly name: string
	readonly windows: number
	readonly created: Date
	readonly attached: boolean
}

export interface TmuxSessionAlertState {
	readonly bell: boolean
	readonly activity: boolean
}

export interface TmuxServiceApi {
	readonly newSession: (
		name: string,
		opts?: {
			readonly cwd?: string
			readonly command?: string
			readonly prefix?: string
			readonly azOptions?: {
				readonly worktreePath?: string
				readonly projectPath?: string
			}
		},
	) => Effect.Effect<void, TmuxError>
	readonly killSession: (name: string) => Effect.Effect<void, SessionNotFoundError>
	readonly listSessions: () => Effect.Effect<ReadonlyArray<TmuxSession>, never>
	readonly hasSession: (name: string) => Effect.Effect<boolean, never>
	readonly sendKeys: (session: string, keys: string) => Effect.Effect<void, SessionNotFoundError>
	readonly sendLiteralCommand: (
		session: string,
		command: string,
	) => Effect.Effect<void, SessionNotFoundError>
	readonly attachCommand: (session: string) => string
	readonly switchClient: (session: string) => Effect.Effect<void, SessionNotFoundError>
	readonly newWindow: (
		session: string,
		windowName: string,
		opts?: {
			readonly cwd?: string
			readonly command?: string
		},
	) => Effect.Effect<void, TmuxError>
	readonly displayPopup: (opts: {
		readonly command: string
		readonly width?: string
		readonly height?: string
		readonly title?: string
		readonly cwd?: string
	}) => Effect.Effect<void, TmuxError>
	readonly renameSession: (
		oldName: string,
		newName: string,
	) => Effect.Effect<void, SessionNotFoundError>
	readonly renameWindow: (
		session: string,
		oldName: string,
		newName: string,
	) => Effect.Effect<void, SessionNotFoundError>
	readonly linkWindow: (source: string, target: string) => Effect.Effect<void, TmuxError>
	readonly listWindows: (session: string) => Effect.Effect<ReadonlyArray<string>, never>
	readonly hasWindow: (session: string, windowName: string) => Effect.Effect<boolean, never>
	readonly selectWindow: (
		session: string,
		windowName: string,
	) => Effect.Effect<void, SessionNotFoundError>
	readonly splitWindow: (
		target: string,
		opts?: {
			readonly horizontal?: boolean
			readonly cwd?: string
			readonly command?: string
		},
	) => Effect.Effect<string, TmuxError>
	readonly killPane: (target: string) => Effect.Effect<void, SessionNotFoundError>
	readonly killWindow: (target: string) => Effect.Effect<void, SessionNotFoundError>
	readonly listPanes: (
		target: string,
	) => Effect.Effect<ReadonlyArray<{ readonly id: string; readonly index: number }>, never>
	readonly capturePane: (session: string, lines?: number) => Effect.Effect<string, never>
	readonly getPaneCurrentCommand: (session: string) => Effect.Effect<string | null, never>
	readonly getSessionAlertState: (session: string) => Effect.Effect<TmuxSessionAlertState, never>
	readonly respawnPane: (
		session: string,
		command: string,
	) => Effect.Effect<void, SessionNotFoundError>
	readonly setWindowOption: (
		target: string,
		key: string,
		value: string,
	) => Effect.Effect<void, SessionNotFoundError>
	readonly setUserOption: (
		session: string,
		key: string,
		value: string,
	) => Effect.Effect<void, SessionNotFoundError>
	readonly getUserOption: (
		session: string,
		key: string,
	) => Effect.Effect<Option.Option<string>, never>
}

const TMUX_LITERAL_CHUNK_SIZE = 512
const EMPTY_SESSIONS: readonly TmuxSession[] = []
const EMPTY_WINDOW_NAMES: readonly string[] = []
const EMPTY_PANES: readonly { readonly id: string; readonly index: number }[] = []

const runTmux = (
	executor: CommandExecutor.CommandExecutor,
	args: ReadonlyArray<string>,
): Effect.Effect<string, TmuxError> =>
	executor
		.string(Command.make("tmux", ...args))
		.pipe(Effect.mapError((error) => new TmuxError({ message: String(error) })))

const withSessionNotFound = <A>(
	session: string,
	effect: Effect.Effect<A, TmuxError>,
): Effect.Effect<A, SessionNotFoundError> =>
	effect.pipe(
		Effect.catchAll((error) =>
			Effect.logWarning(error).pipe(
				Effect.zipRight(Effect.fail(new SessionNotFoundError({ session }))),
			),
		),
	)

export class TmuxService extends Effect.Service<TmuxService>()("TmuxService", {
	dependencies: [BunContext.layer],
	effect: Effect.gen(function* () {
		const executor = yield* CommandExecutor.CommandExecutor
		return {
			newSession: (name, opts) =>
				Effect.gen(function* () {
					const args = ["new-session", "-d", "-s", name]
					if (opts?.cwd !== undefined) {
						args.push("-c", opts.cwd)
					}
					if (opts?.command !== undefined) {
						args.push(opts.command)
					}
					yield* runTmux(executor, args)

					const prefix = opts?.prefix ?? "C-a"
					yield* runTmux(executor, ["set-option", "-t", name, "prefix", prefix])
					yield* runTmux(executor, ["set-option", "-t", name, "prefix2", "None"])
					yield* runTmux(executor, ["set-option", "-t", name, "history-limit", "500000"])
					yield* runTmux(executor, ["set-option", "-t", name, "mode-keys", "vi"])
					yield* runTmux(executor, ["set-option", "-t", name, "remain-on-exit", "on"])

					if (opts?.azOptions?.worktreePath !== undefined) {
						yield* runTmux(executor, [
							"set-option",
							"-t",
							name,
							"@az_worktree",
							opts.azOptions.worktreePath,
						])
					}
					if (opts?.azOptions?.projectPath !== undefined) {
						yield* runTmux(executor, [
							"set-option",
							"-t",
							name,
							"@az_project",
							opts.azOptions.projectPath,
						])
					}
				}),
			killSession: (name: string) =>
				withSessionNotFound(name, runTmux(executor, ["kill-session", "-t", name])).pipe(
					Effect.asVoid,
				),
			listSessions: () =>
				runTmux(executor, [
					"list-sessions",
					"-F",
					"#{session_name}:#{session_windows}:#{session_created}:#{session_attached}",
				]).pipe(
					Effect.map((output) =>
						output
							.trim()
							.split("\n")
							.filter((line) => line.length > 0)
							.map((line) => {
								const [name, windows, created, attached] = line.split(":")
								return {
									name,
									windows: Number.parseInt(windows ?? "0", 10),
									created: new Date(Number.parseInt(created ?? "0", 10) * 1000),
									attached: attached === "1",
								} satisfies TmuxSession
							}),
					),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(EMPTY_SESSIONS)),
						),
					),
				),
			hasSession: (name: string) =>
				runTmux(executor, ["list-sessions", "-F", "#{session_name}"]).pipe(
					Effect.map((output) =>
						output
							.trim()
							.split("\n")
							.filter((line) => line.length > 0)
							.includes(name),
					),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(false)),
						),
					),
				),
			sendKeys: (session: string, keys: string) =>
				withSessionNotFound(
					session,
					runTmux(executor, ["send-keys", "-t", session, keys, "Enter"]),
				).pipe(Effect.asVoid),
			sendLiteralCommand: (session: string, command: string) =>
				Effect.gen(function* () {
					yield* Stream.fromIterable(Arr.chunksOf([...command], TMUX_LITERAL_CHUNK_SIZE)).pipe(
						Stream.runForEach((chunk) =>
							runTmux(executor, ["send-keys", "-t", session, "-l", chunk.join("")]),
						),
					)
					yield* runTmux(executor, ["send-keys", "-t", session, "Enter"])
				}).pipe(Effect.asVoid, (effect) => withSessionNotFound(session, effect)),
			attachCommand: (session: string) => `tmux attach-session -t ${session}`,
			switchClient: (session: string) =>
				withSessionNotFound(session, runTmux(executor, ["switch-client", "-t", session])).pipe(
					Effect.asVoid,
				),
			newWindow: (session: string, windowName: string, opts) =>
				Effect.gen(function* () {
					const args = ["new-window", "-t", session, "-n", windowName]
					if (opts?.cwd !== undefined) args.push("-c", opts.cwd)
					if (opts?.command !== undefined) args.push(opts.command)
					yield* runTmux(executor, args)
				}),
			displayPopup: (opts) =>
				Effect.gen(function* () {
					const args = [
						"display-popup",
						"-E",
						"-w",
						opts.width ?? "80%",
						"-h",
						opts.height ?? "80%",
					]
					if (opts.cwd !== undefined) args.push("-d", opts.cwd)
					if (opts.title !== undefined) args.push("-T", opts.title)
					args.push(opts.command)
					yield* runTmux(executor, args)
				}),
			renameSession: (oldName: string, newName: string) =>
				withSessionNotFound(
					oldName,
					runTmux(executor, ["rename-session", "-t", oldName, newName]),
				).pipe(Effect.asVoid),
			renameWindow: (session: string, oldName: string, newName: string) =>
				withSessionNotFound(
					`${session}:${oldName}`,
					runTmux(executor, ["rename-window", "-t", `${session}:${oldName}`, newName]),
				).pipe(Effect.asVoid),
			linkWindow: (source: string, target: string) =>
				runTmux(executor, ["link-window", "-s", source, "-t", target]).pipe(
					Effect.asVoid,
					Effect.catchAll((error) =>
						Effect.logWarning(error).pipe(
							Effect.zipRight(
								Effect.fail(new TmuxError({ message: `Failed to link ${source} to ${target}` })),
							),
						),
					),
				),
			listWindows: (session: string) =>
				runTmux(executor, ["list-windows", "-t", session, "-F", "#{window_name}"]).pipe(
					Effect.map((output) =>
						output
							.trim()
							.split("\n")
							.filter((line) => line.length > 0),
					),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(EMPTY_WINDOW_NAMES)),
						),
					),
				),
			hasWindow: (session: string, windowName: string) =>
				runTmux(executor, ["list-windows", "-t", session, "-F", "#{window_name}"]).pipe(
					Effect.map((output) =>
						output
							.split("\n")
							.filter((line) => line.length > 0)
							.includes(windowName),
					),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(false)),
						),
					),
				),
			selectWindow: (session: string, windowName: string) =>
				withSessionNotFound(
					`${session}:${windowName}`,
					runTmux(executor, ["select-window", "-t", `${session}:${windowName}`]),
				).pipe(Effect.asVoid),
			splitWindow: (target: string, opts) =>
				Effect.gen(function* () {
					const args = ["split-window", "-t", target, "-P", "-F", "#{pane_id}"]
					args.push(opts?.horizontal === true ? "-h" : "-v")
					if (opts?.cwd !== undefined) args.push("-c", opts.cwd)
					if (opts?.command !== undefined) args.push(opts.command)
					const paneId = yield* runTmux(executor, args)
					return paneId.trim()
				}),
			killPane: (target: string) =>
				withSessionNotFound(target, runTmux(executor, ["kill-pane", "-t", target])).pipe(
					Effect.asVoid,
				),
			killWindow: (target: string) =>
				withSessionNotFound(target, runTmux(executor, ["kill-window", "-t", target])).pipe(
					Effect.asVoid,
				),
			listPanes: (target: string) =>
				runTmux(executor, ["list-panes", "-t", target, "-F", "#{pane_id}:#{pane_index}"]).pipe(
					Effect.map((output) =>
						output
							.trim()
							.split("\n")
							.filter((line) => line.length > 0)
							.map((line) => {
								const [id, index] = line.split(":")
								return {
									id: id ?? "",
									index: Number.parseInt(index ?? "0", 10),
								}
							}),
					),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(EMPTY_PANES)),
						),
					),
				),
			capturePane: (session: string, lines?: number) =>
				Effect.gen(function* () {
					const args = ["capture-pane", "-t", session, "-p", "-e", "-J"]
					if (lines !== undefined) {
						args.push("-S", String(-Math.abs(lines)))
					}
					return yield* runTmux(executor, args)
				}).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed("")),
						),
					),
				),
			getPaneCurrentCommand: (session: string) =>
				runTmux(executor, ["display-message", "-t", session, "-p", "#{pane_current_command}"]).pipe(
					Effect.map((output) => {
						const value = output.trim()
						return value.length === 0 ? null : value
					}),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed<string | null>(null)),
						),
					),
				),
			getSessionAlertState: (session: string) =>
				runTmux(executor, [
					"display-message",
					"-t",
					session,
					"-p",
					"#{session_bell_flag}|#{session_activity_flag}",
				]).pipe(
					Effect.map((output) => {
						const [bellFlag, activityFlag] = output.trim().split("|")
						return {
							bell: bellFlag === "1",
							activity: activityFlag === "1",
						} satisfies TmuxSessionAlertState
					}),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(
								Effect.succeed({
									bell: false,
									activity: false,
								} satisfies TmuxSessionAlertState),
							),
						),
					),
				),
			respawnPane: (session: string, command: string) =>
				withSessionNotFound(
					session,
					Effect.gen(function* () {
						yield* runTmux(executor, ["send-keys", "-t", session, "C-c"])
						yield* Effect.sleep("100 millis")
						yield* runTmux(executor, ["send-keys", "-t", session, command, "Enter"])
					}),
				).pipe(Effect.asVoid),
			setWindowOption: (target: string, key: string, value: string) =>
				withSessionNotFound(
					target,
					runTmux(executor, ["set-window-option", "-t", target, key, value]),
				).pipe(Effect.asVoid),
			setUserOption: (session: string, key: string, value: string) =>
				withSessionNotFound(
					session,
					runTmux(executor, ["set-option", "-t", session, key, value]),
				).pipe(Effect.asVoid),
			getUserOption: (session: string, key: string) =>
				runTmux(executor, ["show-option", "-t", session, "-v", key]).pipe(
					Effect.map((output) => {
						const value = output.trim()
						return value.length > 0 ? Option.some(value) : Option.none<string>()
					}),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(Option.none<string>())),
						),
					),
				),
		} satisfies TmuxServiceApi
	}),
}) {}

export const TmuxServiceLive = TmuxService.Default
