import { DaemonRpcClient } from "@azedarach/shared/rpc"
import { Atom, Result } from "@effect-atom/atom"
import { Effect, Schedule, SubscriptionRef } from "effect"
import {
	deriveWaitingSessionOptions,
	toWaitingSessionSourcesFromDaemonSnapshot,
	type WaitingSessionSource,
} from "../lib/waitingSessions.js"
import { getTuiProjectContextRead } from "../services/TuiProjectContextService.js"
import { currentProjectAtom, projectsAtom } from "./project.js"
import { appRuntime } from "./runtime.js"

const EMPTY_WAITING_SESSION_SOURCES: readonly WaitingSessionSource[] = []
const WAITING_SESSION_SYNC_INTERVAL = "5 seconds"

const collectKnownProjectPaths = (
	projectPaths: readonly string[],
	currentProjectPath: string | undefined,
): readonly string[] => {
	const paths = new Set(projectPaths)
	if (currentProjectPath !== undefined) {
		paths.add(currentProjectPath)
	}
	return [...paths].sort((left, right) => left.localeCompare(right))
}

const refreshWaitingSessions = (
	ref: SubscriptionRef.SubscriptionRef<readonly WaitingSessionSource[]>,
) =>
	Effect.gen(function* () {
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		if (daemonRpcClient._tag === "None") {
			yield* SubscriptionRef.set(ref, EMPTY_WAITING_SESSION_SOURCES)
			return
		}

		const sessionSnapshot = daemonRpcClient.value.sessionSnapshot
		if (sessionSnapshot === undefined) {
			yield* SubscriptionRef.set(ref, EMPTY_WAITING_SESSION_SOURCES)
			return
		}

		const projectContext = yield* getTuiProjectContextRead
		const projects = yield* SubscriptionRef.get(projectContext.projects)
		const currentProjectPath = yield* projectContext.getCurrentPath()
		const projectPaths = collectKnownProjectPaths(
			projects.map((project) => project.path),
			currentProjectPath,
		)
		if (projectPaths.length === 0) {
			yield* SubscriptionRef.set(ref, EMPTY_WAITING_SESSION_SOURCES)
			return
		}

		const results = yield* Effect.forEach(
			projectPaths,
			(projectPath) => sessionSnapshot({ projectPath }),
			{ concurrency: 4 },
		)
		yield* SubscriptionRef.set(
			ref,
			results.flatMap((result) => toWaitingSessionSourcesFromDaemonSnapshot(result.sessions)),
		)
	})

const waitingSessionsRefAtom = appRuntime.atom(
	Effect.gen(function* () {
		const ref = yield* SubscriptionRef.make<readonly WaitingSessionSource[]>(
			EMPTY_WAITING_SESSION_SOURCES,
		)
		const sync = refreshWaitingSessions(ref).pipe(
			Effect.catchAll((error) =>
				Effect.logWarning(`waiting sessions daemon snapshot refresh failed: ${String(error)}`).pipe(
					Effect.asVoid,
				),
			),
		)
		yield* sync
		yield* Effect.scheduleForked(Schedule.spaced(WAITING_SESSION_SYNC_INTERVAL))(sync)
		return ref
	}),
	{ initialValue: undefined },
)

export const tmuxSessionsAtom = appRuntime.subscriptionRef((get) =>
	get.result(waitingSessionsRefAtom),
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
