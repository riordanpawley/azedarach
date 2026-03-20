import type { DaemonBoardTask, TrackedIssue } from "@azedarach/shared/rpc"

export interface BackendDaemonBoardProjectionInput {
	readonly issues: ReadonlyArray<TrackedIssue>
	readonly sessionsByIssueId: ReadonlyMap<
		string,
		{
			readonly state: DaemonBoardTask["sessionState"]
			readonly startedAt: string | null
			readonly tmuxSessionName: string
			readonly worktreePath: string | null
		}
	>
	readonly devServerIssueIds: ReadonlySet<string>
}

export type BackendDaemonBoardTaskSnapshot = DaemonBoardTask

const PR_URL_PATTERN = /PR:\s*(https:\/\/[^\s]+\/pull\/(\d+))/g

const parsePrInfo = (
	notes: string | undefined,
): {
	readonly hasPR?: true
	readonly prUrl?: string
	readonly prNumber?: number
} => {
	if (notes === undefined || notes.length === 0) {
		return {}
	}
	const matches = [...notes.matchAll(PR_URL_PATTERN)]
	const lastMatch = matches.at(-1)
	if (lastMatch === undefined) {
		return {}
	}
	const prUrl = lastMatch[1]
	const prNumberText = lastMatch[2]
	if (prUrl === undefined) {
		return {}
	}
	if (prNumberText === undefined) {
		return { hasPR: true, prUrl }
	}
	const prNumber = Number.parseInt(prNumberText, 10)
	return Number.isFinite(prNumber) ? { hasPR: true, prUrl, prNumber } : { hasPR: true, prUrl }
}

const resolveParentEpicId = (
	issue: TrackedIssue,
	issuesById: ReadonlyMap<string, TrackedIssue>,
): string | undefined => {
	const directParent = issue.dependencies?.find(
		(dependency) => dependency.dependency_type === "parent-child",
	)
	if (directParent === undefined) {
		return undefined
	}
	const parentIssue = issuesById.get(directParent.id)
	if (parentIssue?.issue_type === "epic") {
		return directParent.id
	}
	const grandparentRef = parentIssue?.dependencies?.find(
		(dependency) => dependency.dependency_type === "parent-child",
	)
	if (grandparentRef === undefined) {
		return undefined
	}
	const grandparentIssue = issuesById.get(grandparentRef.id)
	return grandparentIssue?.issue_type === "epic" ? grandparentRef.id : undefined
}

const toBoardTask = (params: {
	readonly issue: TrackedIssue
	readonly issuesById: ReadonlyMap<string, TrackedIssue>
	readonly sessionsByIssueId: BackendDaemonBoardProjectionInput["sessionsByIssueId"]
	readonly devServerIssueIds: ReadonlySet<string>
}): BackendDaemonBoardTaskSnapshot => {
	const session = params.sessionsByIssueId.get(params.issue.id)
	const sessionState = session?.state ?? "idle"
	const prInfo = parsePrInfo(params.issue.notes)
	return {
		id: params.issue.id,
		title: params.issue.title,
		description: params.issue.description,
		status: params.issue.status,
		priority: params.issue.priority,
		issue_type: params.issue.issue_type,
		created_at: params.issue.created_at,
		updated_at: params.issue.updated_at,
		closed_at: params.issue.closed_at,
		assignee: params.issue.assignee,
		labels: params.issue.labels,
		design: params.issue.design,
		notes: params.issue.notes,
		acceptance: params.issue.acceptance,
		estimate: params.issue.estimate,
		implementations: [...params.issue.implementations],
		dependent_count: params.issue.dependent_count,
		dependency_count: params.issue.dependency_count,
		sessionState,
		sessionStartedAt: session?.startedAt ?? undefined,
		hasTmuxSession: session === undefined ? undefined : true,
		hasWorktree: session?.worktreePath === null || session === undefined ? undefined : true,
		parentEpicId: resolveParentEpicId(params.issue, params.issuesById),
		hasDevServer: params.devServerIssueIds.has(params.issue.id) ? true : undefined,
		...prInfo,
	}
}

export const buildBoardTaskSnapshots = (
	input: BackendDaemonBoardProjectionInput,
): ReadonlyArray<BackendDaemonBoardTaskSnapshot> => {
	const issuesById = new Map(input.issues.map((issue) => [issue.id, issue] as const))
	return input.issues.map((issue) =>
		toBoardTask({
			issue,
			issuesById,
			sessionsByIssueId: input.sessionsByIssueId,
			devServerIssueIds: input.devServerIssueIds,
		}),
	)
}
