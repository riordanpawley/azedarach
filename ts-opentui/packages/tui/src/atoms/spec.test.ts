import { describe, expect, it } from "bun:test"
import { DateTime } from "effect"
import type { ImplementationRegistry } from "../contracts.js"
import { resolveSelectedImplementation, toTuiSpecPublishOutcome } from "./spec.js"

const registry: ImplementationRegistry = {
	default_implementation: "default",
	implicit_default_allowed: true,
	implementations: [
		{
			name: "default",
			description: undefined,
			directory: undefined,
			created_at: "2026-01-01T00:00:00.000Z",
			updated_at: "2026-01-01T00:00:00.000Z",
			is_default: true,
			is_builtin: true,
		},
		{
			name: "ios",
			description: undefined,
			directory: undefined,
			created_at: "2026-01-01T00:00:00.000Z",
			updated_at: "2026-01-01T00:00:00.000Z",
			is_default: false,
			is_builtin: false,
		},
	],
}

describe("resolveSelectedImplementation", () => {
	it("keeps an explicitly requested implementation when available", () => {
		expect(resolveSelectedImplementation("ios", registry)).toBe("ios")
	})

	it("falls back to the registry default when the requested implementation is unavailable", () => {
		expect(resolveSelectedImplementation("android", registry)).toBe("default")
	})
})

describe("toTuiSpecPublishOutcome", () => {
	it("converts daemon rpc ISO timestamps into DateTime.Utc values", () => {
		const mapped = toTuiSpecPublishOutcome({
			started_at: "2026-03-19T15:00:00.000Z",
			finished_at: "2026-03-19T15:00:05.000Z",
			status: "success",
			total_requirements: 4,
			total_links: 9,
			outcomes: [],
		})

		expect(DateTime.formatIso(mapped.started_at)).toBe("2026-03-19T15:00:00.000Z")
		expect(DateTime.formatIso(mapped.finished_at)).toBe("2026-03-19T15:00:05.000Z")
		expect(mapped.status).toBe("success")
		expect(mapped.total_requirements).toBe(4)
	})
})
