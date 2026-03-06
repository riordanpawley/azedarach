import { Duration, Schedule } from "effect"

export const makeSessionRecoveryRetrySchedule = (
	retryBaseDelayMs: number,
	retryMaxDelayMs: number,
) =>
	Schedule.exponential(Duration.millis(retryBaseDelayMs)).pipe(
		Schedule.jittered,
		Schedule.modifyDelay((_output, duration) =>
			Duration.min(duration, Duration.millis(retryMaxDelayMs)),
		),
	)
