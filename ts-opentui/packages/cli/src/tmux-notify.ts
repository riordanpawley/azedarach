import { FileSystem, Command as PlatformCommand } from "@effect/platform"
import { Console, Effect } from "effect"
import type { TmuxStatus } from "../../../src/core/TmuxSessionMonitor.js"
import { deriveWaitingAttentionPlan } from "./output-formatting.js"

export type HookEvent =
	| "user_prompt"
	| "idle_prompt"
	| "permission_request"
	| "pretooluse"
	| "stop"
	| "session_end"

export const VALID_HOOK_EVENTS: readonly HookEvent[] = [
	"user_prompt",
	"idle_prompt",
	"permission_request",
	"pretooluse",
	"stop",
	"session_end",
]

export const isValidHookEvent = (event: string): event is HookEvent =>
	event === "user_prompt" ||
	event === "idle_prompt" ||
	event === "permission_request" ||
	event === "pretooluse" ||
	event === "stop" ||
	event === "session_end"

export const mapHookEventToTmuxStatus = (event: HookEvent): TmuxStatus => {
	switch (event) {
		case "user_prompt":
		case "pretooluse":
			return "busy"
		case "idle_prompt":
		case "permission_request":
		case "stop":
			return "waiting"
		case "session_end":
			return "idle"
	}
}

const AZ_STATUS_OPTION = "@az_status"
const AZ_WAITING_ALERTED_OPTION = "@az_waiting_alerted"
const BELL_CHAR = "\u0007"
const WAITING_WINDOW_BELL_STYLE = "fg=colour226,bg=colour237,bold"
const WAITING_WINDOW_ACTIVITY_STYLE = "fg=colour220,bg=colour237,bold"

type TmuxOptionScope = "session" | "window"

interface TmuxOptionUpdate {
	readonly scope: TmuxOptionScope
	readonly option: string
	readonly value: string
}

const WAITING_ATTENTION_OPTION_UPDATES: readonly TmuxOptionUpdate[] = [
	{ scope: "window", option: "monitor-bell", value: "on" },
	{ scope: "window", option: "monitor-activity", value: "on" },
	{ scope: "session", option: "bell-action", value: "any" },
	{ scope: "session", option: "activity-action", value: "any" },
	{ scope: "window", option: "window-status-bell-style", value: WAITING_WINDOW_BELL_STYLE },
	{ scope: "window", option: "window-status-activity-style", value: WAITING_WINDOW_ACTIVITY_STYLE },
]

const setTmuxSessionOption = (
	sessionName: string,
	optionName: string,
	value: string,
	verbose: boolean,
) =>
	PlatformCommand.exitCode(
		PlatformCommand.make("tmux", "set-option", "-t", sessionName, optionName, value),
	).pipe(
		Effect.catchAll((error) =>
			Effect.logWarning(error).pipe(
				Effect.zipRight(
					verbose
						? Console.log(`Could not set tmux option ${optionName}: ${error}`).pipe(Effect.as(1))
						: Effect.succeed(1),
				),
			),
		),
	)

const setTmuxWindowOption = (
	sessionName: string,
	optionName: string,
	value: string,
	verbose: boolean,
) =>
	PlatformCommand.exitCode(
		PlatformCommand.make("tmux", "set-window-option", "-t", sessionName, optionName, value),
	).pipe(
		Effect.catchAll((error) =>
			Effect.logWarning(error).pipe(
				Effect.zipRight(
					verbose
						? Console.log(`Could not set tmux window option ${optionName}: ${error}`).pipe(
								Effect.as(1),
							)
						: Effect.succeed(1),
				),
			),
		),
	)

const getTmuxSessionOption = (sessionName: string, optionName: string) =>
	PlatformCommand.string(
		PlatformCommand.make("tmux", "show-option", "-t", sessionName, "-v", optionName),
	).pipe(
		Effect.map((value) => value.trim()),
		Effect.catchAll((error) =>
			Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
				Effect.zipRight(Effect.succeed("")),
			),
		),
	)

const ringSessionPaneBell = (sessionName: string) =>
	Effect.gen(function* () {
		const paneTty = yield* PlatformCommand.string(
			PlatformCommand.make("tmux", "display-message", "-p", "-t", sessionName, "#{pane_tty}"),
		).pipe(
			Effect.map((value) => value.trim()),
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed("")),
				),
			),
		)
		if (paneTty.length === 0) {
			return false
		}

		const fs = yield* FileSystem.FileSystem
		return yield* fs.writeFileString(paneTty, BELL_CHAR).pipe(
			Effect.as(true),
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed(false)),
				),
			),
		)
	})

export const buildWaitingAttentionOptionCommands = (
	sessionName: string,
): readonly (readonly string[])[] =>
	WAITING_ATTENTION_OPTION_UPDATES.map((update) =>
		update.scope === "session"
			? ["set-option", "-t", sessionName, update.option, update.value]
			: ["set-window-option", "-t", sessionName, update.option, update.value],
	)

const applyTmuxAttentionStyles = (sessionName: string, verbose: boolean) =>
	Effect.gen(function* () {
		// Keep bell monitoring + alert styles session-local so Az sessions stay readable
		// in native tmux pickers without changing the user's global theme.
		for (const update of WAITING_ATTENTION_OPTION_UPDATES) {
			if (update.scope === "session") {
				yield* setTmuxSessionOption(sessionName, update.option, update.value, verbose)
				continue
			}
			yield* setTmuxWindowOption(sessionName, update.option, update.value, verbose)
		}
	})

const applyTmuxWaitingAttentionSignal = (
	sessionName: string,
	status: TmuxStatus,
	verbose: boolean,
) =>
	Effect.gen(function* () {
		yield* applyTmuxAttentionStyles(sessionName, verbose)

		const currentFlag = yield* getTmuxSessionOption(sessionName, AZ_WAITING_ALERTED_OPTION)
		const plan = deriveWaitingAttentionPlan(status, currentFlag.length > 0 ? currentFlag : null)

		let nextFlag: "0" | "1" = plan.nextFlag
		if (plan.ringBell) {
			const bellSent = yield* ringSessionPaneBell(sessionName)
			if (!bellSent) {
				nextFlag = "0"
				if (verbose) {
					yield* Console.log(`Could not ring tmux bell for session ${sessionName}`)
				}
			}
		}

		yield* setTmuxSessionOption(sessionName, AZ_WAITING_ALERTED_OPTION, nextFlag, verbose)
	})

export const applyNotifyStatusToTmux = (
	sessionName: string,
	status: TmuxStatus,
	verbose: boolean,
) =>
	Effect.gen(function* () {
		yield* setTmuxSessionOption(sessionName, AZ_STATUS_OPTION, status, verbose)
		yield* applyTmuxWaitingAttentionSignal(sessionName, status, verbose)
	})
