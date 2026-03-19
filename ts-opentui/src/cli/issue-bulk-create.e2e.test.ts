import { afterEach, describe, expect, it } from "bun:test"
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"

const decoder = new TextDecoder()
const AZ_BIN_PATH = join(import.meta.dir, "..", "..", "packages", "entry", "src", "main.ts")

const runAz = (args: readonly string[], cwd: string): string => {
	const result = Bun.spawnSync({
		cmd: ["bun", "run", AZ_BIN_PATH, ...args],
		cwd,
		stdout: "pipe",
		stderr: "pipe",
	})

	const stdout = decoder.decode(result.stdout).trim()
	const stderr = decoder.decode(result.stderr).trim()
	if (result.exitCode !== 0) {
		throw new Error(
			[
				`az command failed with exit code ${result.exitCode}`,
				`args: ${args.join(" ")}`,
				`stdout: ${stdout}`,
				`stderr: ${stderr}`,
			].join("\n"),
		)
	}

	return stdout
}

const runAzFromStdin = (args: readonly string[], cwd: string, stdinPath: string): string => {
	const quotedArgs = args.map((arg) => JSON.stringify(arg)).join(" ")
	const result = Bun.spawnSync({
		cmd: [
			"/bin/zsh",
			"-lc",
			`cat ${JSON.stringify(stdinPath)} | bun run ${JSON.stringify(AZ_BIN_PATH)} ${quotedArgs}`,
		],
		cwd,
		stdout: "pipe",
		stderr: "pipe",
	})

	const stdout = decoder.decode(result.stdout).trim()
	const stderr = decoder.decode(result.stderr).trim()
	if (result.exitCode !== 0) {
		throw new Error(
			[
				`az command failed with exit code ${result.exitCode}`,
				`args: ${args.join(" ")}`,
				`stdout: ${stdout}`,
				`stderr: ${stderr}`,
			].join("\n"),
		)
	}

	return stdout
}

const writeConfig = async (projectDir: string): Promise<string> => {
	const configDir = join(projectDir, ".azedarach")
	await mkdir(configDir, { recursive: true })
	const configPath = join(configDir, "config.json")
	const config = {
		$schema: 4,
		issueTracker: {
			local: {
				syncEnabled: false,
				backups: {
					enabled: true,
					intervalMinutes: 60,
					writeCooldownSeconds: 300,
					maxBackups: 30,
					directory: ".azedarach/backups",
				},
			},
		},
	}

	await writeFile(configPath, `${JSON.stringify(config, null, 2)}\n`, "utf8")
	return configPath
}

type IssueJson = {
	readonly id: string
	readonly title: string
	readonly issue_type: string
	readonly priority: number
	readonly description?: string
	readonly labels?: readonly string[]
}

type BulkCreateJsonSummary = {
	readonly requestCount: number
	readonly createdCount: number
	readonly failedCount: number
	readonly results: ReadonlyArray<{
		readonly index: number
		readonly requestedTitle?: string
		readonly issueId?: string
		readonly created: boolean
		readonly error?: string
	}>
}

const tempDirs: string[] = []

const createTempProject = async (): Promise<string> => {
	const dir = await mkdtemp(join(tmpdir(), "az-bulk-create-e2e-"))
	tempDirs.push(dir)
	return dir
}

afterEach(async () => {
	await Promise.all(
		tempDirs.splice(0, tempDirs.length).map((dir) => rm(dir, { recursive: true, force: true })),
	)
})

describe("az issue bulk-create e2e", () => {
	it("creates issues from a JSON file and continues after per-item failures", async () => {
		const projectDir = await createTempProject()
		await writeConfig(projectDir)

		const payloadPath = join(projectDir, "bulk-create.json")
		await writeFile(
			payloadPath,
			`${JSON.stringify(
				[
					{
						title: "First bulk-created issue",
						type: "task",
						priority: 2,
						description: "Created from JSON file",
						labels: ["agent", "json"],
					},
					{
						description: "Missing title should fail but not stop the batch",
					},
					{
						title: "Second bulk-created issue",
						type: "bug",
						priority: 1,
					},
				],
				null,
				2,
			)}\n`,
			"utf8",
		)

		const output = runAz(
			["issue", "bulk-create", "--project-dir", projectDir, "--input", payloadPath],
			projectDir,
		)

		expect(output).toContain("Bulk create finished: 2 succeeded, 1 failed.")
		expect(output).toContain('- "First bulk-created issue": created a')
		expect(output).toContain("- item 2: failed")
		expect(output).toContain('- "Second bulk-created issue": created b')

		const first = JSON.parse(
			runAz(["issue", "get", "--project-dir", projectDir, "--json", "a"], projectDir),
		) as IssueJson
		const second = JSON.parse(
			runAz(["issue", "get", "--project-dir", projectDir, "--json", "b"], projectDir),
		) as IssueJson

		expect(first.title).toBe("First bulk-created issue")
		expect(first.issue_type).toBe("task")
		expect(first.priority).toBe(2)
		expect(first.description).toBe("Created from JSON file")
		expect(first.labels).toEqual(["agent", "json"])
		expect(second.title).toBe("Second bulk-created issue")
		expect(second.issue_type).toBe("bug")
		expect(second.priority).toBe(1)
	})

	it("supports stdin input and preserves request order in JSON output", async () => {
		const projectDir = await createTempProject()
		await writeConfig(projectDir)

		const stdinPath = join(projectDir, "stdin-bulk-create.json")
		await writeFile(
			stdinPath,
			`${JSON.stringify({
				issues: [
					{
						title: "stdin issue one",
						type: "task",
					},
					{
						description: "Missing title should fail in-place",
					},
					{
						title: "stdin issue two",
						priority: 3,
					},
				],
			})}\n`,
			"utf8",
		)

		const rawSummary = runAzFromStdin(
			["issue", "bulk-create", "--project-dir", projectDir, "--input", "-", "--json"],
			projectDir,
			stdinPath,
		)
		const summary = JSON.parse(rawSummary) as BulkCreateJsonSummary

		expect(summary.requestCount).toBe(3)
		expect(summary.createdCount).toBe(2)
		expect(summary.failedCount).toBe(1)
		expect(summary.results.map((result) => result.created)).toEqual([true, false, true])
		expect(summary.results[0]?.requestedTitle).toBe("stdin issue one")
		expect(summary.results[0]?.issueId).toBe("a")
		expect(summary.results[1]?.requestedTitle).toBeUndefined()
		expect(summary.results[1]?.error).toContain("title")
		expect(summary.results[2]?.requestedTitle).toBe("stdin issue two")
		expect(summary.results[2]?.issueId).toBe("b")
	})
})
