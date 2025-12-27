/**
 * PlanningService - Effect service for AI-powered task planning
 *
 * Implements an OpenSpec-inspired workflow:
 * 1. Accept a feature/task description
 * 2. Generate an initial plan with Claude API
 * 3. Iteratively review and refine (4-5 passes) for:
 *    - Proper task decomposition (small, focused tasks)
 *    - Parallelization opportunities
 *    - Dependency optimization
 * 4. Generate beads (epic + child tasks) with dependencies
 *
 * The generated beads are optimized for parallel Claude Code session development.
 */

import { FetchHttpClient, HttpClient, HttpClientRequest } from "@effect/platform"
import { Data, Effect, Schema, SubscriptionRef } from "effect"
import { BeadsClient, type Issue } from "./BeadsClient.js"

// ============================================================================
// Schema Definitions
// ============================================================================

/**
 * A planned task within the spec
 */
const PlannedTaskSchema = Schema.Struct({
	id: Schema.String, // Temporary ID for dependency linking (e.g., "task-1")
	title: Schema.String,
	description: Schema.String,
	type: Schema.Literal("task", "bug", "feature", "chore"),
	priority: Schema.Number,
	estimate: Schema.optional(Schema.Number), // Hours estimate
	dependsOn: Schema.Array(Schema.String), // IDs of tasks this depends on
	canParallelize: Schema.Boolean, // Can run in parallel with siblings
	design: Schema.optional(Schema.String), // Technical design notes
	acceptance: Schema.optional(Schema.String), // Acceptance criteria
})

export type PlannedTask = Schema.Schema.Type<typeof PlannedTaskSchema>

/**
 * The complete plan structure
 */
const PlanSchema = Schema.Struct({
	epicTitle: Schema.String,
	epicDescription: Schema.String,
	summary: Schema.String,
	tasks: Schema.Array(PlannedTaskSchema),
	reviewNotes: Schema.optional(Schema.String), // Notes from review passes
	parallelizationScore: Schema.optional(Schema.Number), // 0-100, how parallelizable
})

export type Plan = Schema.Schema.Type<typeof PlanSchema>

/**
 * Review feedback from AI
 */
const ReviewFeedbackSchema = Schema.Struct({
	score: Schema.Number, // 0-100 quality score
	issues: Schema.Array(Schema.String),
	suggestions: Schema.Array(Schema.String),
	parallelizationOpportunities: Schema.Array(Schema.String),
	tasksTooLarge: Schema.Array(Schema.String), // Task IDs that should be split
	missingDependencies: Schema.Array(
		Schema.Struct({
			taskId: Schema.String,
			shouldDependOn: Schema.String,
			reason: Schema.String,
		}),
	),
	isApproved: Schema.Boolean, // Ready for beads generation?
})

export type ReviewFeedback = Schema.Schema.Type<typeof ReviewFeedbackSchema>

/**
 * Planning session state
 */
export interface PlanningState {
	readonly status:
		| "idle"
		| "generating"
		| "reviewing"
		| "refining"
		| "creating_beads"
		| "complete"
		| "error"
	readonly featureDescription: string | null
	readonly currentPlan: Plan | null
	readonly reviewPass: number
	readonly maxReviewPasses: number
	readonly reviewHistory: ReadonlyArray<ReviewFeedback>
	readonly createdBeads: ReadonlyArray<Issue>
	readonly error: string | null
}

const initialState: PlanningState = {
	status: "idle",
	featureDescription: null,
	currentPlan: null,
	reviewPass: 0,
	maxReviewPasses: 5,
	reviewHistory: [],
	createdBeads: [],
	error: null,
}

// ============================================================================
// Error Types
// ============================================================================

export class PlanningError extends Data.TaggedError("PlanningError")<{
	readonly message: string
	readonly phase: "generation" | "review" | "refinement" | "beads_creation"
	readonly cause?: unknown
}> {}

export class AIResponseError extends Data.TaggedError("AIResponseError")<{
	readonly message: string
	readonly statusCode?: number
	readonly response?: string
}> {}

// ============================================================================
// Prompts
// ============================================================================

