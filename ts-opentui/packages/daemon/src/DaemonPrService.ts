import { AppConfig, type ResolvedConfig } from "@azedarach/config"
import type { DaemonPullRequest, TrackedIssue } from "@azedarach/shared/rpc"
import { Command, CommandExecutor, FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Data, Effect, Schema } from "effect"
import {
	DaemonAttachmentService,
	type DaemonAttachmentServiceApi,
} from "./DaemonAttachmentService.js"
import { DaemonSessionService, type DaemonSessionServiceApi } from "./DaemonSessionService.js"
import {
	type TrackerIssueDaemonError,
	TrackerIssueDaemonService,
	type TrackerIssueDaemonServiceApi,
} from "./TrackerIssueDaemonService.js"

const BranchNameMapSchema = Schema.parseJson(
	Schema.Record({ key: Schema.String, value: Schema.String }),
)

export class DaemonPrError extends Data.TaggedError("DaemonPrError")<{
	readonly reason:
		| "command-failed"
		| "config"
		| "issue-tracker"
		| "merge-conflict"
		| "pr-disabled"
		| "validation-failed"
		| "worktree-missing"
	readonly message: string
}> {}

export interface DaemonPrServiceApi {
	readonly create: (
		issueId: string,
		projectPath: string,
	) => Effect.Effect<DaemonPullRequest, DaemonPrError>
	readonly cleanup: (params: {
		readonly issueId: string
		readonly projectPath: string
		readonly closeIssue?: boolean
	}) => Effect.Effect<void, DaemonPrError>
	readonly mergeToMain: (params: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<void, DaemonPrError>
	readonly updateFromBase: (params: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<void, DaemonPrError>
	readonly mergeBaseIntoBranch: (params: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<void, DaemonPrError>
	readonly abortMerge: (params: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<void, DaemonPrError>
	readonly checkMergeConflicts: (params: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<
		{
			readonly hasConflictRisk: boolean
			readonly conflictingFiles: ReadonlyArray<string>
			readonly baseBranch: string
			readonly issueBranch: string
		},
		DaemonPrError
	>
	readonly checkUncommittedChanges: (params: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<
		{
			readonly hasUncommittedChanges: boolean
			readonly changedFiles: ReadonlyArray<string>
		},
		DaemonPrError
	>
	readonly checkBranchBehindBase: (params: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<
		{
			readonly behind: number
			readonly ahead: number
			readonly baseBranch: string
		},
		DaemonPrError
	>
	readonly getEffectiveBaseBranch: (params: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<
		{
			readonly baseBranch: string
			readonly parentEpicId: string | undefined
		},
		DaemonPrError
	>
	readonly mergeIssueIntoIssue: (params: {
		readonly sourceIssueId: string
		readonly targetIssueId: string
		readonly projectPath: string
	}) => Effect.Effect<void, DaemonPrError>
	readonly getTargetBranch: (params: {
		readonly issueId: string
		readonly projectPath: string
	}) => Effect.Effect<
		{
			readonly targetBranch: string
			readonly isEpicChild: boolean
		},
		DaemonPrError
	>
	readonly checkGhCli: () => Effect.Effect<boolean, never>
}

const mapConfigError = (message: string): DaemonPrError =>
	new DaemonPrError({
		reason: "config",
		message,
	})

const mapCommandError = (message: string): DaemonPrError =>
	new DaemonPrError({
		reason: "command-failed",
		message,
	})

const mapIssueTrackerError = (message: string): DaemonPrError =>
	new DaemonPrError({
		reason: "issue-tracker",
		message,
	})

const mapPrDisabledError = (message: string): DaemonPrError =>
	new DaemonPrError({
		reason: "pr-disabled",
		message,
	})

const mapMergeConflictError = (message: string): DaemonPrError =>
	new DaemonPrError({
		reason: "merge-conflict",
		message,
	})

const mapValidationError = (message: string): DaemonPrError =>
	new DaemonPrError({
		reason: "validation-failed",
		message,
	})

const mapWorktreeMissingError = (message: string): DaemonPrError =>
	new DaemonPrError({
		reason: "worktree-missing",
		message,
	})

const readParentEpicId = (issue: TrackedIssue): string | undefined =>
	issue.dependencies?.find((dependency) => dependency.dependency_type === "parent-child")?.id

const buildWorktreePath = (projectPath: string, issueId: string, pathService: Path.Path): string =>
	pathService.join(
		pathService.dirname(projectPath),
		`${pathService.basename(projectPath)}-${issueId}`,
	)

const generatePullRequestTitle = (issue: TrackedIssue): string => {
	const typePrefix = `[${issue.issue_type}] `
	return `${typePrefix}${issue.title} (${issue.id})`
}

const toNonEmptyLines = (input: string): ReadonlyArray<string> =>
	input
		.split("\n")
		.map((line) => line.trim())
		.filter((line) => line.length > 0)

const limitWithOverflow = (
	items: ReadonlyArray<string>,
	limit: number,
): { readonly visible: ReadonlyArray<string>; readonly overflowCount: number } => ({
	visible: items.slice(0, limit),
	overflowCount: Math.max(0, items.length - limit),
})

const generatePullRequestBody = (
	issue: TrackedIssue,
	baseBranch: string,
	draftContext?: {
		readonly commitSubjects: ReadonlyArray<string>
		readonly changedFiles: ReadonlyArray<string>
	},
): string => {
	const lines: string[] = []

	lines.push("## Summary")
	lines.push("")
	lines.push(`Resolves ${issue.id}: ${issue.title}`)
	lines.push(`Base branch: \`${baseBranch}\``)
	lines.push("")

	if (issue.description !== undefined) {
		lines.push("## Description")
		lines.push("")
		lines.push(issue.description)
		lines.push("")
	}

	if (issue.design !== undefined) {
		lines.push("## Design Notes")
		lines.push("")
		lines.push(issue.design)
		lines.push("")
	}

	if (draftContext !== undefined && draftContext.commitSubjects.length > 0) {
		const { visible, overflowCount } = limitWithOverflow(draftContext.commitSubjects, 8)
		lines.push("## What Changed")
		lines.push("")
		for (const subject of visible) {
			lines.push(`- ${subject}`)
		}
		if (overflowCount > 0) {
			lines.push(`- ...and ${overflowCount} more commit${overflowCount === 1 ? "" : "s"}`)
		}
		lines.push("")
	}

	if (draftContext !== undefined && draftContext.changedFiles.length > 0) {
		const { visible, overflowCount } = limitWithOverflow(draftContext.changedFiles, 20)
		lines.push("## Changed Files")
		lines.push("")
		for (const file of visible) {
			lines.push(`- \`${file}\``)
		}
		if (overflowCount > 0) {
			lines.push(`- ...and ${overflowCount} more file${overflowCount === 1 ? "" : "s"}`)
		}
		lines.push("")
	}

	lines.push("## Test Plan")
	lines.push("")
	lines.push("- [ ] Manual testing")
	lines.push("- [ ] Type check passes")
	lines.push("")
	lines.push("---")
	lines.push("Generated with Azedarach")

	return lines.join("\n")
}

const parsePullRequestJson = (output: string): DaemonPullRequest => {
	const data = Schema.decodeUnknownSync(
		Schema.parseJson(
			Schema.Struct({
				number: Schema.Number,
				url: Schema.String,
				title: Schema.String,
				state: Schema.Literal("OPEN", "CLOSED", "MERGED"),
				isDraft: Schema.optional(Schema.Boolean),
				headRefName: Schema.String,
			}),
		),
	)(output)
	return {
		number: data.number,
		url: data.url,
		title: data.title,
		state: data.state === "OPEN" ? "open" : data.state === "CLOSED" ? "closed" : "merged",
		draft: data.isDraft ?? false,
		branch: data.headRefName,
	}
}

export class DaemonPrService extends Effect.Service<DaemonPrService>()("DaemonPrService", {
	dependencies: [
		AppConfig.Default,
		TrackerIssueDaemonService.Default,
		DaemonSessionService.Default,
		DaemonAttachmentService.Default,
		BunContext.layer,
	],
	effect: Effect.gen(function* () {
		const appConfig = yield* AppConfig
		const issues: TrackerIssueDaemonServiceApi = yield* TrackerIssueDaemonService
		const sessions: DaemonSessionServiceApi = yield* DaemonSessionService
		const attachments: DaemonAttachmentServiceApi = yield* DaemonAttachmentService
		const executor = yield* CommandExecutor.CommandExecutor
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		const mapTrackerError = (error: TrackerIssueDaemonError): DaemonPrError =>
			mapIssueTrackerError(error.message)

		const runInDirectory = (cwd: string, executable: string, args: ReadonlyArray<string>) =>
			executor.string(Command.make(executable, ...args).pipe(Command.workingDirectory(cwd))).pipe(
				Effect.map((output) => output.trim()),
				Effect.mapError((error) => mapCommandError(String(error))),
			)

		const exitCodeInDirectory = (cwd: string, executable: string, args: ReadonlyArray<string>) =>
			executor
				.exitCode(Command.make(executable, ...args).pipe(Command.workingDirectory(cwd)))
				.pipe(Effect.orElseSucceed(() => 1))

		const runShellInDirectory = (cwd: string, command: string) =>
			executor
				.exitCode(Command.make("sh", "-lc", command).pipe(Command.workingDirectory(cwd)))
				.pipe(Effect.mapError((error) => mapValidationError(String(error))))

		const readBranchMap = (projectPath: string): Effect.Effect<Readonly<Record<string, string>>> =>
			Effect.gen(function* () {
				const mapPath = pathService.join(projectPath, ".azedarach", "branch-name-map.json")
				const exists = yield* fs.exists(mapPath).pipe(Effect.orElseSucceed(() => false))
				if (!exists) {
					return {}
				}
				const content = yield* fs.readFileString(mapPath).pipe(Effect.orElseSucceed(() => "{}"))
				return yield* Schema.decode(BranchNameMapSchema)(content).pipe(
					Effect.orElseSucceed(() => ({})),
				)
			})

		const resolveIssue = (issueId: string, projectPath: string) =>
			issues.get(issueId, projectPath).pipe(Effect.mapError(mapTrackerError))

		const resolveIssueWorktreePath = (issueId: string, projectPath: string) =>
			Effect.gen(function* () {
				const worktreePath = buildWorktreePath(projectPath, issueId, pathService)
				const exists = yield* fs.exists(worktreePath).pipe(Effect.orElseSucceed(() => false))
				if (!exists) {
					return yield* Effect.fail(
						mapWorktreeMissingError(`No worktree found for ${issueId} at ${worktreePath}`),
					)
				}
				return worktreePath
			})

		const getCurrentBranch = (cwd: string) =>
			runInDirectory(cwd, "git", ["rev-parse", "--abbrev-ref", "HEAD"])

		const getBranchForIssue = (issueId: string, projectPath: string) =>
			Effect.gen(function* () {
				const worktreePath = buildWorktreePath(projectPath, issueId, pathService)
				const worktreeExists = yield* fs
					.exists(worktreePath)
					.pipe(Effect.orElseSucceed(() => false))
				if (worktreeExists) {
					return yield* getCurrentBranch(worktreePath)
				}
				const branchMap = yield* readBranchMap(projectPath)
				return branchMap[issueId]
			})

		const resolveBaseBranch = (issue: TrackedIssue, projectPath: string) =>
			Effect.gen(function* () {
				const gitConfig = yield* appConfig
					.getGitConfigForProjectPath(projectPath)
					.pipe(Effect.mapError((error) => mapConfigError(error.message)))
				const parentEpicId = readParentEpicId(issue)
				if (parentEpicId === undefined) {
					return gitConfig.baseBranch
				}
				const mappedBranch = yield* readBranchMap(projectPath).pipe(
					Effect.map((branchMap) => branchMap[parentEpicId]),
				)
				return mappedBranch ?? gitConfig.baseBranch
			})

		const resolveMergeDirectory = (issue: TrackedIssue, projectPath: string) =>
			Effect.gen(function* () {
				const parentEpicId = readParentEpicId(issue)
				if (parentEpicId === undefined) {
					return projectPath
				}
				const parentWorktreePath = buildWorktreePath(projectPath, parentEpicId, pathService)
				const parentWorktreeExists = yield* fs
					.exists(parentWorktreePath)
					.pipe(Effect.orElseSucceed(() => false))
				return parentWorktreeExists ? parentWorktreePath : projectPath
			})

		const commitIfStaged = (cwd: string, message: string) =>
			Effect.gen(function* () {
				yield* runInDirectory(cwd, "git", ["add", "-A"])
				const stagedDiffExitCode = yield* exitCodeInDirectory(cwd, "git", [
					"diff",
					"--cached",
					"--quiet",
				])
				if (stagedDiffExitCode === 1) {
					yield* runInDirectory(cwd, "git", ["commit", "-m", message])
				}
			})

		const maybePushBranch = (cwd: string, branch: string, gitConfig: ResolvedConfig["git"]) =>
			gitConfig.pushEnabled
				? runInDirectory(cwd, "git", ["push", "-u", gitConfig.remote, branch]).pipe(Effect.asVoid)
				: Effect.void

		const maybeDeleteRemoteBranch = (
			cwd: string,
			branch: string | undefined,
			gitConfig: ResolvedConfig["git"],
		) =>
			branch === undefined || !gitConfig.pushEnabled
				? Effect.void
				: runInDirectory(cwd, "git", ["push", gitConfig.remote, "--delete", branch]).pipe(
						Effect.catchAll(() => Effect.void),
						Effect.asVoid,
					)

		const runValidationCommands = (cwd: string) =>
			Effect.gen(function* () {
				const mergeConfig = yield* appConfig.getMergeConfig()
				for (const command of mergeConfig.validateCommands) {
					const exitCode = yield* runShellInDirectory(cwd, command)
					if (exitCode !== 0) {
						return yield* Effect.fail(
							mapValidationError(`Validation command failed in ${cwd}: ${command}`),
						)
					}
				}
			})

		const maybeFetchBaseBranch = (projectPath: string, baseBranch: string) =>
			Effect.gen(function* () {
				const gitConfig = yield* appConfig
					.getGitConfigForProjectPath(projectPath)
					.pipe(Effect.mapError((error) => mapConfigError(error.message)))
				if (!gitConfig.fetchEnabled) {
					return
				}

				yield* runInDirectory(projectPath, "git", ["fetch", gitConfig.remote, baseBranch]).pipe(
					Effect.catchAll(() => Effect.void),
				)
				yield* runInDirectory(projectPath, "git", [
					"fetch",
					gitConfig.remote,
					`${baseBranch}:${baseBranch}`,
				]).pipe(Effect.catchAll(() => Effect.void))
			})

		const listMergeConflictFiles = (cwd: string, baseBranch: string) =>
			runInDirectory(cwd, "git", [
				"merge-tree",
				"--write-tree",
				"--name-only",
				"--no-messages",
				baseBranch,
				"HEAD",
			]).pipe(
				Effect.map((output) =>
					output
						.trim()
						.split("\n")
						.slice(1)
						.map((line) => line.trim())
						.filter((line) => line.length > 0 && !line.startsWith(".azedarach/")),
				),
				Effect.catchAll(() => Effect.succeed([] as ReadonlyArray<string>)),
			)

		const maybeStartConflictResolutionSession = (params: {
			readonly issueId: string
			readonly projectPath: string
			readonly conflictingFiles: ReadonlyArray<string>
		}) =>
			sessions
				.start({
					issueId: params.issueId,
					projectPath: params.projectPath,
					initialPrompt: `There are merge conflicts in: ${params.conflictingFiles.join(", ")}. Please resolve these conflicts, then stage and commit the resolution.`,
				})
				.pipe(
					Effect.catchAll(() => Effect.void),
					Effect.asVoid,
				)

		const failIfMergeConflict = (params: {
			readonly issueId: string
			readonly projectPath: string
			readonly baseBranch: string
			readonly worktreePath: string
			readonly mergeCommitMessage: string
			readonly retryHint: string
		}) =>
			Effect.gen(function* () {
				const mergeTreeExitCode = yield* exitCodeInDirectory(params.worktreePath, "git", [
					"merge-tree",
					"--write-tree",
					params.baseBranch,
					"HEAD",
				])
				if (mergeTreeExitCode === 0) {
					return
				}

				const conflictingFiles = yield* listMergeConflictFiles(
					params.worktreePath,
					params.baseBranch,
				)
				yield* runInDirectory(params.worktreePath, "git", [
					"merge",
					params.baseBranch,
					"-m",
					params.mergeCommitMessage,
				]).pipe(Effect.catchAll(() => Effect.void))
				if (conflictingFiles.length > 0) {
					yield* maybeStartConflictResolutionSession({
						issueId: params.issueId,
						projectPath: params.projectPath,
						conflictingFiles,
					})
				}
				return yield* Effect.fail(
					mapMergeConflictError(
						conflictingFiles.length > 0
							? `Merge conflicts detected in: ${conflictingFiles.join(", ")}. ${params.retryHint}`
							: `Merge conflicts detected while merging ${params.baseBranch}. ${params.retryHint}`,
					),
				)
			})

		return {
			create: (issueId, projectPath) =>
				Effect.gen(function* () {
					const prConfig = yield* appConfig.getPRConfig()
					if (!prConfig.enabled) {
						return yield* Effect.fail(
							mapPrDisabledError("PR creation is disabled in the current configuration."),
						)
					}

					const issue = yield* resolveIssue(issueId, projectPath)
					const worktreePath = yield* resolveIssueWorktreePath(issueId, projectPath)
					const issueBranch = yield* getCurrentBranch(worktreePath)
					const baseBranch = yield* resolveBaseBranch(issue, projectPath)
					const gitConfig = yield* appConfig
						.getGitConfigForProjectPath(projectPath)
						.pipe(Effect.mapError((error) => mapConfigError(error.message)))

					yield* issues.sync(worktreePath).pipe(
						Effect.mapError(mapTrackerError),
						Effect.catchAll(() => Effect.void),
					)
					yield* commitIfStaged(worktreePath, `Complete ${issueId}: ${issue.title}`)
					yield* maybePushBranch(worktreePath, issueBranch, gitConfig)

					const commitSubjects = yield* runInDirectory(worktreePath, "git", [
						"log",
						"--format=%s",
						`${baseBranch}..HEAD`,
					]).pipe(Effect.orElseSucceed(() => ""))
					const changedFiles = yield* runInDirectory(worktreePath, "git", [
						"diff",
						"--name-only",
						`${baseBranch}...HEAD`,
					]).pipe(Effect.orElseSucceed(() => ""))

					const pullRequestTitle = generatePullRequestTitle(issue)
					const pullRequestBody = generatePullRequestBody(issue, baseBranch, {
						commitSubjects: toNonEmptyLines(commitSubjects),
						changedFiles: toNonEmptyLines(changedFiles),
					})

					const createArgs = [
						"pr",
						"create",
						"--title",
						pullRequestTitle,
						"--body",
						pullRequestBody,
						"--base",
						baseBranch,
					]
					if (prConfig.autoDraft) {
						createArgs.push("--draft")
					}
					const pullRequestUrl = yield* runInDirectory(worktreePath, "gh", createArgs)
					const pullRequest = yield* runInDirectory(worktreePath, "gh", [
						"pr",
						"view",
						pullRequestUrl,
						"--json",
						"number,url,title,state,isDraft,headRefName",
					]).pipe(Effect.map(parsePullRequestJson))

					const existingNotes = issue.notes
					const nextNotes =
						existingNotes === undefined || existingNotes.trim().length === 0
							? `PR: ${pullRequest.url}`
							: `${existingNotes}\nPR: ${pullRequest.url}`
					yield* issues
						.update(issueId, { notes: nextNotes }, projectPath)
						.pipe(Effect.mapError(mapTrackerError))

					return pullRequest
				}),
			cleanup: ({ issueId, projectPath, closeIssue = true }) =>
				Effect.gen(function* () {
					const gitConfig = yield* appConfig
						.getGitConfigForProjectPath(projectPath)
						.pipe(Effect.mapError((error) => mapConfigError(error.message)))
					const worktreePath = buildWorktreePath(projectPath, issueId, pathService)
					const worktreeExists = yield* fs
						.exists(worktreePath)
						.pipe(Effect.orElseSucceed(() => false))
					const branchName = yield* getBranchForIssue(issueId, projectPath)

					yield* sessions.stop({ issueId, projectPath }).pipe(Effect.catchAll(() => Effect.void))

					if (worktreeExists) {
						yield* runInDirectory(projectPath, "git", [
							"worktree",
							"remove",
							worktreePath,
							"--force",
						])
					}

					yield* maybeDeleteRemoteBranch(projectPath, branchName, gitConfig)

					if (branchName !== undefined) {
						yield* runInDirectory(projectPath, "git", ["branch", "-D", branchName]).pipe(
							Effect.catchAll(() => Effect.void),
							Effect.asVoid,
						)
					}

					if (closeIssue) {
						yield* issues
							.close(issueId, undefined, projectPath)
							.pipe(Effect.mapError(mapTrackerError))
						const issueAttachments = yield* attachments
							.list(issueId, projectPath)
							.pipe(Effect.catchAll(() => Effect.succeed([])))
						for (const attachment of issueAttachments) {
							yield* attachments
								.remove({
									issueId,
									attachmentId: attachment.id,
									projectPath,
								})
								.pipe(Effect.catchAll(() => Effect.void))
						}
						yield* issues.sync(projectPath).pipe(
							Effect.mapError(mapTrackerError),
							Effect.catchAll(() => Effect.void),
						)
					}
				}),
			mergeToMain: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const issue = yield* resolveIssue(issueId, projectPath)
					const gitConfig = yield* appConfig
						.getGitConfigForProjectPath(projectPath)
						.pipe(Effect.mapError((error) => mapConfigError(error.message)))
					const worktreePath = yield* resolveIssueWorktreePath(issueId, projectPath)
					const issueBranch = yield* getCurrentBranch(worktreePath)
					const baseBranch = yield* resolveBaseBranch(issue, projectPath)
					const mergeDirectory = yield* resolveMergeDirectory(issue, projectPath)
					const mergeDirectoryBranch = yield* getCurrentBranch(mergeDirectory)

					yield* sessions.stop({ issueId, projectPath }).pipe(Effect.catchAll(() => Effect.void))
					yield* issues.sync(worktreePath).pipe(
						Effect.mapError(mapTrackerError),
						Effect.catchAll(() => Effect.void),
					)
					yield* commitIfStaged(worktreePath, `Complete ${issueId}: ${issue.title}`)

					if (mergeDirectoryBranch !== baseBranch) {
						yield* runInDirectory(mergeDirectory, "git", ["checkout", baseBranch])
					}

					yield* runInDirectory(mergeDirectory, "git", [
						"merge",
						"--no-ff",
						issueBranch,
						"-m",
						`Merge ${issueId}: ${issue.title}`,
					])
					yield* runValidationCommands(mergeDirectory)

					if (gitConfig.pushEnabled) {
						yield* runInDirectory(mergeDirectory, "git", [
							"push",
							gitConfig.remote,
							baseBranch,
						]).pipe(Effect.asVoid)
					}
				}),
			updateFromBase: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const issue = yield* resolveIssue(issueId, projectPath)
					const worktreePath = yield* resolveIssueWorktreePath(issueId, projectPath)
					const baseBranch = yield* resolveBaseBranch(issue, projectPath)
					const issueBranch = yield* getCurrentBranch(worktreePath)
					const gitConfig = yield* appConfig
						.getGitConfigForProjectPath(projectPath)
						.pipe(Effect.mapError((error) => mapConfigError(error.message)))

					yield* maybeFetchBaseBranch(projectPath, baseBranch)
					yield* failIfMergeConflict({
						issueId,
						projectPath,
						baseBranch,
						worktreePath,
						mergeCommitMessage: `Merge ${baseBranch} into ${issueBranch}`,
						retryHint: "Resolve them in the session, then retry the operation.",
					})
					yield* runInDirectory(worktreePath, "git", ["merge", baseBranch, "--no-edit"]).pipe(
						Effect.mapError((error) =>
							mapCommandError(`Failed to merge ${baseBranch} into ${issueId}: ${error.message}`),
						),
					)
					if (gitConfig.pushEnabled) {
						yield* runInDirectory(worktreePath, "git", [
							"push",
							gitConfig.remote,
							issueBranch,
						]).pipe(Effect.catchAll(() => Effect.void))
					}
					yield* issues.sync(worktreePath).pipe(
						Effect.mapError(mapTrackerError),
						Effect.catchAll(() => Effect.void),
					)
				}),
			mergeBaseIntoBranch: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const issue = yield* resolveIssue(issueId, projectPath)
					const worktreePath = yield* resolveIssueWorktreePath(issueId, projectPath)
					const baseBranch = yield* resolveBaseBranch(issue, projectPath)
					const issueBranch = yield* getCurrentBranch(worktreePath)
					const gitConfig = yield* appConfig
						.getGitConfigForProjectPath(projectPath)
						.pipe(Effect.mapError((error) => mapConfigError(error.message)))

					yield* maybeFetchBaseBranch(projectPath, baseBranch)

					const statusOutput = yield* runInDirectory(worktreePath, "git", [
						"status",
						"--porcelain",
					]).pipe(Effect.orElseSucceed(() => ""))
					const hasUncommittedChanges = statusOutput.trim().length > 0
					if (hasUncommittedChanges) {
						yield* runInDirectory(worktreePath, "git", [
							"stash",
							"push",
							"-m",
							"azedarach-merge-stash",
						]).pipe(Effect.catchAll(() => Effect.void))
					}

					yield* failIfMergeConflict({
						issueId,
						projectPath,
						baseBranch,
						worktreePath,
						mergeCommitMessage: `Merge ${baseBranch} into ${issueId}`,
						retryHint: "Resolve them in the session, then retry attach.",
					})
					yield* runInDirectory(worktreePath, "git", ["merge", baseBranch, "--no-edit"]).pipe(
						Effect.mapError((error) =>
							mapCommandError(`Failed to merge ${baseBranch} into ${issueId}: ${error.message}`),
						),
					)
					if (hasUncommittedChanges) {
						yield* runInDirectory(worktreePath, "git", ["stash", "pop"]).pipe(
							Effect.catchAll(() => Effect.void),
						)
					}
					if (gitConfig.pushEnabled) {
						yield* runInDirectory(worktreePath, "git", [
							"push",
							gitConfig.remote,
							issueBranch,
						]).pipe(Effect.catchAll(() => Effect.void))
					}
				}),
			abortMerge: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const worktreePath = yield* resolveIssueWorktreePath(issueId, projectPath)
					yield* runInDirectory(worktreePath, "git", ["merge", "--abort"]).pipe(
						Effect.mapError((error) =>
							mapCommandError(`Failed to abort merge for ${issueId}: ${error.message}`),
						),
					)
				}),
			checkMergeConflicts: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const issue = yield* resolveIssue(issueId, projectPath)
					const worktreePath = yield* resolveIssueWorktreePath(issueId, projectPath)
					const issueBranch = yield* getCurrentBranch(worktreePath)
					const baseBranch = yield* resolveBaseBranch(issue, projectPath)
					yield* maybeFetchBaseBranch(projectPath, baseBranch)

					const mergeTreeExitCode = yield* exitCodeInDirectory(worktreePath, "git", [
						"merge-tree",
						"--write-tree",
						baseBranch,
						issueBranch,
					])
					const conflictingFiles =
						mergeTreeExitCode === 0 ? [] : yield* listMergeConflictFiles(worktreePath, baseBranch)

					return {
						hasConflictRisk: mergeTreeExitCode !== 0,
						conflictingFiles,
						baseBranch,
						issueBranch,
					}
				}),
			checkUncommittedChanges: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const worktreePath = yield* resolveIssueWorktreePath(issueId, projectPath)
					const statusOutput = yield* runInDirectory(worktreePath, "git", [
						"status",
						"--porcelain",
					]).pipe(Effect.orElseSucceed(() => ""))
					const changedFiles = statusOutput
						.split("\n")
						.map((line) => line.trim())
						.filter((line) => line.length > 0)
						.map((line) => line.slice(3))
						.map((file) => {
							const renameIndex = file.indexOf(" -> ")
							return renameIndex >= 0 ? file.slice(renameIndex + 4).trim() : file.trim()
						})
						.filter((file) => file.length > 0)

					return {
						hasUncommittedChanges: changedFiles.length > 0,
						changedFiles,
					}
				}),
			checkBranchBehindBase: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const issue = yield* resolveIssue(issueId, projectPath)
					const worktreePath = yield* resolveIssueWorktreePath(issueId, projectPath)
					const baseBranch = yield* resolveBaseBranch(issue, projectPath)
					yield* maybeFetchBaseBranch(projectPath, baseBranch)

