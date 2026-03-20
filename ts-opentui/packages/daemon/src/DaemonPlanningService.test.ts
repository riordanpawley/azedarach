import { describe, expect, it } from "bun:test"
import { DAEMON_RPC_PROTOCOL_VERSION, type TrackedIssue } from "@azedarach/shared/rpc"
import { Effect, Layer } from "effect"
import {
	type DaemonPlanningPlan,
	type DaemonPlanningReviewFeedback,
	DaemonPlanningService,
} from "./DaemonPlanningService.js"
import {
	makeDaemonPlanningCreateIssuesRpcHandler,
	makeDaemonPlanningGenerateRpcHandler,
	makeDaemonPlanningRefineRpcHandler,
	makeDaemonPlanningReviewRpcHandler,
} from "./GlobalDaemonRpcHandlers.js"
import { TrackerIssueDaemonService } from "./TrackerIssueDaemonService.js"

const makeTrackedIssue = (params: {
	readonly id: string
	readonly title: string
	readonly type: TrackedIssue["issue_type"]
	readonly priority: number
	readonly description?: string
	readonly design?: string
	readonly acceptance?: string
}): TrackedIssue => ({
	id: params.id,
	title: params.title,
	status: "open",
	priority: params.priority,
	issue_type: params.type,
	created_at: "2026-03-20T00:00:00.000Z",
	updated_at: "2026-03-20T00:00:00.000Z",
	closed_at: null,
	assignee: null,
	description: params.description,
	design: params.design,
	acceptance: params.acceptance,
	notes: undefined,
	estimate: undefined,
	labels: undefined,
	implementations: [],
	dependencies: undefined,
	dependents: undefined,
	dependency_count: undefined,
	dependent_count: undefined,
})

const makeTrackerIssuesStub = () => {
	const createdInputs: Array<{
		readonly title: string
		readonly type: TrackedIssue["issue_type"]
	}> = []
	const dependencyCalls: Array<{
		readonly issueId: string
		readonly dependsOnId: string
		readonly dependencyType: "blocks" | "parent-child" | "related" | "discovered-from"
	}> = []

	let issueCounter = 0

	const service: TrackerIssueDaemonService = {
		_tag: "TrackerIssueDaemonService",
		get: () => Effect.dieMessage("unexpected get call"),
		list: () => Effect.dieMessage("unexpected list call"),
		create: (input) =>
			Effect.sync(() => {
				createdInputs.push({
					title: input.title,
					type: input.type ?? "task",
				})
				issueCounter += 1
				return makeTrackedIssue({
					id: `issue-${issueCounter}`,
					title: input.title,
					type: input.type ?? "task",
					priority: input.priority ?? 3,
					description: input.description,
					design: input.design,
					acceptance: input.acceptance,
				})
			}),
		update: () => Effect.dieMessage("unexpected update call"),
		addDependency: (issueId, dependsOnId, dependencyType) =>
			Effect.sync(() => {
				dependencyCalls.push({ issueId, dependsOnId, dependencyType })
			}),
		removeDependency: () => Effect.dieMessage("unexpected removeDependency call"),
		close: () => Effect.dieMessage("unexpected close call"),
		delete: () => Effect.dieMessage("unexpected delete call"),
		sync: () => Effect.dieMessage("unexpected sync call"),
	}

	return {
		service,
		createdInputs,
		dependencyCalls,
	}
}

const makeLayer = (tracker: TrackerIssueDaemonService) =>
	Layer.provide(
		DaemonPlanningService.DefaultWithoutDependencies,
		Layer.succeed(TrackerIssueDaemonService, tracker),
	)

const samplePlan: DaemonPlanningPlan = {
	epicTitle: "Planning RPC Slice",
	epicDescription: "Implement the planning RPC slice.",
	summary: "Split the work into distinct daemon-local planning steps.",
	tasks: [
		{
			id: "task-1",
			title: "Define planning payloads",
			description: "Add local planning request and response types.",
			type: "task",
			priority: 1,
			estimate: 2,
			dependsOn: [],
			canParallelize: true,
		},
		{
			id: "task-2",
			title: "Wire planning handlers",
			description: "Expose daemonPlanning RPC handlers in the global handler set.",
			type: "feature",
			priority: 2,
			estimate: 3,
			dependsOn: ["task-1"],
			canParallelize: false,
		},
	],
}

