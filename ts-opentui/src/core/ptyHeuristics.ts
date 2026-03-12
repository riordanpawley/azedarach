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
	readonly detectedState: SessionState | null
}): SessionState | null => {
	if (params.foregroundKind !== "shell") {
		return null
	}

	if (params.currentState !== "busy" && params.currentState !== "initializing") {
		return null
	}

	// Treat a shell bell edge as "waiting" only when PTY pattern matching also
	// identified a waiting prompt in the current capture.
	if (params.bellFlag && !params.previousBellFlag && params.detectedState === "waiting") {
		return "waiting"
	}

	return "done"
}
