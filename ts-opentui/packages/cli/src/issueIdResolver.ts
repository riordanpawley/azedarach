import type { GlobalDaemonBootstrap } from "@azedarach/daemon-control"
import { Effect } from "effect"
import type { ResolvedConfig } from "./contracts.js"

const LINEAR_IDENTIFIER_PATTERN = /^([A-Za-z][A-Za-z0-9]*)-([0-9]+)$/
const NUMERIC_ISSUE_SUFFIX_PATTERN = /^[0-9]+$/
const LINEAR_PREFIX_PATTERN = /^[A-Za-z][A-Za-z0-9]*$/
export const INFER_PREFIX_SAMPLE_LIMIT = 200

export interface CliIssueIdResolutionContext {
	readonly getConfig: Effect.Effect<ResolvedConfig, unknown, never>
	readonly listIssueIds: (
		projectPath: string,
	) => Effect.Effect<ReadonlyArray<string>, unknown, GlobalDaemonBootstrap>
}

let cliIssueIdResolutionContext: CliIssueIdResolutionContext | undefined

export const configureCliIssueIdResolutionContext = (
	context: CliIssueIdResolutionContext,
): void => {
	cliIssueIdResolutionContext = context
}

const parseLinearIdentifier = (
	issueId: string,
): { readonly prefix: string; readonly suffix: string } | undefined => {
	const match = LINEAR_IDENTIFIER_PATTERN.exec(issueId.trim())
	if (!match) {
		return undefined
	}

	return {
		prefix: match[1]!.toUpperCase(),
		suffix: match[2]!,
	}
}

const resolveConfiguredLinearPrefix = (config: ResolvedConfig): string | undefined => {
	if (!("linear" in config.issueTracker)) {
		return undefined
	}

	const team = config.issueTracker.linear.team?.trim()
	if (!team || !LINEAR_PREFIX_PATTERN.test(team)) {
		return undefined
	}

	return team.toUpperCase()
}

/**
 * Infer the dominant Linear issue prefix from issue identifiers.
 *
 * Returns undefined when there are no Linear identifiers, or if the
 * highest-frequency prefix is tied with another prefix.
 */
export const inferLinearIssuePrefixFromIds = (issueIds: readonly string[]): string | undefined => {
	const counts = new Map<string, number>()

	for (const issueId of issueIds) {
		const parsed = parseLinearIdentifier(issueId)
		if (!parsed) {
			continue
		}
		counts.set(parsed.prefix, (counts.get(parsed.prefix) ?? 0) + 1)
	}

	if (counts.size === 0) {
		return undefined
	}

	let winningPrefix: string | undefined
	let winningCount = 0
	let isTie = false

	for (const [prefix, count] of counts.entries()) {
		if (count > winningCount) {
			winningPrefix = prefix
			winningCount = count
			isTie = false
			continue
		}
		if (count === winningCount) {
			isTie = true
		}
	}

	if (isTie) {
		return undefined
	}

	return winningPrefix
}

/**
 * Resolve CLI issue identifiers.
 *
 * Supports shorthand numeric suffixes (for example `123`) by inferring a
 * Linear prefix from config (`issueTracker.linear.team`) or, when missing,
 * from existing project issue IDs.
 */
export const resolveCliIssueId = (rawIssueId: string, projectPath: string) =>
	Effect.gen(function* () {
		const trimmedIssueId = rawIssueId.trim()
		if (!NUMERIC_ISSUE_SUFFIX_PATTERN.test(trimmedIssueId)) {
			return trimmedIssueId
		}

		const context = cliIssueIdResolutionContext
		if (context === undefined) {
			return yield* Effect.die(new Error("CLI issue ID resolver context has not been configured"))
		}

		const config = yield* context.getConfig
		const configuredPrefix = resolveConfiguredLinearPrefix(config)
		if (configuredPrefix) {
			return `${configuredPrefix}-${trimmedIssueId}`
		}

		const issueIds = yield* context.listIssueIds(projectPath)
		const inferredPrefix = inferLinearIssuePrefixFromIds(issueIds)
		return inferredPrefix ? `${inferredPrefix}-${trimmedIssueId}` : trimmedIssueId
	})
