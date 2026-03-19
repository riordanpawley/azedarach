import { Effect } from "effect"
import type { ImplementationRegistry } from "../contracts.js"
import { IssueTrackerClient } from "./runtimeServices.js"

const normalizeImplementationName = (value: string): string => value.trim().toLowerCase()

const normalizeImplementationList = (
	implementations: readonly string[] | undefined,
): readonly string[] | undefined => {
	if (implementations === undefined) {
		return undefined
	}

	const seen = new Set<string>()
	const normalized: string[] = []
	for (const implementation of implementations) {
		const value = normalizeImplementationName(implementation)
		if (value.length === 0 || seen.has(value)) {
			continue
		}
		seen.add(value)
		normalized.push(value)
	}

	return normalized.length > 0 ? normalized : undefined
}

const hasImplementation = (registry: ImplementationRegistry, implementation: string): boolean =>
	registry.implementations.some((entry) => entry.name === implementation)

export const resolveIssueEditorDefaultImplementation = (
	registry: ImplementationRegistry,
	configuredDefaultImplementation: string | undefined,
): string => {
	const normalizedConfigured = configuredDefaultImplementation
		? normalizeImplementationName(configuredDefaultImplementation)
		: undefined

	if (
		normalizedConfigured !== undefined &&
		normalizedConfigured.length > 0 &&
		hasImplementation(registry, normalizedConfigured)
	) {
		return normalizedConfigured
	}

	return registry.default_implementation
}

export const resolveIssueCreateImplementations = (
	registry: ImplementationRegistry,
	options?: {
		readonly requestedImplementations?: readonly string[]
		readonly configuredDefaultImplementation?: string
	},
): readonly string[] => {
	const requestedImplementations = normalizeImplementationList(options?.requestedImplementations)
	if (requestedImplementations !== undefined) {
		return requestedImplementations
	}

	return [
		resolveIssueEditorDefaultImplementation(registry, options?.configuredDefaultImplementation),
	] as const
}

export const getIssueCreateImplementations = (options?: {
	readonly requestedImplementations?: readonly string[]
	readonly configuredDefaultImplementation?: string
	readonly cwd?: string
}) =>
	Effect.gen(function* () {
		const issueTrackerClient = yield* IssueTrackerClient
		const registry = yield* issueTrackerClient.getImplementationRegistry(options?.cwd)
		return resolveIssueCreateImplementations(registry, options)
	})
