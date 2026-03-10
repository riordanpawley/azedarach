import type { SessionState } from "../ui/types.js"

export type ForegroundKind = "agent" | "shell" | "subprocess" | "unknown"

export const shouldApplyHighPriorityDetectedState = (params: {
	readonly currentState: SessionState
	readonly detectedState: "waiting" | "error"
	readonly foregroundKind: ForegroundKind
}): boolean => {
	if (params.detectedState === "waiting") {
		return params.currentState === "initializing" || params.currentState === "busy"
	}

	// Keep explicit command/test failures from forcing a running agent or one of
	// its child processes into a terminal error state mid-flight.
	if (params.foregroundKind === "agent" || params.foregroundKind === "subprocess") {
		return false
	}

	return (
		params.currentState === "initializing" ||
		params.currentState === "busy" ||
		params.currentState === "waiting"
	)
}

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
