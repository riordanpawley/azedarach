import type { CleanupOptions } from "../core/PRWorkflow.js"

const ACTION_PALETTE_NETWORK_ACTIONS = new Set(["P", "m", "O"])

export const createWorktreeOnlyCleanupOptions = (
	issueId: string,
	projectPath: string,
): CleanupOptions => ({
	issueId,
	projectPath,
	closeIssue: false,
})

export const isActionPaletteNetworkAction = (action: string): boolean =>
	ACTION_PALETTE_NETWORK_ACTIONS.has(action)
