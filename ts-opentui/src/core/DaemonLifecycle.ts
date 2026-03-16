import { Data } from "effect"

export type DaemonLifecycleState =
	| "starting"
	| "ready"
	| "degraded"
	| "recovering"
	| "stopping"
	| "crashed"

export type DaemonLifecycleEvent =
	| "bootstrap_succeeded"
	| "bootstrap_failed"
	| "health_check_failed"
	| "health_check_recovered"
	| "restart_requested"
	| "shutdown_requested"
	| "shutdown_completed"
	| "shutdown_failed"
	| "recovery_exhausted"

export interface DaemonLifecycleTransition {
	readonly from: DaemonLifecycleState
	readonly event: DaemonLifecycleEvent
	readonly to: DaemonLifecycleState
	readonly reason: string
}

export class InvalidDaemonLifecycleTransitionError extends Data.TaggedError(
	"InvalidDaemonLifecycleTransitionError",
)<{
	readonly from: DaemonLifecycleState
	readonly event: DaemonLifecycleEvent
}> {}

const transitions: Readonly<
	Record<
		DaemonLifecycleState,
		Readonly<Partial<Record<DaemonLifecycleEvent, DaemonLifecycleTransition>>>
	>
> = {
	starting: {
		bootstrap_succeeded: {
			from: "starting",
			event: "bootstrap_succeeded",
			to: "ready",
			reason: "daemon bootstrap completed",
		},
		bootstrap_failed: {
			from: "starting",
			event: "bootstrap_failed",
			to: "degraded",
			reason: "daemon bootstrap failed",
		},
		shutdown_requested: {
			from: "starting",
			event: "shutdown_requested",
			to: "stopping",
			reason: "shutdown requested during bootstrap",
		},
	},
	ready: {
		health_check_failed: {
			from: "ready",
			event: "health_check_failed",
			to: "degraded",
			reason: "health checks failed",
		},
		restart_requested: {
			from: "ready",
			event: "restart_requested",
			to: "recovering",
			reason: "restart requested",
		},
		shutdown_requested: {
			from: "ready",
			event: "shutdown_requested",
			to: "stopping",
			reason: "shutdown requested",
		},
	},
	degraded: {
		health_check_recovered: {
			from: "degraded",
			event: "health_check_recovered",
			to: "ready",
			reason: "health checks recovered",
		},
		restart_requested: {
			from: "degraded",
			event: "restart_requested",
			to: "recovering",
			reason: "restart requested from degraded state",
		},
		recovery_exhausted: {
			from: "degraded",
			event: "recovery_exhausted",
			to: "crashed",
			reason: "recovery attempts exhausted",
		},
		shutdown_requested: {
			from: "degraded",
			event: "shutdown_requested",
			to: "stopping",
			reason: "shutdown requested from degraded state",
		},
	},
	recovering: {
		health_check_recovered: {
			from: "recovering",
			event: "health_check_recovered",
			to: "ready",
			reason: "recovery succeeded",
		},
		health_check_failed: {
			from: "recovering",
			event: "health_check_failed",
			to: "degraded",
			reason: "recovery run failed",
		},
		recovery_exhausted: {
			from: "recovering",
			event: "recovery_exhausted",
			to: "crashed",
			reason: "recovery attempts exhausted",
		},
		shutdown_requested: {
			from: "recovering",
			event: "shutdown_requested",
			to: "stopping",
			reason: "shutdown requested while recovering",
		},
	},
	stopping: {
		shutdown_completed: {
			from: "stopping",
			event: "shutdown_completed",
			to: "starting",
			reason: "shutdown completed",
		},
		shutdown_failed: {
			from: "stopping",
			event: "shutdown_failed",
			to: "crashed",
			reason: "shutdown failed",
		},
	},
	crashed: {
		restart_requested: {
			from: "crashed",
			event: "restart_requested",
			to: "recovering",
			reason: "manual restart from crashed state",
		},
		shutdown_requested: {
			from: "crashed",
			event: "shutdown_requested",
			to: "stopping",
			reason: "shutdown requested from crashed state",
		},
	},
}

export const resolveDaemonLifecycleTransition = (
	from: DaemonLifecycleState,
	event: DaemonLifecycleEvent,
): DaemonLifecycleTransition => {
	const transition = transitions[from][event]
	if (transition === undefined) {
		throw new InvalidDaemonLifecycleTransitionError({
			from,
			event,
		})
	}
	return transition
}
