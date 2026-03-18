/**
 * PR Workflow Atoms
 *
 * Handles PR creation, merge, and cleanup operations.
 */

import { Command } from "@effect/platform"
import { Effect } from "effect"
import { AppConfig } from "../../config/index.js"
import { ImageAttachmentService } from "../../core/ImageAttachmentService.js"
import {
	GHCLIError,
	PRAlreadyExistsError,
	PRBranchProtectionError,
	PRError,
} from "../../core/PRWorkflow.js"
import type { DaemonIssue } from "../../rpc/DaemonRpcSchemas.js"
import { OfflineService } from "../../services/OfflineService.js"
import { ProjectService } from "../../services/ProjectService.js"
import { getRequiredDaemonDomainRpcClients } from "../../services/RequiredDaemonDomainRpcClient.js"
import { appRuntime } from "./runtime.js"

const logWorkflowError = (action: string) => (error: unknown) =>
	Effect.logError(`PR workflow ${action} failed: ${String(error)}`)

const parsePullRequestUrl = (url: string): number => {
	const match = url.match(/\/pull\/(\d+)/)
	return match === null ? 0 : Number.parseInt(match[1]!, 10)
}

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

const runGit = (args: readonly string[], cwd: string) =>
	Command.string(Command.make("git", ...args).pipe(Command.workingDirectory(cwd))).pipe(
		Effect.mapError(
			(error) =>
				new PRError({
					message: `git ${args.join(" ")} failed: ${String(error)}`,
					command: `git ${args.join(" ")}`,
				}),
		),
	)

const runGitExitCode = (args: readonly string[], cwd: string) =>
	Command.exitCode(Command.make("git", ...args).pipe(Command.workingDirectory(cwd))).pipe(
		Effect.catchAll((error) =>
			Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
				Effect.zipRight(Effect.succeed(1)),
			),
		),
	)

const runGH = (args: readonly string[], cwd: string) =>
	Command.string(Command.make("gh", ...args).pipe(Command.workingDirectory(cwd))).pipe(
		Effect.mapError((error) => {
			const errorStr = String(error)
			const isPRCreate = args[0] === "pr" && args[1] === "create"

			if (errorStr.includes("gh auth login") || errorStr.includes("not logged")) {
				return new GHCLIError({ message: "gh CLI not authenticated. Run: gh auth login" })
			}
			if (errorStr.includes("command not found") || errorStr.includes("ENOENT")) {
				return new GHCLIError({ message: "gh CLI not installed. Run: brew install gh" })
			}
			if (isPRCreate && errorStr.includes("already exists")) {
				return new PRAlreadyExistsError({
					message: "A pull request already exists for this branch",
				})
			}
			if (
				isPRCreate &&
				(errorStr.includes("protected branch") || errorStr.includes("branch protection"))
			) {
				return new PRBranchProtectionError({
					operation: "pr-create",
					message: "Branch protection prevented PR creation",
				})
			}
			return new PRError({
				message: `gh ${args.join(" ")} failed: ${errorStr}`,
				command: `gh ${args.join(" ")}`,
			})
		}),
	)

const checkGHCLI = () =>
	Command.exitCode(Command.make("gh", "auth", "status")).pipe(
		Effect.map((exitCode) => exitCode === 0),
		Effect.catchAll((error) =>
			Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
				Effect.zipRight(Effect.succeed(false)),
			),
		),
	)

const getCurrentBranch = (cwd: string) =>
	runGit(["branch", "--show-current"], cwd).pipe(
		Effect.map((output) => output.trim()),
		Effect.flatMap((branch) =>
			branch.length > 0
				? Effect.succeed(branch)
				: Effect.fail(
						new PRError({
							message: `Unable to determine current branch for ${cwd}`,
							command: "git branch --show-current",
						}),
					),
		),
	)

const hasStagedChanges = (cwd: string) =>
	runGitExitCode(["diff", "--cached", "--quiet"], cwd).pipe(Effect.map((code) => code === 1))

