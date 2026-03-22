/**
 * Task CRUD Atoms
 *
 * Handles task creation, deletion, movement, and editing.
 */

import { AppConfig, type CliTool } from "@azedarach/config"
import { resolveProjectPathFromContext } from "@azedarach/shared/project-path"
import { DaemonRpcClient, type TrackedIssueRelationshipRef } from "@azedarach/shared/rpc"
import { Command } from "@effect/platform"
import { Data, Effect, Schema } from "effect"
import type { DependencyRef, IssueType } from "../contracts.js"
import { stripAnsi } from "../lib/ansi.js"
import { TuiBoardStoreService } from "../services/TuiBoardStoreService.js"
import { getTuiProjectContextRead } from "../services/TuiProjectContextService.js"
import type { ColumnStatus, TaskWithSession } from "../types.js"
import {
	formatForToast,
	getIssueCreateImplementations,
	IssueEditorService,
	NavigationService,
	OverlayService,
	ToastService,
} from "../utils/runtimeServices.js"
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

const getCurrentProjectPath = Effect.gen(function* () {
	const projectContext = yield* getTuiProjectContextRead
	return yield* resolveProjectPathFromContext(projectContext)
})

const isIssueType = (value: string | undefined): value is IssueType =>
	value === "bug" ||
	value === "feature" ||
	value === "task" ||
	value === "epic" ||
	value === "chore"

export const resolveIssueType = (
	value: string | undefined,
	fallback: IssueType = "task",
): IssueType => (isIssueType(value) ? value : fallback)

const toEpicChildDependencyRef = (child: TrackedIssueRelationshipRef): DependencyRef => ({
	id: child.id,
	title: child.title,
	status: child.status,
	dependency_type: child.dependency_type,
	issue_type: child.issue_type,
})

