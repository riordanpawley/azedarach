import { Effect, SubscriptionRef } from "effect"
import type { ResolvedConfig } from "../config/defaults.js"
import { AppConfig } from "../config/AppConfig.js"
import { IssueTrackerClient } from "../core/IssueTrackerClient.js"

const LINEAR_IDENTIFIER_PATTERN = /^([A-Za-z][A-Za-z0-9]*)-([0-9]+)$/
const NUMERIC_ISSUE_SUFFIX_PATTERN = /^[0-9]+$/
const LINEAR_PREFIX_PATTERN = /^[A-Za-z][A-Za-z0-9]*$/
const INFER_PREFIX_SAMPLE_LIMIT = 200

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

		const appConfig = yield* AppConfig
		const config = yield* SubscriptionRef.get(appConfig.config)
		const configuredPrefix = resolveConfiguredLinearPrefix(config)
		if (configuredPrefix) {
			return `${configuredPrefix}-${trimmedIssueId}`
		}

		const issueClient = yield* IssueTrackerClient
		const issueSample = yield* issueClient
			.list(undefined, projectPath, {
				includeClosed: true,
				limit: INFER_PREFIX_SAMPLE_LIMIT,
			})
			.pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed([])),
					),
				),
			)

		const inferredPrefix = inferLinearIssuePrefixFromIds(issueSample.map((issue) => issue.id))
		return inferredPrefix ? `${inferredPrefix}-${trimmedIssueId}` : trimmedIssueId
	})