const ensureNotBaseBranch = (currentBranch: string, baseBranch: string, action: string) =>
	currentBranch === baseBranch
		? Effect.fail(
				new PRError({
					message: `Refusing to ${action} the base branch '${baseBranch}'. Switch to the issue branch and retry.`,
					command: `git branch --show-current`,
				}),
			)
		: Effect.void

const isLinkedWorktree = (cwd: string) =>
	Effect.gen(function* () {
		const gitDir = yield* runGit(["rev-parse", "--git-dir"], cwd)
		const commonDir = yield* runGit(["rev-parse", "--git-common-dir"], cwd)
		return gitDir.trim() !== commonDir.trim()
	})

const cleanupLinkedWorktree = (cwd: string) =>
	Effect.gen(function* () {
		const worktreePath = yield* runGit(["rev-parse", "--show-toplevel"], cwd)
		yield* runGit(["worktree", "remove", "--force", worktreePath.trim()], cwd)
	})

// ============================================================================
// PR Workflow Atoms
// ============================================================================

/**
 * Create a PR for a bead's worktree branch
 *
 * Usage: const createPR = useAtomSet(createPRAtom, { mode: "promise" })
 *        const pr = await createPR(issueId)
 */
export const createPRAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		const appConfig = yield* AppConfig
		const offlineService = yield* OfflineService
		const daemonRpcDomains = yield* getRequiredDaemonDomainRpcClients()

		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

		const prStatus = yield* offlineService.isPREnabled()
		if (!prStatus.enabled) {
			return yield* Effect.fail(
				new PRError({
					message: offlineService.getDisabledMessage("PR creation", prStatus),
					issueId,
				}),
			)
		}

		const issue = yield* daemonRpcDomains.issueTask
			.issueShow({ issueId, cwd: projectPath })
			.pipe(Effect.map((result) => result.issue))
		const gitConfig = yield* appConfig.getGitConfig()
		const baseBranch = gitConfig.baseBranch
		const currentBranch = yield* getCurrentBranch(projectPath)
		yield* ensureNotBaseBranch(currentBranch, baseBranch, "create a PR")
		const ghAvailable = yield* checkGHCLI()
		if (!ghAvailable) {
			return yield* Effect.fail(
				new GHCLIError({ message: "gh CLI not installed or authenticated" }),
			)
		}

		const title = buildIssuePRTitle(issue)
		const body = buildIssuePRBody(issue, { baseBranch })

		const prUrl = yield* runGH(
			["pr", "create", "--title", title, "--body", body, "--base", baseBranch, "--draft"],
			projectPath,
		).pipe(Effect.map((output) => output.trim()))

		yield* daemonRpcDomains.issueTask
			.issueUpdate({
				issueId,
				fields: { notes: `PR: ${prUrl}` },
				cwd: projectPath,
			})
			.pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.void),
					),
				),
			)

		return {
			number: parsePullRequestUrl(prUrl),
			url: prUrl,
			title,
			state: "open" as const,
			draft: true,
			branch: currentBranch,
		}
	}).pipe(Effect.tapError(logWorkflowError("create"))),
)

/**
 * Cleanup worktree and branches after PR merge or abandonment
 *
 * Usage: const cleanup = useAtomSet(cleanupAtom, { mode: "promise" })
 *        await cleanup(issueId)
 */
