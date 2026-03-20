import { AppConfig } from "@azedarach/config"
import type { TrackedIssue } from "@azedarach/shared/rpc"
import { Command, CommandExecutor, FileSystem, Path } from "@effect/platform"
import { Data, Effect, Option, Schedule, Schema } from "effect"
import {
	BackendDaemonSessionRecovery,
	type BackendDaemonSessionRecoveryApi,
	type BackendDaemonSessionSnapshot,
	type BackendDaemonSessionState,
} from "./BackendDaemonSessionRecovery.js"
import {
	type CliToolName,
	deepMerge,
	generateHookConfig,
	getToolDefinition,
	type JsonObject,
	type JsonValue,
} from "./DaemonCliTooling.js"
import { getIssueSessionName } from "./DaemonSessionNames.js"
import {
	TrackerIssueDaemonService,
	type TrackerIssueDaemonServiceApi,
} from "./TrackerIssueDaemonService.js"

const WINDOW_CODE = "code"
const INIT_DONE_OPTION = "@az_init_done"
const INIT_FAILED_OPTION = "@az_init_failed"
const INIT_FAILED_COMMAND_OPTION = "@az_init_failed_command"
const INIT_FAILED_STATUS_OPTION = "@az_status"
const SHELL_READY_OPTION = "@az_shell_ready"
const DEFAULT_BRANCH_SLUG_MAX_LENGTH = 24
const MIN_BRANCH_SLUG_MAX_LENGTH = 4
const SHELL_FOREGROUND_COMMANDS = new Set(["bash", "dash", "fish", "sh", "zsh"])

const JsonValueSchema: Schema.Schema<JsonValue> = Schema.suspend(() =>
	Schema.Union(
		Schema.String,
		Schema.Number,
		Schema.Boolean,
		Schema.Null,
		Schema.Array(JsonValueSchema),
		Schema.Record({ key: Schema.String, value: JsonValueSchema }),
	),
)
const JsonObjectSchema: Schema.Schema<JsonObject> = Schema.Record({
	key: Schema.String,
	value: JsonValueSchema,
})
const ToolSettingsJsonSchema = Schema.parseJson(JsonObjectSchema)
const BranchNameMapJsonSchema = Schema.parseJson(
	Schema.Record({ key: Schema.String, value: Schema.String }),
)

type DaemonSessionState = BackendDaemonSessionState

type PathOps = Pick<Path.Path, "basename" | "dirname" | "join">

interface DaemonWorktreeRecord {
	readonly path: string
	readonly branch: string | null
}

interface EnsureWorktreeRequest {
	readonly issueId: string
	readonly issueTitle: string
	readonly projectPath: string
	readonly cliTool: CliToolName
	readonly copyPaths: readonly string[]
	readonly preCompactEnabled: boolean
	readonly branchSlugMaxLength?: number
	readonly sourceWorktreePath?: string
	readonly baseBranch?: string
}

interface EnsureWorktreeResult {
	readonly path: string
	readonly branch: string
}

interface EnsureSessionWindowRequest {
	readonly sessionName: string
	readonly worktreePath: string
	readonly projectPath: string
	readonly shell: string
	readonly tmuxPrefix: string
	readonly initCommands: readonly string[]
	readonly backgroundTasks: readonly string[]
	readonly command: string
}

export class DaemonSessionError extends Data.TaggedError("DaemonSessionError")<{
	readonly reason:
		| "not-found"
		| "invalid-state"
		| "worktree-missing"
		| "session-limit"
		| "session-metadata"
		| "tracker"
		| "git"
		| "tmux"
		| "config"
	readonly message: string
	readonly issueId?: string
	readonly currentState?: DaemonSessionState
	readonly expectedState?: DaemonSessionState
	readonly worktreePath?: string
}> {}

export interface DaemonSessionMutationRequest {
	readonly issueId: string
	readonly projectPath: string
	readonly initialPrompt?: string
	readonly imagePaths?: readonly string[]
	readonly sessionEnv?: Readonly<Record<string, string>>
	readonly dangerouslySkipPermissions?: boolean
}

export interface DaemonSessionServiceApi {
	readonly start: (
		request: DaemonSessionMutationRequest,
	) => Effect.Effect<BackendDaemonSessionSnapshot, DaemonSessionError>
	readonly stop: (
		request: DaemonSessionMutationRequest,
	) => Effect.Effect<BackendDaemonSessionSnapshot, DaemonSessionError>
	readonly pause: (
		request: DaemonSessionMutationRequest,
	) => Effect.Effect<BackendDaemonSessionSnapshot, DaemonSessionError>
	readonly resume: (
		request: DaemonSessionMutationRequest,
	) => Effect.Effect<BackendDaemonSessionSnapshot, DaemonSessionError>
	readonly recover: (
		request: DaemonSessionMutationRequest,
	) => Effect.Effect<BackendDaemonSessionSnapshot, DaemonSessionError>
}

const normalizeBranchSlugMaxLength = (value?: number): number => {
	if (typeof value !== "number" || !Number.isFinite(value)) {
		return DEFAULT_BRANCH_SLUG_MAX_LENGTH
	}

	const normalized = Math.floor(value)
	return normalized >= MIN_BRANCH_SLUG_MAX_LENGTH ? normalized : DEFAULT_BRANCH_SLUG_MAX_LENGTH
}

const slugifyIssueTitleForBranch = (title: string, maxLength: number): string => {
	const base = title
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "")

	if (base.length === 0) {
		return "task"
	}

	return base.slice(0, maxLength).replace(/-+$/g, "") || "task"
}

const sanitizeBranchAuthor = (value: string): string =>
	value
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "")
		.trim()

