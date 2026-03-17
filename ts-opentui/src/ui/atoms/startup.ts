import { Atom } from "@effect-atom/atom"
import { boardRenderStateAtom } from "./board.js"

export type StartupCapabilityState = {
	readonly board: "loading" | "ready" | "error"
	readonly sessionMonitor: "blocked" | "ready"
}

export const startupCapabilityStateAtom = Atom.readable<StartupCapabilityState>((get) => {
	const board = get(boardRenderStateAtom)
	return {
		board: board._tag,
		sessionMonitor: board._tag === "ready" ? "ready" : "blocked",
	}
})

export const sessionMonitorReadyAtom = Atom.readable((get) => {
	const startup = get(startupCapabilityStateAtom)
	return startup.sessionMonitor === "ready"
})
