const MAX_PROMPT_TITLE_LENGTH = 160

export const buildStartWorkPrompt = (params: {
	readonly taskId: string
	readonly issueType: string
	readonly title: string
}): string => {
	const safeIssueType = sanitizePromptInline(params.issueType)
	const safeTitle = sanitizePromptInline(params.title, MAX_PROMPT_TITLE_LENGTH)

	return `work on issue ${params.taskId} (${safeIssueType}): ${safeTitle}

Start by running \`az prime\`. Then continue the task using the context it prints without waiting for further instruction.`
}

const sanitizePromptInline = (value: string, maxLength?: number): string => {
	const normalized = value
		.replace(/\p{Cc}/gu, " ")
		.replace(/\s+/g, " ")
		.trim()
	const safe = normalized.replace(/</g, "[").replace(/>/g, "]")
	if (maxLength === undefined || safe.length <= maxLength) {
		return safe
	}

	return `${safe.slice(0, Math.max(0, maxLength - 3)).trimEnd()}...`
}
