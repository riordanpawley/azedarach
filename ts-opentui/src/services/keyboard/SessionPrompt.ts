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

    let prompt = `work on issue ${params.taskId} (${safeIssueType}): ${safeTitle}

Start by running \`az prime\`.
\`AZEDARACH_ISSUE_ID\` is already set for this session, so \`az prime\` should include issue-specific context.
If context looks stale, refresh with \`${showCommand}\`.`

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

const sanitizePromptInline = (value: string): string => {
    const normalized = value.replace(/[\u0000-\u001f\u007f]/g, " ").replace(/\s+/g, " ").trim()
    return normalized.replace(/</g, "[").replace(/>/g, "]")
}