export const toEpicChildDependencyRefs = (
	children: ReadonlyArray<TrackedIssueRelationshipRef> | undefined,
): ReadonlyArray<DependencyRef> =>
	(children ?? [])
		.filter((child) => child.dependency_type === "parent-child")
		.map((child) => toEpicChildDependencyRef(child))

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
	({ taskId, newStatus }: { taskId: string; newStatus: ColumnStatus }) =>
		Effect.gen(function* () {
			const daemonRpcClient = yield* DaemonRpcClient
			const board = yield* TuiBoardStoreService
			const projectPath = yield* getCurrentProjectPath
			yield* daemonRpcClient.issueUpdate({
				issueId: taskId,
				patch: { status: newStatus },
				projectPath,
			})
			yield* board.patchTaskFromMutation(taskId, {
				status: newStatus,
				updated_at: new Date().toISOString(),
			})
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Move multiple tasks at once
 */
export const moveTasksAtom = appRuntime.fn(
	({ taskIds, newStatus }: { taskIds: string[]; newStatus: ColumnStatus }) =>
		Effect.gen(function* () {
			const daemonRpcClient = yield* DaemonRpcClient
			const projectPath = yield* getCurrentProjectPath
			const board = yield* TuiBoardStoreService
			yield* Effect.forEach(
				taskIds,
				(taskId) =>
					daemonRpcClient
						.issueUpdate({
							issueId: taskId,
							patch: { status: newStatus },
							projectPath,
						})
						.pipe(
							Effect.zipRight(
								board.patchTaskFromMutation(taskId, {
									status: newStatus,
									updated_at: new Date().toISOString(),
								}),
							),
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
			const daemonRpcClient = yield* DaemonRpcClient
			const board = yield* TuiBoardStoreService
			const navigation = yield* NavigationService
			const toast = yield* ToastService
			const overlay = yield* OverlayService
			const projectPath = yield* getCurrentProjectPath

			yield* overlay.pop()

			const implementations = yield* getIssueCreateImplementations({
				requestedImplementations: params.implementations,
				cwd: projectPath,
			})
			const issueResult = yield* daemonRpcClient.issueCreate({
				projectPath,
				input: {
					title: params.title,
					type: resolveIssueType(params.type),
					priority: params.priority,
					description: params.description,
					implementations,
				},
			})

			yield* board.upsertIssueFromMutation(issueResult.issue)
			yield* navigation.jumpToTask(issueResult.issue.id)
			yield* toast.show("success", `Created task: ${issueResult.issue.id}`)

			return issueResult.issue
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
			const daemonRpcClient = yield* DaemonRpcClient
			const board = yield* TuiBoardStoreService
			const navigation = yield* NavigationService
			const toast = yield* ToastService
			const overlay = yield* OverlayService
			const projectPath = yield* getCurrentProjectPath

			yield* overlay.pop()

			const implementations = yield* getIssueCreateImplementations({
				requestedImplementations: params.implementations,
				cwd: projectPath,
			})
			const issueResult = yield* daemonRpcClient.issueCreate({
				projectPath,
				input: {
					title: params.title,
					type: resolveIssueType(params.type),
					priority: params.priority,
					implementations,
				},
			})
			const linkResult = yield* daemonRpcClient
				.issueUpdate({
					issueId: issueResult.issue.id,
					projectPath,
					patch: { parent: parentEpicId },
				})
				.pipe(
					Effect.map(() => ({ linked: true as const })),
					Effect.catchAll((error) =>
						Effect.gen(function* () {
							yield* Effect.logWarning(
								`Fork create child created ${issueResult.issue.id} but failed to link parent ${parentEpicId}: ${formatForToast(error)}`,
							)
							const toast = yield* ToastService
							yield* toast.show(
								"warning",
								`Created ${issueResult.issue.id} but failed to link to epic ${parentEpicId}`,
							)
							return { linked: false as const }
						}),
					),
				)

			yield* board.upsertIssueFromMutation(issueResult.issue, {
				parentEpicId: linkResult.linked ? parentEpicId : undefined,
			})
			yield* navigation.jumpToTask(issueResult.issue.id)
			yield* toast.show("success", `Forked ${issueResult.issue.id} from ${sourceTaskId}`)

			return issueResult.issue
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
			const daemonRpcClient = yield* DaemonRpcClient
			const board = yield* TuiBoardStoreService
			const toast = yield* ToastService
			const overlay = yield* OverlayService
			const projectPath = yield* getCurrentProjectPath

			yield* overlay.pop()

			const implementations = yield* getIssueCreateImplementations({
				requestedImplementations: params.implementations,
				cwd: projectPath,
			})
			const epicResult = yield* daemonRpcClient.issueCreate({
				projectPath,
				input: {
					title: params.title,
					type: "epic",
					priority: params.priority,
					implementations,
				},
			})
			yield* board.upsertIssueFromMutation(epicResult.issue)

			const reparentResult = yield* daemonRpcClient
				.issueUpdate({
					issueId: sourceTaskId,
					projectPath,
					patch: { parent: epicResult.issue.id },
				})
				.pipe(
					Effect.map(() => ({ reparented: true as const })),
					Effect.catchAll((error) =>
						Effect.gen(function* () {
							yield* Effect.logWarning(
								`Fork create epic created ${epicResult.issue.id} but failed to reparent ${sourceTaskId}: ${formatForToast(error)}`,
							)
							const toast = yield* ToastService
							yield* toast.show(
								"warning",
								`Created epic ${epicResult.issue.id} but failed to reparent ${sourceTaskId}`,
							)
							return { reparented: false as const }
						}),
					),
				)

			if (reparentResult.reparented) {
				yield* board.patchTaskFromMutation(sourceTaskId, {
					parentEpicId: epicResult.issue.id,
					updated_at: new Date().toISOString(),
				})
			}

			yield* toast.show(
				"success",
				reparentResult.reparented
					? `Created epic ${epicResult.issue.id} and reparented ${sourceTaskId}`
					: `Created epic ${epicResult.issue.id}`,
			)

			yield* overlay.push({
				_tag: "create",
				title: "Create Forked Task",
				initial: {
					type: "task",
					priority: params.priority,
					implementations: epicResult.issue.implementations,
				},
				context: { _tag: "forkChild", parentEpicId: epicResult.issue.id, sourceTaskId },
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
 * 2. We call daemon issue create directly via RPC
 *
 * This avoids the unreliability of AI tools executing CLI commands and parsing free-form output.
 *
 * Usage: const aiCreate = useAtom(aiCreateTaskAtom, { mode: "promise" })
 *        const issueId = await aiCreate("Add dark mode toggle to settings")
 */
export const aiCreateTaskAtom = appRuntime.fn((description: string) =>
	Effect.gen(function* () {
		const board = yield* TuiBoardStoreService
		const navigation = yield* NavigationService
		const toast = yield* ToastService
		const overlay = yield* OverlayService
		const daemonRpcClient = yield* DaemonRpcClient
		const projectContext = yield* getTuiProjectContextRead
		const appConfig = yield* AppConfig

		// Dismiss overlay first

		yield* overlay.pop()
		yield* toast.show("info", "Creating task with AI...")

		// Get current project path (or cwd if no project selected)
		const projectPath = yield* resolveProjectPathFromContext(projectContext)

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
		const taskType = resolveIssueType(parsed.type)
		const priority =
			typeof parsed.priority === "number" && parsed.priority >= 1 && parsed.priority <= 4
				? parsed.priority
				: 2

		// Phase 2: Create the issue directly via daemon issue RPC
		const implementations = yield* getIssueCreateImplementations({ cwd: projectPath })
		const createdIssueResult = yield* daemonRpcClient.issueCreate({
			projectPath,
			input: {
				title: parsed.title,
				type: taskType,
				priority,
				description: parsed.description,
				implementations,
			},
		})

		yield* board.upsertIssueFromMutation(createdIssueResult.issue)
		yield* navigation.jumpToTask(createdIssueResult.issue.id)
		yield* toast.show("success", `Created ${taskType}: ${createdIssueResult.issue.id}`)

		return createdIssueResult.issue.id
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
		const daemonRpcClient = yield* DaemonRpcClient
		const board = yield* TuiBoardStoreService
		const projectPath = yield* getCurrentProjectPath
		yield* daemonRpcClient.issueDelete({
			issueId,
			projectPath,
		})
		yield* board.removeTaskFromMutation(issueId)
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
			const daemonRpcClient = yield* DaemonRpcClient
			const projectPath = yield* getCurrentProjectPath
			const result = yield* daemonRpcClient.issueGet({
				issueId: epicId,
				projectPath,
			})
			return toEpicChildDependencyRefs(result.issue.dependents)
		}).pipe(
			Effect.catchAll((error) =>
				Effect.gen(function* () {
					yield* Effect.logError(error)
					return [] as const
				}),
			),
		),
	)
