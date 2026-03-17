/**
 * Task CRUD Atoms
 *
 * Handles task creation, deletion, movement, and editing.
 */

import { Command } from "@effect/platform"
import { Data, Effect, Option, Schema } from "effect"
import { AppConfig } from "../../config/index.js"
import type { CliTool } from "../../config/schema.js"
import { IssueEditorService } from "../../core/IssueEditorService.js"
import { resolveIssueCreateImplementations } from "../../core/IssueImplementations.js"
import { stripAnsi } from "../../lib/ansi.js"
import type { DaemonRpcClientApi } from "../../rpc/DaemonRpcClient.js"
import { DaemonRpcClient } from "../../rpc/DaemonRpcClient.js"
import type { DaemonIssue, DaemonIssueStatus, DaemonIssueType } from "../../rpc/DaemonRpcSchemas.js"
import { BoardService } from "../../services/BoardService.js"
import { formatForToast } from "../../services/ErrorFormatter.js"
import { NavigationService } from "../../services/NavigationService.js"
import { OverlayService } from "../../services/OverlayService.js"
import { ProjectService } from "../../services/ProjectService.js"
import { ToastService } from "../../services/ToastService.js"
import type { TaskWithSession } from "../types.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Schema for AI response parsing
// ============================================================================

const AITaskResponseSchema = Schema.Struct({
	title: Schema.String,
	type: Schema.optional(Schema.String),
	priority: Schema.optional(Schema.Number),
	description: Schema.optional(Schema.String),
})

type AITaskResponse = Schema.Schema.Type<typeof AITaskResponseSchema>

const decodeAIResponseJson = Schema.decode(Schema.parseJson(AITaskResponseSchema))

class AITaskCreateError extends Data.TaggedError("AITaskCreateError")<{
	readonly message: string
}> {}

class MissingDaemonIssueRpcError extends Data.TaggedError("MissingDaemonIssueRpcError")<{
	readonly method: string
}> {}

type DaemonIssueRpcMethod =
	| "issueCreate"
	| "issueUpdate"
	| "issueDelete"
	| "issueEpicWithChildren"
	| "issueImplementationRegistry"

const requireDaemonIssueRpcClient = () =>
	Effect.serviceOption(DaemonRpcClient).pipe(
		Effect.flatMap(
			Option.match({
				onNone: () => Effect.fail(new MissingDaemonIssueRpcError({ method: "daemonRpcClient" })),
				onSome: Effect.succeed,
			}),
		),
	)

const requireDaemonIssueRpc = <TMethod extends DaemonIssueRpcMethod>(
	daemonRpcClient: DaemonRpcClientApi,
	method: TMethod,
) =>
	Effect.fromNullable(daemonRpcClient[method]).pipe(
		Effect.orElseFail(() => new MissingDaemonIssueRpcError({ method })),
	)

const parseDaemonIssueStatus = (status: string): DaemonIssueStatus | undefined => {
	switch (status) {
		case "open":
		case "in_progress":
		case "blocked":
		case "closed":
		case "tombstone":
			return status
		default:
			return undefined
	}
}

const parseDaemonIssueType = (issueType: string | undefined): DaemonIssueType | undefined => {
	switch (issueType) {
		case "bug":
		case "feature":
		case "task":
		case "epic":
		case "chore":
			return issueType
		default:
			return undefined
	}
}

const toBoardMutationIssue = (issue: DaemonIssue) => ({
	id: issue.id,
	title: issue.title,
	description: issue.description,
	status: issue.status,
	priority: issue.priority,
	issue_type: issue.issue_type,
	created_at: issue.created_at,
	updated_at: issue.updated_at,
	closed_at: issue.closed_at,
	assignee: issue.assignee,
	labels: issue.labels,
	design: issue.design,
	notes: issue.notes,
	acceptance: issue.acceptance,
	estimate: issue.estimate,
	implementations: issue.implementations,
	dependent_count: issue.dependent_count,
	dependency_count: issue.dependency_count,
})

const resolveIssueCreateImplementationsFromDaemon = (
	daemonRpcClient: DaemonRpcClientApi,
	params: { readonly requestedImplementations?: readonly string[] },
) =>
	Effect.gen(function* () {
		const issueImplementationRegistry = yield* requireDaemonIssueRpc(
			daemonRpcClient,
			"issueImplementationRegistry",
		)
		const registryResult = yield* issueImplementationRegistry()
		return resolveIssueCreateImplementations(registryResult.registry, {
			requestedImplementations: params.requestedImplementations,
		})
	})

