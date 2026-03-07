/**
 * Spec workspace atoms
 *
 * Handles loading and refreshing Spec workspace data.
 */

import { Effect, SubscriptionRef } from "effect"
import { SpecService } from "../../core/SpecService.js"
import type { SpecCoverageReport, SpecPublishOutcome } from "../../core/specTypes.js"
import { DEFAULT_SPEC_PUBLISH_CONFIG } from "../../core/specTypes.js"
import { appRuntime } from "./runtime.js"

const EMPTY_COVERAGE_REPORT: SpecCoverageReport = {
    requirements: [],
    unlinked_requirement_ids: [],
    integrity_gaps: [],
}

export interface SpecWorkspaceState {
    readonly isLoading: boolean
    readonly error: string | null
    readonly coverageReport: SpecCoverageReport
    readonly publishConfig: typeof DEFAULT_SPEC_PUBLISH_CONFIG
    readonly lastPublishOutcome: SpecPublishOutcome | null
    readonly refreshedAt: string | null
}

export const DEFAULT_SPEC_WORKSPACE_STATE: SpecWorkspaceState = {
    isLoading: false,
    error: null,
    coverageReport: EMPTY_COVERAGE_REPORT,
    publishConfig: DEFAULT_SPEC_PUBLISH_CONFIG,
    lastPublishOutcome: null,
    refreshedAt: null,
}

const errorMessage = (error: unknown): string =>
    error instanceof Error ? error.message : String(error)

export const specWorkspaceStateRefAtom = appRuntime.atom(
    SubscriptionRef.make<SpecWorkspaceState>(DEFAULT_SPEC_WORKSPACE_STATE),
    { initialValue: undefined },
)

export const specWorkspaceStateAtom = appRuntime.subscriptionRef((get) =>
    get.result(specWorkspaceStateRefAtom),
)

export const refreshSpecWorkspaceAtom = appRuntime.fn((_: undefined, get) =>
    Effect.gen(function* () {
        const spec = yield* SpecService
        const stateRef = yield* get.result(specWorkspaceStateRefAtom)

        yield* SubscriptionRef.update(stateRef, (state) => ({
            ...state,
            isLoading: true,
            error: null,
        }))

        const [coverageReport, publishConfig, lastPublishOutcome] = yield* Effect.all([
            spec.getCoverageReport(),
            spec.getPublishConfig(),
            spec.getLastPublishOutcome(),
        ])

        yield* SubscriptionRef.set(stateRef, {
            isLoading: false,
            error: null,
            coverageReport,
            publishConfig,
            lastPublishOutcome: lastPublishOutcome ?? null,
            refreshedAt: new Date().toISOString(),
        })
    }).pipe(
        Effect.catchAll((error) =>
            Effect.gen(function* () {
                const stateRef = yield* get.result(specWorkspaceStateRefAtom)
                yield* SubscriptionRef.update(stateRef, (state) => ({
                    ...state,
                    isLoading: false,
                    error: errorMessage(error),
                }))
            }),
        ),
    ),
)