export const cleanupAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		const appConfig = yield* AppConfig
		const offlineService = yield* OfflineService
		const imageAttachmentService = yield* ImageAttachmentService
		const daemonRpcDomains = yield* getRequiredDaemonDomainRpcClients()

		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()
		const baseBranch = yield* appConfig
			.getGitConfig()
			.pipe(Effect.map((config) => config.baseBranch))
		const currentBranch = yield* getCurrentBranch(projectPath).pipe(
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed("")),
				),
			),
		)
		if (currentBranch.length > 0) {
			yield* ensureNotBaseBranch(currentBranch, baseBranch, "cleanup")
		}

		yield* daemonRpcDomains.taskSession
			.sessionStop({ issueId, projectPath })
			.pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.void),
					),
				),
			)

		const linkedWorktree = yield* isLinkedWorktree(projectPath).pipe(
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed(false)),
				),
			),
		)
		if (linkedWorktree) {
			yield* cleanupLinkedWorktree(projectPath).pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.void),
					),
				),
			)
		}

		if (currentBranch.length > 0) {
			const pushStatus = yield* offlineService.isGitPushEnabled()
			if (pushStatus.enabled) {
				yield* runGit(["push", "origin", "--delete", currentBranch], projectPath).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.void),
						),
					),
				)
			}

			yield* runGit(["branch", "-D", currentBranch], projectPath).pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.void),
					),
				),
			)
		}

		yield* daemonRpcDomains.issueTask
			.issueUpdate({
				issueId,
				fields: { status: "closed" },
				cwd: projectPath,
			})
			.pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.void),
					),
				),
			)

		yield* imageAttachmentService
			.cleanupImagesForIssue(issueId)
			.pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.void),
					),
				),
			)
	}).pipe(Effect.catchAll((error) => logWorkflowError("cleanup")(error).pipe(Effect.asVoid))),
)

/**
 * Merge worktree branch to main and clean up
 *
 * Merges the worktree branch to main locally without creating a PR.
 * Ideal for completed work that doesn't need review.
 *
 * Usage: const mergeToMain = useAtomSet(mergeToMainAtom, { mode: "promise" })
 *        await mergeToMain(issueId)
 */
export const mergeToMainAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		const appConfig = yield* AppConfig
		const offlineService = yield* OfflineService
		const daemonRpcDomains = yield* getRequiredDaemonDomainRpcClients()

		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()
		const issue = yield* daemonRpcDomains.issueTask
			.issueShow({ issueId, cwd: projectPath })
			.pipe(Effect.map((result) => result.issue))
		const gitConfig = yield* appConfig.getGitConfig()
		const sourceBranch = yield* getCurrentBranch(projectPath)
		const baseBranch = gitConfig.baseBranch
		yield* ensureNotBaseBranch(sourceBranch, baseBranch, "merge into main")

		yield* runGit(["add", "-A"], projectPath)
		const shouldCommit = yield* hasStagedChanges(projectPath)
		if (shouldCommit) {
			yield* runGit(["commit", "-m", `Complete ${issueId}: ${issue.title}`], projectPath)
		}

		const mergeTreeExitCode = yield* runGitExitCode(
			["merge-tree", "--write-tree", baseBranch, sourceBranch],
			projectPath,
		)
		if (mergeTreeExitCode !== 0) {
			return yield* Effect.fail(
				new PRError({
					message: `Merge conflicts detected while merging ${sourceBranch} into ${baseBranch}. Resolve conflicts manually and retry.`,
					issueId,
				}),
			)
		}

		yield* runGit(["checkout", baseBranch], projectPath)
		yield* runGit(
			["merge", sourceBranch, "--no-ff", "-m", `Merge ${issueId}: ${issue.title}`, "-X", "ours"],
			projectPath,
		).pipe(
			Effect.mapError((error) => {
				const errorStr = String(error)
				if (errorStr.includes("CONFLICT")) {
					return new PRError({
						message: `Merge conflict while merging ${sourceBranch} into ${baseBranch}. Resolve manually and retry.`,
						issueId,
					})
				}
				return error
			}),
		)

		const pushStatus = yield* offlineService.isGitPushEnabled()
		if (pushStatus.enabled) {
			yield* runGit(["push", "origin", baseBranch], projectPath).pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.void),
					),
				),
			)
		}
	}).pipe(Effect.tapError(logWorkflowError("merge"))),
)

/**
 * Check if gh CLI is available and authenticated
 *
 * Usage: const ghAvailable = useAtomValue(ghCLIAvailableAtom)
 */
export const ghCLIAvailableAtom = appRuntime.atom(
	Effect.gen(function* () {
		return yield* checkGHCLI()
	}),
	{ initialValue: false },
)
