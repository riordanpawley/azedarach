/**
 * Spec workspace atoms
 *
 * Handles loading and refreshing Spec workspace data.
 */

import { Effect, SubscriptionRef } from "effect"
import { AppConfig } from "../../config/index.js"
import { SpecService } from "../../core/SpecService.js"
import type {
	SpecCoverageReport,
	SpecParityReport,
	SpecPublishOutcome,
} from "../../core/specTypes.js"
import { DEFAULT_SPEC_PUBLISH_CONFIG } from "../../core/specTypes.js"
import type { DaemonImplementationRegistryResult } from "../../rpc/DaemonRpcSchemas.js"
import { EditorService } from "../../services/EditorService.js"
import {
	getRequiredDaemonDomainRpcClients,
	type RequiredDaemonIssueTaskRpcClient,
} from "../../services/RequiredDaemonDomainRpcClient.js"
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

type DaemonImplementationRegistry = DaemonImplementationRegistryResult["registry"]

const implementationNames = (registry: DaemonImplementationRegistry): readonly string[] =>
	registry.implementations.map((implementation) => implementation.name)

const resolveSelectedImplementation = (
	requestedImplementation: string | null,
	registry: DaemonImplementationRegistry,
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

const loadSpecWorkspaceState = ({
	stateRef,
	spec,
	issueTaskRpcClient,
	editor,
	requestedImplementation,
}: {
	readonly stateRef: SubscriptionRef.SubscriptionRef<SpecWorkspaceState>
	readonly spec: SpecService
	readonly issueTaskRpcClient: RequiredDaemonIssueTaskRpcClient
	readonly editor: EditorService
	readonly requestedImplementation: string | null
}) =>
	Effect.gen(function* () {
		yield* SubscriptionRef.update(stateRef, (state) => ({
			...state,
			isLoading: true,
			error: null,
		}))

		const registry = (yield* issueTaskRpcClient.issueImplementationRegistry()).registry
		const availableImplementations = implementationNames(registry)
		const selectedImplementation = resolveSelectedImplementation(requestedImplementation, registry)

		const [coverageReport, parityReport, publishConfig, lastPublishOutcome] = yield* Effect.all([
			spec.getCoverageReport(),
			spec.getParityReport(selectedImplementation),
			spec.getPublishConfig(),
			spec.getLastPublishOutcome(),
		])

		yield* editor.syncSpecImplementations(availableImplementations, selectedImplementation)

		yield* SubscriptionRef.set(stateRef, {
			isLoading: false,
			error: null,
			availableImplementations,
			selectedImplementation,
			coverageReport,
			parityReport,
			publishConfig,
			lastPublishOutcome: lastPublishOutcome ?? null,
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
		const spec = yield* SpecService
		const daemonRpcDomains = yield* getRequiredDaemonDomainRpcClients()
		const editor = yield* EditorService
		const stateRef = yield* get.result(specWorkspaceStateRefAtom)
		const specConfig = yield* appConfig.getSpecConfig()
		if (!specConfig.enabled) {
			yield* SubscriptionRef.set(stateRef, DEFAULT_SPEC_WORKSPACE_STATE)
			return
		}
		const selectedImplementation = yield* editor.getSpecSelectedImplementation()

		yield* loadSpecWorkspaceState({
			stateRef,
			spec,
			issueTaskRpcClient: daemonRpcDomains.issueTask,
			editor,
			requestedImplementation: selectedImplementation,
		})
	}),
)
