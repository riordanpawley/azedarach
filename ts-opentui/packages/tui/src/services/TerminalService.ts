import { Command } from "@effect/platform"
import * as CommandExecutor from "@effect/platform/CommandExecutor"
import { BunContext } from "@effect/platform-bun"
import { Data, Effect } from "effect"

export class NotInsideTmuxError extends Data.TaggedError("NotInsideTmuxError")<{
	readonly message: string
}> {}

export class TmuxCommandError extends Data.TaggedError("TmuxCommandError")<{
	readonly message: string
	readonly command: string
}> {}

export interface TerminalServiceI {
	readonly isInsideTmux: () => Effect.Effect<boolean, never>
	readonly openInTmuxWindow: (
		cmd: string,
		windowName?: string,
	) => Effect.Effect<void, NotInsideTmuxError | TmuxCommandError>
	readonly switchToSession: (
		sessionName: string,
	) => Effect.Effect<void, NotInsideTmuxError | TmuxCommandError>
}

const insideTmux = (): boolean => process.env.TMUX !== undefined

const notInsideTmuxError = () =>
	new NotInsideTmuxError({
		message: "Azedarach must run inside tmux. Start with: tmux new-session -s az 'bun run dev'",
	})

const tmuxCommandError = (message: string, command: ReadonlyArray<string>) =>
	new TmuxCommandError({
		message,
		command: command.join(" "),
	})

const runTmuxCommand = (
	executor: CommandExecutor.CommandExecutor,
	args: ReadonlyArray<string>,
): Effect.Effect<void, NotInsideTmuxError | TmuxCommandError> =>
	Effect.gen(function* () {
		if (!insideTmux()) {
			return yield* Effect.fail(notInsideTmuxError())
		}
		const command = Command.make("tmux", ...args)
		const exitCode = yield* executor
			.exitCode(command)
			.pipe(
				Effect.mapError((error) =>
					tmuxCommandError(`Failed to execute tmux command: ${String(error)}`, ["tmux", ...args]),
				),
			)
		if (exitCode !== 0) {
			return yield* Effect.fail(
				tmuxCommandError(`tmux command exited with status ${exitCode}`, ["tmux", ...args]),
			)
		}
	})

export class TerminalService extends Effect.Service<TerminalService>()("TerminalService", {
	dependencies: [BunContext.layer],
	effect: Effect.gen(function* () {
		const executor = yield* CommandExecutor.CommandExecutor
		return {
			isInsideTmux: () => Effect.sync(insideTmux),
			openInTmuxWindow: (cmd: string, windowName?: string) =>
				windowName === undefined
					? runTmuxCommand(executor, ["new-window", cmd])
					: runTmuxCommand(executor, ["new-window", "-n", windowName, cmd]),
			switchToSession: (sessionName: string) =>
				runTmuxCommand(executor, ["switch-client", "-t", sessionName]),
		} satisfies TerminalServiceI
	}),
}) {}
