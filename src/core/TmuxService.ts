import { Command, type CommandExecutor } from "@effect/platform"
import { Data, Effect, Option } from "effect"

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
			newSession: (
				name: string,
				opts?: {
					cwd?: string
					command?: string
					prefix?: string
					/** Azedarach-specific options for session state tracking */
					azOptions?: {
						/** Path to the worktree directory */
						worktreePath?: string
						/** Path to the main project directory */
						projectPath?: string
					}
				},
			) =>
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

					// Add Ctrl-a Tab keybinding to toggle between Claude and dev server sessions
					// Session naming format: {type}-{beadId}
					// - Claude sessions: claude-{beadId}
					// - Dev servers: dev-{beadId}
					// Also supports legacy format: az-dev-{beadId}
					// Note: Shell syntax requires no semicolon after 'then', 'else', or before 'fi'
					const toggleScript =
						'current=$(tmux display-message -p "#S"); ' +
						// dev-{beadId} → claude-{beadId}
						'if [ "${current#dev-}" != "$current" ]; then ' +
						'beadId="${current#dev-}"; target="claude-$beadId"; msg="No Claude session"; ' +
						// claude-{beadId} → dev-{beadId}
						'elif [ "${current#claude-}" != "$current" ]; then ' +
						'beadId="${current#claude-}"; target="dev-$beadId"; msg="No dev server (Space+r to start)"; ' +
						// Legacy: az-dev-{beadId} → claude-{beadId}
						'elif [ "${current#az-dev-}" != "$current" ]; then ' +
						'beadId="${current#az-dev-}"; target="claude-$beadId"; msg="No Claude session"; ' +
						"else " +
						'msg="Unknown session format"; ' +
						"fi; " +
						'if [ -n "$target" ] && tmux has-session -t "$target" 2>/dev/null; then ' +
						'tmux switch-client -t "$target"; ' +
						"else " +
						'tmux display-message "$msg"; ' +
						"fi"
					yield* runTmux(["bind-key", "-T", "prefix", "Tab", "run-shell", toggleScript])

					// Set azedarach session options for state tracking
					// These enable crash recovery - TmuxSessionMonitor can reconstruct state from tmux
					if (opts?.azOptions?.worktreePath) {
						yield* runTmux(["set-option", "-t", name, "@az_worktree", opts.azOptions.worktreePath])
					}
					if (opts?.azOptions?.projectPath) {
						yield* runTmux(["set-option", "-t", name, "@az_project", opts.azOptions.projectPath])
					}
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

			/**
			 * Create a new window in an existing session
			 *
			 * @param session - tmux session name
			 * @param windowName - name for the new window
			 * @param opts.cwd - Working directory for the new window
			 * @param opts.command - Command to run in the new window
			 */
			newWindow: (
				session: string,
				windowName: string,
				opts?: {
					cwd?: string
					command?: string
				},
			) =>
				Effect.gen(function* () {
					const args = ["new-window", "-t", session, "-n", windowName]
					if (opts?.cwd) args.push("-c", opts.cwd)
					if (opts?.command) args.push(opts.command)
					yield* runTmux(args)
				}),

			/**
			 * Display a popup window with a command
			 *
			 * Opens a centered popup window that runs the specified command.
			 * The -E flag closes the popup when the command exits.
			 *
			 * @param opts.command - Command to run in the popup
			 * @param opts.width - Width as percentage (default "80%")
			 * @param opts.height - Height as percentage (default "80%")
			 * @param opts.title - Optional title for the popup border
			 * @param opts.cwd - Working directory for the command (optional)
			 */
			displayPopup: (opts: {
				command: string
				width?: string
				height?: string
				title?: string
				cwd?: string
			}) =>
				Effect.gen(function* () {
					const args = [
						"display-popup",
						"-E", // Close popup when command exits
						"-w",
						opts.width ?? "80%",
						"-h",
						opts.height ?? "80%",
					]
					if (opts.cwd) {
						args.push("-d", opts.cwd)
					}
					if (opts.title) {
						args.push("-T", opts.title)
					}
					args.push(opts.command)
					yield* runTmux(args)
				}),

			/**
			 * Capture pane content for state detection
			 *
			 * Captures the visible content of a tmux pane. Used by PTYMonitor
			 * to detect session state from output patterns.
			 *
			 * @param session - tmux session name
			 * @param lines - number of lines to capture from history (negative = from end)
			 * @returns captured pane content as string
			 */
			capturePane: (session: string, lines?: number) =>
				Effect.gen(function* () {
					const args = ["capture-pane", "-t", session, "-p"]
					if (lines !== undefined) {
						// -S sets the start line (negative = from history end)
						args.push("-S", String(-Math.abs(lines)))
					}
					return yield* runTmux(args)
				}).pipe(
					Effect.catchAll(() => Effect.succeed("")), // Return empty on error (session may be dead)
				),

			/**
			 * Set a user-defined option on a tmux session
			 *
			 * User options start with @ and are stored on the session itself.
			 * This makes tmux the source of truth for session metadata.
			 *
			 * @param session - tmux session name
			 * @param key - option key (should start with @ for user options)
			 * @param value - option value (string)
			 */
			setUserOption: (session: string, key: string, value: string) =>
				runTmux(["set-option", "-t", session, key, value]).pipe(
					Effect.asVoid,
					Effect.catchAll(() => Effect.fail(new SessionNotFoundError({ session }))),
				),

			/**
			 * Get a user-defined option from a tmux session
			 *
			 * Returns None if the option doesn't exist or session is not found.
			 *
			 * @param session - tmux session name
			 * @param key - option key (should start with @ for user options)
			 * @returns Option<string> - the option value or None if not found
			 */
			getUserOption: (session: string, key: string) =>
				Effect.gen(function* () {
					// -v returns just the value (not "key value" format)
					const output = yield* runTmux(["show-option", "-t", session, "-v", key])
					const value = output.trim()
					return value.length > 0 ? Option.some(value) : Option.none()
				}).pipe(
					Effect.catchAll(() => Effect.succeed(Option.none())), // Return None on error
				),
		}
	}),
}) {}
