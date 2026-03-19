/**
 * Spec workspace atoms
 *
 * Handles loading and refreshing Spec workspace data.
 */

import { AppConfig } from "@azedarach/config"
import {
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type SpecPublishOutcome as DaemonSpecPublishOutcome,
} from "@azedarach/shared/rpc"
import { Data, DateTime, Effect, SubscriptionRef } from "effect"
import type {
	ImplementationRegistry,
	SpecCoverageReport,
	SpecParityReport,
	SpecPublishOutcome,
} from "../contracts.js"
import { DEFAULT_SPEC_PUBLISH_CONFIG } from "../contracts.js"
import { EditorService, ProjectService } from "../utils/runtimeServices.js"
import { appRuntime } from "./runtime.js"

const EMPTY_COVERAGE_REPORT: SpecCoverageReport = {
	requirements: [],
	unlinked_requirement_ids: [],
	fully_implemented_requirement_ids: [],
	partially_implemented_requirement_ids: [],
	integrity_gaps: [],
}

export interface SpecWorkspaceState {
	readonly isLoading: boolean
	readonly error: string | null
	readonly availableImplementations: readonly string[]
	readonly selectedImplementation: string | null
	readonly coverageReport: SpecCoverageReport
	readonly parityReport: SpecParityReport | null
	readonly publishConfig: typeof DEFAULT_SPEC_PUBLISH_CONFIG
	readonly lastPublishOutcome: SpecPublishOutcome | null
	readonly refreshedAt: string | null
}

export const DEFAULT_SPEC_WORKSPACE_STATE: SpecWorkspaceState = {
	isLoading: false,
	error: null,
	availableImplementations: [],
	selectedImplementation: null,
	coverageReport: EMPTY_COVERAGE_REPORT,
	parityReport: null,
	publishConfig: DEFAULT_SPEC_PUBLISH_CONFIG,
	lastPublishOutcome: null,
	refreshedAt: null,
}

const errorMessage = (error: unknown): string =>
	error instanceof Error ? error.message : String(error)

class TuiSpecRpcUnavailableError extends Data.TaggedError("TuiSpecRpcUnavailableError")<{
	readonly message: string
}> {}

const implementationNames = (registry: ImplementationRegistry): readonly string[] =>
	registry.implementations.map((implementation) => implementation.name)

export const resolveSelectedImplementation = (
	requestedImplementation: string | null,
	registry: ImplementationRegistry,
): string => {
	const availableImplementations = implementationNames(registry)
	if (
		requestedImplementation !== null &&
		availableImplementations.includes(requestedImplementation)
	) {
		return requestedImplementation
	}
	return registry.default_implementation
}

export const toTuiSpecPublishOutcome = (outcome: DaemonSpecPublishOutcome): SpecPublishOutcome => ({
	...outcome,
	started_at: DateTime.unsafeMake(outcome.started_at),
	finished_at: DateTime.unsafeMake(outcome.finished_at),
})

const getDaemonRpcClient = (): Effect.Effect<DaemonRpcClientApi, TuiSpecRpcUnavailableError> =>
	Effect.gen(function* () {
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		if (daemonRpcClient._tag === "None") {
			return yield* Effect.fail(
				new TuiSpecRpcUnavailableError({
					message: "Daemon RPC client is unavailable for the TUI spec workspace",
				}),
			)
		}
		return daemonRpcClient.value
	})

const loadSpecWorkspaceState = ({
	stateRef,
	daemonRpcClient,
	editor,
	projectPath,
	requestedImplementation,
}: {
	readonly stateRef: SubscriptionRef.SubscriptionRef<SpecWorkspaceState>
	readonly daemonRpcClient: DaemonRpcClientApi
	readonly editor: EditorService
	readonly projectPath: string
	readonly requestedImplementation: string | null
}) =>
	Effect.gen(function* () {
		yield* SubscriptionRef.update(stateRef, (state) => ({
			...state,
			isLoading: true,
			error: null,
		}))

		const registryResponse = yield* daemonRpcClient.implementationGetRegistry({
			projectPath,
		})
		const registry = registryResponse.registry
		const availableImplementations = implementationNames(registry)
		const selectedImplementation = resolveSelectedImplementation(requestedImplementation, registry)

		const [readResult, parityResult, publishConfigResult, publishOutcomeResult] = yield* Effect.all(
			[
				daemonRpcClient.specRead({
					projectPath,
				}),
				daemonRpcClient.specParity({
					implementation: selectedImplementation,
					projectPath,
				}),
				daemonRpcClient.specPublishConfigGet({
					projectPath,
				}),
				daemonRpcClient.specPublishOutcomeGet({
					projectPath,
				}),
			],
		)

		yield* editor.syncSpecImplementations(availableImplementations, selectedImplementation)

		yield* SubscriptionRef.set(stateRef, {
			isLoading: false,
			error: null,
			availableImplementations,
			selectedImplementation,
			coverageReport: readResult.coverage,
			parityReport: parityResult.report,
			publishConfig: publishConfigResult.config,
			lastPublishOutcome:
				publishOutcomeResult.last_outcome === null
					? null
					: toTuiSpecPublishOutcome(publishOutcomeResult.last_outcome),
			refreshedAt: new Date().toISOString(),
		})
	}).pipe(
		Effect.catchAll((error) =>
			Effect.gen(function* () {
				yield* SubscriptionRef.update(stateRef, (state) => ({
					...state,
					isLoading: false,
					error: errorMessage(error),
				}))
			}),
		),
	)

export const specWorkspaceStateRefAtom = appRuntime.atom(
	SubscriptionRef.make<SpecWorkspaceState>(DEFAULT_SPEC_WORKSPACE_STATE),
	{ initialValue: undefined },
)

export const specWorkspaceStateAtom = appRuntime.subscriptionRef((get) =>
	get.result(specWorkspaceStateRefAtom),
)

export const refreshSpecWorkspaceAtom = appRuntime.fn((_: undefined, get) =>
	Effect.gen(function* () {
		const appConfig = yield* AppConfig
		const editor = yield* EditorService
		const projectService = yield* ProjectService
		const stateRef = yield* get.result(specWorkspaceStateRefAtom)
		const specConfig = yield* appConfig.getSpecConfig()
		if (!specConfig.enabled) {
			yield* SubscriptionRef.set(stateRef, DEFAULT_SPEC_WORKSPACE_STATE)
			return
		}
		const daemonRpcClient = yield* getDaemonRpcClient()
		const selectedImplementation = yield* editor.getSpecSelectedImplementation()
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

		yield* loadSpecWorkspaceState({
			stateRef,
			daemonRpcClient,
			editor,
			projectPath,
			requestedImplementation: selectedImplementation,
		})
	}),
)
