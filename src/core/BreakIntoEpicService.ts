/**
 * BreakIntoEpicService - Effect service for breaking a task into an epic with subtasks
 *
 * Uses Claude AI to analyze a task and suggest parallelizable child tasks.
 * Manages overlay state and orchestrates the conversion process.
 *
 * Architecture:
 * - State in SubscriptionRef (reactive for atoms)
 * - Keyboard handling in InputHandlersService
 * - React component is pure render only
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { Effect, Schema, SubscriptionRef } from "effect"
import { ProjectService } from "../services/ProjectService.js"
import { BeadsClient } from "./BeadsClient.js"

// ============================================================================
// Schema Definitions
// ============================================================================

/**
 * Suggested child task from Claude
 */
const SuggestedChildTaskSchema = Schema.Struct({
	title: Schema.String,
	description: Schema.optional(Schema.String),
})

const SuggestedChildTasksSchema = Schema.mutable(Schema.Array(SuggestedChildTaskSchema))

export type SuggestedChildTask = Schema.Schema.Type<typeof SuggestedChildTaskSchema>

const decodeSuggestedTasks = Schema.decodeUnknown(SuggestedChildTasksSchema)

// ============================================================================
// State Types
// ============================================================================

export type BreakIntoEpicState =
	| { readonly _tag: "closed" }
	| {
			readonly _tag: "loading"
			readonly taskId: string
			readonly taskTitle: string
			readonly taskDescription?: string
	  }
	| {
			readonly _tag: "suggestions"
			readonly taskId: string
			readonly taskTitle: string
			readonly tasks: SuggestedChildTask[]
			readonly selectedIndex: number
	  }
	| {
			readonly _tag: "error"
			readonly taskId: string
			readonly taskTitle: string
			readonly message: string
	  }
	| { readonly _tag: "executing" }

// ============================================================================
// Service Implementation
// ============================================================================

export class BreakIntoEpicService extends Effect.Service<BreakIntoEpicService>()(
	"BreakIntoEpicService",
	{
		dependencies: [BeadsClient.Default, ProjectService.Default],
		effect: Effect.gen(function* () {
			const beadsClient = yield* BeadsClient
			const projectService = yield* ProjectService

			// Reactive state for overlay
			const overlayState = yield* SubscriptionRef.make<BreakIntoEpicState>({ _tag: "closed" })

			/**
			 * Get the project path from ProjectService, falling back to process.cwd()
			 */
			const getProjectPath = (): Effect.Effect<string> =>
				projectService.getCurrentPath().pipe(Effect.map((p) => p ?? process.cwd()))

			/**
			 * Fetch suggestions from Claude
			 */
			const fetchSuggestions = (
				title: string,
				description?: string,
			): Effect.Effect<SuggestedChildTask[], Error, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const projectPath = yield* getProjectPath()

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
					const claudeCmd = Command.make("claude", ...args).pipe(
						Command.workingDirectory(projectPath),
					)

					const rawOutput = yield* Command.string(claudeCmd).pipe(
						Effect.timeout("30 seconds"),
						Effect.mapError((e) => new Error(`Claude CLI failed: ${e}`)),
					)

					// Parse JSON output
					const cleanOutput = rawOutput
						.trim()
						.replace(/^```json?\s*/i, "")
						.replace(/\s*```$/i, "")
						.trim()

					const jsonParsed = yield* Effect.try({
						try: () => JSON.parse(cleanOutput),
						catch: (e) =>
							new Error(`Failed to parse Claude output: ${e}\nRaw output: ${rawOutput}`),
					})

					const parsed = yield* decodeSuggestedTasks(jsonParsed).pipe(
						Effect.mapError(
							(e) => new Error(`Claude returned invalid data: ${e}\nRaw output: ${rawOutput}`),
						),
					)

					return parsed
				})

			return {
				// Expose SubscriptionRef for atom subscription
				overlayState,

				/**
				 * Open the overlay for a task and start fetching suggestions
				 */
				openOverlay: (taskId: string, taskTitle: string, taskDescription?: string) =>
					Effect.gen(function* () {
						// Set loading state
						yield* SubscriptionRef.set(overlayState, {
							_tag: "loading",
							taskId,
							taskTitle,
							taskDescription,
						})

						// Fetch suggestions
						const result = yield* fetchSuggestions(taskTitle, taskDescription).pipe(Effect.either)

						if (result._tag === "Left") {
							yield* SubscriptionRef.set(overlayState, {
								_tag: "error",
								taskId,
								taskTitle,
								message: result.left.message,
							})
						} else if (result.right.length === 0) {
							yield* SubscriptionRef.set(overlayState, {
								_tag: "error",
								taskId,
								taskTitle,
								message: "Claude couldn't suggest any subtasks for this task",
							})
						} else {
							yield* SubscriptionRef.set(overlayState, {
								_tag: "suggestions",
								taskId,
								taskTitle,
								tasks: result.right,
								selectedIndex: 0,
							})
						}
					}),

				/**
				 * Close the overlay
				 */
				closeOverlay: () => SubscriptionRef.set(overlayState, { _tag: "closed" }),

				/**
				 * Move selection down
				 */
				selectNext: () =>
					SubscriptionRef.update(overlayState, (s) => {
						if (s._tag !== "suggestions") return s
						const newIndex = Math.min(s.selectedIndex + 1, s.tasks.length - 1)
						return { ...s, selectedIndex: newIndex }
					}),

				/**
				 * Move selection up
				 */
				selectPrevious: () =>
					SubscriptionRef.update(overlayState, (s) => {
						if (s._tag !== "suggestions") return s
						const newIndex = Math.max(s.selectedIndex - 1, 0)
						return { ...s, selectedIndex: newIndex }
					}),

				/**
				 * Execute the break into epic operation
				 */
				confirm: () =>
					Effect.gen(function* () {
						const state = yield* SubscriptionRef.get(overlayState)
						if (state._tag !== "suggestions") return

						const { taskId, tasks } = state

						// Set executing state
						yield* SubscriptionRef.set(overlayState, { _tag: "executing" })

						// Create all child tasks in parallel
						const createdChildren = yield* Effect.all(
							tasks.map((child) =>
								beadsClient.create({
									title: child.title,
									description: child.description,
									type: "task",
									priority: 2,
								}),
							),
							{ concurrency: "unbounded" },
						)

						// Link all children to the parent epic
						yield* Effect.all(
							createdChildren.map((child) =>
								beadsClient.addDependency(child.id, taskId, "parent-child"),
							),
							{ concurrency: "unbounded" },
						)

						// Close overlay
						yield* SubscriptionRef.set(overlayState, { _tag: "closed" })

						return {
							epicId: taskId,
							childIds: createdChildren.map((c) => c.id),
							childCount: createdChildren.length,
						}
					}),

				/**
				 * Get current task ID (for keyboard handlers)
				 */
				getTaskId: () =>
					SubscriptionRef.get(overlayState).pipe(
						Effect.map((s) => {
							if (s._tag === "loading" || s._tag === "suggestions" || s._tag === "error") {
								return s.taskId
							}
							return null
						}),
					),
			}
		}),
	},
) {}
