import { afterEach, describe, expect, it } from "bun:test"
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"

const decoder = new TextDecoder()
const AZ_BIN_PATH = join(import.meta.dir, "..", "..", "bin", "az.ts")

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

const runAzJson = <T>(args: readonly string[], cwd: string): T => JSON.parse(runAz(args, cwd)) as T

const writeConfig = async (projectDir: string): Promise<string> => {
	const configDir = join(projectDir, ".azedarach")
	await mkdir(configDir, { recursive: true })
	const configPath = join(configDir, "config.json")
	const config = {
		$schema: 4,
		issueTracker: {
			local: {
				syncEnabled: false,
			},
		},
	}

	await writeFile(configPath, `${JSON.stringify(config, null, 2)}\n`, "utf8")
	return configPath
}

type CreatedIssue = {
	readonly id: string
	readonly title: string
}

type IssueDependencyRef = {
	readonly id: string
	readonly dependency_type: string
}

type IssueListEntry = {
	readonly id: string
	readonly title: string
	readonly dependency_count?: number
	readonly dependent_count?: number
	readonly dependencies?: readonly IssueDependencyRef[]
	readonly dependents?: readonly IssueDependencyRef[]
}

const tempDirs: string[] = []

const createTempProject = async (): Promise<string> => {
	const dir = await mkdtemp(join(tmpdir(), "az-issue-list-e2e-"))
	tempDirs.push(dir)
	return dir
}

const createIssue = (projectDir: string, title: string): CreatedIssue =>
	runAzJson(["issue", "create", "--project-dir", projectDir, "--json", title], projectDir)

const summarizeRelationships = (relationships: readonly IssueDependencyRef[] | undefined) =>
	(relationships ?? []).map((relationship) => ({
		id: relationship.id.toLowerCase(),
		dependency_type: relationship.dependency_type,
	}))

afterEach(async () => {
	await Promise.all(
		tempDirs.splice(0, tempDirs.length).map((dir) => rm(dir, { recursive: true, force: true })),
	)
})

describe("az issue list e2e", () => {
	it("filters by parent through the real CLI path", async () => {
		const projectDir = await createTempProject()
		await writeConfig(projectDir)

		const parent = createIssue(projectDir, "Parent issue")
		const matchingChild = createIssue(projectDir, "Matching child")
		const otherParent = createIssue(projectDir, "Other parent")
		const otherChild = createIssue(projectDir, "Other child")

		runAz(
			["issue", "update", "--project-dir", projectDir, "--parent", parent.id.toUpperCase(), matchingChild.id],
			projectDir,
		)
		runAz(
			["issue", "update", "--project-dir", projectDir, "--parent", otherParent.id, otherChild.id],
			projectDir,
		)

		const listed = runAzJson<readonly IssueListEntry[]>(
			["issue", "list", "--project-dir", projectDir, "--parent", parent.id.toUpperCase(), "--json"],
			projectDir,
		)

		expect(listed.map((issue) => issue.id)).toEqual([matchingChild.id])
		expect(summarizeRelationships(listed[0]?.dependencies)).toEqual([
			{
				id: parent.id,
				dependency_type: "parent-child",
			},
		])
	}, 20_000)

	it("projects dependency metadata in issue list JSON output", async () => {
		const projectDir = await createTempProject()
		await writeConfig(projectDir)

		const parent = createIssue(projectDir, "Parent issue")
		const blocker = createIssue(projectDir, "Blocking issue")
		const child = createIssue(projectDir, "Child issue")

		runAz(["issue", "update", "--project-dir", projectDir, "--parent", parent.id, child.id], projectDir)
		runAz(
			["issue", "dep", "add", "--project-dir", projectDir, "--type", "blocks", child.id, blocker.id],
			projectDir,
		)

		const listed = runAzJson<readonly IssueListEntry[]>(
			["issue", "list", "--project-dir", projectDir, "--limit", "20", "--json"],
			projectDir,
		)

		const listedParent = listed.find((issue) => issue.id === parent.id)
		const listedBlocker = listed.find((issue) => issue.id === blocker.id)
		const listedChild = listed.find((issue) => issue.id === child.id)

		expect(listedChild).toBeDefined()
		expect(listedChild?.dependency_count).toBe(2)
		expect(summarizeRelationships(listedChild?.dependencies)).toEqual([
			{ id: parent.id, dependency_type: "parent-child" },
			{ id: blocker.id, dependency_type: "blocks" },
		])

		expect(listedParent).toBeDefined()
		expect(listedParent?.dependent_count).toBe(1)
		expect(summarizeRelationships(listedParent?.dependents)).toEqual([
			{
				id: child.id,
				dependency_type: "parent-child",
			},
		])

		expect(listedBlocker).toBeDefined()
		expect(listedBlocker?.dependent_count).toBe(1)
		expect(summarizeRelationships(listedBlocker?.dependents)).toEqual([
			{
				id: child.id,
				dependency_type: "blocks",
			},
		])
	}, 20_000)
})