describe("DaemonPlanningService", () => {
	it("generates a deterministic plan from a feature description", async () => {
		const tracker = makeTrackerIssuesStub()
		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const planning = yield* DaemonPlanningService
				return yield* planning.generate("Add daemon planning RPC support")
			}).pipe(Effect.provide(makeLayer(tracker.service))),
		)

		expect(result.epicTitle).toContain("Daemon Planning")
		expect(result.tasks.length).toBeGreaterThan(0)
		expect(result.parallelizationScore).toBeGreaterThan(0)
	})

	it("reviews plans with typed feedback", async () => {
		const tracker = makeTrackerIssuesStub()
		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const planning = yield* DaemonPlanningService
				return yield* planning.review(samplePlan)
			}).pipe(Effect.provide(makeLayer(tracker.service))),
		)

		expect(result.isApproved).toBe(true)
		expect(result.parallelizationOpportunities).toContain("task-1")
	})

	it("refines plans with review notes when approval is not granted", async () => {
		const tracker = makeTrackerIssuesStub()
		const feedback: DaemonPlanningReviewFeedback = {
			score: 42,
			issues: ["Split the implementation work further."],
			suggestions: ["Keep the planning payloads focused."],
			parallelizationOpportunities: [],
			tasksTooLarge: ["task-2"],
			missingDependencies: [],
			isApproved: false,
		}

		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const planning = yield* DaemonPlanningService
				return yield* planning.refine(samplePlan, feedback)
			}).pipe(Effect.provide(makeLayer(tracker.service))),
		)

		expect(result.reviewNotes).toContain("Split the implementation work further.")
		expect(result.parallelizationScore).toBeGreaterThanOrEqual(42)
	})

	it("creates issues through the tracker service and preserves dependency order", async () => {
		const tracker = makeTrackerIssuesStub()
		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const planning = yield* DaemonPlanningService
				return yield* planning.createIssues({
					plan: samplePlan,
				})
			}).pipe(Effect.provide(makeLayer(tracker.service))),
		)

		expect(result.createdIssues.map((issue) => issue.title)).toEqual([
			"Planning RPC Slice",
			"Define planning payloads",
			"Wire planning handlers",
		])
		expect(tracker.createdInputs.map((input) => input.title)).toEqual([
			"Planning RPC Slice",
			"Define planning payloads",
			"Wire planning handlers",
		])
		expect(tracker.dependencyCalls).toEqual([
			{ issueId: "issue-2", dependsOnId: "issue-1", dependencyType: "parent-child" },
			{ issueId: "issue-3", dependsOnId: "issue-1", dependencyType: "parent-child" },
			{ issueId: "issue-3", dependsOnId: "issue-2", dependencyType: "blocks" },
		])
	})
})

describe("daemon planning RPC handlers", () => {
	it("wraps the planning service results in rpc envelopes", async () => {
		const handler = makeDaemonPlanningGenerateRpcHandler({
			generate: () =>
				Effect.succeed({
					epicTitle: "Generated",
					epicDescription: "Generated plan",
					summary: "Summary",
					tasks: [],
				}),
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				featureDescription: "Generate a plan",
			}),
		)

		expect(result.plan.epicTitle).toBe("Generated")
	})

	it("wraps review and refine results in rpc envelopes", async () => {
		const reviewHandler = makeDaemonPlanningReviewRpcHandler({
			review: () =>
				Effect.succeed({
					score: 90,
					issues: [],
					suggestions: [],
					parallelizationOpportunities: [],
					tasksTooLarge: [],
					missingDependencies: [],
					isApproved: true,
				}),
		})
		const refineHandler = makeDaemonPlanningRefineRpcHandler({
			refine: (plan) => Effect.succeed({ ...plan, reviewNotes: "refined" }),
		})

		const review = await Effect.runPromise(
			reviewHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				plan: samplePlan,
			}),
		)
		const refined = await Effect.runPromise(
			refineHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				plan: samplePlan,
				feedback: review.feedback,
			}),
		)

		expect(review.feedback.isApproved).toBe(true)
		expect(refined.plan.reviewNotes).toBe("refined")
	})

	it("wraps createIssues results in rpc envelopes", async () => {
		const tracker = makeTrackerIssuesStub()
		const handler = makeDaemonPlanningCreateIssuesRpcHandler({
			createIssues: (params) =>
				Effect.gen(function* () {
					const planning = yield* DaemonPlanningService
					return yield* planning.createIssues(params)
				}).pipe(Effect.provide(makeLayer(tracker.service))),
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				plan: samplePlan,
			}),
		)

		expect(result.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(result.createdIssues.length).toBe(3)
	})
})
