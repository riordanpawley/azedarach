import { describe, expect, it } from "bun:test"
import {
	resolveDaemonIntervalMsFromEnv,
	resolveDaemonOperationsPolicy,
} from "./DaemonOperationsPolicy.js"

describe("DaemonOperationsPolicy", () => {
	it("enables auto-daemonize by default", () => {
		const policy = resolveDaemonOperationsPolicy({
			command: "sync",
			noDaemonFlag: false,
			env: {},
		})
		expect(policy.autoDaemonize).toBe(true)
		expect(policy.decision).toBe("enabled-by-default")
	})

	it("enables auto-daemonize via AZEDARACH_DAEMON_MODE=on", () => {
		const policy = resolveDaemonOperationsPolicy({
			command: "tui-default",
			noDaemonFlag: false,
			env: {
				AZEDARACH_DAEMON_MODE: "on",
			},
		})
		expect(policy.autoDaemonize).toBe(true)
		expect(policy.decision).toBe("enabled-by-env")
	})

	it("disables auto-daemonize via AZEDARACH_DAEMON_MODE=off", () => {
		const policy = resolveDaemonOperationsPolicy({
			command: "sync",
			noDaemonFlag: false,
			env: {
				AZEDARACH_DAEMON_MODE: "off",
			},
		})
		expect(policy.autoDaemonize).toBe(false)
		expect(policy.decision).toBe("disabled-by-env")
	})

	it("marks invalid AZEDARACH_DAEMON_MODE as ignored and keeps daemon enabled", () => {
		const policy = resolveDaemonOperationsPolicy({
			command: "sync",
			noDaemonFlag: false,
			env: {
				AZEDARACH_DAEMON_MODE: "banana",
			},
		})
		expect(policy.autoDaemonize).toBe(true)
		expect(policy.decision).toBe("ignored-invalid-env")
	})

	it("parses valid AZEDARACH_DAEMON_INTERVAL_MS", () => {
		const interval = resolveDaemonIntervalMsFromEnv({
			AZEDARACH_DAEMON_INTERVAL_MS: "2500",
		})
		expect(interval.intervalMs).toBe(2500)
		expect(interval.warning).toBeUndefined()
	})

	it("returns warning for invalid AZEDARACH_DAEMON_INTERVAL_MS", () => {
		const interval = resolveDaemonIntervalMsFromEnv({
			AZEDARACH_DAEMON_INTERVAL_MS: "abc",
		})
		expect(interval.intervalMs).toBeUndefined()
		expect(interval.warning).toContain("Ignoring invalid AZEDARACH_DAEMON_INTERVAL_MS='abc'")
	})
})
