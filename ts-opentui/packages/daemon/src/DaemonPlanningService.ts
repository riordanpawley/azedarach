import type {
	PlanningPlan,
	PlanningReviewFeedback,
	PlanningReviewMissingDependency,
	PlanningTask,
	TrackedIssue,
} from "@azedarach/shared/rpc"
import { Data, Effect } from "effect"
import {
	TrackerIssueDaemonService,
	type TrackerIssueDaemonServiceApi,
} from "./TrackerIssueDaemonService.js"

export type DaemonPlanningTask = PlanningTask
export type DaemonPlanningPlan = PlanningPlan
export type DaemonPlanningReviewFeedback = PlanningReviewFeedback

export interface DaemonPlanningGenerateRequest {
	readonly featureDescription: string
}

export interface DaemonPlanningReviewRequest {
	readonly plan: DaemonPlanningPlan
}

export interface DaemonPlanningRefineRequest {
	readonly plan: DaemonPlanningPlan
	readonly feedback: DaemonPlanningReviewFeedback
}

export interface DaemonPlanningGenerateResult {
	readonly plan: DaemonPlanningPlan
}

export interface DaemonPlanningReviewResult {
	readonly feedback: DaemonPlanningReviewFeedback
}

export interface DaemonPlanningRefineResult {
	readonly plan: DaemonPlanningPlan
}

export interface DaemonPlanningCreateIssuesRequest {
	readonly plan: DaemonPlanningPlan
}

export interface DaemonPlanningCreateIssuesResult {
	readonly createdIssues: ReadonlyArray<TrackedIssue>
}

export class DaemonPlanningError extends Data.TaggedError("DaemonPlanningError")<{
	readonly reason: "invalid-input" | "generation" | "review" | "refinement" | "issues-creation"
	readonly message: string
}> {}

export interface DaemonPlanningServiceApi {
	readonly generate: (
		featureDescription: string,
	) => Effect.Effect<DaemonPlanningPlan, DaemonPlanningError>
	readonly review: (
		plan: DaemonPlanningPlan,
	) => Effect.Effect<DaemonPlanningReviewFeedback, DaemonPlanningError>
	readonly refine: (
		plan: DaemonPlanningPlan,
		feedback: DaemonPlanningReviewFeedback,
	) => Effect.Effect<DaemonPlanningPlan, DaemonPlanningError>
	readonly createIssues: (
		params: DaemonPlanningCreateIssuesRequest,
	) => Effect.Effect<DaemonPlanningCreateIssuesResult, DaemonPlanningError | Error>
}

const DEFAULT_FEATURE_TITLE = "Planned feature"
const DEFAULT_TASK_PREFIX = "task"

const clampScore = (score: number): number => Math.max(0, Math.min(100, Math.trunc(score)))

const compactWhitespace = (value: string): string => value.replace(/\s+/g, " ").trim()

const titleCase = (value: string): string =>
	value
		.split(/[\s_-]+/)
		.filter((part) => part.length > 0)
		.map((part) => part[0]?.toUpperCase() + part.slice(1).toLowerCase())
		.join(" ")

const sanitizeFeatureTitle = (featureDescription: string): string => {
	const compact = compactWhitespace(featureDescription)
	if (compact.length === 0) {
		return DEFAULT_FEATURE_TITLE
	}

	const firstClause = compact.split(/[.;]/, 1)[0] ?? compact
	const shortened = firstClause.slice(0, 72)
	return titleCase(shortened)
}

const splitFeatureDescription = (featureDescription: string): ReadonlyArray<string> => {
	const compact = compactWhitespace(featureDescription)
	if (compact.length === 0) {
		return []
	}

	return compact
		.split(/(?:,| and | then |;)+/i)
		.map((part) => part.trim())
		.filter((part, index, parts) => part.length > 0 && parts.indexOf(part) === index)
}