const unwrapMarkdownJson = (value: string): string => {
	const trimmed = value.trim()
	const match = /^```(?:json)?\s*([\s\S]*?)\s*```$/i.exec(trimmed)
	return (match?.[1] ?? trimmed).trim()
}

const findBalancedJsonObject = (value: string): string | undefined => {
	for (let start = 0; start < value.length; start++) {
		if (value[start] !== "{") continue
		let depth = 0
		let inString = false
		let escaped = false
		for (let cursor = start; cursor < value.length; cursor++) {
			const char = value[cursor]
			if (inString) {
				if (escaped) {
					escaped = false
					continue
				}
				if (char === "\\") {
					escaped = true
					continue
				}
				if (char === '"') {
					inString = false
				}
				continue
			}
			if (char === '"') {
				inString = true
				continue
			}
			if (char === "{") {
				depth += 1
				continue
			}
			if (char === "}") {
				depth -= 1
				if (depth === 0) {
					const candidate = value.slice(start, cursor + 1)
					try {
						JSON.parse(candidate)
						return candidate
					} catch {
						break
					}
				}
			}
		}
	}
	return undefined
}

export const extractJsonPayload = (rawOutput: string): string => {
	const normalized = unwrapMarkdownJson(stripAnsi(rawOutput))
	try {
		JSON.parse(normalized)
		return normalized
	} catch {
		const balanced = findBalancedJsonObject(normalized)
		if (balanced !== undefined) {
			return balanced
		}
		throw new AITaskCreateError({
			message: `Failed to find JSON object in AI output\nRaw output: ${rawOutput}`,
		})
	}
}

export const buildAiCreateCommand = (params: {
	readonly cliTool: CliTool
	readonly prompt: string
	readonly model: string
}): Readonly<{ executable: string; args: ReadonlyArray<string> }> => {
	switch (params.cliTool) {
		case "claude":
			return {
				executable: "claude",
				args: ["-p", params.prompt, "--model", params.model, "--output-format", "text"],
			}
		case "opencode":
			return {
				executable: "opencode",
				args: ["run", "--model", params.model, params.prompt],
			}
		case "codex":
			return {
				executable: "codex",
				args: ["exec", "--model", params.model, "--color", "never", params.prompt],
			}
	}
}

// ============================================================================
// Task Movement Atoms
// ============================================================================

/**
 * Move a task to a new status
 *
 * Usage: const moveTask = useAtomSet(moveTaskAtom, { mode: "promise" })
 *        await moveTask({ taskId: "az-123", newStatus: "in_progress" })
 */
