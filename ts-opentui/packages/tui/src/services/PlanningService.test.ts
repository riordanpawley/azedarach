import { describe, expect, it } from "bun:test"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import { Effect, Layer, SubscriptionRef } from "effect"
import type { Issue, Plan, ReviewFeedback } from "../contracts.js"
import { PlanningService } from "./PlanningService.js"

const unexpectedDaemonRpcCall = <A>(): Effect.Effect<A, DaemonRpcClientError> =>
	Effect.die("unexpected daemon rpc call")

const makeDaemonRpcClientStub = (options?: {
	readonly planningGenerate?: DaemonRpcClientApi["planningGenerate"]
	readonly planningReview?: DaemonRpcClientApi["planningReview"]
	readonly planningRefine?: DaemonRpcClientApi["planningRefine"]
	readonly planningCreateIssues?: DaemonRpcClientApi["planningCreateIssues"]
}): DaemonRpcClientApi => ({
	status: () => unexpectedDaemonRpcCall(),
	health: () => unexpectedDaemonRpcCall(),
	logs: () => unexpectedDaemonRpcCall(),
	stop: () => unexpectedDaemonRpcCall(),
	restart: () => unexpectedDaemonRpcCall(),
	attach: () => unexpectedDaemonRpcCall(),
	reconnect: () => unexpectedDaemonRpcCall(),
	heartbeat: () => unexpectedDaemonRpcCall(),
	eventStream: () => unexpectedDaemonRpcCall(),
	sessionSnapshot: () => unexpectedDaemonRpcCall(),
	boardReadModel: () => unexpectedDaemonRpcCall(),
	sessionStart: () => unexpectedDaemonRpcCall(),
	sessionStop: () => unexpectedDaemonRpcCall(),
	sessionPause: () => unexpectedDaemonRpcCall(),
	sessionResume: () => unexpectedDaemonRpcCall(),
	sessionRecover: () => unexpectedDaemonRpcCall(),
	sessionUpdateState: () => unexpectedDaemonRpcCall(),
	devServerStatus: () => unexpectedDaemonRpcCall(),
	devServerList: () => unexpectedDaemonRpcCall(),
	devServerStart: () => unexpectedDaemonRpcCall(),
	devServerStop: () => unexpectedDaemonRpcCall(),
	queueEnqueue: () => unexpectedDaemonRpcCall(),
	queueQuery: () => unexpectedDaemonRpcCall(),
	queueCancel: () => unexpectedDaemonRpcCall(),
	issueGet: () => unexpectedDaemonRpcCall(),
	issueList: () => unexpectedDaemonRpcCall(),
	issueCreate: () => unexpectedDaemonRpcCall(),
	issueUpdate: () => unexpectedDaemonRpcCall(),
	issueAddDependency: () => unexpectedDaemonRpcCall(),
	issueRemoveDependency: () => unexpectedDaemonRpcCall(),
	issueClose: () => unexpectedDaemonRpcCall(),
	issueDelete: () => unexpectedDaemonRpcCall(),
	issueSync: () => unexpectedDaemonRpcCall(),
	implementationGetRegistry: () => unexpectedDaemonRpcCall(),
	implementationCreate: () => unexpectedDaemonRpcCall(),
	implementationUpdate: () => unexpectedDaemonRpcCall(),
	implementationDelete: () => unexpectedDaemonRpcCall(),
	implementationSetDefault: () => unexpectedDaemonRpcCall(),
	specRequirementList: () => unexpectedDaemonRpcCall(),
	specRequirementGet: () => unexpectedDaemonRpcCall(),
	specRequirementCreate: () => unexpectedDaemonRpcCall(),
	specRequirementUpdate: () => unexpectedDaemonRpcCall(),
	specRequirementDelete: () => unexpectedDaemonRpcCall(),
	specRead: () => unexpectedDaemonRpcCall(),
	specLint: () => unexpectedDaemonRpcCall(),
	specParity: () => unexpectedDaemonRpcCall(),
	specIssueLinks: () => unexpectedDaemonRpcCall(),
	specRequirementIssues: () => unexpectedDaemonRpcCall(),
	specLinkList: () => unexpectedDaemonRpcCall(),
	specLinkAdd: () => unexpectedDaemonRpcCall(),
	specLinkRemove: () => unexpectedDaemonRpcCall(),
	specLinkUpdate: () => unexpectedDaemonRpcCall(),
	specPublishConfigGet: () => unexpectedDaemonRpcCall(),
	specPublishConfigSet: () => unexpectedDaemonRpcCall(),
	specPublishOutcomeGet: () => unexpectedDaemonRpcCall(),
	specSyncMarkdown: () => unexpectedDaemonRpcCall(),
	specPublish: () => unexpectedDaemonRpcCall(),
	planningGenerate: options?.planningGenerate ?? unexpectedDaemonRpcCall,
	planningReview: options?.planningReview ?? unexpectedDaemonRpcCall,
	planningRefine: options?.planningRefine ?? unexpectedDaemonRpcCall,
	planningCreateIssues: options?.planningCreateIssues ?? unexpectedDaemonRpcCall,
})

