import { describe, expect, it } from "bun:test"
import { resolveAzEntrypointMode, route } from "./index.js"

describe("@azedarach/entry routing", () => {
	it("copies route definitions without mutation", () => {
		const definition = { name: "az", path: "/bin/az" }
		expect(route(definition)).toEqual(definition)
		expect(route(definition)).not.toBe(definition)
	})

	it("routes bare az invocations to the TUI", () => {
		expect(resolveAzEntrypointMode(["bun", "az"])).toBe("tui")
		expect(resolveAzEntrypointMode(["bun", "az", "--help"])).toBe("cli")
	})

	it("routes subcommands to the CLI", () => {
		expect(resolveAzEntrypointMode(["bun", "az", "issue", "create", "Title"])).toBe("cli")
		expect(resolveAzEntrypointMode(["bun", "az", "dev", "list"])).toBe("cli")
	})
})
