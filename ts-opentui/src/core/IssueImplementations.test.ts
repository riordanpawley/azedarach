import { describe, expect, it } from "bun:test"
import {
	formatIssueImplementations,
	parseIssueImplementations,
	resolveIssueCreateImplementations,
	resolveIssueEditorDefaultImplementation,
} from "./IssueImplementations.js"
import type { ImplementationRegistry } from "./IssueTrackerClient.js"

const buildRegistry = (overrides?: Partial<ImplementationRegistry>): ImplementationRegistry => ({
	default_implementation: overrides?.default_implementation ?? "ts-opentui",
	implicit_default_allowed: overrides?.implicit_default_allowed ?? false,
	implementations: overrides?.implementations ?? [
		{
			name: "ts-opentui",
			description: "TypeScript implementation",
			created_at: "2026-03-10T00:00:00.000Z",
			updated_at: "2026-03-10T00:00:00.000Z",
			is_default: true,
			is_builtin: false,
		},
		{
			name: "default",
			description: "Shared implementation",
			created_at: "2026-03-10T00:00:00.000Z",
			updated_at: "2026-03-10T00:00:00.000Z",
			is_default: false,
			is_builtin: true,
		},
	],
})

describe("IssueImplementations", () => {
	it("parses and normalizes comma-separated editor values", () => {
		expect(parseIssueImplementations(" TS-OpenTUI, default, ts-opentui ,  ")).toEqual([
			"ts-opentui",
			"default",
		])
	})

	it("formats implementation lists for the editor metadata line", () => {
		expect(formatIssueImplementations(["default", "ts-opentui"])).toBe("default, ts-opentui")
		expect(formatIssueImplementations(undefined)).toBe("")
	})

	it("prefers the configured editor default when it exists in the registry", () => {
		const registry = buildRegistry()
		expect(resolveIssueEditorDefaultImplementation(registry, "default")).toBe("default")
	})

	it("falls back to the registry default when the configured editor default is unknown", () => {
		const registry = buildRegistry()
		expect(resolveIssueEditorDefaultImplementation(registry, "go-bubbletea")).toBe("ts-opentui")
	})

	it("uses requested implementations before falling back to the configured default", () => {
		const registry = buildRegistry()
		expect(
			resolveIssueCreateImplementations(registry, {
				requestedImplementations: ["default", "ts-opentui"],
				configuredDefaultImplementation: "ts-opentui",
			}),
		).toEqual(["default", "ts-opentui"])
	})

	it("falls back to the registry default implementation when no request is provided", () => {
		const registry = buildRegistry()
		expect(resolveIssueCreateImplementations(registry)).toEqual(["ts-opentui"])
	})
})
