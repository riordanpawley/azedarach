import { expect, test } from "bun:test"
import { Effect, ScheduleDecision, ScheduleIntervals } from "effect"
import { makeDaemonRecoverySweepSchedule } from "../../packages/daemon/src/BackendDaemonControlService.js"

const assertContinue = (decision: ScheduleDecision.ScheduleDecision): ScheduleDecision.Continue => {
	if (!ScheduleDecision.isContinue(decision)) {
		throw new Error("expected schedule to continue")
	}
	return decision
}

test("daemon recovery sweep catches up after a long run without overlapping", async () => {
	const schedule = makeDaemonRecoverySweepSchedule()

	const [firstState, firstOutput, firstDecision] = await Effect.runPromise(
		schedule.step(0, undefined, schedule.initial),
	)
	expect(firstOutput).toBe(0)
	expect(ScheduleIntervals.start(assertContinue(firstDecision).intervals)).toBe(2000)

	const [, secondOutput, secondDecision] = await Effect.runPromise(
		schedule.step(3000, undefined, firstState),
	)
	expect(secondOutput).toBe(1)
	expect(ScheduleIntervals.start(assertContinue(secondDecision).intervals)).toBe(4000)
})
