import { Command, type CommandExecutor } from "@effect/platform"
import { Data, Effect } from "effect"

// Errors
// biome-ignore lint/complexity/noBannedTypes: <eh>
export class TmuxNotFoundError extends Data.TaggedError("TmuxNotFoundError")<{}> {}
export class SessionNotFoundError extends Data.TaggedError("SessionNotFoundError")<{
	session: string
}> {}
export class TmuxError extends Data.TaggedError("TmuxError")<{ message: string }> {}

// Session info type
export interface TmuxSession {
	name: string
	windows: number
	created: Date
	attached: boolean
}

// Helper to run tmux commands
const runTmux = (
	args: string[],
): Effect.Effect<string, TmuxError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const command = Command.make("tmux", ...args)
		return yield* Command.string(command)
	}).pipe(Effect.mapError((e) => new TmuxError({ message: String(e) })))

// Service implementation
export class TmuxService extends Effect.Service<TmuxService>()("TmuxService", {
	dependencies: [],
	effect: Effect.gen(function* () {
		return {
			newSession: (name: string, opts?: { cwd?: string; command?: string; prefix?: string }) =>
				Effect.gen(function* () {
					const args = ["new-session", "-d", "-s", name]
					if (opts?.cwd) args.push("-c", opts.cwd)
					if (opts?.command) args.push(opts.command)
					yield* runTmux(args)

					// Set custom prefix for this session (default C-a to avoid Claude capturing C-b)
					const prefix = opts?.prefix ?? "C-a"
					yield* runTmux(["set-option", "-t", name, "prefix", prefix])
					yield* runTmux(["set-option", "-t", name, "prefix2", "None"])

					// Increase scrollback buffer (default 2000 is far too small for Claude output)
					yield* runTmux(["set-option", "-t", name, "history-limit", "500000"])

					// Enable vi-style copy mode keys (Ctrl-u/d work for half-page scroll in copy mode)
					yield* runTmux(["set-option", "-t", name, "mode-keys", "vi"])

					// Bind Ctrl-u to enter copy mode and scroll up (global, idempotent)
					// This lets users scroll Claude output without prefix+[ first
					yield* runTmux(["bind-key", "-n", "C-u", "copy-mode", "-u"])
				}),

			killSession: (name: string) =>
				runTmux(["kill-session", "-t", name]).pipe(
					Effect.asVoid,
					Effect.catchAll(() => Effect.fail(new SessionNotFoundError({ session: name }))),
				),

			listSessions: () =>
				Effect.gen(function* () {
					const output = yield* runTmux([
						"list-sessions",
						"-F",
						"#{session_name}:#{session_windows}:#{session_created}:#{session_attached}",
					])
					return output
						.trim()
						.split("\n")
						.filter(Boolean)
						.map((line) => {
							const [name, windows, created, attached] = line.split(":")
							return {
								name,
								windows: parseInt(windows, 10),
								created: new Date(parseInt(created, 10) * 1000),
								attached: attached === "1",
							}
						})
				}).pipe(
					Effect.catchAll(() => Effect.succeed([])), // No sessions = empty list
				),

			hasSession: (name: string) =>
				Effect.gen(function* () {
					// Query tmux directly for session names (avoids circular service reference)
					const output = yield* runTmux(["list-sessions", "-F", "#{session_name}"])
					const sessions = output.trim().split("\n").filter(Boolean)
					return sessions.includes(name)
				}).pipe(
					Effect.catchAll(() => Effect.succeed(false)), // No sessions = doesn't exist
				),

			sendKeys: (session: string, keys: string) =>
				runTmux(["send-keys", "-t", session, keys, "Enter"]).pipe(
					Effect.asVoid,
					Effect.catchAll(() => Effect.fail(new SessionNotFoundError({ session }))),
				),

			attachCommand: (session: string) => `tmux attach-session -t ${session}`,

			switchClient: (session: string) =>
				runTmux(["switch-client", "-t", session]).pipe(
					Effect.asVoid,
					Effect.catchAll(() => Effect.fail(new SessionNotFoundError({ session }))),
				),
		}
	}),
}) {}
