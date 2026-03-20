import { Effect, type SubscriptionRef } from "effect"
import { type Project, ProjectService } from "../utils/runtimeServices.js"

export interface TuiProjectContextReadApi {
	readonly currentProject: SubscriptionRef.SubscriptionRef<Project | undefined>
	readonly projects: SubscriptionRef.SubscriptionRef<ReadonlyArray<Project>>
	readonly getCurrentPath: () => Effect.Effect<string | undefined>
}

type LegacyProjectReadAuthority = {
	readonly currentProject: SubscriptionRef.SubscriptionRef<Project | undefined>
	readonly projects: SubscriptionRef.SubscriptionRef<ReadonlyArray<Project>>
	readonly getCurrentPath: () => Effect.Effect<string | undefined>
}

export const createTuiProjectContextReadFacade = (
	authority: LegacyProjectReadAuthority,
): TuiProjectContextReadApi => ({
	currentProject: authority.currentProject,
	projects: authority.projects,
	getCurrentPath: authority.getCurrentPath,
})

export const getTuiProjectContextRead = Effect.gen(function* () {
	const authority = yield* ProjectService
	return createTuiProjectContextReadFacade(authority)
})

export const getTuiCurrentProjectRef = getTuiProjectContextRead.pipe(
	Effect.map((context) => context.currentProject),
)

export const getTuiProjectsRef = getTuiProjectContextRead.pipe(
	Effect.map((context) => context.projects),
)
