import type { CommandExecutor } from "@effect/platform"
import { Data, Effect, type Scope } from "effect"

export interface KeyboardServiceApi {
	readonly handleKey: (
		key: string,
	) => Effect.Effect<void, unknown, CommandExecutor.CommandExecutor | Scope.Scope>
}

export class TuiKeyboardServiceNotConfiguredError extends Data.TaggedError(
	"TuiKeyboardServiceNotConfiguredError",
)<{
	readonly message: string
}> {}

let configuredTuiKeyboardService: KeyboardServiceApi | undefined

export const configureTuiKeyboardService = (keyboardService: KeyboardServiceApi): void => {
	configuredTuiKeyboardService = keyboardService
}

export class KeyboardService extends Effect.Service<KeyboardService>()("KeyboardService", {
	effect: Effect.gen(function* () {
		if (configuredTuiKeyboardService === undefined) {
			return yield* Effect.fail(
				new TuiKeyboardServiceNotConfiguredError({
					message: "TUI keyboard service must be configured before launch",
				}),
			)
		}

		return configuredTuiKeyboardService
	}),
}) {}
