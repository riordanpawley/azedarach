import { describe, expect, it } from "bun:test"
import type { ImplementationRegistry } from "../contracts.js"
import {
	formatIssueImplementations,
	parseIssueImplementations,
	resolveIssueCreateImplementations,
	resolveIssueEditorDefaultImplementation,
} from "./issueImplementations.js"

const registry: ImplementationRegistry = {
	default_implementation: "default",
	implicit_default_allowed: false,
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
			name: "alpha",
			description: undefined,
			directory: undefined,
			created_at: "2026-01-02T00:00:00.000Z",
			updated_at: "2026-01-02T00:00:00.000Z",
			is_default: false,
			is_builtin: false,
		},
	],
}

describe("resolveIssueEditorDefaultImplementation", () => {
	it("uses the configured implementation when it exists", () => {
		expect(resolveIssueEditorDefaultImplementation(registry, "ALPHA")).toBe("alpha")
	})

	it("falls back to registry default when configured implementation is missing", () => {
		expect(resolveIssueEditorDefaultImplementation(registry, "missing")).toBe("default")
	})
})

describe("resolveIssueCreateImplementations", () => {
	it("normalizes and deduplicates requested implementations", () => {
		expect(
			resolveIssueCreateImplementations(registry, {
				requestedImplementations: ["Alpha", "alpha", "  beta  ", " "],
			}),
		).toEqual(["alpha", "beta"])
	})

	it("falls back to the resolved default implementation when none are requested", () => {
		expect(resolveIssueCreateImplementations(registry)).toEqual(["default"])
	})
})

describe("issue implementation parsing", () => {
	it("normalizes and deduplicates parsed implementation values", () => {
		expect(parseIssueImplementations(" Alpha, alpha, beta , ")).toEqual(["alpha", "beta"])
	})

	it("formats implementation values for editor markdown", () => {
		expect(formatIssueImplementations(["tui", "daemon"])).toBe("tui, daemon")
	})
})
