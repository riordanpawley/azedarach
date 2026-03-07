import { describe, expect, it } from "bun:test"
import { buildGuardedInitCommand, buildInitWaitCommand } from "./WorktreeSessionService.js"

describe("buildInitWaitCommand", () => {
	it("waits indefinitely for init completion marker", () => {
		const command = buildInitWaitCommand("aze-123")

		expect(command).toContain("until [")
		expect(command).toContain("@az_init_done")
		expect(command).toContain("do sleep 1; done")
	})
})

describe("buildGuardedInitCommand", () => {
	it("records init failure metadata and marks startup as done when a command fails", () => {
		const command = buildGuardedInitCommand("aze-123", "pnpm install")

		expect(command).toContain("@az_init_failed 1")
		expect(command).toContain("@az_init_failed_command")
		expect(command).toContain("@az_init_done 1")
		expect(command).toContain("@az_status waiting")
		expect(command).toContain("Session startup blocked")
	})
})
