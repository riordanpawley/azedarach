import type { CommandExecutor } from "@effect/platform"
import type { Effect } from "effect"

export type KeyMode =
	| "normal"
	| "select"
	| "action"
	| "goto-pending"
	| "goto-jump"
	| "search"
	| "overlay"
	| "sort"
	| "filter"
	| "spec"
	| "orchestrate"
	| "mergeSelect"
	| "*"

export type KeybindingDeps = CommandExecutor.CommandExecutor

export interface Keybinding {
	readonly key: string
	readonly mode: KeyMode | ReadonlyArray<KeyMode>
	readonly description: string
	readonly action: Effect.Effect<void, never, KeybindingDeps>
}
