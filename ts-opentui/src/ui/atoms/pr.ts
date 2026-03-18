/**
 * PR Workflow Atoms
 *
 * PR/worktree authority is hard-cut from TUI runtime.
 * PR operations must run through daemon RPC and are intentionally disabled here
 * until daemon PR RPC endpoints are implemented.
 */

import { Effect } from "effect"
import type { DaemonIssue } from "../../rpc/DaemonRpcSchemas.js"
import { ToastService } from "../../services/ToastService.js"
import { appRuntime } from "./runtime.js"

const prUnavailableMessage = (action: string) =>
	`PR action '${action}' is unavailable in daemon-rpc runtime; use daemon RPC PR commands.`

const unavailable = (action: string) =>
	Effect.gen(function* () {
		const toast = yield* ToastService
		yield* toast.show("warning", prUnavailableMessage(action))
		return yield* Effect.fail(new Error(prUnavailableMessage(action)))
	})

export const buildIssuePRTitle = (
	issue: Pick<DaemonIssue, "id" | "title" | "issue_type">,
): string => {
	const typePrefix = issue.issue_type ? `[${issue.issue_type}] ` : ""
	return `${typePrefix}${issue.title} (${issue.id})`
}

interface PRDraftContext {
	readonly baseBranch: string
}

export const buildIssuePRBody = (
	issue: Pick<DaemonIssue, "id" | "title" | "description" | "design">,
	draftContext?: PRDraftContext,
): string => {
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

	lines.push(`## Test Plan`)
	lines.push(``)
	lines.push(`- [ ] Manual testing`)
	lines.push(`- [ ] Type check passes`)
	lines.push(``)
	lines.push(`---`)
	lines.push(`🤖 Generated with [Azedarach](https://github.com/riordanpawley/azedarach)`)

	return lines.join("\n")
}

export const createPRAtom = appRuntime.fn((_issueId: string) => unavailable("create"))

export const cleanupAtom = appRuntime.fn((_issueId: string) =>
	unavailable("cleanup").pipe(Effect.asVoid),
)

export const mergeToMainAtom = appRuntime.fn((_issueId: string) =>
	unavailable("merge-to-main").pipe(Effect.asVoid),
)

export const ghCLIAvailableAtom = appRuntime.atom(Effect.succeed(false), { initialValue: false })
