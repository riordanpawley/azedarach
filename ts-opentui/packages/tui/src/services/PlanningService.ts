import { DaemonRpcClient } from "@azedarach/shared/rpc"
import { Data, Effect, SubscriptionRef } from "effect"
import type { Issue, Plan, PlanningState, ReviewFeedback } from "../contracts.js"

export class PlanningServiceError extends Data.TaggedError("PlanningServiceError")<{
	readonly reason: "rpc-failed"
	readonly operation:
		| "planningGenerate"
		| "planningReview"
		| "planningRefine"
		| "planningCreateIssues"
	readonly message: string
}> {}

export interface PlanningServiceApi {
	readonly state: SubscriptionRef.SubscriptionRef<PlanningState>
	readonly runPlanningWorkflow: (
		featureDescription: string,
	) => Effect.Effect<ReadonlyArray<Issue>, PlanningServiceError>
	readonly reset: () => Effect.Effect<void>
}

const initialState: PlanningState = {
	status: "idle",
	featureDescription: null,
	currentPlan: null,
	reviewPass: 0,
	maxReviewPasses: 5,
	reviewHistory: [],
	createdIssues: [],
	error: null,
}

const rpcFailure = (
	operation: PlanningServiceError["operation"],
	message: string,
): PlanningServiceError =>
	new PlanningServiceError({
		reason: "rpc-failed",
		operation,
		message,
	})

export class PlanningService extends Effect.Service<PlanningService>()("PlanningService", {
	effect: Effect.gen(function* () {
		const daemonRpcClient = yield* DaemonRpcClient
		const state = yield* SubscriptionRef.make<PlanningState>(initialState)

		const generatePlan = (featureDescription: string): Effect.Effect<Plan, PlanningServiceError> =>
			Effect.gen(function* () {
				yield* SubscriptionRef.set(state, {
					...initialState,
					status: "generating",
					featureDescription,
				})

				return yield* daemonRpcClient
					.planningGenerate({
						featureDescription,
					})
					.pipe(
						Effect.map((result) => result.plan),
						Effect.tap((plan) =>
							SubscriptionRef.update(
								state,
								(current): PlanningState => ({
									...current,
									status: "reviewing",
									currentPlan: plan,
								}),
							),
						),
						Effect.mapError((error) => rpcFailure("planningGenerate", error.message)),
					)
			})

		const reviewPlan = (plan: Plan): Effect.Effect<ReviewFeedback, PlanningServiceError> =>
			daemonRpcClient
				.planningReview({
					plan,
				})
				.pipe(
					Effect.map((result) => result.feedback),
					Effect.mapError((error) => rpcFailure("planningReview", error.message)),
				)

		const refinePlan = (
			plan: Plan,
			feedback: ReviewFeedback,
		): Effect.Effect<Plan, PlanningServiceError> =>
			Effect.gen(function* () {
				yield* SubscriptionRef.update(
					state,
					(current): PlanningState => ({
						...current,
						status: "refining",
					}),
				)

				return yield* daemonRpcClient
					.planningRefine({
						feedback,
						plan,
					})
					.pipe(
						Effect.map((result) => result.plan),
						Effect.mapError((error) => rpcFailure("planningRefine", error.message)),
					)
			})

		const createIssuesFromPlan = (
			plan: Plan,
		): Effect.Effect<ReadonlyArray<Issue>, PlanningServiceError> =>
			Effect.gen(function* () {
				yield* SubscriptionRef.update(
					state,
					(current): PlanningState => ({
						...current,
						status: "creating_issues",
					}),
				)

				return yield* daemonRpcClient
					.planningCreateIssues({
						plan,
					})
					.pipe(
						Effect.map((result) => result.createdIssues),
						Effect.tap((createdIssues) =>
							SubscriptionRef.update(
								state,
								(current): PlanningState => ({
									...current,
									status: "complete",
									createdIssues,
								}),
							),
						),
						Effect.mapError((error) => rpcFailure("planningCreateIssues", error.message)),
					)
			})

		const service: PlanningServiceApi = {
			state,
			runPlanningWorkflow: (featureDescription) =>
				Effect.gen(function* () {
					let plan = yield* generatePlan(featureDescription)

					for (let pass = 1; pass <= initialState.maxReviewPasses; pass += 1) {
						yield* SubscriptionRef.update(
							state,
							(current): PlanningState => ({
								...current,
								reviewPass: pass,
							}),
						)

						const feedback = yield* reviewPlan(plan)
						yield* SubscriptionRef.update(
							state,
							(current): PlanningState => ({
								...current,
								reviewHistory: [...current.reviewHistory, feedback],
							}),
						)

						if (feedback.isApproved) {
							break
						}

						if (pass < initialState.maxReviewPasses) {
							plan = yield* refinePlan(plan, feedback)
							yield* SubscriptionRef.update(
								state,
								(current): PlanningState => ({
									...current,
									status: "reviewing",
									currentPlan: plan,
								}),
							)
						}
					}

					return yield* createIssuesFromPlan(plan)
				}).pipe(
					Effect.catchAll((error) =>
						SubscriptionRef.update(
							state,
							(current): PlanningState => ({
								...current,
								status: "error",
								error: error.message,
							}),
						).pipe(Effect.zipRight(Effect.fail(error))),
					),
				),
			reset: () => SubscriptionRef.set(state, initialState),
		}

		return service
	}),
}) {}
