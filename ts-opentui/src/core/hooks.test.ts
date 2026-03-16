import { describe, expect, it } from "bun:test"
import { generateCodexSessionHookCliArgs } from "./hooks.js"

describe("generateCodexSessionHookCliArgs", () => {
	it("creates SessionStart/Stop codex hook overrides using az-notify", () => {
		const args = generateCodexSessionHookCliArgs("kl", {
			projectPath: "/tmp/azedarach",
			azNotifyPath: "/tmp/az-notify.sh",
		})

		expect(args).toHaveLength(3)
		expect(args[0]).toBe("--enable codex_hooks")
		expect(args[1]).toContain("hooks.SessionStart")
		expect(args[1]).toContain('\\"/tmp/az-notify.sh\\" user_prompt \\"kl\\"')
		expect(args[2]).toContain("hooks.Stop")
		expect(args[2]).toContain('\\"/tmp/az-notify.sh\\" session_end \\"kl\\"')
	})
})