					const revListOutput = yield* runInDirectory(worktreePath, "git", [
						"rev-list",
						"--left-right",
						"--count",
						`${baseBranch}...HEAD`,
					])
					const counts = revListOutput.trim().split(/\s+/)
					const behindCount = Number.parseInt(counts[0] ?? "0", 10)
					const aheadCount = Number.parseInt(counts[1] ?? "0", 10)

					return {
						behind: Number.isNaN(behindCount) ? 0 : behindCount,
						ahead: Number.isNaN(aheadCount) ? 0 : aheadCount,
						baseBranch,
					}
				}),
			getEffectiveBaseBranch: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const issue = yield* resolveIssue(issueId, projectPath)
					const baseBranch = yield* resolveBaseBranch(issue, projectPath)
					return {
						baseBranch,
						parentEpicId: readParentEpicId(issue),
					}
				}),
			mergeIssueIntoIssue: ({ sourceIssueId, targetIssueId, projectPath }) =>
				Effect.gen(function* () {
					if (sourceIssueId === targetIssueId) {
						return yield* Effect.fail(
							mapValidationError("Cannot merge an issue branch into itself."),
						)
					}

					const sourceIssue = yield* resolveIssue(sourceIssueId, projectPath)
					const sourceWorktreePath = yield* resolveIssueWorktreePath(sourceIssueId, projectPath)
					const sourceBranch = yield* getCurrentBranch(sourceWorktreePath)
					const targetWorktreePath = yield* resolveIssueWorktreePath(targetIssueId, projectPath)
					const targetBranch = yield* getCurrentBranch(targetWorktreePath)
					const gitConfig = yield* appConfig
						.getGitConfigForProjectPath(projectPath)
						.pipe(Effect.mapError((error) => mapConfigError(error.message)))

					yield* commitIfStaged(
						sourceWorktreePath,
						`Complete ${sourceIssueId}: ${sourceIssue.title}`,
					)
					yield* maybePushBranch(sourceWorktreePath, sourceBranch, gitConfig)

					yield* failIfMergeConflict({
						issueId: targetIssueId,
						projectPath,
						baseBranch: sourceBranch,
						worktreePath: targetWorktreePath,
						mergeCommitMessage: `Merge ${sourceIssueId} into ${targetIssueId}`,
						retryHint: "Resolve them in the target session, then retry the merge operation.",
					})

					yield* runInDirectory(targetWorktreePath, "git", [
						"merge",
						"--no-ff",
						sourceBranch,
						"-m",
						`Merge ${sourceIssueId} into ${targetIssueId}`,
					]).pipe(
						Effect.mapError((error) =>
							mapCommandError(
								`Failed to merge ${sourceIssueId} into ${targetIssueId}: ${error.message}`,
							),
						),
					)

					yield* runValidationCommands(targetWorktreePath)

					if (gitConfig.pushEnabled) {
						yield* runInDirectory(targetWorktreePath, "git", [
							"push",
							gitConfig.remote,
							targetBranch,
						]).pipe(Effect.catchAll(() => Effect.void))
					}

					yield* issues
						.close(sourceIssueId, `Merged into ${targetIssueId}`, projectPath)
						.pipe(Effect.mapError(mapTrackerError))
					yield* issues.sync(projectPath).pipe(
						Effect.mapError(mapTrackerError),
						Effect.catchAll(() => Effect.void),
					)
				}),
			getTargetBranch: ({ issueId, projectPath }) =>
				Effect.gen(function* () {
					const issue = yield* resolveIssue(issueId, projectPath)
					const targetBranch = yield* resolveBaseBranch(issue, projectPath)
					return {
						targetBranch,
						isEpicChild: readParentEpicId(issue) !== undefined,
					}
				}),
			checkGhCli: () =>
				exitCodeInDirectory(process.cwd(), "gh", ["auth", "status"]).pipe(
					Effect.map((exitCode) => exitCode === 0),
				),
		} satisfies DaemonPrServiceApi
	}),
}) {}
