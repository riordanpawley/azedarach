import { afterEach, describe, expect, it } from "bun:test"
import { mkdir, mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises"
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

const writeConfig = async (
	projectDir: string,
	overrides?: {
		readonly intervalMinutes?: number
		readonly writeCooldownSeconds?: number
		readonly maxBackups?: number
	},
): Promise<string> => {
	const configPath = join(projectDir, ".azedarach.json")
	const config = {
		$schema: 4,
		issueTracker: {
			local: {
				syncEnabled: false,
				backups: {
					enabled: true,
					intervalMinutes: overrides?.intervalMinutes ?? 60,
					writeCooldownSeconds: overrides?.writeCooldownSeconds ?? 300,
					maxBackups: overrides?.maxBackups ?? 30,
					directory: ".azedarach/backups",
				},
			},
		},
	}

	await writeFile(configPath, `${JSON.stringify(config, null, 2)}\n`, "utf8")
	return configPath
}

const parseCreatedIssueId = (raw: string): string => {
	const match = raw.match(/Created issue\s+([A-Za-z0-9-]+)/)
	if (match === null || match[1] === undefined) {
		throw new Error(`Could not parse created issue id from output: ${raw}`)
	}
	return match[1]
}

const tempDirs: string[] = []
const createTempProject = async (): Promise<string> => {
	const dir = await mkdtemp(join(tmpdir(), "az-backup-e2e-"))
	tempDirs.push(dir)
	return dir
}

afterEach(async () => {
	await Promise.all(
		tempDirs.splice(0, tempDirs.length).map((dir) => rm(dir, { recursive: true, force: true })),
	)
})

describe("az issue backup e2e", () => {
	it("creates an automatic sqlite backup during issue create", async () => {
		const projectDir = await createTempProject()
		await writeConfig(projectDir)

		const createRaw = runAz(
			["issue", "create", "--project-dir", projectDir, "Backup smoke test"],
			projectDir,
		)
		const created = parseCreatedIssueId(createRaw)
		expect(/^[a-z]+$/.test(created)).toBe(true)
		expect(created).toBe("a")

		const secondCreateRaw = runAz(
			["issue", "create", "--project-dir", projectDir, "Backup smoke test follow-up"],
			projectDir,
		)
		const secondCreated = parseCreatedIssueId(secondCreateRaw)
		expect(secondCreated).toBe("b")

		const dbPath = join(projectDir, ".azedarach", "issues.db")
		const dbContent = await readFile(dbPath)
		expect(dbContent.byteLength > 0).toBe(true)

		const backupDir = join(projectDir, ".azedarach", "backups")
		const backups = await readdir(backupDir)
		expect(backups.length).toBe(1)
		expect(/^issues-\d{8}T\d{6}Z\.db$/.test(backups[0] ?? "")).toBe(true)
	})

	it("prunes old backup files to default retention after auto-backup", async () => {
		const projectDir = await createTempProject()
		await writeConfig(projectDir)
		const backupDir = join(projectDir, ".azedarach", "backups")
		await mkdir(backupDir, { recursive: true })

		for (let index = 0; index < 35; index += 1) {
			const second = String(index).padStart(2, "0")
			const seededName = `issues-20250101T0000${second}Z.db`
			await writeFile(join(backupDir, seededName), "seed", "utf8")
		}

		runAz(["issue", "create", "--project-dir", projectDir, "Retention prune test"], projectDir)

		const backups = (await readdir(backupDir)).filter((name) =>
			/^issues-\d{8}T\d{6}Z\.db$/.test(name),
		)
		expect(backups.length).toBe(30)
	})
})
