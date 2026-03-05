export const buildStartWorkPrompt = (params: {
	readonly taskId: string
	readonly issueType: string
	readonly title: string
	readonly hasWorktree: boolean
	readonly attachmentPaths: readonly string[]
}): string => {
	const showCommand = `az issue get ${params.taskId}`
	const updateCommand = `az issue update ${params.taskId} --design "..."`

	let prompt = `work on issue ${params.taskId} (${params.issueType}): ${params.title}

Run \`${showCommand}\` to see full description and context.

Before starting implementation:
1. If ANYTHING is unclear or underspecified, ASK ME questions before proceeding
2. Once you understand the task, update the issue with your implementation plan using \`${updateCommand}\`

Goal: Make this issue self-sufficient so any future session could pick it up without extra context.`

	if (params.hasWorktree) {
		prompt += `

NOTE: This worktree has existing work. Check:
- \`git status\` to see uncommitted changes
- \`git log --oneline -5\` to see recent commits
- Read the design notes on the issue for context from previous sessions`
	}

	if (params.attachmentPaths.length > 0) {
		prompt += `\n\nAttached images (use Read tool to view):\n${params.attachmentPaths.join("\n")}`
	}

	return prompt
}

export const buildChatPrompt = (params: {
	readonly taskId: string
	readonly title: string
	readonly chatModel: string
}): string => {
	const showCommand = `az issue get ${params.taskId}`

	return `Let's chat about issue ${params.taskId}: ${params.title}

Run \`${showCommand}\` to see full description and context.

Help me with one of:
- Clarifying requirements or scope
- Improving the description so any coding session could pick it up
- Breaking down into subtasks if too large
- Adding acceptance criteria
- Just chatting about the task or exploring ideas

Note: You're running with ${params.chatModel} for fast, cheap discussion. When ready to implement, use \`/model <model>\` to switch models.

What would you like to discuss?`
}
