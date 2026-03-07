#!/usr/bin/env bun

import { readFileSync } from "node:fs"
import { dirname, relative, resolve } from "node:path"
import { fileURLToPath } from "node:url"

type RequirementKind = "functional" | "acceptance"

interface ParsedRequirement {
	readonly id: string
	readonly title: string
	readonly body: string
	readonly kind: RequirementKind
	readonly sourcePath: string
	readonly sourceLine: number
	readonly section: string | null
}

interface CliOptions {
	readonly apply: boolean
	readonly projectDir: string
}

const functionalRequirementPattern = /^- (AZ-FR-\d{4}[A-Za-z]?):\s*(.+)$/
const acceptanceRequirementHeadingPattern = /^### (AZ-AT-\d{4}[A-Za-z]?)\s+(.+)$/
const sectionHeadingPattern = /^##\s+(.+)$/

const scriptDir = dirname(fileURLToPath(import.meta.url))
const tsOpentuiDir = resolve(scriptDir, "..")
const repoRoot = resolve(tsOpentuiDir, "..")

const functionalSourcePath = resolve(repoRoot, "docs/spec/04-functional-requirements.md")
const acceptanceSourcePath = resolve(repoRoot, "docs/spec/06-acceptance-catalog.md")

const usage = `Usage:
  bun run scripts/migrate-docs-spec-to-az-spec.ts [--apply] [--project-dir <path>]

Defaults:
  --dry-run is implicit unless --apply is provided.
  --project-dir defaults to repository root: ${repoRoot}
`

const parseCliOptions = (argv: readonly string[]): CliOptions => {
	let apply = false
	let projectDir = repoRoot

	for (let index = 0; index < argv.length; index += 1) {
		const arg = argv[index]
		if (arg === "--apply") {
			apply = true
			continue
		}
		if (arg === "--project-dir") {
			const value = argv[index + 1]
			if (value === undefined || value.startsWith("--")) {
				throw new Error("Missing value for --project-dir")
			}
			projectDir = resolve(value)
			index += 1
			continue
		}
		if (arg === "--help" || arg === "-h") {
			console.log(usage)
			process.exit(0)
		}
		throw new Error(`Unknown argument: ${arg}`)
	}

	return { apply, projectDir }
}

const parseFunctionalRequirements = (sourcePath: string): ParsedRequirement[] => {
	const relativeSourcePath = relative(repoRoot, sourcePath)
	const lines = readFileSync(sourcePath, "utf8").split(/\r?\n/)
	const requirements: ParsedRequirement[] = []
	let currentSection: string | null = null

	for (let index = 0; index < lines.length; index += 1) {
		const line = lines[index] ?? ""
		const sectionMatch = line.match(sectionHeadingPattern)
		if (sectionMatch) {
			currentSection = sectionMatch[1]?.trim() ?? null
		}

		const match = line.match(functionalRequirementPattern)
		if (!match) {
			continue
		}

		const id = (match[1] ?? "").toUpperCase()
		const statement = (match[2] ?? "").trim()
		if (statement.length === 0) {
			throw new Error(
				`Missing requirement statement for ${id} at ${relativeSourcePath}:${index + 1}`,
			)
		}

		requirements.push({
			id,
			title: statement,
			body: statement,
			kind: "functional",
			sourcePath: relativeSourcePath,
			sourceLine: index + 1,
			section: currentSection,
		})
	}

	return requirements
}

const trimOuterBlankLines = (lines: readonly string[]): string[] => {
	let start = 0
	while (start < lines.length && lines[start]?.trim() === "") {
		start += 1
	}

	let end = lines.length
	while (end > start && lines[end - 1]?.trim() === "") {
		end -= 1
	}

	return lines.slice(start, end)
}

const parseAcceptanceRequirements = (sourcePath: string): ParsedRequirement[] => {
	const relativeSourcePath = relative(repoRoot, sourcePath)
	const lines = readFileSync(sourcePath, "utf8").split(/\r?\n/)
	const requirements: ParsedRequirement[] = []
	let currentSection: string | null = null

	let currentId: string | null = null
	let currentTitle: string | null = null
	let currentStartLine = 0
	let currentBodyLines: string[] = []

	const flushCurrent = () => {
		if (currentId === null || currentTitle === null) {
			return
		}
		const body = trimOuterBlankLines(currentBodyLines).join("\n").trim()
		if (body.length === 0) {
			throw new Error(
				`Missing scenario body for ${currentId} at ${relativeSourcePath}:${currentStartLine}`,
			)
		}
		requirements.push({
			id: currentId,
			title: currentTitle,
			body,
			kind: "acceptance",
			sourcePath: relativeSourcePath,
			sourceLine: currentStartLine,
			section: currentSection,
		})
	}

	for (let index = 0; index < lines.length; index += 1) {
		const line = lines[index] ?? ""
		const sectionMatch = line.match(sectionHeadingPattern)
		if (sectionMatch) {
			currentSection = sectionMatch[1]?.trim() ?? null
		}

		const headingMatch = line.match(acceptanceRequirementHeadingPattern)
		if (headingMatch) {
			flushCurrent()
			currentId = (headingMatch[1] ?? "").toUpperCase()
			currentTitle = (headingMatch[2] ?? "").trim()
			currentStartLine = index + 1
			currentBodyLines = []
			continue
		}

		if (currentId !== null) {
			currentBodyLines.push(line)
		}
	}

	flushCurrent()
	return requirements
}

