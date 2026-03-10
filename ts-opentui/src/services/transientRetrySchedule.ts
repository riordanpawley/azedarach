import { Duration, Schedule } from "effect"

const normalizeRetryAttempts = (value: number): number =>
	Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1

export const makeTransientRetrySchedule = <E>(options: {
	readonly retryBaseDelayMs: number
	readonly retryMaxDelayMs: number
	readonly retryMaxAttempts: number
	readonly while: (error: E) => boolean
}) =>
	Schedule.exponential(Duration.millis(options.retryBaseDelayMs)).pipe(
		Schedule.jittered,
		Schedule.modifyDelay((_output, duration) =>
			Duration.min(duration, Duration.millis(options.retryMaxDelayMs)),
		),
		Schedule.whileInput(options.while),
		Schedule.intersect(Schedule.recurs(normalizeRetryAttempts(options.retryMaxAttempts) - 1)),
	)
