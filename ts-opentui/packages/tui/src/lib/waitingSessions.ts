import type { Project } from "../contracts.js"

export interface WaitingSessionSource {
	readonly issueId: string
	readonly sessionName: string
	readonly projectPath: string | null
	readonly status: string
}

export interface WaitingSessionOption {
	readonly issueId: string
	readonly sessionName: string
	readonly projectPath: string | null
	readonly projectName: string
	readonly isCurrentProject: boolean
	readonly isRegisteredProject: boolean
}

const getProjectFallbackName = (projectPath: string | null): string => {
	if (!projectPath) {
		return "Unknown project"
	}

	const segments = projectPath.split(/[/\\]+/).filter((segment) => segment.length > 0)
	return segments[segments.length - 1] ?? projectPath
}

const compareBooleansDesc = (left: boolean, right: boolean): number => {
	if (left === right) return 0
	return left ? -1 : 1
}

export const deriveWaitingSessionOptions = (
	sessions: readonly WaitingSessionSource[],
	projects: readonly Project[],
	currentProjectPath: string | undefined,
): readonly WaitingSessionOption[] => {
	const projectByPath = new Map<string, Project>(projects.map((project) => [project.path, project]))

	return sessions
		.filter((session) => session.status === "waiting")
		.map((session) => {
			const project = session.projectPath ? projectByPath.get(session.projectPath) : undefined
			return {
				issueId: session.issueId,
				sessionName: session.sessionName,
				projectPath: session.projectPath,
				projectName: project?.name ?? getProjectFallbackName(session.projectPath),
				isCurrentProject:
					currentProjectPath !== undefined && session.projectPath === currentProjectPath,
				isRegisteredProject: project !== undefined,
			} satisfies WaitingSessionOption
		})
		.sort((left, right) => {
			const currentProjectDiff = compareBooleansDesc(left.isCurrentProject, right.isCurrentProject)
			if (currentProjectDiff !== 0) return currentProjectDiff

			const registeredDiff = compareBooleansDesc(
				left.isRegisteredProject,
				right.isRegisteredProject,
			)
			if (registeredDiff !== 0) return registeredDiff

			const projectDiff = left.projectName.localeCompare(right.projectName)
			if (projectDiff !== 0) return projectDiff

			const issueDiff = left.issueId.localeCompare(right.issueId)
			if (issueDiff !== 0) return issueDiff

			return left.sessionName.localeCompare(right.sessionName)
		})
}

export const deriveCurrentProjectWaitingIssueIds = (
	waitingSessions: readonly WaitingSessionOption[],
): readonly string[] => {
	const issueIds = new Set<string>()
	const orderedIssueIds: string[] = []

	for (const session of waitingSessions) {
		if (!session.isCurrentProject || issueIds.has(session.issueId)) {
			continue
		}

		issueIds.add(session.issueId)
		orderedIssueIds.push(session.issueId)
	}

	return orderedIssueIds
}
