import type { Issue } from "../core/IssueTrackerClient.js"
import type { AgentPhase, SessionState } from "../core/StateDetector.js"

export type PRState = "open" | "draft" | "merged" | "closed"

export interface SessionMetrics {
	readonly contextPercent?: number
	readonly sessionStartedAt?: string
	readonly estimatedTokens?: number
	readonly lastCompactedAt?: string
	readonly recentOutput?: string
	readonly agentPhase?: AgentPhase
}

export interface GitStatus {
	readonly gitBehindCount?: number
	readonly hasUncommittedChanges?: boolean
	readonly gitAdditions?: number
	readonly gitDeletions?: number
}

export interface PRInfo {
	readonly hasPR: boolean
	readonly prUrl?: string
	readonly prNumber?: number
	readonly prState?: PRState
}

export const parsePRInfo = (notes: string | undefined): Partial<PRInfo> => {
	if (!notes) return {}

	const matches = [...notes.matchAll(/PR:\s*(https:\/\/[^\s]+\/pull\/\d+)/g)]
	if (matches.length === 0) return {}

	const lastMatch = matches[matches.length - 1]
	const prUrl = lastMatch?.[1]
	const prNumberMatch = prUrl?.match(/\/pull\/(\d+)/)
	const prNumber = prNumberMatch ? Number.parseInt(prNumberMatch[1], 10) : undefined

	return {
		hasPR: true,
		prUrl,
		prNumber,
	}
}

export interface TaskWithSession extends Issue, SessionMetrics, GitStatus, Partial<PRInfo> {
	readonly sessionState: SessionState
	readonly hasTmuxSession?: boolean
	readonly hasWorktree?: boolean
	readonly hasMergeConflict?: boolean
	readonly hasDevServer?: boolean
	readonly parentEpicId?: string
}

export const hasTaskSessionPresence = (
	task: Pick<TaskWithSession, "sessionState" | "hasTmuxSession">,
): boolean => task.sessionState !== "idle" || task.hasTmuxSession === true

export const hasTaskWorktreeContext = (
	task: Pick<TaskWithSession, "sessionState" | "hasTmuxSession" | "hasWorktree">,
): boolean => task.hasWorktree === true || hasTaskSessionPresence(task)

export const COLUMNS = [
	{ id: "open", title: "Open", status: "open" },
	{ id: "in_progress", title: "In Progress", status: "in_progress" },
	{ id: "blocked", title: "Blocked", status: "blocked" },
	{ id: "closed", title: "Closed", status: "closed" },
] as const

export type ColumnId = (typeof COLUMNS)[number]["id"]
export type ColumnStatus = (typeof COLUMNS)[number]["status"]

export const DEFAULT_JUMP_LABEL_CHARS = "asdfjkl;"

export function generateJumpLabels(count: number, chars = DEFAULT_JUMP_LABEL_CHARS): string[] {
	const labels: string[] = []

	for (let i = 0; i < chars.length && labels.length < count; i++) {
		for (let j = 0; j < chars.length && labels.length < count; j++) {
			labels.push(chars[i] + chars[j])
		}
	}

	return labels
}
