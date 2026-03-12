/**
 * Tmux capability detection for UI/runtime gating.
 *
 * We intentionally derive capabilities from process context (TMUX env) rather
 * than probing tmux command availability. "Non-tmux mode" is a first-class UX
 * mode even when tmux is installed.
 */

export interface TmuxCapabilities {
	readonly inTmuxContext: boolean
	readonly tmuxActionsEnabled: boolean
}

const hasTmuxEnv = (): boolean => {
	const tmuxValue = process.env.TMUX
	if (tmuxValue === undefined) return false
	return tmuxValue.trim().length > 0
}

export const detectTmuxCapabilities = (): TmuxCapabilities => {
	const inTmuxContext = hasTmuxEnv()
	return {
		inTmuxContext,
		tmuxActionsEnabled: inTmuxContext,
	}
}
