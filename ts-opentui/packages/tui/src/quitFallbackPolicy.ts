export interface DirectQuitFallbackContext {
	readonly key: string | undefined
	readonly ctrl: boolean
	readonly meta: boolean
	readonly shift: boolean
	readonly modeTag: string
	readonly hasOverlay: boolean
	readonly inDrillDown: boolean
}

/**
 * Direct quit fallback is only allowed when no overlay is visible.
 * Overlay-aware key handling should always take precedence.
 */
export const shouldRequestShutdownFromDirectQuitFallback = (
	context: DirectQuitFallbackContext,
): boolean =>
	!context.ctrl &&
	!context.meta &&
	!context.shift &&
	context.key === "q" &&
	context.modeTag === "normal" &&
	!context.hasOverlay &&
	!context.inDrillDown