const GENERATION_PROMPT = `You are an expert software architect creating a development plan.

Given the feature description, create a detailed implementation plan optimized for parallel development by multiple AI coding agents (Claude Code sessions).

CRITICAL REQUIREMENTS:
1. **Small Tasks**: Each task should be completable in 30 minutes to 2 hours. If larger, split it.
2. **Independence**: Maximize tasks that can run in parallel without blocking each other.
3. **Clear Boundaries**: Each task should touch a distinct set of files to avoid merge conflicts.
4. **Explicit Dependencies**: Only add dependencies where truly necessary (shared types, APIs, etc.)
5. **Design Notes**: Include specific implementation guidance for each task.

Output a JSON object matching this schema:
{
  "epicTitle": "Brief title for the epic",
  "epicDescription": "Detailed description of the feature",
  "summary": "Brief summary of the implementation approach",
  "tasks": [
    {
      "id": "task-1",
      "title": "Concise task title",
      "description": "What this task accomplishes",
      "type": "task|bug|feature|chore",
      "priority": 1-4,
      "estimate": hours (optional),
      "dependsOn": ["task-id", ...],
      "canParallelize": true|false,
      "design": "Technical implementation notes",
      "acceptance": "How to verify completion"
    }
  ],
  "parallelizationScore": 0-100
}

Feature description:
`

const REVIEW_PROMPT = `You are reviewing a development plan for quality and parallelization.

Evaluate this plan against these criteria:
1. **Task Size**: Are all tasks small enough (30min-2hr)? Flag any that are too large.
2. **Parallelization**: What percentage of tasks can run independently? Suggest improvements.
3. **Dependencies**: Are dependencies minimal and correct? Flag missing or unnecessary ones.
4. **Clarity**: Is each task's scope clear? Are there ambiguities?
5. **Completeness**: Does the plan cover all aspects of the feature?

Output a JSON review:
{
  "score": 0-100,
  "issues": ["issue1", "issue2"],
  "suggestions": ["suggestion1", "suggestion2"],
  "parallelizationOpportunities": ["opportunity1"],
  "tasksTooLarge": ["task-id1", "task-id2"],
  "missingDependencies": [
    {"taskId": "task-x", "shouldDependOn": "task-y", "reason": "why"}
  ],
  "isApproved": true|false
}

Current plan:
`

const REFINEMENT_PROMPT = `You are refining a development plan based on review feedback.

Apply the suggested improvements while maintaining:
1. Maximum parallelization
2. Small, focused tasks (30min-2hr each)
3. Minimal, correct dependencies
4. Clear scope boundaries

Review feedback to address:
{FEEDBACK}

Current plan:
{PLAN}

Output the refined plan in the same JSON format as the original.`

// ============================================================================
// Service Implementation
// ============================================================================

