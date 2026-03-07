export const buildStartWorkPrompt = (params: {
	readonly taskId: string
	readonly issueType: string
	readonly title: string
	readonly hasWorktree: boolean
	readonly attachmentPaths: readonly string[]
	readonly localMode: boolean
}): string => {
	const safeIssueType = sanitizePromptInline(params.issueType)
	const safeTitle = sanitizePromptInline(params.title)
	const showCommand = `az issue get ${params.taskId}`
	const updateCommand = `az issue update ${params.taskId} --design "..."`

	let prompt = `work on issue ${params.taskId} (${safeIssueType}): ${safeTitle}

Context for this session is already injected (\`az prime\` + \`${showCommand}\`).
Only rerun \`${showCommand}\` if details are stale or missing.

Before starting implementation:
1. If ANYTHING is unclear or underspecified, ASK ME questions before proceeding
2. Once you understand the task, update the issue with your implementation plan using \`${updateCommand}\`

Goal: Make this issue self-sufficient so any future session could pick it up without extra context.`

	prompt += `

Issue nesting rule:
- If additional work must be completed before closing \`${params.taskId}\`, create it as a child of \`${params.taskId}\` (for example, \`az issue update [new-id] --parent ${params.taskId}\`).
- If additional work is intentionally deferred to a later session (not required to close \`${params.taskId}\`), do NOT make it a child; link it with a discovered-from edge instead (for example, \`az issue dep add --type discovered-from [new-id] ${params.taskId}\`).
- Close \`${params.taskId}\` only after its child issues are completed.`

	if (params.localMode) {
		prompt += `

Local workflow mode guardrails:
- Use plain \`git\` commands in this worktree.
- Do not use \`git -C [path]\` unless intentionally targeting a different repository/path.
- Do not run remote cleanup/sync commands unless explicitly asked.`
	}

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
	const safeTitle = sanitizePromptInline(params.title)
	const showCommand = `az issue get ${params.taskId}`

	return `Let's chat about issue ${params.taskId}: ${safeTitle}

Context for this session is already injected (\`az prime\` + \`${showCommand}\`).
Only rerun \`${showCommand}\` if details are stale or missing.

Help me with one of:
- Clarifying requirements or scope
- Improving the description so any coding session could pick it up
- Breaking down into subtasks if too large
- Adding acceptance criteria
- Just chatting about the task or exploring ideas

Note: You're running with ${params.chatModel} for fast, cheap discussion. When ready to implement, use \`/model [model]\` to switch models.

What would you like to discuss?`
}

const sanitizePromptInline = (value: string): string => {
	const normalized = value.replace(/[\u0000-\u001f\u007f]/g, " ").replace(/\s+/g, " ").trim()
	return normalized.replace(/</g, "[").replace(/>/g, "]")
}
