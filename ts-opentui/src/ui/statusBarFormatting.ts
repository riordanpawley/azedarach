export const formatWaitingSummary = (
	waitingIssueIds: ReadonlyArray<string>,
	terminalWidth: number,
): string | null => {
	if (waitingIssueIds.length === 0) {
		return null
	}

	if (terminalWidth < 90) {
		return `Wait: ${waitingIssueIds.length}`
	}

	if (waitingIssueIds.length === 1) {
		return `Wait: ${waitingIssueIds[0]}`
	}

	if (terminalWidth < 120) {
		return `Wait: ${waitingIssueIds[0]} +${waitingIssueIds.length - 1}`
	}

	const visibleIds = waitingIssueIds.slice(0, 2)
	const remaining = waitingIssueIds.length - visibleIds.length

	return remaining > 0
		? `Wait: ${visibleIds.join(", ")} +${remaining}`
		: `Wait: ${visibleIds.join(", ")}`
}
