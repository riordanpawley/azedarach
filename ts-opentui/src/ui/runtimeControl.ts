/**
 * Runtime control bridge for TUI lifecycle commands originating outside React hooks.
 */

let shutdownHandler: (() => void) | null = null
let forcedExitTimer: ReturnType<typeof setTimeout> | null = null
const FORCE_EXIT_DELAY_MS = 150

export const registerShutdownHandler = (handler: () => void): void => {
	shutdownHandler = handler
}

export const clearShutdownHandler = (): void => {
	shutdownHandler = null
	if (forcedExitTimer !== null) {
		clearTimeout(forcedExitTimer)
		forcedExitTimer = null
	}
}

export const requestShutdown = (): void => {
	shutdownHandler?.()
	if (forcedExitTimer !== null || process.env.NODE_ENV === "test") return
	forcedExitTimer = setTimeout(() => {
		process.exit(0)
	}, FORCE_EXIT_DELAY_MS)
}
