import type { SessionState } from "../ui/types.js"

export type ForegroundKind = "agent" | "shell" | "subprocess" | "unknown"

export const deriveShellForegroundState = (params: {
	readonly currentState: SessionState
	readonly foregroundKind: ForegroundKind
	readonly bellFlag: boolean
	readonly previousBellFlag: boolean
}): SessionState | null => {
	if (params.foregroundKind !== "shell") {
		return null
	}

	if (params.currentState !== "busy" && params.currentState !== "initializing") {
		return null
	}

	// Codex often returns to a shell prompt and rings the pane bell when it needs
	// the user. Treat a fresh bell edge as "waiting" instead of generic "done".
	if (params.bellFlag && !params.previousBellFlag) {
		return "waiting"
	}

	return "done"
}
