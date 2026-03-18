/**
 * Runtime control bridge for TUI lifecycle commands originating outside React hooks.
 */

let shutdownHandler: (() => void) | null = null

export const registerShutdownHandler = (handler: () => void): void => {
	shutdownHandler = handler
}

export const clearShutdownHandler = (): void => {
	shutdownHandler = null
}

export const requestShutdown = (): void => {
	shutdownHandler?.()
}
