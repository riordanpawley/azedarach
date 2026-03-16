import { describe, expect, it } from "bun:test"
import {
	InvalidDaemonLifecycleTransitionError,
	resolveDaemonLifecycleTransition,
} from "./DaemonLifecycle.js"

describe("DaemonLifecycle", () => {
	it("resolves deterministic transitions", () => {
		expect(resolveDaemonLifecycleTransition("starting", "bootstrap_succeeded")).toMatchObject({
			from: "starting",
			event: "bootstrap_succeeded",
			to: "ready",
		})
		expect(resolveDaemonLifecycleTransition("ready", "restart_requested")).toMatchObject({
			from: "ready",
			event: "restart_requested",
			to: "recovering",
		})
		expect(resolveDaemonLifecycleTransition("recovering", "health_check_recovered")).toMatchObject({
			from: "recovering",
			event: "health_check_recovered",
			to: "ready",
		})
	})

	it("rejects invalid transitions", () => {
		expect(() => resolveDaemonLifecycleTransition("starting", "health_check_recovered")).toThrow(
			InvalidDaemonLifecycleTransitionError,
		)
	})
})
