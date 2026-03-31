import type { Issue } from "./IssueTrackerClient.js"

export interface PRDraftContext {
	readonly baseBranch: string
	readonly commitSubjects?: readonly string[]
	readonly changedFiles?: readonly string[]
}

type PRIssueInput = Pick<Issue, "id" | "title" | "description" | "design"> & {
	readonly issue_type?: Issue["issue_type"]
}

const AUTO_CLOSE_FOOTER_LABEL = "Closes"
const AUTO_CLOSE_FOOTER_KEYWORDS = ["Closes", "Resolves", "Fixes"] as const

const escapeRegExp = (input: string): string => input.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")

const normalizeBody = (input: string): string => input.replace(/\r\n/g, "\n").trimEnd()

const hasAutoCloseFooterForIssue = (body: string, issueId: string): boolean => {
	const issueIdPattern = escapeRegExp(issueId)
	const keywordPattern = AUTO_CLOSE_FOOTER_KEYWORDS.join("|")
	const lines = body
		.split("\n")
		.map((line) => line.trim())
		.filter((line) => line.length > 0)
	const footerLine = lines[lines.length - 1]
	if (footerLine === undefined) {
		return false
	}

	const footerPattern = new RegExp(`^(?:${keywordPattern})\\s+${issueIdPattern}(?:\\b|$)$`, "i")
	return footerPattern.test(footerLine)
}

export const appendLinkedIssueAutoCloseFooter = (body: string, issueId: string): string => {
	const normalizedBody = normalizeBody(body)
	if (normalizedBody.length === 0) {
		return `${AUTO_CLOSE_FOOTER_LABEL} ${issueId}`
	}
	if (hasAutoCloseFooterForIssue(normalizedBody, issueId)) {
		return normalizedBody
	}

	return `${normalizedBody}\n\n${AUTO_CLOSE_FOOTER_LABEL} ${issueId}`
}

export const buildIssuePRTitle = (issue: Pick<Issue, "id" | "title" | "issue_type">): string => {
	const typePrefix = issue.issue_type ? `[${issue.issue_type}] ` : ""
	return `${typePrefix}${issue.title} (${issue.id})`
}

const toNonEmptyLines = (input: string): readonly string[] =>
	input
		.split("\n")
		.map((line) => line.trim())
		.filter((line) => line.length > 0)

const limitWithOverflow = (
	items: readonly string[],
	limit: number,
): { readonly visible: readonly string[]; readonly overflowCount: number } => ({
	visible: items.slice(0, limit),
	overflowCount: Math.max(0, items.length - limit),
})

export const buildIssuePRBody = (issue: PRIssueInput, draftContext?: PRDraftContext): string => {
	const lines: string[] = []

	lines.push(`## Summary`)
	lines.push(``)
	lines.push(`Resolves ${issue.id}: ${issue.title}`)
	if (draftContext) {
		lines.push(`Base branch: \`${draftContext.baseBranch}\``)
	}
	lines.push(``)

	if (issue.description) {
		lines.push(`## Description`)
		lines.push(``)
		lines.push(issue.description)
		lines.push(``)
	}

	if (issue.design) {
		lines.push(`## Design Notes`)
		lines.push(``)
		lines.push(issue.design)
		lines.push(``)
	}

	const commitSubjects = draftContext?.commitSubjects ?? []
	if (commitSubjects.length > 0) {
		const { visible, overflowCount } = limitWithOverflow(commitSubjects, 8)
		lines.push(`## What Changed`)
		lines.push(``)
		for (const subject of visible) {
			lines.push(`- ${subject}`)
		}
		if (overflowCount > 0) {
			lines.push(`- ...and ${overflowCount} more commit${overflowCount === 1 ? "" : "s"}`)
		}
		lines.push(``)
	}

	const changedFiles = draftContext?.changedFiles ?? []
	if (changedFiles.length > 0) {
		const { visible, overflowCount } = limitWithOverflow(changedFiles, 20)
		lines.push(`## Changed Files`)
		lines.push(``)
		for (const file of visible) {
			lines.push(`- \`${file}\``)
		}
		if (overflowCount > 0) {
			lines.push(`- ...and ${overflowCount} more file${overflowCount === 1 ? "" : "s"}`)
		}
		lines.push(``)
	}

	lines.push(`## Test Plan`)
	lines.push(``)
	lines.push(`- [ ] Manual testing`)
	lines.push(`- [ ] Type check passes`)
	lines.push(``)
	lines.push(`---`)
	lines.push(`🤖 Generated with [Azedarach](https://github.com/riordanpawley/azedarach)`)

	return appendLinkedIssueAutoCloseFooter(lines.join("\n"), issue.id)
}