const buildGeneratedTasks = (featureDescription: string): ReadonlyArray<DaemonPlanningTask> => {
	const parts = splitFeatureDescription(featureDescription)
	const taskBodies =
		parts.length > 0
			? parts
			: [
					"Confirm the scope and acceptance criteria",
					"Implement the primary behaviour",
					"Add verification and cleanup",
				]

	const tasks: Array<DaemonPlanningTask> = taskBodies.slice(0, 4).map((part, index) => {
		const id = `${DEFAULT_TASK_PREFIX}-${index + 1}`
		const previousId = index > 0 ? `${DEFAULT_TASK_PREFIX}-${index}` : undefined
		const canParallelize = index === 0 || index === taskBodies.length - 1
		return {
			id,
			title: index === 0 ? `Define ${part}` : titleCase(part),
			description: `Deliver ${part.toLowerCase()} for the feature.`,
			type: index === 1 ? "feature" : "task",
			priority: Math.min(4, index + 1),
			estimate: index === 0 ? 2 : index === 1 ? 4 : 1,
			dependsOn: previousId === undefined ? [] : [previousId],
			canParallelize,
			design: index === 1 ? "Implement the core path behind the service boundary." : undefined,
			acceptance:
				index === 0
					? "The scope is explicit and the workstream is bounded."
					: index === 1
						? "The primary behavior is implemented."
						: "The change is validated and ready to ship.",
		}
	})

	return tasks.length > 0
		? tasks
		: [
				{
					id: `${DEFAULT_TASK_PREFIX}-1`,
					title: "Define requirements",
					description: "Clarify the requested behavior.",
					type: "task",
					priority: 1,
					estimate: 1,
					dependsOn: [],
					canParallelize: true,
				},
			]
}

const makeGeneratedPlan = (featureDescription: string): DaemonPlanningPlan => {
	const trimmed = compactWhitespace(featureDescription)
	const epicTitle = sanitizeFeatureTitle(featureDescription)
	const tasks = buildGeneratedTasks(featureDescription)
	const parallelizationScore = clampScore(
		55 + tasks.filter((task) => task.canParallelize).length * 10,
	)
	return {
		epicTitle,
		epicDescription: trimmed.length > 0 ? trimmed : "Feature planning request.",
		summary: `Break the feature into a small, dependency-aware plan for ${epicTitle.toLowerCase()}.`,
		tasks,
		parallelizationScore,
	}
}

const isKnownTaskId = (taskIds: ReadonlySet<string>, dependencyId: string): boolean =>
	taskIds.has(dependencyId)

const reviewPlan = (plan: DaemonPlanningPlan): DaemonPlanningReviewFeedback => {
	const taskIds = new Set(plan.tasks.map((task) => task.id))
	const issues: Array<string> = []
	const suggestions: Array<string> = []
	const parallelizationOpportunities: Array<string> = []
	const tasksTooLarge: Array<string> = []
	const missingDependencies: Array<PlanningReviewMissingDependency> = []

	if (plan.tasks.length === 0) {
		issues.push("The plan has no tasks.")
		suggestions.push("Add at least one implementation task.")
	}

	for (const task of plan.tasks) {
		if (task.description.length > 180 || task.title.length > 80) {
			tasksTooLarge.push(task.id)
		}

		if (task.canParallelize) {
			parallelizationOpportunities.push(task.id)
		}

		for (const dependencyId of task.dependsOn) {
			if (!isKnownTaskId(taskIds, dependencyId)) {
				missingDependencies.push({
					taskId: task.id,
					shouldDependOn: dependencyId,
					reason: "The dependency target is not present in the plan.",
				})
			}
		}
	}

	if (plan.tasks.length < 2) {
		issues.push("The plan is too small to demonstrate parallel execution.")
		suggestions.push("Split the work into at least two implementation-focused tasks.")
	}

	if (plan.parallelizationScore !== undefined && plan.parallelizationScore < 60) {
		issues.push("The plan is not sufficiently parallelizable.")
		suggestions.push("Mark independent tasks as parallelizable and reduce dependency chains.")
	}

	const score = clampScore(
		100 -
			issues.length * 12 -
			tasksTooLarge.length * 8 -
			missingDependencies.length * 10 +
			parallelizationOpportunities.length * 4,
	)

	return {
		score,
		issues,
		suggestions,
		parallelizationOpportunities,
		tasksTooLarge,
		missingDependencies,
		isApproved: score >= 80 && tasksTooLarge.length === 0 && missingDependencies.length === 0,
	}
}

