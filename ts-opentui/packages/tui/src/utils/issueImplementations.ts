import { Effect } from "effect"
import type { ImplementationRegistry } from "../contracts.js"
import { getImplementationRegistryFromDaemon } from "./daemonRpc.js"

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

export const parseIssueImplementations = (value: string): readonly string[] | undefined =>
	normalizeImplementationList(value.split(","))

export const formatIssueImplementations = (
	implementations: readonly string[] | undefined,
): string => implementations?.join(", ") ?? ""

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
	]
}

export const getIssueCreateImplementations = (options?: {
	readonly requestedImplementations?: readonly string[]
	readonly configuredDefaultImplementation?: string
	readonly cwd?: string
}) =>
	getImplementationRegistryFromDaemon(options?.cwd).pipe(
		Effect.map((registry) => resolveIssueCreateImplementations(registry, options)),
	)
