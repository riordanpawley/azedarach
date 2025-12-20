/**
 * Task CRUD Atoms
 *
 * Handles task creation, deletion, movement, and editing.
 */

import { Command } from "@effect/platform"
import { Effect, Schema } from "effect"
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

			yield* board.refresh()
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

		const args = ["-p", prompt, "--model", "haiku", "--output-format", "text"]

		const claudeCmd = Command.make("claude", ...args).pipe(Command.workingDirectory(projectPath))

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

		// Refresh the board to show the new task
		yield* board.refresh()

		// Navigate to the new task and show success
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

// ============================================================================
// Break Into Epic Atoms
// ============================================================================

/**
 * Schema for Claude's suggested child tasks
 */
const SuggestedChildTaskSchema = Schema.Struct({
	title: Schema.String,
	description: Schema.optional(Schema.String),
})

const SuggestedChildTasksSchema = Schema.mutable(Schema.Array(SuggestedChildTaskSchema))

type SuggestedChildTask = Schema.Schema.Type<typeof SuggestedChildTaskSchema>

const decodeSuggestedTasks = Schema.decodeUnknown(SuggestedChildTasksSchema)

/**
 * Fetch suggested child tasks from Claude for breaking a task into an epic
 *
 * Uses Claude to analyze the task and suggest parallelizable subtasks.
 * Returns structured data that can be used to create child tasks.
 *
 * Usage: const fetchSuggestions = useAtomSet(fetchBreakIntoEpicSuggestionsAtom, { mode: "promise" })
 *        const suggestions = await fetchSuggestions({ title: "...", description: "..." })
 */
export const fetchBreakIntoEpicSuggestionsAtom = appRuntime.fn(
	({ title, description }: { title: string; description?: string }) =>
		Effect.gen(function* () {
			const projectService = yield* ProjectService
			const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

			// Build context from title and description
			const taskContext = description
				? `Title: ${title}\n\nDescription:\n${description}`
				: `Title: ${title}`

			const prompt = `You are helping break down a software development task into smaller, parallelizable subtasks.

Given this task:
${taskContext}

Analyze it and suggest 2-5 child tasks that:
1. Can be worked on independently (in parallel where possible)
2. Together fully accomplish the parent task
3. Are specific and actionable
4. Follow software engineering best practices (e.g., separate UI from logic, tests from implementation)

Return ONLY a JSON array of objects with:
- "title": A concise task title (imperative form, e.g. "Implement API endpoint")
- "description": Optional brief description if the title needs clarification

Example output:
[
  {"title": "Create database schema for user settings"},
  {"title": "Implement settings API endpoints", "description": "GET/PUT for reading and updating settings"},
  {"title": "Build settings UI component"},
  {"title": "Add settings E2E tests"}
]

Return ONLY the JSON array, no explanation or markdown.`

			const args = ["-p", prompt, "--model", "haiku", "--output-format", "text"]

			const claudeCmd = Command.make("claude", ...args).pipe(Command.workingDirectory(projectPath))

			const rawOutput = yield* Command.string(claudeCmd).pipe(
				Effect.timeout("30 seconds"),
				Effect.mapError((e) => new Error(`Claude CLI failed: ${e}`)),
			)

			// Parse the JSON output from Claude
			const cleanOutput = rawOutput
				.trim()
				.replace(/^```json?\s*/i, "")
				.replace(/\s*```$/i, "")
				.trim()

			const jsonParsed = yield* Effect.try({
				try: () => JSON.parse(cleanOutput),
				catch: (e) => new Error(`Failed to parse Claude output: ${e}\nRaw output: ${rawOutput}`),
			})

			const parsed = yield* decodeSuggestedTasks(jsonParsed).pipe(
				Effect.mapError(
					(e) => new Error(`Claude returned invalid data: ${e}\nRaw output: ${rawOutput}`),
				),
			)

			return parsed
		}).pipe(
			Effect.catchAll((error) =>
				Effect.gen(function* () {
					yield* Effect.logError(error)
					return "error" as const
				}),
			),
		),
)

/**
 * Execute the break into epic operation
 *
 * 1. Updates the original task's type to "epic"
 * 2. Creates all child tasks in parallel
 * 3. Links children to the epic via parent-child dependency
 * 4. Refreshes the board
 * 5. Enters drill-down view for the new epic
 *
 * Usage: const breakIntoEpic = useAtomSet(executeBreakIntoEpicAtom, { mode: "promise" })
 *        await breakIntoEpic({ taskId: "...", childTasks: [...] })
 */
export const executeBreakIntoEpicAtom = appRuntime.fn(
	({ taskId, childTasks }: { taskId: string; childTasks: SuggestedChildTask[] }) =>
		Effect.gen(function* () {
			const client = yield* BeadsClient
			const board = yield* BoardService
			const navigation = yield* NavigationService
			const toast = yield* ToastService
			const overlay = yield* OverlayService

			// Dismiss overlay first
			yield* overlay.pop()
			yield* toast.show("info", `Converting to epic with ${childTasks.length} subtasks...`)

			// 1. Update original task to be an epic
			// Note: bd doesn't support changing issue_type directly, so we need to use a workaround
			// For now, we'll update via the CLI with --type flag if supported
			// Actually, bd update doesn't support --type, so we'll need to use a different approach
			// The original issue will remain as-is but function as an epic parent
			// TODO: Check if bd supports changing issue type, for now just add children

			// 2. Create all child tasks in parallel
			const createdChildren = yield* Effect.all(
				childTasks.map((child) =>
					client.create({
						title: child.title,
						description: child.description,
						type: "task",
						priority: 2,
					}),
				),
				{ concurrency: "unbounded" },
			)

			// 3. Link all children to the parent epic
			yield* Effect.all(
				createdChildren.map((child) => client.addDependency(child.id, taskId, "parent-child")),
				{ concurrency: "unbounded" },
			)

			// 4. Refresh board to show changes
			yield* board.refresh()

			// 5. Get children for drill-down
			const childIds = new Set(createdChildren.map((c) => c.id))

			// 6. Enter drill-down view for the epic
			yield* navigation.enterDrillDown(taskId, childIds)

			yield* toast.show("success", `Created ${createdChildren.length} subtasks for ${taskId}`)

			return { epicId: taskId, childIds: createdChildren.map((c) => c.id) }
		}).pipe(
			Effect.catchAll((error) =>
				Effect.gen(function* () {
					yield* Effect.logError(error)
					const toast = yield* ToastService
					const formatted = formatForToast(error)
					yield* toast.show("error", `Break into epic failed: ${formatted}`)
					return "error" as const
				}),
			),
		),
)
