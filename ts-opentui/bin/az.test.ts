import { describe, expect, it } from "bun:test"
import { resolveAzEntrypointMode } from "./az.js"

describe("resolveAzEntrypointMode", () => {
	it("routes bare az invocations to the TUI", () => {
		expect(resolveAzEntrypointMode(["bun", "az"])).toBe("tui")
		expect(resolveAzEntrypointMode(["bun", "az", "--help"])).toBe("cli")
	})

	it("routes subcommands to the CLI", () => {
		expect(resolveAzEntrypointMode(["bun", "az", "issue", "create", "Title"])).toBe("cli")
		expect(resolveAzEntrypointMode(["bun", "az", "dev", "list"])).toBe("cli")
	})
})
