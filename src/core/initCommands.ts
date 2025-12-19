/**
 * InitCommands - Reusable worktree initialization command execution
 *
 * Runs the configured initCommands (e.g., "direnv allow", "pnpm install") in a worktree.
 * Used by both ClaudeSessionManager (on session start) and DevServerService (on dev server start)
 * to ensure dependencies are synced before running commands.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { Data, Effect } from "effect"

/**
 * Error when an init command fails and continueOnFailure is false
 */
export class InitCommandError extends Data.TaggedError("InitCommandError")<{
	readonly message: string
	readonly command: string
	readonly worktreePath: string
}> {}

/**
 * Result of running init commands
 */
export interface InitCommandsResult {
	/** Commands that failed (empty if all succeeded) */
	readonly failedCommands: readonly string[]
	/** Whether any command failed */
	readonly hasFailures: boolean
}

/**
 * Options for running init commands
 */
export interface RunInitCommandsOptions {
	/** Path to the worktree directory */
	readonly worktreePath: string
	/** List of commands to run */
	readonly initCommands: readonly string[]
	/** Environment variables to set for commands */
	readonly env: Readonly<Record<string, string>>
	/** Continue running remaining commands if one fails */
	readonly continueOnFailure: boolean
	/** Run commands in parallel instead of sequentially */
	readonly parallel: boolean
}

/**
 * Run init commands in a worktree
 *
 * Executes the configured initCommands (from worktree config) in the specified directory.
 * Respects continueOnFailure and parallel settings from config.
 *
 * @example
 * ```ts
 * const result = yield* runInitCommands({
 *   worktreePath: "/Users/user/project-az-123",
 *   initCommands: ["direnv allow", "pnpm install"],
 *   env: {},
 *   continueOnFailure: true,
 *   parallel: false,
 * })
 * if (result.hasFailures) {
 *   yield* Effect.logWarning(`Some init commands failed: ${result.failedCommands.join(", ")}`)
 * }
 * ```
 */
export const runInitCommands = (
	options: RunInitCommandsOptions,
): Effect.Effect<InitCommandsResult, InitCommandError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const { worktreePath, initCommands, env, continueOnFailure, parallel } = options

		// Fast path: nothing to do
		if (initCommands.length === 0) {
			return { failedCommands: [], hasFailures: false }
		}

		const failedCommands: string[] = []

		const runSingleCommand = (cmd: string) =>
			Effect.gen(function* () {
				yield* Effect.log(`Running init command: ${cmd}`)

				const initCmd = Command.make("sh", "-c", cmd).pipe(
					Command.workingDirectory(worktreePath),
					Command.env(env),
				)

				const exitCode = yield* Command.exitCode(initCmd).pipe(
					Effect.catchAll(() => Effect.succeed(1)),
				)

				if (exitCode !== 0) {
					failedCommands.push(cmd)
					yield* Effect.logWarning(`Init command failed: ${cmd}`)

					if (!continueOnFailure) {
						return yield* Effect.fail(
							new InitCommandError({
								message: `Init command failed: ${cmd}`,
								command: cmd,
								worktreePath,
							}),
						)
					}
				}
			})

		if (parallel) {
			// Run all commands in parallel
			yield* Effect.all(initCommands.map(runSingleCommand), { concurrency: "unbounded" })
		} else {
			// Run commands sequentially (default)
			for (const cmd of initCommands) {
				yield* runSingleCommand(cmd)
			}
		}

		return {
			failedCommands: failedCommands as readonly string[],
			hasFailures: failedCommands.length > 0,
		}
	})
