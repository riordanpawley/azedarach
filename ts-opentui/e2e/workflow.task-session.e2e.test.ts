import { afterEach, describe, expect, it, setDefaultTimeout } from "bun:test"
import {
	createTempProject,
	parseCreatedIssueId,
	removeTempProject,
	TuiHarness,
	tmuxAvailable,
} from "./harness.js"

type IssueJson = {
	readonly id: string
	readonly status: "open" | "in_progress" | "blocked" | "closed" | "tombstone"
}

const runIfTmux = tmuxAvailable() ? it : it.skip
setDefaultTimeout(120_000)

const projects: string[] = []
const harnesses: TuiHarness[] = []

const waitForIssueStatus = async (
	harness: TuiHarness,
	projectDir: string,
	issueId: string,
	expected: IssueJson["status"],
	timeoutMs = 15_000,
): Promise<void> => {
	const deadline = Date.now() + timeoutMs
	while (Date.now() < deadline) {
		const issue = harness.runAzJson<IssueJson>(
			["issue", "get", "--project-dir", projectDir, "--json", issueId],
			projectDir,
		)
		if (issue.status === expected) return
		await Bun.sleep(200)
	}
	throw new Error(`Timed out waiting for issue ${issueId} status=${expected}`)
}

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

describe("tui workflow e2e: isolated daemon read-model baseline", () => {
	runIfTmux(
		"keeps TUI running while task state mutates through daemon-backed CLI operations",
		async () => {
			const projectDir = await createTempProject()
			projects.push(projectDir)

			const harness = await TuiHarness.create({
				projectDir,
				startupTimeoutMs: 45_000,
			})
			harnesses.push(harness)
			const createdIssueRaw = harness.runAz(
				[
					"issue",
					"create",
					"--project-dir",
					projectDir,
					"--no-default-parent",
					"workflow session journey",
				],
				projectDir,
			)
			const issueId = parseCreatedIssueId(createdIssueRaw)

			await harness.start()
			await harness.waitForText("NOR")
			await harness.waitForText("Tasks:")
			await harness.waitForText("Active:")

			harness.runAz(
				["issue", "update", "--project-dir", projectDir, "--status", "in_progress", issueId],
				projectDir,
			)
			await waitForIssueStatus(harness, projectDir, issueId, "in_progress")

			harness.runAz(
				["issue", "update", "--project-dir", projectDir, "--status", "blocked", issueId],
				projectDir,
			)
			await waitForIssueStatus(harness, projectDir, issueId, "blocked")

			harness.runAz(
				["issue", "update", "--project-dir", projectDir, "--status", "open", issueId],
				projectDir,
			)
			await waitForIssueStatus(harness, projectDir, issueId, "open")

			harness.send(["j", "k"])
			expect(harness.currentCommand()).toBe("bun")
			await harness.waitForText("Active:", 20_000)
		},
	)
})
