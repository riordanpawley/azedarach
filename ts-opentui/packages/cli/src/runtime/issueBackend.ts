export type ConfiguredIssueBackend = "tracker" | "legacy" | "local" | "linear"

export interface IssueTrackerBackendConfigShape {
	readonly tracker?: unknown
	readonly legacy?: unknown
	readonly linear?: unknown
	readonly local?: unknown
}

export const resolveConfiguredIssueBackend = (
	issueTracker: IssueTrackerBackendConfigShape,
): ConfiguredIssueBackend => {
	if (issueTracker.tracker !== undefined) return "tracker"
	if (issueTracker.legacy !== undefined) return "legacy"
	if (issueTracker.linear !== undefined) return "linear"
	return "local"
}
