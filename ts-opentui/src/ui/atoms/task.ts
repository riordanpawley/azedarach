/**
 * Task CRUD Atoms
 *
 * Handles task creation, deletion, movement, and editing.
 */

import { Command } from "@effect/platform"
import { Effect, Schema } from "effect"
import { AppConfig } from "../../config/index.js"
import { BeadEditorService } from "../../core/BeadEditorService.js"
import { BeadsClient } from "../../core/BeadsClient.js"
import { BoardService } from "../../services/BoardService.js"
import { formatForToast } from "../../services/ErrorFormatter.js"
import { NavigationService } from "../../services/NavigationService.js"
import { OverlayService } from "../../services/OverlayService.js"
import { ProjectService } from "../../services/ProjectService.js"
import { ToastService } from "../../services/ToastService.js"
import type { TaskWithSession } from "../types.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Schema for Claude response parsing
// ============================================================================

const ClaudeTaskResponseSchema = Schema.Struct({
	title: Schema.String,
	type: Schema.optional(Schema.String),
	priority: Schema.optional(Schema.Number),
	description: Schema.optional(Schema.String),
})

type ClaudeTaskResponse = Schema.Schema.Type<typeof ClaudeTaskResponseSchema>

const decodeClaudeResponse = Schema.decodeUnknown(ClaudeTaskResponseSchema)

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
			const client = yield* BeadsClient
			yield* client.update(taskId, { status: newStatus })
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Move multiple tasks at once
 */
export const moveTasksAtom = appRuntime.fn(
	({ taskIds, newStatus }: { taskIds: string[]; newStatus: string }) =>
		Effect.gen(function* () {
			const client = yield* BeadsClient
			yield* Effect.all(
				taskIds.map((id) => client.update(id, { status: newStatus })),
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
 * Handles the complete create flow: dismiss overlay, create bead, refresh board,
 * navigate to new task, show toast. All logic in Effects, not React callbacks.
 *
 * Usage: const createTask = useAtomSet(createTaskAtom, { mode: "promise" })
 *        await createTask({ title: "New task", type: "task", priority: 2 })
 */
export const createTaskAtom = appRuntime.fn(
	(params: { title: string; type?: string; priority?: number; description?: string }) =>
		Effect.gen(function* () {
			const client = yield* BeadsClient
			const board = yield* BoardService
			const navigation = yield* NavigationService
			const toast = yield* ToastService
			const overlay = yield* OverlayService

			yield* overlay.pop()

			const issue = yield* client.create(params)

			yield* board.requestRefresh()
			yield* navigation.jumpToTask(issue.id)
			yield* toast.show("success", `Created task: ${issue.id}`)

			return issue
		}).pipe(Effect.tapError(Effect.logError)),
)

/**
 * Edit a bead in $EDITOR
 *
 * Opens the bead in $EDITOR as structured markdown, parses changes on save,
 * and applies updates via bd update.
 *
 * Usage: const editBead = useAtomSet(editBeadAtom, { mode: "promise" })
 *        await editBead(task)
 */
export const editBeadAtom = appRuntime.fn((bead: TaskWithSession) =>
	Effect.gen(function* () {
		const editor = yield* BeadEditorService
		yield* editor.editBead(bead)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Create a new bead via $EDITOR
 *
 * Opens a template in $EDITOR, parses the result, and creates a new bead.
 *
 * Usage: const createBead = useAtom(createBeadViaEditorAtom, { mode: "promise" })
 *        const { id, title } = await createBead()
 */
export const createBeadViaEditorAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* BeadEditorService
		return yield* editor.createBead()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Create a bead from natural language using Claude CLI
 *
 * Two-phase approach for reliability:
 * 1. Claude extracts structured data (title, type, priority) from natural language
 * 2. We call bd create directly via BeadsClient
 *
 * This avoids the unreliability of Claude executing CLI commands and parsing free-form output.
 *
 * Usage: const claudeCreate = useAtom(claudeCreateSessionAtom, { mode: "promise" })
 *        const beadId = await claudeCreate("Add dark mode toggle to settings")
 */
export const claudeCreateSessionAtom = appRuntime.fn((description: string) =>
	Effect.gen(function* () {
		const board = yield* BoardService
		const navigation = yield* NavigationService
		const toast = yield* ToastService
		const overlay = yield* OverlayService
		const beadsClient = yield* BeadsClient
		const projectService = yield* ProjectService
		const appConfig = yield* AppConfig

		// Dismiss overlay first

		yield* overlay.pop()
		yield* toast.show("info", "Creating task with Claude...")

		// Get current project path (or cwd if no project selected)
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

		// Phase 1: Ask Claude to extract structured task data
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
		const toolModelConfig = cliTool === "claude" ? modelConfig.claude : modelConfig.opencode
		const chatModel =
			modelConfig.chat ??
			toolModelConfig.chat ??
			modelConfig.default ??
			toolModelConfig.default ??
			"haiku"

		const args = ["-p", prompt, "--model", chatModel, "--output-format", "text"]

		const claudeCmd = Command.make(cliTool, ...args).pipe(Command.workingDirectory(projectPath))

		const rawOutput = yield* Command.string(claudeCmd).pipe(
			Effect.timeout("15 seconds"),
			Effect.mapError((e) => new Error(`Claude CLI failed: ${e}`)),
		)

		// Parse the JSON output from Claude
		// Handle potential markdown code blocks or extra whitespace
		const cleanOutput = rawOutput
			.trim()
			.replace(/^```json?\s*/i, "")
			.replace(/\s*```$/i, "")
			.trim()

		// Parse JSON
		const jsonParsed = yield* Effect.try({
			try: () => JSON.parse(cleanOutput),
			catch: (e) => new Error(`Failed to parse Claude output: ${e}\nRaw output: ${rawOutput}`),
		})

		// Validate with Schema
		const parsed: ClaudeTaskResponse = yield* decodeClaudeResponse(jsonParsed).pipe(
			Effect.mapError(
				(e) => new Error(`Claude returned invalid data: ${e}\nRaw output: ${rawOutput}`),
			),
		)

		// Normalize type and priority
		const validTypes = ["task", "bug", "feature", "chore", "epic"]
		const taskType = validTypes.includes(parsed.type ?? "") ? parsed.type : "task"
		const priority =
			typeof parsed.priority === "number" && parsed.priority >= 1 && parsed.priority <= 4
				? parsed.priority
				: 2

		// Phase 2: Create the bead directly via BeadsClient
		const createdIssue = yield* beadsClient.create({
			title: parsed.title,
			type: taskType,
			priority,
			description: parsed.description,
		})

		yield* board.requestRefresh()
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
 * Delete a bead entirely
 *
 * Usage: const deleteBead = useAtom(deleteBeadAtom, { mode: "promise" })
 *        await deleteBead(beadId)
 */
export const deleteBeadAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const client = yield* BeadsClient
		yield* client.delete(beadId)
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
			const client = yield* BeadsClient
			const result = yield* client.getEpicWithChildren(epicId)
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
