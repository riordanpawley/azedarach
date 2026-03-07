import { describe, expect, it } from "bun:test"
import {
	buildInitWaitCommand,
	DEFAULT_INIT_WAIT_TIMEOUT_SECONDS,
} from "./WorktreeSessionService.js"

describe("buildInitWaitCommand", () => {
	it("builds a bounded wait script with timeout fallback marker", () => {
		const command = buildInitWaitCommand("aze-123")

		expect(command).toContain(`az_wait_timeout=${DEFAULT_INIT_WAIT_TIMEOUT_SECONDS}`)
		expect(command).toContain("az_wait_elapsed=0")
		expect(command).toContain("$az_wait_elapsed")
		expect(command).toContain("@az_init_done")
		expect(command).toContain("@az_init_wait_timed_out 1")
	})

	it("clamps invalid timeout values to one second", () => {
		const command = buildInitWaitCommand("aze-123", 0)
		expect(command).toContain("az_wait_timeout=1")
	})
})
