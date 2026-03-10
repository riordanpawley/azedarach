import { describe, expect, it } from "bun:test"
import { buildAiCreateCommand, extractJsonPayload } from "./task.js"

describe("buildAiCreateCommand", () => {
	it("builds claude print command with text output", () => {
		const command = buildAiCreateCommand({
			cliTool: "claude",
			model: "claude-4.5-haiku",
			prompt: "hello",
		})
		expect(command.executable).toBe("claude")
		expect(command.args).toEqual([
			"-p",
			"hello",
			"--model",
			"claude-4.5-haiku",
			"--output-format",
			"text",
		])
	})

	it("builds opencode run command", () => {
		const command = buildAiCreateCommand({
			cliTool: "opencode",
			model: "gpt-5-mini",
			prompt: "hello",
		})
		expect(command.executable).toBe("opencode")
		expect(command.args).toEqual(["run", "--model", "gpt-5-mini", "hello"])
	})

	it("builds codex exec command", () => {
		const command = buildAiCreateCommand({
			cliTool: "codex",
			model: "gpt-5-mini",
			prompt: "hello",
		})
		expect(command.executable).toBe("codex")
		expect(command.args).toEqual(["exec", "--model", "gpt-5-mini", "--color", "never", "hello"])
	})
})

describe("extractJsonPayload", () => {
	it("returns plain JSON output", () => {
		expect(extractJsonPayload('{"title":"A","type":"task"}')).toBe('{"title":"A","type":"task"}')
	})

	it("unwraps markdown json fences", () => {
		expect(extractJsonPayload('```json\n{"title":"A"}\n```')).toBe('{"title":"A"}')
	})

	it("extracts balanced JSON from noisy output", () => {
		const raw =
			'WARNING noisy preface\n\u001b[31mcolor\u001b[0m\n{"title":"A","description":"x"}\ntrailer'
		expect(extractJsonPayload(raw)).toBe('{"title":"A","description":"x"}')
	})

	it("throws on missing JSON object", () => {
		expect(() => extractJsonPayload("no json here")).toThrow()
	})
})
