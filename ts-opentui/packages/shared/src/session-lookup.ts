import { issueIdsEqualForLookup, parseIssueSessionName } from "./session-names.js"

export interface IssueSessionCandidate {
	readonly issueId: string
	readonly sessionName: string
}

export const buildIssueSessionCandidatesFromSnapshots = (
	snapshots: ReadonlyArray<{
		readonly issueId: string
		readonly tmuxSessionName: string
	}>,
): ReadonlyArray<IssueSessionCandidate> =>
	snapshots.map((snapshot) => ({
		issueId: snapshot.issueId,
		sessionName: snapshot.tmuxSessionName,
	}))

export const buildIssueSessionCandidatesFromSessionNames = (
	sessionNames: ReadonlyArray<string>,
	projectPath?: string,
): ReadonlyArray<IssueSessionCandidate> => {
	const candidates: IssueSessionCandidate[] = []

	for (const sessionName of sessionNames) {
		const parsed = parseIssueSessionName(sessionName, projectPath)
		if (parsed?.type === "issue") {
			candidates.push({
				issueId: parsed.issueId,
				sessionName,
			})
		}
	}

	return candidates
}

export const findIssueSessionNameByIssueId = (
	issueId: string,
	candidates: ReadonlyArray<IssueSessionCandidate>,
): string | null => {
	for (const candidate of candidates) {
		if (issueIdsEqualForLookup(candidate.issueId, issueId)) {
			return candidate.sessionName
		}
	}

	return null
}
