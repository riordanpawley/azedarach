import type { TmuxCapabilities } from "../contracts.js"

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
