import { Atom, Result } from "@effect-atom/atom"
import { Effect } from "effect"
import { deriveWaitingSessionOptions, TmuxSessionMonitor } from "../utils/runtimeServices.js"
import { currentProjectAtom, projectsAtom } from "./project.js"
import { appRuntime } from "./runtime.js"

export const tmuxSessionsAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const monitor = yield* TmuxSessionMonitor
		return monitor.sessions
	}),
)

export const waitingSessionOptionsAtom = Atom.readable((get) => {
	const sessionsResult = get(tmuxSessionsAtom)
	const projectsResult = get(projectsAtom)
	const currentProjectResult = get(currentProjectAtom)

	if (!Result.isSuccess(sessionsResult) || !Result.isSuccess(projectsResult)) {
		return []
	}

	const sessions = sessionsResult.value
	const projects = projectsResult.value
	const currentProject = Result.isSuccess(currentProjectResult)
		? currentProjectResult.value
		: undefined

	return deriveWaitingSessionOptions(sessions, projects, currentProject?.path)
})
