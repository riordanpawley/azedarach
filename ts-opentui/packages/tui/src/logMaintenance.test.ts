import { describe, expect, it } from "bun:test"
import { truncateAzLogOnStartup } from "./logMaintenance.js"

const makeTempLogPath = () => `/tmp/az-log-maintenance-${crypto.randomUUID()}.log`

describe("truncateAzLogOnStartup", () => {
	it("truncates to last 50_000 lines when log exceeds threshold", async () => {
		const logPath = makeTempLogPath()
		const inputLines = Array.from({ length: 50_005 }, (_, index) => `line-${index + 1}`)
		await Bun.write(logPath, `${inputLines.join("\n")}\n`)

		try {
			await truncateAzLogOnStartup(logPath)
			const content = await Bun.file(logPath).text()
			const outputLines = content.trimEnd().split("\n")

			expect(outputLines.length).toBe(50_000)
			expect(outputLines[0]).toBe("line-6")
			expect(outputLines[49_999]).toBe("line-50005")
		} finally {
			await Bun.file(logPath).delete()
		}
	})

	it("keeps log unchanged when it is at threshold", async () => {
		const logPath = makeTempLogPath()
		const inputLines = Array.from({ length: 50_000 }, (_, index) => `line-${index + 1}`)
		const expected = `${inputLines.join("\n")}\n`
		await Bun.write(logPath, expected)

		try {
			await truncateAzLogOnStartup(logPath)
			const content = await Bun.file(logPath).text()
			expect(content).toBe(expected)
		} finally {
			await Bun.file(logPath).delete()
		}
	})

	it("returns without error when log file does not exist", async () => {
		const logPath = makeTempLogPath()
		await truncateAzLogOnStartup(logPath)
		expect(await Bun.file(logPath).exists()).toBeFalse()
	})
})