const sanitizeIssueIdForBranchSegment = (value: string): string => {
	const normalized = value
		.toLowerCase()
		.trim()
		.replace(/[^a-z0-9._-]+/g, "-")
		.replace(/^-+|-+$/g, "")

	return normalized.length > 0 ? normalized : "issue"
}

const composeIssueBranchName = (author: string, issueId: string, slug: string): string =>
	`${author}/${sanitizeIssueIdForBranchSegment(issueId)}/${slug}`

const quoteForShellSingleString = (value: string): string => `'${value.replace(/'/g, `'"'"'`)}'`

const sanitizeTmuxOptionValue = (value: string): string => value.replace(/\s+/g, " ").trim()

const buildInitWaitCommand = (sessionName: string): string => {
	const showDoneCommand = `tmux show-option -t ${sessionName} -v ${INIT_DONE_OPTION} 2>/dev/null`
	return `until [ "$(${showDoneCommand})" = "1" ]; do sleep 1; done`
}

const buildInitCompletionMarkerCommand = (sessionName: string): string =>
	`tmux set-option -t ${sessionName} ${INIT_DONE_OPTION} 1`

const buildGuardedInitCommand = (sessionName: string, initCommand: string): string => {
	const failedCheck = `$(tmux show-option -t ${sessionName} -v ${INIT_FAILED_OPTION} 2>/dev/null)`
	const sanitizedCommand = sanitizeTmuxOptionValue(initCommand)
	const commandLabel = quoteForShellSingleString(sanitizedCommand)
	const failureMessage = quoteForShellSingleString(
		`[azedarach] init command failed: ${sanitizedCommand}. Session startup blocked. Inspect this pane and retry.`,
	)

	return [
		`if [ "${failedCheck}" != "1" ]; then`,
		`${initCommand}; az_init_ec=$?;`,
		`if [ "$az_init_ec" -ne 0 ]; then`,
		`tmux set-option -t ${sessionName} ${INIT_FAILED_OPTION} 1;`,
		`tmux set-option -t ${sessionName} ${INIT_FAILED_COMMAND_OPTION} ${commandLabel};`,
		`tmux set-option -t ${sessionName} ${INIT_DONE_OPTION} 1;`,
		`tmux set-option -t ${sessionName} ${INIT_FAILED_STATUS_OPTION} waiting;`,
		`printf '%s\\n' ${failureMessage};`,
		"fi;",
		"fi",
	].join(" ")
}

const sendBlockedInitMessage = (sessionName: string): string => {
	const blockedMessage = quoteForShellSingleString(
		"[azedarach] Session startup blocked because init failed. Fix the issue above and restart the session.",
	)
	return [
		`tmux set-option -t ${sessionName} ${INIT_FAILED_STATUS_OPTION} waiting`,
		`; printf '%s\\n' ${blockedMessage}`,
		`; printf 'Failed init command: %s\\n' "$(tmux show-option -t ${sessionName} -v ${INIT_FAILED_COMMAND_OPTION} 2>/dev/null)"`,
	].join("")
}

const getWorktreePath = (projectPath: string, issueId: string, pathService: PathOps): string =>
	pathService.join(
		pathService.dirname(projectPath),
		`${pathService.basename(projectPath)}-${issueId}`,
	)

const readParentEpicId = (issue: TrackedIssue): string | undefined =>
	issue.dependencies?.find((dependency) => dependency.dependency_type === "parent-child")?.id

const isLiveForegroundCommand = (command: string | null): boolean =>
	command !== null && !SHELL_FOREGROUND_COMMANDS.has(command)

