import { describe, expect, it } from "bun:test"
import { TuiHarness } from "./harness.js"

const PROJECT_ROOT = "/Users/riordan/prog/azedarach-tf"

describe("tui startup smoke e2e", () => {
	it("launches, reaches a usable board, and quits cleanly without InterruptedException spam", async () => {
		const harness = await TuiHarness.create({
			projectDir: PROJECT_ROOT,
			startupTimeoutMs: 45_000,
		})

		try {
			await harness.start()

			await harness.waitForText("Space Menu")
			await harness.waitForText("Open")
			harness.assertContains("NOR")

			harness.send("j")
			await Bun.sleep(200)
			expect(harness.paneCurrentCommand()).toBe("bun")

			harness.assertNoInterruptedExceptionSpam(1)

			await harness.quit()
			harness.assertNoInterruptedExceptionSpam(1)
		} finally {
			if (harness.isStarted()) {
				await harness.writeArtifacts("startup-smoke")
			}
			await harness.cleanup()
		}
	}, 90_000)
})
