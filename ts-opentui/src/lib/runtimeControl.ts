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