const refinePlan = (
	plan: DaemonPlanningPlan,
	feedback: DaemonPlanningReviewFeedback,
): DaemonPlanningPlan => {
	if (feedback.isApproved) {
		return plan.reviewNotes === undefined
			? plan
			: {
					...plan,
					parallelizationScore: plan.parallelizationScore ?? feedback.score,
				}
	}

	const notes = [...feedback.issues, ...feedback.suggestions].join(" ")
	return {
		...plan,
		reviewNotes:
			plan.reviewNotes === undefined ? notes : compactWhitespace(`${plan.reviewNotes} ${notes}`),
		parallelizationScore: clampScore(Math.max(plan.parallelizationScore ?? 0, feedback.score)),
	}
}

const toIssueInput = (task: DaemonPlanningTask) => ({
	title: task.title,
	description: task.description,
	type: task.type,
	priority: task.priority,
	design: task.design,
	acceptance: task.acceptance,
	estimate: task.estimate,
})

const createIssuesFromPlan = (
	issues: TrackerIssueDaemonServiceApi,
	params: DaemonPlanningCreateIssuesRequest,
): Effect.Effect<DaemonPlanningCreateIssuesResult, DaemonPlanningError | Error> =>
	Effect.gen(function* () {
		const epic = yield* issues.create({
			title: params.plan.epicTitle,
			description: params.plan.epicDescription,
			type: "epic",
			priority: 1,
			design: params.plan.summary,
		})

		const createdIssues: Array<TrackedIssue> = [epic]
		const idMapping = new Map<string, string>()
		const unresolved = new Set(params.plan.tasks.map((task) => task.id))
		let iterationBudget = Math.max(1, params.plan.tasks.length * params.plan.tasks.length)

		while (unresolved.size > 0 && iterationBudget > 0) {
			iterationBudget -= 1
			let progress = false

			for (const task of params.plan.tasks) {
				if (!unresolved.has(task.id)) {
					continue
				}

				const ready = task.dependsOn.every((dependencyId) => idMapping.has(dependencyId))
				if (!ready) {
					continue
				}

				const issue = yield* issues.create(toIssueInput(task))
				idMapping.set(task.id, issue.id)
				unresolved.delete(task.id)
				createdIssues.push(issue)
				progress = true

				yield* issues.addDependency(issue.id, epic.id, "parent-child")

				for (const dependencyId of task.dependsOn) {
					const realDependencyId = idMapping.get(dependencyId)
					if (realDependencyId !== undefined) {
						yield* issues.addDependency(issue.id, realDependencyId, "blocks")
					}
				}
			}

			if (!progress) {
				break
			}
		}

		if (unresolved.size > 0) {
			return yield* Effect.fail(
				new DaemonPlanningError({
					reason: "issues-creation",
					message: `Could not resolve dependencies for planning tasks: ${[...unresolved].join(", ")}`,
				}),
			)
		}

		return { createdIssues }
	}).pipe(
		Effect.mapError((error) =>
			error instanceof DaemonPlanningError
				? error
				: new DaemonPlanningError({
						reason: "issues-creation",
						message: error.message ?? "Issue creation failed.",
					}),
		),
	)

export class DaemonPlanningService extends Effect.Service<DaemonPlanningService>()(
	"DaemonPlanningService",
	{
		dependencies: [TrackerIssueDaemonService.Default],
		effect: Effect.gen(function* () {
			const trackerIssues = yield* TrackerIssueDaemonService

			return {
				generate: (featureDescription: string) =>
					Effect.gen(function* () {
						const normalized = compactWhitespace(featureDescription)
						if (normalized.length === 0) {
							return yield* Effect.fail(
								new DaemonPlanningError({
									reason: "invalid-input",
									message: "Planning requires a non-empty feature description.",
								}),
							)
						}

						return makeGeneratedPlan(normalized)
					}),
				review: (plan: DaemonPlanningPlan) => Effect.succeed(reviewPlan(plan)),
				refine: (plan: DaemonPlanningPlan, feedback: DaemonPlanningReviewFeedback) =>
					Effect.succeed(refinePlan(plan, feedback)),
				createIssues: (params: DaemonPlanningCreateIssuesRequest) =>
					createIssuesFromPlan(trackerIssues, params),
			} satisfies DaemonPlanningServiceApi
		}),
	},
) {}