const plan: Plan = {
	epicTitle: "Epic",
	epicDescription: "Epic desc",
	summary: "Summary",
	tasks: [
		{
			id: "task-1",
			title: "Task 1",
			description: "Do task 1",
			type: "task",
			priority: 2,
			dependsOn: [],
			canParallelize: true,
		},
	],
}

const approvedFeedback: ReviewFeedback = {
	score: 92,
	issues: [],
	suggestions: [],
	parallelizationOpportunities: [],
	tasksTooLarge: [],
	missingDependencies: [],
	isApproved: true,
}

const createdIssues: ReadonlyArray<Issue> = [
	{
		id: "epic-1",
		title: "Epic",
		status: "open",
		priority: 1,
		issue_type: "epic",
		created_at: "2026-03-20T00:00:00.000Z",
		updated_at: "2026-03-20T00:00:00.000Z",
		implementations: ["ts-opentui"],
	},
]

const makeLayer = (daemonRpcClient: DaemonRpcClientApi) =>
	PlanningService.Default.pipe(Layer.provide(Layer.succeed(DaemonRpcClient, daemonRpcClient)))

describe("PlanningService", () => {
	it("runs the planning workflow through daemon rpc steps and updates state", async () => {
		const service = await Effect.runPromise(
			Effect.gen(function* () {
				return yield* PlanningService
			}).pipe(
				Effect.provide(
					makeLayer(
						makeDaemonRpcClientStub({
							planningGenerate: ({ featureDescription }) => {
								expect(featureDescription).toBe("Ship planning RPC")
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									plan,
								})
							},
							planningReview: ({ plan: requestedPlan }) => {
								expect(requestedPlan).toEqual(plan)
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									feedback: approvedFeedback,
								})
							},
							planningRefine: () => unexpectedDaemonRpcCall(),
							planningCreateIssues: ({ plan: requestedPlan }) => {
								expect(requestedPlan).toEqual(plan)
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									createdIssues,
								})
							},
						}),
					),
				),
			),
		)

		const runResult = await Effect.runPromise(service.runPlanningWorkflow("Ship planning RPC"))
		expect(runResult).toEqual(createdIssues)

		const finalState = await Effect.runPromise(SubscriptionRef.get(service.state))
		expect(finalState.status).toBe("complete")
		expect(finalState.currentPlan).toEqual(plan)
		expect(finalState.reviewPass).toBe(1)
		expect(finalState.reviewHistory).toEqual([approvedFeedback])
		expect(finalState.createdIssues).toEqual(createdIssues)
	})

	it("moves to error state when a daemon rpc step fails", async () => {
		const service = await Effect.runPromise(
			Effect.gen(function* () {
				return yield* PlanningService
			}).pipe(
				Effect.provide(
					makeLayer(
						makeDaemonRpcClientStub({
							planningGenerate: () =>
								Effect.fail({
									_tag: "DaemonRpcActionError",
									action: "planningGenerate",
									code: "test-failure",
									message: "generate failed",
								} satisfies DaemonRpcClientError),
						}),
					),
				),
			),
		)

		const exit = await Effect.runPromiseExit(service.runPlanningWorkflow("Broken planning RPC"))
		expect(exit._tag).toBe("Failure")

		const finalState = await Effect.runPromise(SubscriptionRef.get(service.state))
		expect(finalState.status).toBe("error")
		expect(finalState.error).toBe("generate failed")
	})
})
