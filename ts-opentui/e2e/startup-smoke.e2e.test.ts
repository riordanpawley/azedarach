import { describe, expect, it } from "bun:test"
import { createTempProject, removeTempProject, TuiHarness } from "./harness.js"

describe("tui startup smoke e2e", () => {
	it("launches, reaches a usable board, and quits cleanly without InterruptedException spam", async () => {
		const projectDir = await createTempProject()
		const harness = await TuiHarness.create({
			projectDir,
			startupTimeoutMs: 45_000,
		})

		try {
			harness.runAz(
				[
					"issue",
					"create",
					"--project-dir",
					projectDir,
					"--no-default-parent",
					"startup smoke task",
				],
				projectDir,
			)
			await harness.start()

			await harness.waitForText("Space Menu")
			await harness.waitForText("Open")
			harness.assertContains("NOR")

			harness.send("j")
			await Bun.sleep(200)
			expect(harness.currentCommand()).toBe("bun")

			harness.assertNoInterruptedExceptionSpam(5)

			await harness.quit()
			harness.assertNoInterruptedExceptionSpam(5)
		} finally {
			if (harness.isStarted()) {
				await harness.writeArtifacts("startup-smoke")
			}
			await harness.cleanup()
			await removeTempProject(projectDir)
		}
	}, 90_000)
})
