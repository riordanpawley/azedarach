import { describe, expect, it } from "bun:test"
import {
	buildWaitingAttentionOptionCommands,
	isValidHookEvent,
	mapHookEventToTmuxStatus,
	VALID_HOOK_EVENTS,
} from "./tmux-notify.js"

describe("isValidHookEvent", () => {
	it("accepts supported hook events and rejects unknown values", () => {
		for (const event of VALID_HOOK_EVENTS) {
			expect(isValidHookEvent(event)).toBe(true)
		}
		expect(isValidHookEvent("unknown_event")).toBe(false)
	})
})

describe("mapHookEventToTmuxStatus", () => {
	it("maps hook events to tmux statuses", () => {
		expect(mapHookEventToTmuxStatus("user_prompt")).toBe("busy")
		expect(mapHookEventToTmuxStatus("pretooluse")).toBe("busy")
		expect(mapHookEventToTmuxStatus("idle_prompt")).toBe("waiting")
		expect(mapHookEventToTmuxStatus("permission_request")).toBe("waiting")
		expect(mapHookEventToTmuxStatus("stop")).toBe("waiting")
		expect(mapHookEventToTmuxStatus("session_end")).toBe("idle")
	})
})

describe("buildWaitingAttentionOptionCommands", () => {
	it("uses window-option commands for window-scoped bell/activity settings", () => {
		const commands = buildWaitingAttentionOptionCommands("az-test")
		const byOption = new Map(commands.map((args) => [args[3], args]))

		expect(byOption.get("monitor-bell")?.[0]).toBe("set-window-option")
		expect(byOption.get("monitor-activity")?.[0]).toBe("set-window-option")
		expect(byOption.get("window-status-bell-style")?.[0]).toBe("set-window-option")
		expect(byOption.get("window-status-activity-style")?.[0]).toBe("set-window-option")
	})

	it("uses session-option commands for session action settings", () => {
		const commands = buildWaitingAttentionOptionCommands("az-test")
		const byOption = new Map(commands.map((args) => [args[3], args]))

		expect(byOption.get("bell-action")?.[0]).toBe("set-option")
		expect(byOption.get("activity-action")?.[0]).toBe("set-option")
	})
})
