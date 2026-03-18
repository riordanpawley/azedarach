import { afterEach, describe, expect, it, setDefaultTimeout } from "bun:test"
import {
	createTempProject,
	parseCreatedIssueId,
	removeTempProject,
	TuiHarness,
	tmuxAvailable,
} from "./harness.js"

const runIfTmux = tmuxAvailable() ? it : it.skip
setDefaultTimeout(120_000)

const projects: string[] = []
const harnesses: TuiHarness[] = []

afterEach(async () => {
	await Promise.all(
		harnesses.splice(0, harnesses.length).map(async (harness) => {
			await harness.cleanup()
		}),
	)
	await Promise.all(
		projects.splice(0, projects.length).map(async (projectDir) => {
			await removeTempProject(projectDir)
		}),
	)
})

describe("tui interaction e2e: keyboard navigation baseline", () => {
	runIfTmux("handles core navigation key input without exiting", async () => {
		const projectDir = await createTempProject()
		projects.push(projectDir)

		const harness = await TuiHarness.create({
			projectDir,
			startupTimeoutMs: 45_000,
		})
		harnesses.push(harness)
		harness.runAz(
			["issue", "create", "--project-dir", projectDir, "--no-default-parent", "overlay test one"],
			projectDir,
		)
		harness.runAz(
			["issue", "create", "--project-dir", projectDir, "--no-default-parent", "overlay test two"],
			projectDir,
		)
		const thirdIssueRaw = harness.runAz(
			["issue", "create", "--project-dir", projectDir, "--no-default-parent", "overlay test three"],
			projectDir,
		)
		const thirdIssueId = parseCreatedIssueId(thirdIssueRaw)
		harness.runAz(
			["issue", "update", "--project-dir", projectDir, "--status", "in_progress", thirdIssueId],
			projectDir,
		)

		await harness.start()
		await harness.waitForText("NOR")
		await harness.waitForText("Tasks:")
		await harness.waitForText("Active:")

		harness.send(["j", "k", "h", "l"])
		await harness.waitForText("NOR")

		expect(harness.currentCommand()).toBe("bun")
		const pane = harness.capturePane(400)
		expect(pane).toContain("Tasks:")
		expect(pane).toContain("Active:")
	})
})