const assertNoDuplicateIds = (requirements: readonly ParsedRequirement[]): void => {
	const seen = new Map<string, ParsedRequirement>()
	for (const requirement of requirements) {
		const existing = seen.get(requirement.id)
		if (existing) {
			throw new Error(
				`Duplicate requirement ID ${requirement.id} at ${requirement.sourcePath}:${requirement.sourceLine} (already seen at ${existing.sourcePath}:${existing.sourceLine})`,
			)
		}
		seen.set(requirement.id, requirement)
	}
}

const runAzCommand = async (args: readonly string[]): Promise<{ readonly stdout: string }> => {
	const processHandle = Bun.spawn({
		cmd: [process.execPath, "run", "bin/az.ts", ...args],
		cwd: tsOpentuiDir,
		stdout: "pipe",
		stderr: "pipe",
	})

	const [exitCode, stdout, stderr] = await Promise.all([
		processHandle.exited,
		new Response(processHandle.stdout).text(),
		new Response(processHandle.stderr).text(),
	])

	if (exitCode !== 0) {
		const errorOutput = stderr.trim().length > 0 ? stderr.trim() : stdout.trim()
		throw new Error(`az ${args.join(" ")} failed (exit ${exitCode}): ${errorOutput}`)
	}

	return { stdout }
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
	typeof value === "object" && value !== null

const parseExistingRequirements = async (projectDir: string): Promise<Set<string>> => {
	const { stdout } = await runAzCommand([
		"spec",
		"req",
		"list",
		"--project-dir",
		projectDir,
		"--json",
	])
	const parsed: unknown = JSON.parse(stdout)
	if (!Array.isArray(parsed)) {
		throw new Error("Unexpected JSON from az spec req list --json (expected array)")
	}

	const ids = new Set<string>()
	for (const item of parsed) {
		if (!isRecord(item)) {
			throw new Error("Unexpected requirement item in list output")
		}
		const idValue = item.id
		if (typeof idValue !== "string" || idValue.trim().length === 0) {
			throw new Error("Requirement list output missing string id field")
		}
		ids.add(idValue.trim().toUpperCase())
	}
	return ids
}

const formatTrace = (requirement: ParsedRequirement): string => {
	const sectionPart = requirement.section === null ? "" : ` section="${requirement.section}"`
	return `${requirement.sourcePath}:${requirement.sourceLine}${sectionPart}`
}

const migrate = async (options: CliOptions): Promise<void> => {
	const parsedRequirements = [
		...parseFunctionalRequirements(functionalSourcePath),
		...parseAcceptanceRequirements(acceptanceSourcePath),
	]
	assertNoDuplicateIds(parsedRequirements)

	console.log(
		`Loaded ${parsedRequirements.length} requirements from docs/spec (functional=${parsedRequirements.filter((item) => item.kind === "functional").length}, acceptance=${parsedRequirements.filter((item) => item.kind === "acceptance").length}).`,
	)
	console.log(`Mode: ${options.apply ? "apply" : "dry-run"}`)
	console.log(`Target project dir: ${options.projectDir}`)

	const existingIds = await parseExistingRequirements(options.projectDir)
	console.log(`Existing az spec requirements: ${existingIds.size}`)

	let createdCount = 0
	let updatedCount = 0

	for (let index = 0; index < parsedRequirements.length; index += 1) {
		const requirement = parsedRequirements[index]
		if (!requirement) {
			continue
		}
		const exists = existingIds.has(requirement.id)
		const actionLabel = exists ? "update" : "create"
		const trace = formatTrace(requirement)
		const progress = `[${index + 1}/${parsedRequirements.length}]`

		if (!options.apply) {
			console.log(
				`${progress} ${actionLabel.toUpperCase()} ${requirement.id} (${requirement.kind}) <- ${trace}`,
			)
			if (exists) {
				updatedCount += 1
			} else {
				createdCount += 1
			}
			continue
		}

		if (exists) {
			await runAzCommand([
				"spec",
				"req",
				"update",
				"--project-dir",
				options.projectDir,
				"--title",
				requirement.title,
				"--body",
				requirement.body,
				"--kind",
				requirement.kind,
				requirement.id,
			])
			updatedCount += 1
			console.log(`${progress} UPDATED ${requirement.id} (${requirement.kind}) <- ${trace}`)
			continue
		}

		await runAzCommand([
			"spec",
			"req",
			"create",
			"--project-dir",
			options.projectDir,
			"--title",
			requirement.title,
			"--body",
			requirement.body,
			"--kind",
			requirement.kind,
			requirement.id,
		])
		existingIds.add(requirement.id)
		createdCount += 1
		console.log(`${progress} CREATED ${requirement.id} (${requirement.kind}) <- ${trace}`)
	}

	console.log(
		`Completed ${options.apply ? "migration" : "dry-run"}: created=${createdCount} updated=${updatedCount} total=${parsedRequirements.length}`,
	)
}

const main = async (): Promise<void> => {
	try {
		const options = parseCliOptions(process.argv.slice(2))
		await migrate(options)
	} catch (error: unknown) {
		const message = error instanceof Error ? error.message : String(error)
		console.error(`Migration failed: ${message}`)
		process.exit(1)
	}
}

await main()
