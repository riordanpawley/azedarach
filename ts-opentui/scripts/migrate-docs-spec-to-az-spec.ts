#!/usr/bin/env bun

import { readFileSync } from "node:fs"
import { dirname, relative, resolve } from "node:path"
import { fileURLToPath } from "node:url"

type RequirementKind = "functional" | "acceptance"

interface ParsedRequirement {
	readonly externalCode: string
	readonly localId: string
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

const toLocalIdFromExternalCode = (externalCode: string): string => {
	const match = /^AZ-(FR|AT)-(\d{4})([A-Z]?)$/.exec(externalCode)
	if (match === null) {
		return externalCode
			.toLowerCase()
			.replace(/[^a-z0-9]+/g, "-")
			.replace(/^-+|-+$/g, "")
	}
	const prefix = (match[1] ?? "").toLowerCase()
	const number = match[2] ?? ""
	const suffix = (match[3] ?? "").toLowerCase()
	return `${prefix}${number}${suffix}`
}

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

		const externalCode = (match[1] ?? "").toUpperCase()
		const statement = (match[2] ?? "").trim()
		if (statement.length === 0) {
			throw new Error(
				`Missing requirement statement for ${externalCode} at ${relativeSourcePath}:${index + 1}`,
			)
		}

		requirements.push({
			externalCode,
			localId: toLocalIdFromExternalCode(externalCode),
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
			externalCode: currentId,
			localId: toLocalIdFromExternalCode(currentId),
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
		const existing = seen.get(requirement.externalCode)
		if (existing) {
			throw new Error(
				`Duplicate requirement ID ${requirement.externalCode} at ${requirement.sourcePath}:${requirement.sourceLine} (already seen at ${existing.sourcePath}:${existing.sourceLine})`,
			)
		}
		seen.set(requirement.externalCode, requirement)
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

interface ExistingRequirementRef {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
}

const parseExistingRequirements = async (
	projectDir: string,
): Promise<{
	readonly byExternalCode: ReadonlyMap<string, ExistingRequirementRef>
	readonly byLocalId: ReadonlyMap<string, ExistingRequirementRef>
}> => {
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

	const byExternalCode = new Map<string, ExistingRequirementRef>()
	const byLocalId = new Map<string, ExistingRequirementRef>()
	for (const item of parsed) {
		if (!isRecord(item)) {
			throw new Error("Unexpected requirement item in list output")
		}
		const idValue = item.id
		const localIdValue = item.local_id
		const externalCodeValue = item.external_code
		if (
			typeof idValue !== "string" ||
			idValue.trim().length === 0 ||
			typeof localIdValue !== "string" ||
			localIdValue.trim().length === 0
		) {
			throw new Error("Requirement list output missing string id field")
		}
		const normalizedRecord: ExistingRequirementRef = {
			id: idValue.trim(),
			local_id: localIdValue.trim(),
			external_code:
				typeof externalCodeValue === "string" && externalCodeValue.trim().length > 0
					? externalCodeValue.trim().toUpperCase()
					: null,
		}
		byLocalId.set(normalizedRecord.local_id, normalizedRecord)
		if (normalizedRecord.external_code !== null) {
			byExternalCode.set(normalizedRecord.external_code, normalizedRecord)
		}
	}
	return {
		byExternalCode,
		byLocalId,
	}
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

	const existing = await parseExistingRequirements(options.projectDir)
	console.log(
		`Existing az spec requirements: ${existing.byLocalId.size} (with external_code=${existing.byExternalCode.size})`,
	)

	let createdCount = 0
	let updatedCount = 0

	for (let index = 0; index < parsedRequirements.length; index += 1) {
		const requirement = parsedRequirements[index]
		if (!requirement) {
			continue
		}
		const existingByExternalCode = existing.byExternalCode.get(requirement.externalCode)
		const existingByLocalId = existing.byLocalId.get(requirement.localId)
		const existingRequirement = existingByExternalCode ?? existingByLocalId
		const exists = existingRequirement !== undefined
		const actionLabel = exists ? "update" : "create"
		const trace = formatTrace(requirement)
		const progress = `[${index + 1}/${parsedRequirements.length}]`

		if (!options.apply) {
			console.log(
				`${progress} ${actionLabel.toUpperCase()} ${requirement.localId} (${requirement.externalCode}, ${requirement.kind}) <- ${trace}`,
			)
			if (exists) {
				updatedCount += 1
			} else {
				createdCount += 1
			}
			continue
		}

		if (exists) {
			const updateSelectorArgs =
				existingRequirement?.external_code !== null && existingRequirement?.external_code !== undefined
					? ["--external-code", existingRequirement.external_code]
					: ["--local-id", existingRequirement?.local_id ?? requirement.localId]
			await runAzCommand([
				"spec",
				"req",
				"update",
				"--project-dir",
				options.projectDir,
				...updateSelectorArgs,
				"--title",
				requirement.title,
				"--body",
				requirement.body,
				"--kind",
				requirement.kind,
			])
			updatedCount += 1
			console.log(
				`${progress} UPDATED ${requirement.localId} (${requirement.externalCode}, ${requirement.kind}) <- ${trace}`,
			)
			continue
		}

		await runAzCommand([
			"spec",
			"req",
			"create",
			"--project-dir",
			options.projectDir,
			"--local-id",
			requirement.localId,
			"--external-code",
			requirement.externalCode,
			"--title",
			requirement.title,
			"--body",
			requirement.body,
			"--kind",
			requirement.kind,
		])
		const createdRef: ExistingRequirementRef = {
			id: requirement.localId,
			local_id: requirement.localId,
			external_code: requirement.externalCode,
		}
		existing.byLocalId.set(requirement.localId, createdRef)
		existing.byExternalCode.set(requirement.externalCode, createdRef)
		createdCount += 1
		console.log(
			`${progress} CREATED ${requirement.localId} (${requirement.externalCode}, ${requirement.kind}) <- ${trace}`,
		)
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