export const moveTaskAtom = appRuntime.fn(
	({ taskId, newStatus }: { taskId: string; newStatus: string }) =>
		Effect.gen(function* () {
			const daemonRpcClient = yield* requireDaemonIssueRpcClient()
			const issueUpdate = yield* requireDaemonIssueRpc(daemonRpcClient, "issueUpdate")
			const status = parseDaemonIssueStatus(newStatus)
			if (status === undefined) {
				return yield* Effect.fail(
					new Error(`Invalid task status for daemon issueUpdate: ${newStatus}`),
				)
			}
			yield* issueUpdate({
				issueId: taskId,
				fields: { status },
			})
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Move multiple tasks at once
 */
export const moveTasksAtom = appRuntime.fn(
	({ taskIds, newStatus }: { taskIds: string[]; newStatus: string }) =>
		Effect.gen(function* () {
			const daemonRpcClient = yield* requireDaemonIssueRpcClient()
			const issueUpdate = yield* requireDaemonIssueRpc(daemonRpcClient, "issueUpdate")
			const status = parseDaemonIssueStatus(newStatus)
			if (status === undefined) {
				return yield* Effect.fail(
					new Error(`Invalid task status for daemon issueUpdate: ${newStatus}`),
				)
			}
			yield* Effect.all(
				taskIds.map((id) =>
					issueUpdate({
						issueId: id,
						fields: { status },
					}),
				),
				{ concurrency: "unbounded" },
			)
		}).pipe(Effect.catchAll(Effect.logError)),
)

// ============================================================================
// Task Creation Atoms
// ============================================================================

/**
 * Create a new task with full orchestration
 *
 * Handles the complete create flow: dismiss overlay, create issue, refresh board,
 * navigate to new task, show toast. All logic in Effects, not React callbacks.
 *
 * Usage: const createTask = useAtomSet(createTaskAtom, { mode: "promise" })
 *        await createTask({ title: "New task", type: "task", priority: 2 })
 */
export const createTaskAtom = appRuntime.fn(
	(params: {
		title: string
		type?: string
		priority?: number
		description?: string
		implementations?: readonly string[]
	}) =>
		Effect.gen(function* () {
			const daemonRpcClient = yield* requireDaemonIssueRpcClient()
			const issueCreate = yield* requireDaemonIssueRpc(daemonRpcClient, "issueCreate")
			const board = yield* BoardService
			const navigation = yield* NavigationService
			const toast = yield* ToastService
			const overlay = yield* OverlayService
			const projectService = yield* ProjectService
			const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

			yield* overlay.pop()

			const implementations = yield* resolveIssueCreateImplementationsFromDaemon(daemonRpcClient, {
				requestedImplementations: params.implementations,
			})
			const issueCreateResult = yield* issueCreate({
				title: params.title,
				type: parseDaemonIssueType(params.type),
				priority: params.priority,
				description: params.description,
				implementations,
				cwd: projectPath,
			})
			const issue = toBoardMutationIssue(issueCreateResult.issue)

			yield* board.upsertIssueFromMutation(issue)
			yield* navigation.jumpToTask(issue.id)
			yield* toast.show("success", `Created task: ${issue.id}`)

			return issue
		}).pipe(Effect.tapError(Effect.logError)),
)

/**
 * Create a forked child task under a parent epic
 */
export const forkCreateChildAtom = appRuntime.fn(
	({
		parentEpicId,
		sourceTaskId,
		params,
	}: {
		parentEpicId: string
		sourceTaskId: string
		params: { title: string; type: string; priority: number; implementations?: readonly string[] }
	}) =>
		Effect.gen(function* () {
			const daemonRpcClient = yield* requireDaemonIssueRpcClient()
			const issueCreate = yield* requireDaemonIssueRpc(daemonRpcClient, "issueCreate")
			const issueUpdate = yield* requireDaemonIssueRpc(daemonRpcClient, "issueUpdate")
			const board = yield* BoardService
			const navigation = yield* NavigationService
			const toast = yield* ToastService
			const overlay = yield* OverlayService
			const projectService = yield* ProjectService
			const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

			yield* overlay.pop()

			const implementations = yield* resolveIssueCreateImplementationsFromDaemon(daemonRpcClient, {
				requestedImplementations: params.implementations,
			})
			const issueCreateResult = yield* issueCreate({
				title: params.title,
				type: parseDaemonIssueType(params.type) ?? "task",
				priority: params.priority,
				implementations,
				cwd: projectPath,
			})
			const issue = toBoardMutationIssue(issueCreateResult.issue)
			const linkResult = yield* issueUpdate({
				issueId: issue.id,
				fields: { parent: parentEpicId },
				cwd: projectPath,
			}).pipe(
				Effect.map(() => ({ linked: true as const })),
				Effect.catchAll((error) =>
					Effect.gen(function* () {
						yield* Effect.logWarning(
							`Fork create child created ${issue.id} but failed to link parent ${parentEpicId}: ${formatForToast(error)}`,
						)
						const toast = yield* ToastService
						yield* toast.show(
							"warning",
							`Created ${issue.id} but failed to link to epic ${parentEpicId}`,
						)
						return { linked: false as const }
					}),
				),
			)

			yield* board.upsertIssueFromMutation(issue, {
				parentEpicId: linkResult.linked ? parentEpicId : undefined,
			})
			yield* navigation.jumpToTask(issue.id)
			yield* toast.show("success", `Forked ${issue.id} from ${sourceTaskId}`)

			return issue
		}).pipe(
			Effect.catchAll((error) =>
				Effect.gen(function* () {
					yield* Effect.logError(error)
					const toast = yield* ToastService
					const formatted = formatForToast(error)
					yield* toast.show("error", `Fork failed: ${formatted}`)
					return "error" as const
				}),
			),
		),
)

/**
 * Create a new parent epic, reparent the source task, then prompt for child creation
 */
export const forkCreateEpicAtom = appRuntime.fn(
	({
		sourceTaskId,
		params,
	}: {
		sourceTaskId: string
		params: { title: string; type: string; priority: number; implementations?: readonly string[] }
	}) =>
		Effect.gen(function* () {
			const daemonRpcClient = yield* requireDaemonIssueRpcClient()
			const issueCreate = yield* requireDaemonIssueRpc(daemonRpcClient, "issueCreate")
			const issueUpdate = yield* requireDaemonIssueRpc(daemonRpcClient, "issueUpdate")
			const board = yield* BoardService
			const toast = yield* ToastService
			const overlay = yield* OverlayService
			const projectService = yield* ProjectService
			const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

			yield* overlay.pop()

			const implementations = yield* resolveIssueCreateImplementationsFromDaemon(daemonRpcClient, {
				requestedImplementations: params.implementations,
			})
			const epicCreateResult = yield* issueCreate({
				title: params.title,
				type: "epic",
				priority: params.priority,
				implementations,
				cwd: projectPath,
			})
			const epic = toBoardMutationIssue(epicCreateResult.issue)
			yield* board.upsertIssueFromMutation(epic)

			const reparentResult = yield* issueUpdate({
				issueId: sourceTaskId,
				fields: { parent: epic.id },
				cwd: projectPath,
			}).pipe(
				Effect.map(() => ({ reparented: true as const })),
				Effect.catchAll((error) =>
					Effect.gen(function* () {
						yield* Effect.logWarning(
							`Fork create epic created ${epic.id} but failed to reparent ${sourceTaskId}: ${formatForToast(error)}`,
						)
						const toast = yield* ToastService
						yield* toast.show(
							"warning",
							`Created epic ${epic.id} but failed to reparent ${sourceTaskId}`,
						)
						return { reparented: false as const }
					}),
				),
			)

			if (reparentResult.reparented) {
				yield* board.patchTaskFromMutation(sourceTaskId, {
					parentEpicId: epic.id,
					updated_at: new Date().toISOString(),
				})
			}

			yield* toast.show(
				"success",
				reparentResult.reparented
					? `Created epic ${epic.id} and reparented ${sourceTaskId}`
					: `Created epic ${epic.id}`,
			)

			yield* overlay.push({
				_tag: "create",
				title: "Create Forked Task",
				initial: {
					type: "task",
					priority: params.priority,
					implementations: epic.implementations,
				},
				context: { _tag: "forkChild", parentEpicId: epic.id, sourceTaskId },
			})
		}).pipe(
			Effect.catchAll((error) =>
				Effect.gen(function* () {
					yield* Effect.logError(error)
					const toast = yield* ToastService
					const formatted = formatForToast(error)
					yield* toast.show("error", `Fork failed: ${formatted}`)
					return "error" as const
				}),
			),
		),
)

/**
 * Edit a bead in $EDITOR
 *
 * Opens the issue in $EDITOR as structured markdown, parses changes on save,
 * and applies updates via tracker update.
 *
 * Usage: const editIssue = useAtomSet(editIssueViaEditorAtom, { mode: "promise" })
 *        await editIssue(task)
 */
export const editIssueViaEditorAtom = appRuntime.fn((issue: TaskWithSession) =>
	Effect.gen(function* () {
		const editor = yield* IssueEditorService
		yield* editor.editIssue(issue)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Create a new issue via $EDITOR
 *
 * Opens a template in $EDITOR, parses the result, and creates a new issue.
 *
 * Usage: const createIssue = useAtom(createIssueViaEditorAtom, { mode: "promise" })
 *        const { id, title } = await createIssue()
 */
export const createIssueViaEditorAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* IssueEditorService
		return yield* editor.createIssue()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Create an issue from natural language using the configured AI CLI
 *
 * Two-phase approach for reliability:
 * 1. AI tool extracts structured data (title, type, priority) from natural language
 * 2. We create the issue via daemon issue RPC
 *
 * This avoids the unreliability of AI tools executing CLI commands and parsing free-form output.
 *
 * Usage: const aiCreate = useAtom(aiCreateTaskAtom, { mode: "promise" })
 *        const issueId = await aiCreate("Add dark mode toggle to settings")
 */
export const aiCreateTaskAtom = appRuntime.fn((description: string) =>
	Effect.gen(function* () {
		const daemonRpcClient = yield* requireDaemonIssueRpcClient()
		const issueCreate = yield* requireDaemonIssueRpc(daemonRpcClient, "issueCreate")
		const board = yield* BoardService
		const navigation = yield* NavigationService
		const toast = yield* ToastService
		const overlay = yield* OverlayService
		const projectService = yield* ProjectService
		const appConfig = yield* AppConfig

		// Dismiss overlay first

		yield* overlay.pop()
		yield* toast.show("info", "Creating task with AI...")

		// Get current project path (or cwd if no project selected)
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

		// Phase 1: Ask the configured AI tool to extract structured task data
		// Using JSON output format for deterministic parsing
		const prompt = `Extract task information from this description and return ONLY a JSON object.

Description: "${description}"

Return a JSON object with these fields:
- "title": A concise task title (imperative form, e.g. "Add dark mode toggle")
- "type": One of "task", "bug", "feature", "chore" (task=general work, feature=new functionality, bug=fix, chore=maintenance)
- "priority": Number 1-4 (1=high, 2=medium, 3=low, 4=backlog)
- "description": Optional longer description if the input has details worth preserving (omit if redundant with title)

Example output:
{"title": "Add dark mode toggle to settings", "type": "feature", "priority": 2}

Return ONLY the JSON object, no explanation or markdown.`

		const cliTool = yield* appConfig.getCliTool()
		const modelConfig = yield* appConfig.getModelConfig()
		const toolModelConfig = modelConfig[cliTool]
		const chatModel =
			modelConfig.chat ??
			toolModelConfig.chat ??
			modelConfig.default ??
			toolModelConfig.default ??
			"haiku"

		const aiCommand = buildAiCreateCommand({
			cliTool,
			prompt,
			model: chatModel,
		})
		const aiCmd = Command.make(aiCommand.executable, ...aiCommand.args).pipe(
			Command.workingDirectory(projectPath),
		)

		const rawOutput = yield* Command.string(aiCmd).pipe(
			Effect.timeout("15 seconds"),
			Effect.mapError((e) => new AITaskCreateError({ message: `AI CLI failed: ${e}` })),
		)

		// Parse JSON output from potentially noisy CLI output.
		const cleanOutput = extractJsonPayload(rawOutput)

		// Parse and validate JSON with schema in one step.
		const parsed: AITaskResponse = yield* decodeAIResponseJson(cleanOutput).pipe(
			Effect.mapError(
				(e) =>
					new AITaskCreateError({
						message: `AI output parse/schema validation failed: ${e}\nRaw output: ${rawOutput}`,
					}),
			),
		)

		// Normalize type and priority
		const taskType = parseDaemonIssueType(parsed.type) ?? "task"
		const priority =
			typeof parsed.priority === "number" && parsed.priority >= 1 && parsed.priority <= 4
				? parsed.priority
				: 2

		// Phase 2: Create the issue via daemon issue RPC
		const implementations = yield* resolveIssueCreateImplementationsFromDaemon(daemonRpcClient, {})
		const issueCreateResult = yield* issueCreate({
			title: parsed.title,
			type: taskType,
			priority,
			description: parsed.description,
			implementations,
			cwd: projectPath,
		})
		const createdIssue = toBoardMutationIssue(issueCreateResult.issue)

		yield* board.upsertIssueFromMutation(createdIssue)
		yield* navigation.jumpToTask(createdIssue.id)
		yield* toast.show("success", `Created ${taskType}: ${createdIssue.id}`)

		return createdIssue.id
	}).pipe(
		Effect.catchAll((error) =>
			Effect.gen(function* () {
				yield* Effect.logError(error)
				const toast = yield* ToastService
				const formatted = formatForToast(error)
				yield* toast.show("error", `Create task failed: ${formatted}`)
				return "error" as const
			}),
		),
	),
)

// ============================================================================
// Task Deletion Atoms
// ============================================================================

/**
 * Delete an issue entirely
 *
 * Usage: const deleteIssue = useAtom(deleteIssueAtom, { mode: "promise" })
 *        await deleteIssue(issueId)
 */
export const deleteIssueAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const daemonRpcClient = yield* requireDaemonIssueRpcClient()
		const issueDelete = yield* requireDaemonIssueRpc(daemonRpcClient, "issueDelete")
		yield* issueDelete({ issueId })
	}).pipe(Effect.catchAll(Effect.logError)),
)

// ============================================================================
// Epic Children Atoms
// ============================================================================

/**
 * Get epic children for a task (only if task is an epic)
 *
 * Returns children array or empty array if not an epic or on error.
 * This is a parameterized atom factory that returns a new atom for each epicId.
 *
 * Usage: const epicChildren = useAtomSet(epicChildrenAtom(epicId), { mode: "promise" })
 *        const children = await epicChildren()
 */
export const epicChildrenAtom = (epicId: string) =>
	appRuntime.fn(() =>
		Effect.gen(function* () {
			const daemonRpcClient = yield* requireDaemonIssueRpcClient()
			const issueEpicWithChildren = yield* requireDaemonIssueRpc(
				daemonRpcClient,
				"issueEpicWithChildren",
			)
			const result = yield* issueEpicWithChildren({ epicId })
			return result.children
		}).pipe(
			Effect.catchAll((error) =>
				Effect.gen(function* () {
					yield* Effect.logError(error)
					return [] as const
				}),
			),
		),
	)
