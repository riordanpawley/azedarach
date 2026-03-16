import { describe, expect, it } from "bun:test"
import { generateCodexSessionHookTomlBlock, mergeCodexSessionHooksIntoConfig } from "./hooks.js"

describe("generateCodexSessionHookTomlBlock", () => {
	it("creates SessionStart/Stop codex hook TOML using az-notify", () => {
		const block = generateCodexSessionHookTomlBlock("kl", {
			projectPath: "/tmp/azedarach",
			azNotifyPath: "/tmp/az-notify.sh",
		})

		expect(block).toContain("[[hooks.SessionStart]]")
		expect(block).toContain('hooks = [{ command = "\\"/tmp/az-notify.sh\\" user_prompt \\"kl\\"')
		expect(block).toContain("[[hooks.Stop]]")
		expect(block).toContain('hooks = [{ command = "\\"/tmp/az-notify.sh\\" session_end \\"kl\\"')
	})
})

describe("mergeCodexSessionHooksIntoConfig", () => {
	it("appends managed block while preserving existing hook config", () => {
		const existing = `[[hooks.SessionStart]]
hooks = [{ command = "echo existing start" }]
`
		const managed = `# >>> azedarach-codex-hooks
[[hooks.SessionStart]]
hooks = [{ command = "echo az start" }]

[[hooks.Stop]]
hooks = [{ command = "echo az stop" }]
# <<< azedarach-codex-hooks
`

		const merged = mergeCodexSessionHooksIntoConfig(existing, managed)
		expect(merged).toContain('hooks = [{ command = "echo existing start" }]')
		expect(merged).toContain('hooks = [{ command = "echo az start" }]')
		expect(merged).toContain('hooks = [{ command = "echo az stop" }]')
	})

	it("replaces previously managed block without duplicating it", () => {
		const existing = `model = "gpt-5"

# >>> azedarach-codex-hooks
[[hooks.SessionStart]]
hooks = [{ command = "old" }]
# <<< azedarach-codex-hooks
`
		const managed = `# >>> azedarach-codex-hooks
[[hooks.SessionStart]]
hooks = [{ command = "new" }]
# <<< azedarach-codex-hooks
`

		const merged = mergeCodexSessionHooksIntoConfig(existing, managed)
		expect(merged).toContain('hooks = [{ command = "new" }]')
		expect(merged).not.toContain('hooks = [{ command = "old" }]')
		expect(merged.match(/# >>> azedarach-codex-hooks/g)?.length).toBe(1)
	})
})