export class DaemonSessionService extends Effect.Service<DaemonSessionService>()(
	"DaemonSessionService",
	{
		dependencies: [
			AppConfig.Default,
			TrackerIssueDaemonService.Default,
			BackendDaemonSessionRecovery.Default,
		],
		effect: Effect.gen(function* () {
			const appConfig = yield* AppConfig
			const issues: TrackerIssueDaemonServiceApi = yield* TrackerIssueDaemonService
			const sessionRecovery: BackendDaemonSessionRecoveryApi = yield* BackendDaemonSessionRecovery
			const executor = yield* CommandExecutor.CommandExecutor
			const fs = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path

			const mapConfigError = (issueId: string | undefined, message: string) =>
				new DaemonSessionError({
					reason: "config",
					message,
					issueId,
				})

			const mapGitError = (issueId: string | undefined, message: string) =>
				new DaemonSessionError({
					reason: "git",
					message,
					issueId,
				})

			const mapTmuxError = (issueId: string | undefined, message: string) =>
				new DaemonSessionError({
					reason: "tmux",
					message,
					issueId,
				})

			const mapTrackerError = (issueId: string | undefined, message: string) =>
				new DaemonSessionError({
					reason: "tracker",
					message,
					issueId,
				})

			const mapSessionMetadataError = (issueId: string | undefined, message: string) =>
				new DaemonSessionError({
					reason: "session-metadata",
					message,
					issueId,
				})

			const runTmux = (issueId: string | undefined, args: ReadonlyArray<string>) =>
				executor
					.string(Command.make("tmux", ...args))
					.pipe(Effect.mapError((error) => mapTmuxError(issueId, String(error))))

			const tmuxExitCode = (args: ReadonlyArray<string>) =>
				executor.exitCode(Command.make("tmux", ...args)).pipe(Effect.orElseSucceed(() => 1))

			const runGit = (
				issueId: string | undefined,
				projectPath: string,
				args: ReadonlyArray<string>,
			) =>
				executor
					.string(Command.make("git", ...args).pipe(Command.workingDirectory(projectPath)))
					.pipe(Effect.mapError((error) => mapGitError(issueId, String(error))))

			const gitExitCode = (projectPath: string, args: ReadonlyArray<string>) =>
				executor
					.exitCode(Command.make("git", ...args).pipe(Command.workingDirectory(projectPath)))
					.pipe(Effect.orElseSucceed(() => 1))

			const readBranchMap = (projectPath: string) =>
				Effect.gen(function* () {
					const mapPath = pathService.join(projectPath, ".azedarach", "branch-name-map.json")
					const exists = yield* fs.exists(mapPath).pipe(Effect.orElseSucceed(() => false))
					if (!exists) {
						return {}
					}

					const content = yield* fs.readFileString(mapPath).pipe(Effect.orElseSucceed(() => "{}"))
					return yield* Schema.decode(BranchNameMapJsonSchema)(content).pipe(
						Effect.orElseSucceed(() => ({})),
					)
				})

			const writeBranchMap = (projectPath: string, branchMap: Readonly<Record<string, string>>) =>
				Effect.gen(function* () {
					const mapPath = pathService.join(projectPath, ".azedarach", "branch-name-map.json")
					yield* fs
						.makeDirectory(pathService.dirname(mapPath), { recursive: true })
						.pipe(Effect.ignore)
					yield* fs
						.writeFileString(mapPath, `${JSON.stringify(branchMap, null, "\t")}\n`)
						.pipe(Effect.ignore)
				})

			const getBranchAuthor = (projectPath: string) =>
				runGit(undefined, projectPath, ["config", "user.name"]).pipe(
					Effect.map((value) => sanitizeBranchAuthor(value.trim())),
					Effect.orElseSucceed(() => sanitizeBranchAuthor(process.env.USER ?? "") || "author"),
				)

			const branchExistsAnywhere = (projectPath: string, branchName: string) =>
				Effect.gen(function* () {
					const localExists =
						(yield* gitExitCode(projectPath, [
							"show-ref",
							"--verify",
							"--quiet",
							`refs/heads/${branchName}`,
						])) === 0
					if (localExists) {
						return true
					}

					return (
						(yield* gitExitCode(projectPath, [
							"show-ref",
							"--verify",
							"--quiet",
							`refs/remotes/origin/${branchName}`,
						])) === 0
					)
				})

			const getCurrentBranch = (issueId: string, projectPath: string) =>
				runGit(issueId, projectPath, ["rev-parse", "--abbrev-ref", "HEAD"]).pipe(
					Effect.map((value) => value.trim()),
				)

			const listWorktrees = (issueId: string | undefined, projectPath: string) =>
				runGit(issueId, projectPath, ["worktree", "list", "--porcelain"]).pipe(
					Effect.map((output) => {
						const worktrees: Array<DaemonWorktreeRecord> = []
						const lines = output.split("\n")
						let currentPath: string | null = null
						let currentBranch: string | null = null
						for (const line of lines) {
							if (line.startsWith("worktree ")) {
								if (currentPath !== null) {
									worktrees.push({ path: currentPath, branch: currentBranch })
								}
								currentPath = line.slice("worktree ".length).trim()
								currentBranch = null
								continue
							}
							if (line.startsWith("branch refs/heads/")) {
								currentBranch = line.slice("branch refs/heads/".length).trim()
							}
						}
						if (currentPath !== null) {
							worktrees.push({ path: currentPath, branch: currentBranch })
						}
						return worktrees
					}),
				)

			const allocateBranchName = (params: {
				readonly issueId: string
				readonly issueTitle: string
				readonly projectPath: string
				readonly branchSlugMaxLength?: number
			}) =>
				Effect.gen(function* () {
					const branchMap = yield* readBranchMap(params.projectPath)
					const existing = branchMap[params.issueId]
					if (existing !== undefined) {
						return existing
					}

					const author = yield* getBranchAuthor(params.projectPath)
					const slugMaxLength = normalizeBranchSlugMaxLength(params.branchSlugMaxLength)
					const baseSlug = slugifyIssueTitleForBranch(params.issueTitle, slugMaxLength)
					const reserved = new Set(Object.values(branchMap))

					for (let attempt = 0; attempt < 1000; attempt += 1) {
						const suffix = attempt === 0 ? "" : `-${attempt + 1}`
						const maxBaseLength = Math.max(1, slugMaxLength - suffix.length)
						const trimmedBase = baseSlug.slice(0, maxBaseLength).replace(/-+$/g, "") || "task"
						const candidate = composeIssueBranchName(
							author,
							params.issueId,
							`${trimmedBase}${suffix}`,
						)
						if (reserved.has(candidate)) {
							continue
						}
						const exists = yield* branchExistsAnywhere(params.projectPath, candidate)
						if (exists) {
							continue
						}
						const nextMap = { ...branchMap, [params.issueId]: candidate }
						yield* writeBranchMap(params.projectPath, nextMap)
						return candidate
					}

					return yield* Effect.fail(
						mapGitError(params.issueId, `Failed to allocate branch name for ${params.issueId}`),
					)
				})

			const copyConfiguredPaths = (
				sourceWorktreePath: string,
				targetWorktreePath: string,
				copyPaths: readonly string[],
			) =>
				Effect.forEach(
					copyPaths,
					(relativePath) =>
						Effect.gen(function* () {
							const sourcePath = pathService.join(sourceWorktreePath, relativePath)
							const targetPath = pathService.join(targetWorktreePath, relativePath)
							const exists = yield* fs.exists(sourcePath).pipe(Effect.orElseSucceed(() => false))
							if (!exists) {
								return
							}
							yield* fs
								.makeDirectory(pathService.dirname(targetPath), { recursive: true })
								.pipe(Effect.ignore)
							yield* fs.copy(sourcePath, targetPath).pipe(Effect.ignore)
						}),
					{ concurrency: "unbounded", discard: true },
				).pipe(Effect.orElseSucceed(() => undefined))

			const injectIssueIdIntoEnvLocal = (targetWorktreePath: string, issueId: string) =>
				Effect.gen(function* () {
					const envLocalPath = pathService.join(targetWorktreePath, ".env.local")
					const exists = yield* fs.exists(envLocalPath).pipe(Effect.orElseSucceed(() => false))
					const existingContent = exists
						? yield* fs.readFileString(envLocalPath).pipe(Effect.orElseSucceed(() => ""))
						: ""
					const issueLine = `AZEDARACH_ISSUE_ID=${issueId.trim()}`
					const lines = existingContent.length > 0 ? existingContent.split(/\r?\n/) : []
					let replaced = false
					const nextLines = lines.map((line) => {
						if (!replaced && line.startsWith("AZEDARACH_ISSUE_ID=")) {
							replaced = true
							return issueLine
						}
						return line
					})
					if (!replaced) {
						nextLines.push(issueLine)
					}
					const compacted = nextLines.filter(
						(line, index, all) => !(index === all.length - 1 && line.length === 0),
					)
					yield* fs.writeFileString(envLocalPath, `${compacted.join("\n")}\n`).pipe(Effect.ignore)
				}).pipe(Effect.orElseSucceed(() => undefined))

			const copyClaudeLocalSettings = (params: {
				readonly issueId: string
				readonly sourceProjectPath: string
				readonly targetWorktreePath: string
				readonly projectPath: string
				readonly preCompactEnabled: boolean
			}) =>
				Effect.gen(function* () {
					const sourceSettingsPath = pathService.join(
						params.sourceProjectPath,
						".claude",
						"settings.local.json",
					)
					const targetDir = pathService.join(params.targetWorktreePath, ".claude")
					const targetSettingsPath = pathService.join(targetDir, "settings.local.json")
					yield* fs.makeDirectory(targetDir, { recursive: true }).pipe(Effect.ignore)

					const hasSourceSettings = yield* fs
						.exists(sourceSettingsPath)
						.pipe(Effect.orElseSucceed(() => false))
					const existingSettings = hasSourceSettings
						? yield* fs.readFileString(sourceSettingsPath).pipe(
								Effect.flatMap((content) => Schema.decode(ToolSettingsJsonSchema)(content)),
								Effect.orElseSucceed(() => ({})),
							)
						: {}
					const hookConfig = yield* Schema.decodeUnknown(JsonObjectSchema)(
						generateHookConfig(params.issueId, {
							projectPath: params.projectPath,
							preCompactEnabled: params.preCompactEnabled,
						}),
					).pipe(Effect.orElseSucceed(() => ({})))
					const mergedSettings = deepMerge(existingSettings, hookConfig)
					yield* fs
						.writeFileString(targetSettingsPath, `${JSON.stringify(mergedSettings, null, "\t")}\n`)
						.pipe(Effect.ignore)
				}).pipe(Effect.orElseSucceed(() => undefined))

			const prepareWorktreeTooling = (params: {
				readonly cliTool: CliToolName
				readonly issueId: string
				readonly sourceProjectPath: string
				readonly targetWorktreePath: string
				readonly projectPath: string
				readonly preCompactEnabled: boolean
			}) => {
				if (params.cliTool !== "claude") {
					return Effect.void
				}

				return copyClaudeLocalSettings({
					issueId: params.issueId,
					sourceProjectPath: params.sourceProjectPath,
					targetWorktreePath: params.targetWorktreePath,
					projectPath: params.projectPath,
					preCompactEnabled: params.preCompactEnabled,
				})
			}

			const tmuxHasSession = (sessionName: string) =>
				tmuxExitCode(["has-session", "-t", sessionName]).pipe(
					Effect.map((exitCode) => exitCode === 0),
				)

			const tmuxHasWindow = (target: string) =>
				tmuxExitCode(["list-windows", "-t", target]).pipe(Effect.map((exitCode) => exitCode === 0))

			const tmuxGetOption = (sessionName: string, key: string) =>
				runTmux(undefined, ["show-option", "-t", sessionName, "-v", key]).pipe(
					Effect.map((value) => {
						const trimmed = value.trim()
						return trimmed.length === 0 ? Option.none<string>() : Option.some(trimmed)
					}),
					Effect.orElseSucceed(() => Option.none<string>()),
				)

			const tmuxSetOption = (sessionName: string, key: string, value: string) =>
				runTmux(undefined, ["set-option", "-t", sessionName, key, value]).pipe(Effect.asVoid)

			const tmuxSendLiteralCommand = (target: string, command: string) =>
				Effect.gen(function* () {
					yield* runTmux(undefined, ["send-keys", "-t", target, "-l", command])
					yield* runTmux(undefined, ["send-keys", "-t", target, "Enter"])
				}).pipe(Effect.asVoid)

			const tmuxGetPaneCurrentCommand = (target: string) =>
				runTmux(undefined, ["display-message", "-t", target, "-p", "#{pane_current_command}"]).pipe(
					Effect.map((value) => {
						const trimmed = value.trim().toLowerCase()
						return trimmed.length === 0 ? null : trimmed
					}),
					Effect.orElseSucceed(() => null),
				)

			const waitForTmuxOption = (sessionName: string, optionKey: string) =>
				Effect.retry(
					Effect.gen(function* () {
						const option = yield* tmuxGetOption(sessionName, optionKey)
						if (Option.isNone(option) || option.value !== "1") {
							return yield* Effect.fail(
								mapTmuxError(undefined, `Timed out waiting for tmux option ${optionKey}`),
							)
						}
					}),
					{ times: 300, schedule: Schedule.spaced("200 millis") },
				)

			const waitForShellReady = (target: string, markerKey: string) =>
				Effect.gen(function* () {
					yield* Effect.sleep("500 millis")
					const sessionName = target.split(":")[0] ?? target
					yield* tmuxSendLiteralCommand(target, `tmux set-option -t ${sessionName} ${markerKey} 1`)
					yield* waitForTmuxOption(sessionName, markerKey)
				})

			const sendCommandIfInitSucceeded = (sessionName: string, target: string, command: string) =>
				Effect.gen(function* () {
					const initFailed = yield* tmuxGetOption(sessionName, INIT_FAILED_OPTION)
					if (Option.isSome(initFailed) && initFailed.value === "1") {
						yield* tmuxSendLiteralCommand(target, sendBlockedInitMessage(sessionName))
						return false
					}
					yield* tmuxSendLiteralCommand(target, command)
					return true
				})

			const tmuxNewSession = (params: {
				readonly sessionName: string
				readonly worktreePath: string
				readonly projectPath: string
				readonly shell: string
				readonly tmuxPrefix: string
			}) =>
				Effect.gen(function* () {
					yield* runTmux(undefined, [
						"new-session",
						"-d",
						"-s",
						params.sessionName,
						"-c",
						params.worktreePath,
						`${params.shell} -i`,
					])
					yield* runTmux(undefined, ["rename-window", "-t", `${params.sessionName}:0`, WINDOW_CODE])
					yield* runTmux(undefined, [
						"set-option",
						"-t",
						params.sessionName,
						"prefix",
						params.tmuxPrefix,
					])
					yield* runTmux(undefined, ["set-option", "-t", params.sessionName, "prefix2", "None"])
					yield* runTmux(undefined, [
						"set-option",
						"-t",
						params.sessionName,
						"history-limit",
						"500000",
					])
					yield* runTmux(undefined, ["set-option", "-t", params.sessionName, "mode-keys", "vi"])
					yield* runTmux(undefined, [
						"set-option",
						"-t",
						params.sessionName,
						"remain-on-exit",
						"on",
					])
					yield* tmuxSetOption(params.sessionName, "@az_worktree", params.worktreePath)
					yield* tmuxSetOption(params.sessionName, "@az_project", params.projectPath)
					yield* waitForShellReady(params.sessionName, SHELL_READY_OPTION)
					yield* tmuxSetOption(params.sessionName, INIT_DONE_OPTION, "0")
					yield* tmuxSetOption(params.sessionName, INIT_FAILED_OPTION, "0")
					yield* tmuxSetOption(params.sessionName, INIT_FAILED_COMMAND_OPTION, "")
				}).pipe(Effect.asVoid)

			const spawnBackgroundTaskWindows = (params: {
				readonly sessionName: string
				readonly worktreePath: string
				readonly shell: string
				readonly initCommands: readonly string[]
				readonly backgroundTasks: readonly string[]
			}) =>
				Effect.forEach(
					params.backgroundTasks,
					(task, index) =>
						Effect.gen(function* () {
							const windowName = `task-${index + 1}`
							const target = `${params.sessionName}:${windowName}`
							yield* runTmux(undefined, [
								"new-window",
								"-t",
								params.sessionName,
								"-n",
								windowName,
								"-c",
								params.worktreePath,
								`${params.shell} -i`,
							])
							yield* waitForShellReady(target, `@az_task_ready_${index + 1}`)
							yield* tmuxSendLiteralCommand(target, buildInitWaitCommand(params.sessionName))
							for (const initCommand of params.initCommands) {
								yield* tmuxSendLiteralCommand(
									target,
									buildGuardedInitCommand(params.sessionName, initCommand),
								)
							}
							yield* runTmux(undefined, [
								"set-window-option",
								"-t",
								target,
								"remain-on-exit",
								"off",
							])
							yield* sendCommandIfInitSucceeded(params.sessionName, target, task)
						}),
					{ concurrency: "unbounded", discard: true },
				)

			const ensureSessionWindow = (params: EnsureSessionWindowRequest) =>
				Effect.gen(function* () {
					const hadSession = yield* tmuxHasSession(params.sessionName)
					if (!hadSession) {
						yield* tmuxNewSession({
							sessionName: params.sessionName,
							worktreePath: params.worktreePath,
							projectPath: params.projectPath,
							shell: params.shell,
							tmuxPrefix: params.tmuxPrefix,
						})
					}

					const codeTarget = `${params.sessionName}:${WINDOW_CODE}`
					const hadCodeWindow = yield* tmuxHasWindow(codeTarget)
					if (!hadCodeWindow) {
						yield* runTmux(undefined, [
							"new-window",
							"-t",
							params.sessionName,
							"-n",
							WINDOW_CODE,
							"-c",
							params.worktreePath,
							`${params.shell} -i`,
						])
						yield* waitForShellReady(codeTarget, `@az_window_ready_${WINDOW_CODE}`)
						yield* tmuxSendLiteralCommand(codeTarget, buildInitWaitCommand(params.sessionName))
						for (const initCommand of params.initCommands) {
							yield* tmuxSendLiteralCommand(
								codeTarget,
								buildGuardedInitCommand(params.sessionName, initCommand),
							)
						}
					}

					if (!hadSession) {
						for (const initCommand of params.initCommands) {
							yield* tmuxSendLiteralCommand(
								codeTarget,
								buildGuardedInitCommand(params.sessionName, initCommand),
							)
						}
						yield* tmuxSendLiteralCommand(
							codeTarget,
							buildInitCompletionMarkerCommand(params.sessionName),
						)
						yield* waitForTmuxOption(params.sessionName, INIT_DONE_OPTION)
						yield* spawnBackgroundTaskWindows({
							sessionName: params.sessionName,
							worktreePath: params.worktreePath,
							shell: params.shell,
							initCommands: params.initCommands,
							backgroundTasks: params.backgroundTasks,
						})
					}

					return yield* sendCommandIfInitSucceeded(params.sessionName, codeTarget, params.command)
				})

			const findTrackedSession = (issueId: string, projectPath: string) =>
				sessionRecovery.listActive(projectPath).pipe(
					Effect.mapError((error) => mapSessionMetadataError(issueId, error.message)),
					Effect.map((sessions) => sessions.find((session) => session.issueId === issueId)),
				)

			const requireTrackedSession = (request: DaemonSessionMutationRequest) =>
				findTrackedSession(request.issueId, request.projectPath).pipe(
					Effect.flatMap((session) =>
						session === undefined
							? Effect.fail(
									new DaemonSessionError({
										reason: "not-found",
										message: `Session not found for ${request.issueId}`,
										issueId: request.issueId,
									}),
								)
							: Effect.succeed(session),
					),
				)

			const ensureState = (params: {
				readonly session: BackendDaemonSessionSnapshot
				readonly expectedState: DaemonSessionState
				readonly operation: string
			}) =>
				params.session.state === params.expectedState
					? Effect.succeed(params.session)
					: Effect.fail(
							new DaemonSessionError({
								reason: "invalid-state",
								message: `${params.operation} requires session state ${params.expectedState}`,
								issueId: params.session.issueId,
								currentState: params.session.state,
								expectedState: params.expectedState,
							}),
						)

			const persistSession = (params: {
				readonly issueId: string
				readonly projectPath: string
				readonly tmuxSessionName: string
				readonly worktreePath: string | null
				readonly state: DaemonSessionState
				readonly startedAt?: string | null
			}) =>
				sessionRecovery
					.updateState({
						issueId: params.issueId,
						projectPath: params.projectPath,
						tmuxSessionName: params.tmuxSessionName,
						worktreePath: params.worktreePath,
						state: params.state,
						startedAt: params.startedAt,
					})
					.pipe(Effect.mapError((error) => mapSessionMetadataError(params.issueId, error.message)))

			const ensureWorktree = (params: EnsureWorktreeRequest) =>
				Effect.gen(function* () {
					const expectedPath = getWorktreePath(params.projectPath, params.issueId, pathService)
					const worktrees = yield* listWorktrees(params.issueId, params.projectPath)
					const existingWorktree = worktrees.find((worktree) => worktree.path === expectedPath)
					if (existingWorktree?.branch !== null && existingWorktree?.branch !== undefined) {
						return {
							path: expectedPath,
							branch: existingWorktree.branch,
						} satisfies EnsureWorktreeResult
					}

					const branchName = yield* allocateBranchName({
						issueId: params.issueId,
						issueTitle: params.issueTitle,
						projectPath: params.projectPath,
						branchSlugMaxLength: params.branchSlugMaxLength,
					})
					const branchExists = yield* branchExistsAnywhere(params.projectPath, branchName)
					if (branchExists) {
						yield* runGit(params.issueId, params.projectPath, [
							"worktree",
							"add",
							expectedPath,
							branchName,
						])
					} else {
						const baseBranch =
							params.baseBranch ?? (yield* getCurrentBranch(params.issueId, params.projectPath))
						yield* runGit(params.issueId, params.projectPath, [
							"worktree",
							"add",
							"-b",
							branchName,
							expectedPath,
							baseBranch,
						])
					}

					const sourceWorktreePath = params.sourceWorktreePath ?? params.projectPath
					yield* prepareWorktreeTooling({
						cliTool: params.cliTool,
						issueId: params.issueId,
						sourceProjectPath: sourceWorktreePath,
						targetWorktreePath: expectedPath,
						projectPath: params.projectPath,
						preCompactEnabled: params.preCompactEnabled,
					})
					yield* copyConfiguredPaths(sourceWorktreePath, expectedPath, params.copyPaths)
					yield* injectIssueIdIntoEnvLocal(expectedPath, params.issueId)

					return {
						path: expectedPath,
						branch: branchName,
					} satisfies EnsureWorktreeResult
				})

			const resolveSourceWorktree = (params: {
				readonly issue: TrackedIssue
				readonly projectPath: string
				readonly cliTool: CliToolName
				readonly copyPaths: readonly string[]
				readonly preCompactEnabled: boolean
				readonly branchSlugMaxLength?: number
			}) =>
				Effect.gen(function* () {
					const parentEpicId = readParentEpicId(params.issue)
					if (parentEpicId === undefined) {
						return { baseBranch: undefined, sourceWorktreePath: undefined } as const
					}

					const parentIssue = yield* issues
						.get(parentEpicId, params.projectPath)
						.pipe(Effect.mapError((error) => mapTrackerError(parentEpicId, error.message)))
					const parentWorktree = yield* ensureWorktree({
						issueId: parentEpicId,
						issueTitle: parentIssue.title,
						projectPath: params.projectPath,
						cliTool: params.cliTool,
						copyPaths: params.copyPaths,
						preCompactEnabled: params.preCompactEnabled,
						branchSlugMaxLength: params.branchSlugMaxLength,
					})

					return {
						baseBranch: parentWorktree.branch,
						sourceWorktreePath: parentWorktree.path,
					} as const
				})

			const buildSessionCommand = (params: {
				readonly issueId: string
				readonly sessionName: string
				readonly cliTool: CliToolName
				readonly model: string | undefined
				readonly initialPrompt?: string
				readonly imagePaths?: readonly string[]
				readonly sessionEnv?: Readonly<Record<string, string>>
				readonly dangerouslySkipPermissions: boolean
				readonly autoCompact: boolean
				readonly continueConversation: boolean
			}) => {
				const tool = getToolDefinition(params.cliTool)
				const sessionSettings = params.autoCompact
					? ({ autoCompactEnabled: true } satisfies JsonObject)
					: undefined
				return tool.buildCommand({
					issueId: params.issueId,
					sessionName: params.sessionName,
					model: params.model,
					initialPrompt: params.initialPrompt,
					imagePaths: params.imagePaths,
					sessionEnv: params.sessionEnv,
					dangerouslySkipPermissions: params.dangerouslySkipPermissions,
					sessionSettings,
					continueConversation: params.continueConversation,
				})
			}

			return {
				start: (request) =>
					Effect.gen(function* () {
						const existing = yield* findTrackedSession(request.issueId, request.projectPath)
						if (existing !== undefined && existing.state !== "idle") {
							return existing
						}

						const [
							issue,
							sessionConfig,
							worktreeConfig,
							hooksConfig,
							gitConfig,
							cliTool,
							modelConfig,
						] = yield* Effect.all([
							issues
								.get(request.issueId, request.projectPath)
								.pipe(Effect.mapError((error) => mapTrackerError(request.issueId, error.message))),
							appConfig.getSessionConfig(),
							appConfig.getWorktreeConfig(),
							appConfig.getHooksConfig(),
							appConfig.getGitConfig(),
							appConfig.getCliTool(),
							appConfig.getModelConfig(),
						])

						const activeSessions = yield* sessionRecovery
							.listActive(request.projectPath)
							.pipe(
								Effect.mapError((error) => mapSessionMetadataError(request.issueId, error.message)),
							)
						const maxSessions = sessionConfig.maxSessions ?? 10
						if (activeSessions.length >= maxSessions) {
							return yield* Effect.fail(
								new DaemonSessionError({
									reason: "session-limit",
									message: `Maximum session limit (${maxSessions}) reached`,
									issueId: request.issueId,
								}),
							)
						}

						const { baseBranch, sourceWorktreePath } = yield* resolveSourceWorktree({
							issue,
							projectPath: request.projectPath,
							cliTool,
							copyPaths: worktreeConfig.copyPaths,
							preCompactEnabled: hooksConfig.preCompact.enabled,
							branchSlugMaxLength: gitConfig.branchSlugMaxLength,
						})
						const worktree = yield* ensureWorktree({
							issueId: request.issueId,
							issueTitle: issue.title,
							projectPath: request.projectPath,
							cliTool,
							copyPaths: worktreeConfig.copyPaths,
							preCompactEnabled: hooksConfig.preCompact.enabled,
							branchSlugMaxLength: gitConfig.branchSlugMaxLength,
							baseBranch,
							sourceWorktreePath,
						})
						const sessionName = getIssueSessionName(request.issueId, request.projectPath)
						const hasLiveTmuxSession = yield* tmuxHasSession(sessionName)
						if (hasLiveTmuxSession) {
							return yield* persistSession({
								issueId: request.issueId,
								projectPath: request.projectPath,
								tmuxSessionName: sessionName,
								worktreePath: worktree.path,
								state: "busy",
								startedAt: existing?.startedAt ?? null,
							})
						}

						const tool = getToolDefinition(cliTool)
						const initCommands = [...worktreeConfig.initCommands, ...tool.getInitCommands()]
						const command = buildSessionCommand({
							issueId: request.issueId,
							sessionName,
							cliTool,
							model: modelConfig[cliTool].default ?? modelConfig.default,
							initialPrompt: request.initialPrompt,
							imagePaths: request.imagePaths,
							sessionEnv: request.sessionEnv,
							dangerouslySkipPermissions:
								request.dangerouslySkipPermissions ?? sessionConfig.dangerouslySkipPermissions,
							autoCompact: false,
							continueConversation: false,
						})
						const initialized = yield* ensureSessionWindow({
							sessionName,
							worktreePath: worktree.path,
							projectPath: request.projectPath,
							shell: sessionConfig.shell,
							tmuxPrefix: sessionConfig.tmuxPrefix,
							initCommands,
							backgroundTasks: sessionConfig.backgroundTasks,
							command,
						})
						if (issue.status !== "in_progress") {
							yield* issues
								.update(request.issueId, { status: "in_progress" }, request.projectPath)
								.pipe(Effect.orElseSucceed(() => undefined))
						}
						return yield* persistSession({
							issueId: request.issueId,
							projectPath: request.projectPath,
							tmuxSessionName: sessionName,
							worktreePath: worktree.path,
							state: initialized ? "initializing" : "waiting",
							startedAt: new Date().toISOString(),
						})
					}),
				stop: (request) =>
					Effect.gen(function* () {
						const existing = yield* findTrackedSession(request.issueId, request.projectPath)
						const sessionName = getIssueSessionName(request.issueId, request.projectPath)
						if (existing?.worktreePath !== null && existing?.worktreePath !== undefined) {
							yield* issues
								.sync(existing.worktreePath)
								.pipe(Effect.orElseSucceed(() => ({ pushed: 0, pulled: 0 })))
						}
						const hasLiveTmuxSession = yield* tmuxHasSession(sessionName)
						if (hasLiveTmuxSession) {
							yield* runTmux(request.issueId, ["kill-session", "-t", sessionName]).pipe(
								Effect.orElseSucceed(() => ""),
							)
						}

						if (existing !== undefined) {
							return yield* persistSession({
								issueId: request.issueId,
								projectPath: request.projectPath,
								tmuxSessionName: existing.tmuxSessionName,
								worktreePath: existing.worktreePath,
								state: "idle",
								startedAt: existing.startedAt,
							})
						}

						if (!hasLiveTmuxSession) {
							return yield* Effect.fail(
								new DaemonSessionError({
									reason: "not-found",
									message: `Session not found for ${request.issueId}`,
									issueId: request.issueId,
								}),
							)
						}

						return {
							issueId: request.issueId,
							projectPath: request.projectPath,
							tmuxSessionName: sessionName,
							worktreePath: getWorktreePath(request.projectPath, request.issueId, pathService),
							state: "idle",
							startedAt: null,
						}
					}),
				pause: (request) =>
					Effect.gen(function* () {
						const session = yield* requireTrackedSession(request)
						yield* runTmux(request.issueId, ["send-keys", "-t", session.tmuxSessionName, "C-c"])
						yield* Effect.sleep("500 millis")
						if (session.worktreePath !== null) {
							yield* issues
								.sync(session.worktreePath)
								.pipe(Effect.orElseSucceed(() => ({ pushed: 0, pulled: 0 })))
							yield* runGit(request.issueId, session.worktreePath, ["add", "-A"])
							const stagedDiffExitCode = yield* gitExitCode(session.worktreePath, [
								"diff",
								"--cached",
								"--quiet",
							])
							if (stagedDiffExitCode === 1) {
								yield* runGit(request.issueId, session.worktreePath, [
									"commit",
									"-m",
									"WIP: Paused session",
								])
							}
						}
						return yield* persistSession({
							issueId: request.issueId,
							projectPath: request.projectPath,
							tmuxSessionName: session.tmuxSessionName,
							worktreePath: session.worktreePath,
							state: "paused",
							startedAt: session.startedAt,
						})
					}),
				resume: (request) =>
					Effect.gen(function* () {
						const session = yield* requireTrackedSession(request)
						yield* ensureState({
							session,
							expectedState: "paused",
							operation: "resume",
						})
						return yield* persistSession({
							issueId: request.issueId,
							projectPath: request.projectPath,
							tmuxSessionName: session.tmuxSessionName,
							worktreePath: session.worktreePath,
							state: "busy",
							startedAt: session.startedAt,
						})
					}),
				recover: (request) =>
					Effect.gen(function* () {
						const session = yield* requireTrackedSession(request)
						yield* ensureState({
							session,
							expectedState: "crashed",
							operation: "recover",
						})
						if (session.worktreePath === null) {
							return yield* Effect.fail(
								new DaemonSessionError({
									reason: "worktree-missing",
									message: `Worktree missing for ${request.issueId}`,
									issueId: request.issueId,
								}),
							)
						}
						const worktreeExists = yield* fs
							.exists(session.worktreePath)
							.pipe(Effect.orElseSucceed(() => false))
						if (!worktreeExists) {
							return yield* Effect.fail(
								new DaemonSessionError({
									reason: "worktree-missing",
									message: `Worktree missing for ${request.issueId}`,
									issueId: request.issueId,
									worktreePath: session.worktreePath,
								}),
							)
						}

						const [sessionConfig, worktreeConfig, cliTool, modelConfig] = yield* Effect.all([
							appConfig.getSessionConfig(),
							appConfig.getWorktreeConfig(),
							appConfig.getCliTool(),
							appConfig.getModelConfig(),
						])
						const activeSessions = yield* sessionRecovery
							.listActive(request.projectPath)
							.pipe(
								Effect.mapError((error) => mapSessionMetadataError(request.issueId, error.message)),
							)
						const activeRecoverableSessions = activeSessions.filter(
							(activeSession) => activeSession.state !== "crashed",
						)
						const maxSessions = sessionConfig.maxSessions ?? 10
						if (activeRecoverableSessions.length >= maxSessions) {
							return yield* Effect.fail(
								new DaemonSessionError({
									reason: "session-limit",
									message: `Maximum session limit (${maxSessions}) reached`,
									issueId: request.issueId,
								}),
							)
						}

						const currentCommand = yield* tmuxGetPaneCurrentCommand(
							`${session.tmuxSessionName}:${WINDOW_CODE}`,
						)
						if (isLiveForegroundCommand(currentCommand)) {
							return yield* persistSession({
								issueId: request.issueId,
								projectPath: request.projectPath,
								tmuxSessionName: session.tmuxSessionName,
								worktreePath: session.worktreePath,
								state: "busy",
								startedAt: session.startedAt,
							})
						}

						const tool = getToolDefinition(cliTool)
						const initCommands = [...worktreeConfig.initCommands, ...tool.getInitCommands()]
						const command = buildSessionCommand({
							issueId: request.issueId,
							sessionName: session.tmuxSessionName,
							cliTool,
							model: modelConfig[cliTool].default ?? modelConfig.default,
							dangerouslySkipPermissions: sessionConfig.dangerouslySkipPermissions,
							autoCompact: false,
							continueConversation: true,
						})
						const initialized = yield* ensureSessionWindow({
							sessionName: session.tmuxSessionName,
							worktreePath: session.worktreePath,
							projectPath: request.projectPath,
							shell: sessionConfig.shell,
							tmuxPrefix: sessionConfig.tmuxPrefix,
							initCommands,
							backgroundTasks: sessionConfig.backgroundTasks,
							command,
						})
						return yield* persistSession({
							issueId: request.issueId,
							projectPath: request.projectPath,
							tmuxSessionName: session.tmuxSessionName,
							worktreePath: session.worktreePath,
							state: initialized ? "initializing" : "waiting",
							startedAt: new Date().toISOString(),
						})
					}),
			} satisfies DaemonSessionServiceApi
		}),
	},
) {}