export class PlanningService extends Effect.Service<PlanningService>()("PlanningService", {
	dependencies: [BeadsClient.Default],
	effect: Effect.gen(function* () {
		const beadsClient = yield* BeadsClient
		const state = yield* SubscriptionRef.make<PlanningState>(initialState)

		// Get API key from environment
		const getApiKey = (): Effect.Effect<string, PlanningError> =>
			Effect.sync(() => process.env.ANTHROPIC_API_KEY).pipe(
				Effect.flatMap((key) =>
					key
						? Effect.succeed(key)
						: Effect.fail(
								new PlanningError({
									message: "ANTHROPIC_API_KEY environment variable not set",
									phase: "generation",
								}),
							),
				),
			)

		/**
		 * Call Claude API with a prompt and get JSON response
		 */
		const callClaude = (prompt: string): Effect.Effect<string, AIResponseError | PlanningError> =>
			Effect.gen(function* () {
				const apiKey = yield* getApiKey()

				const httpClient = yield* HttpClient.HttpClient

				const request = HttpClientRequest.post("https://api.anthropic.com/v1/messages").pipe(
					HttpClientRequest.setHeaders({
						"content-type": "application/json",
						"x-api-key": apiKey,
						"anthropic-version": "2023-06-01",
					}),
					HttpClientRequest.jsonBody({
						model: "claude-sonnet-4-20250514",
						max_tokens: 8192,
						messages: [
							{
								role: "user",
								content: prompt,
							},
						],
					}),
				)

				const response = yield* httpClient.execute(request).pipe(
					Effect.flatMap((res) =>
						res.status >= 200 && res.status < 300
							? res.json
							: Effect.fail(
									new AIResponseError({
										message: `API request failed with status ${res.status}`,
										statusCode: res.status,
									}),
								),
					),
					Effect.mapError((e) =>
						e._tag === "AIResponseError"
							? e
							: new AIResponseError({
									message: `API request failed: ${String(e)}`,
								}),
					),
				)

				// Extract text from Claude response
				const content = (response as { content?: Array<{ type: string; text?: string }> }).content
				if (!content || content.length === 0) {
					return yield* Effect.fail(
						new AIResponseError({
							message: "Empty response from Claude API",
						}),
					)
				}

				const textBlock = content.find((block) => block.type === "text")
				if (!textBlock || !textBlock.text) {
					return yield* Effect.fail(
						new AIResponseError({
							message: "No text content in Claude response",
						}),
					)
				}

				return textBlock.text
			}).pipe(Effect.provide(FetchHttpClient.layer))

		/**
		 * Parse JSON from Claude response, handling markdown code blocks
		 */
		const parseJsonResponse = <A, I>(
			schema: Schema.Schema<A, I>,
			text: string,
			phase: "generation" | "review" | "refinement",
		): Effect.Effect<A, PlanningError> =>
			Effect.gen(function* () {
				// Extract JSON from potential markdown code blocks
				let jsonStr = text.trim()

				// Handle ```json ... ``` blocks
				const jsonMatch = jsonStr.match(/```(?:json)?\s*([\s\S]*?)```/)
				if (jsonMatch) {
					jsonStr = jsonMatch[1]?.trim() ?? jsonStr
				}

				// Parse and validate
				const parsed = yield* Effect.try({
					try: () => JSON.parse(jsonStr),
					catch: (e) =>
						new PlanningError({
							message: `Failed to parse JSON response: ${e}`,
							phase,
							cause: e,
						}),
				})

				return yield* Schema.decodeUnknown(schema)(parsed).pipe(
					Effect.mapError(
						(e) =>
							new PlanningError({
								message: `Invalid response structure: ${e}`,
								phase,
								cause: e,
							}),
					),
				)
			})

		/**
		 * Generate initial plan from feature description
		 */
		const generatePlan = (
			featureDescription: string,
		): Effect.Effect<Plan, PlanningError | AIResponseError> =>
			Effect.gen(function* () {
				yield* SubscriptionRef.set(state, {
					...initialState,
					status: "generating",
					featureDescription,
				})

				const prompt = GENERATION_PROMPT + featureDescription
				const response = yield* callClaude(prompt)
				const plan = yield* parseJsonResponse(PlanSchema, response, "generation")

				yield* SubscriptionRef.update(state, (s) => ({
					...s,
					status: "reviewing",
					currentPlan: plan,
				}))

				return plan
			})

		/**
		 * Review a plan and get feedback
		 */
		const reviewPlan = (
			plan: Plan,
		): Effect.Effect<ReviewFeedback, PlanningError | AIResponseError> =>
			Effect.gen(function* () {
				const prompt = REVIEW_PROMPT + JSON.stringify(plan, null, 2)
				const response = yield* callClaude(prompt)
				return yield* parseJsonResponse(ReviewFeedbackSchema, response, "review")
			})

		/**
		 * Refine a plan based on review feedback
		 */
		const refinePlan = (
			plan: Plan,
			feedback: ReviewFeedback,
		): Effect.Effect<Plan, PlanningError | AIResponseError> =>
			Effect.gen(function* () {
				yield* SubscriptionRef.update(state, (s) => ({ ...s, status: "refining" }))

				const prompt = REFINEMENT_PROMPT.replace(
					"{FEEDBACK}",
					JSON.stringify(feedback, null, 2),
				).replace("{PLAN}", JSON.stringify(plan, null, 2))

				const response = yield* callClaude(prompt)
				return yield* parseJsonResponse(PlanSchema, response, "refinement")
			})

		/**
		 * Create beads from the finalized plan
		 */
		const createBeadsFromPlan = (plan: Plan): Effect.Effect<ReadonlyArray<Issue>, PlanningError> =>
			Effect.gen(function* () {
				yield* SubscriptionRef.update(state, (s) => ({ ...s, status: "creating_beads" }))

				const createdBeads: Issue[] = []
				const idMapping = new Map<string, string>() // Map temp IDs to real bead IDs

				// 1. Create the epic first
				const epic = yield* beadsClient
					.create({
						title: plan.epicTitle,
						description: plan.epicDescription,
						type: "epic",
						priority: 1,
						design: plan.summary,
					})
					.pipe(
						Effect.mapError(
							(e) =>
								new PlanningError({
									message: `Failed to create epic: ${e}`,
									phase: "beads_creation",
									cause: e,
								}),
						),
					)

				createdBeads.push(epic)

				// 2. Create tasks in dependency order
				// First, create tasks with no dependencies
				const noDeps = plan.tasks.filter((t) => t.dependsOn.length === 0)
				const withDeps = plan.tasks.filter((t) => t.dependsOn.length > 0)

				// Create tasks without dependencies
				for (const task of noDeps) {
					const bead = yield* beadsClient
						.create({
							title: task.title,
							description: task.description,
							type: task.type,
							priority: task.priority,
							design: task.design,
							acceptance: task.acceptance,
							estimate: task.estimate,
						})
						.pipe(
							Effect.mapError(
								(e) =>
									new PlanningError({
										message: `Failed to create task "${task.title}": ${e}`,
										phase: "beads_creation",
										cause: e,
									}),
							),
						)

					idMapping.set(task.id, bead.id)
					createdBeads.push(bead)

					// Link to epic as child
					yield* beadsClient.addDependency(bead.id, epic.id, "parent-child").pipe(
						Effect.mapError(
							(e) =>
								new PlanningError({
									message: `Failed to link task to epic: ${e}`,
									phase: "beads_creation",
									cause: e,
								}),
						),
					)
				}

				// Create tasks with dependencies (may need multiple passes)
				let remaining = [...withDeps]
				let maxIterations = 10 // Prevent infinite loops

				while (remaining.length > 0 && maxIterations > 0) {
					maxIterations--
					const canCreate: typeof remaining = []
					const stillWaiting: typeof remaining = []

					for (const task of remaining) {
						const allDepsResolved = task.dependsOn.every((depId) => idMapping.has(depId))
						if (allDepsResolved) {
							canCreate.push(task)
						} else {
							stillWaiting.push(task)
						}
					}

					for (const task of canCreate) {
						const bead = yield* beadsClient
							.create({
								title: task.title,
								description: task.description,
								type: task.type,
								priority: task.priority,
								design: task.design,
								acceptance: task.acceptance,
								estimate: task.estimate,
							})
							.pipe(
								Effect.mapError(
									(e) =>
										new PlanningError({
											message: `Failed to create task "${task.title}": ${e}`,
											phase: "beads_creation",
											cause: e,
										}),
								),
							)

						idMapping.set(task.id, bead.id)
						createdBeads.push(bead)

						// Link to epic as child
						yield* beadsClient.addDependency(bead.id, epic.id, "parent-child").pipe(
							Effect.mapError(
								(e) =>
									new PlanningError({
										message: `Failed to link task to epic: ${e}`,
										phase: "beads_creation",
										cause: e,
									}),
							),
						)

						// Add task dependencies (blocks relationship)
						for (const depId of task.dependsOn) {
							const realDepId = idMapping.get(depId)
							if (realDepId) {
								yield* beadsClient.addDependency(bead.id, realDepId, "blocks").pipe(
									Effect.mapError(
										(e) =>
											new PlanningError({
												message: `Failed to add dependency: ${e}`,
												phase: "beads_creation",
												cause: e,
											}),
									),
								)
							}
						}
					}

					remaining = stillWaiting
				}

				if (remaining.length > 0) {
					yield* Effect.logWarning(
						`Could not resolve dependencies for ${remaining.length} tasks: ${remaining.map((t) => t.id).join(", ")}`,
					)
				}

				yield* SubscriptionRef.update(state, (s) => ({
					...s,
					status: "complete",
					createdBeads,
				}))

				return createdBeads
			})

		/**
		 * Run the complete planning workflow
		 */
		const runPlanningWorkflow = (
			featureDescription: string,
		): Effect.Effect<ReadonlyArray<Issue>, PlanningError | AIResponseError> =>
			Effect.gen(function* () {
				// 1. Generate initial plan
				let plan = yield* generatePlan(featureDescription)

				// 2. Review and refine loop
				const maxPasses = 5
				for (let pass = 1; pass <= maxPasses; pass++) {
					yield* SubscriptionRef.update(state, (s) => ({
						...s,
						reviewPass: pass,
					}))

					const feedback = yield* reviewPlan(plan)

					yield* SubscriptionRef.update(state, (s) => ({
						...s,
						reviewHistory: [...s.reviewHistory, feedback],
					}))

					if (feedback.isApproved) {
						yield* Effect.log(`Plan approved after ${pass} review passes`)
						break
					}

					if (pass < maxPasses) {
						plan = yield* refinePlan(plan, feedback)
						yield* SubscriptionRef.update(state, (s) => ({
							...s,
							currentPlan: plan,
						}))
					}
				}

				// 3. Create beads from the final plan
				return yield* createBeadsFromPlan(plan)
			}).pipe(
				Effect.catchAll((error) =>
					Effect.gen(function* () {
						yield* SubscriptionRef.update(state, (s) => ({
							...s,
							status: "error",
							error: String(error),
						}))
						return yield* Effect.fail(error)
					}),
				),
			)

		/**
		 * Reset planning state
		 */
		const reset = (): Effect.Effect<void> => SubscriptionRef.set(state, initialState)

		return {
			state,
			generatePlan,
			reviewPlan,
			refinePlan,
			createBeadsFromPlan,
			runPlanningWorkflow,
			reset,
		}
	}),
}) {}
