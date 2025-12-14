/**
 * EditorService - Bead editor using $EDITOR
 *
 * Serializes beads to structured markdown, opens $EDITOR, parses changes,
 * and applies updates via BeadsClient.
 */

import { Command, type CommandExecutor, FileSystem, Path } from "@effect/platform"
import { Context, Data, Effect, Layer } from "effect"
import type { Issue } from "./BeadsClient"
import { BeadsClient } from "./BeadsClient"

// ============================================================================
// Popup State Tracking (for cleanup on SIGINT)
// ============================================================================

/**
 * Track active editor popup state for cleanup on process exit.
 * Stores both channel name and temp file path so we can kill the editor.
 */
let activeEditorState: { channel: string; tempFile: string } | null = null

/**
 * Kill any active tmux popup by terminating the editor process.
 * Called on SIGINT to prevent orphaned popups.
 *
 * The popup is created with `-E` flag, so it closes when its command exits.
 * By killing the editor (which has the temp file open), we cause the popup to close.
 */
export const killActivePopup = (): void => {
	if (activeEditorState) {
		const { tempFile } = activeEditorState
		try {
			// Use lsof to find PIDs of processes with the temp file open, then kill them
			// This works on both macOS and Linux
			// lsof -t returns just PIDs, one per line
			const result = Bun.spawnSync(["lsof", "-t", tempFile], {
				stdin: "ignore",
				stdout: "pipe",
				stderr: "ignore",
			})

			if (result.stdout) {
				const output = Buffer.isBuffer(result.stdout)
					? result.stdout.toString()
					: String(result.stdout)
				const pids = output.trim().split("\n").filter(Boolean)

				for (const pidStr of pids) {
					const pid = parseInt(pidStr, 10)
					if (!isNaN(pid) && pid > 0) {
						try {
							process.kill(pid, "SIGTERM")
						} catch {
							// Process may have already exited
						}
					}
				}
			}
		} catch {
			// lsof may not be available or other error - ignore during cleanup
		}
		activeEditorState = null
	}
}

// ============================================================================
// Error Types
// ============================================================================

/**
 * Error when markdown format is invalid
 */
export class ParseMarkdownError extends Data.TaggedError("ParseMarkdownError")<{
	readonly message: string
	readonly markdown: string
}> {}

/**
 * Error when editor execution fails
 */
export class EditorError extends Data.TaggedError("EditorError")<{
	readonly message: string
}> {}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * Result of creating a new bead
 */
export interface CreatedBead {
	readonly id: string
	readonly title: string
	readonly type: string
}

export interface EditorServiceImpl {
	/**
	 * Edit a bead in $EDITOR
	 *
	 * 1. Serializes bead to markdown
	 * 2. Writes to /tmp/azedarach-{id}.md
	 * 3. Opens $EDITOR (blocking)
	 * 4. Parses result
	 * 5. Applies changes via bd update
	 *
	 * If type changed: bd delete + bd create (preserve deps)
	 */
	readonly editBead: (
		bead: Issue,
	) => Effect.Effect<
		void,
		ParseMarkdownError | EditorError,
		CommandExecutor.CommandExecutor | BeadsClient | FileSystem.FileSystem
	>

	/**
	 * Create a new bead via $EDITOR
	 *
	 * 1. Creates blank template with all fields
	 * 2. Writes to /tmp/azedarach-new.md
	 * 3. Opens $EDITOR (blocking)
	 * 4. Parses result
	 * 5. Creates bead via bd create
	 *
	 * Returns the created bead ID and title.
	 */
	readonly createBead: () => Effect.Effect<
		CreatedBead,
		ParseMarkdownError | EditorError,
		CommandExecutor.CommandExecutor | FileSystem.FileSystem
	>
}

export class EditorService extends Context.Tag("EditorService")<
	EditorService,
	EditorServiceImpl
>() {}

// ============================================================================
// Markdown Serialization
// ============================================================================

/**
 * Priority number to label (0 -> P0, 4 -> P4)
 */
const priorityToLabel = (priority: number): string => `P${priority}`

/**
 * Type to display label
 */
const typeToLabel = (type: string): string => type

/**
 * Serialize a bead to markdown format
 */
const serializeBeadToMarkdown = (bead: Issue): string => {
	const lines: string[] = []

	// Header
	lines.push(`# ${bead.id}: ${bead.title}`)
	lines.push("───────────────────────────────────────────────────")
	lines.push("")

	// Metadata section
	lines.push(
		`Type:     ${typeToLabel(bead.issue_type)}        (read-only - changing requires delete+create)`,
	)
	lines.push(`Priority: ${priorityToLabel(bead.priority)}`)
	lines.push(`Status:   ${bead.status}`)
	lines.push(`Assignee: ${bead.assignee || ""}`)
	lines.push(`Labels:   ${(bead.labels || []).join(", ")}`)
	lines.push(`Estimate: ${bead.estimate ?? ""}`)
	lines.push("")

	// Description
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Description")
	lines.push("")
	lines.push(bead.description || "")
	lines.push("")

	// Design
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Design")
	lines.push("")
	lines.push(bead.design || "")
	lines.push("")

	// Notes
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Notes")
	lines.push("")
	lines.push(bead.notes || "")
	lines.push("")

	// Acceptance Criteria
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Acceptance Criteria")
	lines.push("")
	lines.push(bead.acceptance || "")
	lines.push("")

	return lines.join("\n")
}

/**
 * Create a blank template for new bead creation
 */
// Anchor placeholders - single words for easy Helix navigation (gw, w, b, e)
const ANCHORS = {
	TITLE: "TITLE",
	DESCRIPTION: "DESCRIPTION",
	DESIGN: "DESIGN",
	NOTES: "NOTES",
	ACCEPTANCE: "ACCEPTANCE",
} as const

const createBlankBeadTemplate = (): string => {
	const lines: string[] = []

	// Header with single-word anchor for easy selection
	lines.push(`# ${ANCHORS.TITLE}`)
	lines.push("───────────────────────────────────────────────────")
	lines.push("")

	// Metadata section - user fills in these values
	lines.push("Type:     task        (task | bug | feature | epic | chore)")
	lines.push("Priority: P2          (P0 = highest, P4 = lowest)")
	lines.push("Status:   open        (open | in_progress | blocked | closed)")
	lines.push("Assignee: ")
	lines.push("Labels:   ")
	lines.push("Estimate: ")
	lines.push("")

	// Description
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Description")
	lines.push("")
	lines.push(ANCHORS.DESCRIPTION)
	lines.push("")

	// Design
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Design")
	lines.push("")
	lines.push(ANCHORS.DESIGN)
	lines.push("")

	// Notes
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Notes")
	lines.push("")
	lines.push(ANCHORS.NOTES)
	lines.push("")

	// Acceptance Criteria
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Acceptance Criteria")
	lines.push("")
	lines.push(ANCHORS.ACCEPTANCE)
	lines.push("")

	return lines.join("\n")
}

/**
 * Fields parsed from a new bead template
 */
interface NewBeadFields {
	title: string
	type: string
	priority: number
	status: string
	assignee?: string
	labels?: string[]
	estimate?: number
	description?: string
	design?: string
	notes?: string
	acceptance?: string
}

/**
 * Parse new bead template markdown
 */
const parseNewBeadTemplate = (
	markdown: string,
): Effect.Effect<NewBeadFields, ParseMarkdownError> =>
	Effect.try({
		try: () => {
			const lines = markdown.split("\n")

			// Parse header for title
			const headerLine = lines[0]
			if (!headerLine?.startsWith("#")) {
				throw new Error("Missing header line")
			}
			const headerMatch = headerLine.match(/^#\s+(?:NEW:\s+)?(.+)$/)
			if (!headerMatch) {
				throw new Error("Invalid header format. Expected: # NEW: {title} or # {title}")
			}
			const title = headerMatch[1]!.trim()
			if (!title || title === ANCHORS.TITLE) {
				throw new Error("Please enter a title for the bead")
			}

			// Find metadata section
			const metadataLines: string[] = []
			let inMetadata = false
			let separatorCount = 0

			for (const line of lines) {
				if (line.startsWith("───")) {
					separatorCount++
					if (separatorCount === 1) {
						inMetadata = true
						continue
					}
					if (separatorCount === 2) {
						break
					}
				}
				if (inMetadata) {
					metadataLines.push(line)
				}
			}

			// Parse metadata fields
			let type = "task"
			let priority = 2
			let status = "open"
			let assignee: string | undefined
			let labels: string[] | undefined
			let estimate: number | undefined

			for (const line of metadataLines) {
				if (line.startsWith("Type:")) {
					const typeValue = line.replace(/^Type:\s*/, "").split("(")[0]!.trim()
					const parsedType = parseTypeLabel(typeValue)
					if (parsedType) type = parsedType
				}
				if (line.startsWith("Priority:")) {
					const priorityValue = line.replace(/^Priority:\s*/, "").split("(")[0]!.trim()
					const parsedPriority = parsePriorityLabel(priorityValue)
					if (parsedPriority !== null) priority = parsedPriority
				}
				if (line.startsWith("Status:")) {
					const statusValue = line.replace(/^Status:\s*/, "").split("(")[0]!.trim()
					if (statusValue) status = statusValue
				}
				if (line.startsWith("Assignee:")) {
					const assigneeValue = line.replace(/^Assignee:\s*/, "").trim()
					if (assigneeValue) assignee = assigneeValue
				}
				if (line.startsWith("Labels:")) {
					const labelsValue = line.replace(/^Labels:\s*/, "").trim()
					if (labelsValue) {
						labels = labelsValue
							.split(",")
							.map((l) => l.trim())
							.filter(Boolean)
					}
				}
				if (line.startsWith("Estimate:")) {
					const estimateValue = line.replace(/^Estimate:\s*/, "").trim()
					if (estimateValue) {
						const parsed = parseInt(estimateValue, 10)
						if (!isNaN(parsed)) estimate = parsed
					}
				}
			}

			// Parse sections
			const description = parseSection(markdown, "Description")
			const design = parseSection(markdown, "Design")
			const notes = parseSection(markdown, "Notes")
			const acceptance = parseSection(markdown, "Acceptance Criteria")

			// Filter out unchanged anchor placeholders
			const cleanDescription =
				description === ANCHORS.DESCRIPTION ? undefined : description || undefined
			const cleanDesign = design === ANCHORS.DESIGN ? undefined : design || undefined
			const cleanNotes = notes === ANCHORS.NOTES ? undefined : notes || undefined
			const cleanAcceptance =
				acceptance === ANCHORS.ACCEPTANCE ? undefined : acceptance || undefined

			return {
				title,
				type,
				priority,
				status,
				assignee,
				labels,
				estimate,
				description: cleanDescription,
				design: cleanDesign,
				notes: cleanNotes,
				acceptance: cleanAcceptance,
			}
		},
		catch: (error) => {
			const errorMessage =
				error instanceof Error
					? error.message
					: typeof error === "string"
						? error
						: "Unknown error"
			return new ParseMarkdownError({
				message: errorMessage,
				markdown,
			})
		},
	})

// ============================================================================
// Markdown Parsing
// ============================================================================

/**
 * Priority label to number (P0 -> 0, P4 -> 4)
 */
const parsePriorityLabel = (label: string): number | null => {
	const match = label.match(/^P([0-4])$/i)
	return match ? parseInt(match[1]!, 10) : null
}

/**
 * Parse type from label (validate against known types)
 */
const parseTypeLabel = (label: string): string | null => {
	const validTypes = ["bug", "feature", "task", "epic", "chore"]
	const normalized = label.toLowerCase().trim()
	return validTypes.includes(normalized) ? normalized : null
}

/**
 * Extract field value from metadata line
 * Format: "FieldName: value"
 */
const extractFieldValue = (line: string, fieldName: string): string => {
	const regex = new RegExp(`^${fieldName}:\\s*(.*)$`, "i")
	const match = line.match(regex)
	return match ? match[1]!.trim() : ""
}

/**
 * Parse markdown section content
 * Finds section by "## SectionName" header and extracts content until next section
 */
const parseSection = (markdown: string, sectionName: string): string => {
	const lines = markdown.split("\n")
	let inSection = false
	const content: string[] = []

	for (const line of lines) {
		// Check if we're entering the target section
		if (line.trim() === `## ${sectionName}`) {
			inSection = true
			continue
		}

		// If we hit another section or separator, stop
		if (inSection && (line.startsWith("##") || line.startsWith("───"))) {
			break
		}

		// Collect content
		if (inSection && line.trim() !== "") {
			content.push(line)
		}
	}

	return content.join("\n").trim()
}

/**
 * Updated issue fields from parsed markdown
 */
interface UpdatedFields {
	priority?: number
	status?: string
	assignee?: string
	labels?: string[]
	estimate?: number
	title?: string
	description?: string
	design?: string
	notes?: string
	acceptance?: string
	type?: string // Special: triggers delete+create
}

/**
 * Parse markdown to extract changed fields
 */
const parseMarkdownToBead = (
	markdown: string,
	original: Issue,
): Effect.Effect<UpdatedFields, ParseMarkdownError> =>
	Effect.try({
		try: () => {
			const lines = markdown.split("\n")
			const updates: UpdatedFields = {}

			// Parse header for title
			const headerLine = lines[0]
			if (!headerLine?.startsWith("#")) {
				throw new Error("Missing header line")
			}
			const headerMatch = headerLine.match(/^#\s+([^:]+):\s+(.+)$/)
			if (!headerMatch) {
				throw new Error("Invalid header format. Expected: # {id}: {title}")
			}
			const parsedTitle = headerMatch[2]!.trim()
			if (parsedTitle !== original.title) {
				updates.title = parsedTitle
			}

			// Find metadata section (lines between first separator and second separator)
			const metadataLines: string[] = []
			let inMetadata = false
			let separatorCount = 0

			for (const line of lines) {
				if (line.startsWith("───")) {
					separatorCount++
					if (separatorCount === 1) {
						inMetadata = true
						continue
					}
					if (separatorCount === 2) {
						break
					}
				}
				if (inMetadata) {
					metadataLines.push(line)
				}
			}

			// Parse metadata fields
			for (const line of metadataLines) {
				// Type (special - triggers delete+create)
				if (line.startsWith("Type:")) {
					const typeValue = extractFieldValue(line, "Type").split("(")[0]!.trim()
					const parsedType = parseTypeLabel(typeValue)
					if (parsedType && parsedType !== original.issue_type) {
						updates.type = parsedType
					}
				}

				// Priority
				if (line.startsWith("Priority:")) {
					const priorityValue = extractFieldValue(line, "Priority")
					const parsedPriority = parsePriorityLabel(priorityValue)
					if (parsedPriority !== null && parsedPriority !== original.priority) {
						updates.priority = parsedPriority
					}
				}

				// Status
				if (line.startsWith("Status:")) {
					const statusValue = extractFieldValue(line, "Status")
					if (statusValue && statusValue !== original.status) {
						updates.status = statusValue
					}
				}

				// Assignee
				if (line.startsWith("Assignee:")) {
					const assigneeValue = extractFieldValue(line, "Assignee")
					if (assigneeValue !== (original.assignee || "")) {
						updates.assignee = assigneeValue || undefined
					}
				}

				// Labels
				if (line.startsWith("Labels:")) {
					const labelsValue = extractFieldValue(line, "Labels")
					const parsedLabels = labelsValue
						? labelsValue
								.split(",")
								.map((l) => l.trim())
								.filter(Boolean)
						: []
					const originalLabels = original.labels || []

					// Compare arrays (simple string comparison)
					const labelsChanged =
						parsedLabels.length !== originalLabels.length ||
						parsedLabels.some((l, i) => l !== originalLabels[i])

					if (labelsChanged) {
						updates.labels = parsedLabels
					}
				}

				// Estimate
				if (line.startsWith("Estimate:")) {
					const estimateValue = extractFieldValue(line, "Estimate")
					const parsedEstimate = estimateValue ? parseInt(estimateValue, 10) : null
					if (parsedEstimate !== (original.estimate || null)) {
						updates.estimate = parsedEstimate || undefined
					}
				}
			}

			// Parse sections
			const description = parseSection(markdown, "Description")
			if (description !== (original.description || "")) {
				updates.description = description
			}

			const design = parseSection(markdown, "Design")
			if (design !== (original.design || "")) {
				updates.design = design
			}

			const notes = parseSection(markdown, "Notes")
			if (notes !== (original.notes || "")) {
				updates.notes = notes
			}

			const acceptance = parseSection(markdown, "Acceptance Criteria")
			if (acceptance !== (original.acceptance || "")) {
				updates.acceptance = acceptance
			}

			return updates
		},
		catch: (error) =>
			new ParseMarkdownError({
				message: `Failed to parse markdown: ${error}`,
				markdown,
			}),
	})

// ============================================================================
// Live Implementation
// ============================================================================

const EditorServiceImpl = Effect.gen(function* () {
	return EditorService.of({
		editBead: (bead) =>
			Effect.gen(function* () {
				const client = yield* BeadsClient
				const fs = yield* FileSystem.FileSystem

				// 1. Serialize to markdown
				const markdown = serializeBeadToMarkdown(bead)

				// 2. Write to temp file
				const tempFile = `/tmp/azedarach-${bead.id}.md`
				yield* fs.writeFileString(tempFile, markdown).pipe(
					Effect.mapError(
						(error) =>
							new EditorError({
								message: `Failed to write temp file: ${error}`,
							}),
					),
				)

				// 3. Get $EDITOR from environment (default to vim)
				const editor = process.env.EDITOR || "vim"

				// 4. Open editor in tmux popup with synchronization
				// tmux display-popup returns immediately, so we use wait-for to block until editor exits:
				// 1. Create popup that signals a channel when done
				// 2. Wait on that channel
				const channel = `az-editor-${Date.now()}`

				// Track popup for cleanup on SIGINT (store both channel and tempFile for killing)
				activeEditorState = { channel, tempFile }

				// Launch popup - when editor exits, it signals the channel
				Bun.spawnSync(
					[
						"tmux",
						"display-popup",
						"-E", // Close popup when command exits
						"-w",
						"90%",
						"-h",
						"90%",
						"--",
						"sh",
						"-c",
						`${editor} "${tempFile}"; tmux wait-for -S ${channel}`,
					],
					{ stdin: "inherit", stdout: "inherit", stderr: "inherit" },
				)

				// Block until the channel is signaled (editor closed)
				const waitResult = Bun.spawnSync(["tmux", "wait-for", channel], {
					stdin: "inherit",
					stdout: "inherit",
					stderr: "inherit",
				})

				// Only clear tracking if wait completed successfully (exit code 0)
				// If interrupted (non-zero exit), leave state set so SIGINT handler can clean up
				if (waitResult.exitCode === 0) {
					activeEditorState = null
				}

				// 6. Read edited content
				const editedMarkdown = yield* fs.readFileString(tempFile).pipe(
					Effect.mapError(
						(error) =>
							new EditorError({
								message: `Failed to read edited file: ${error}`,
							}),
					),
				)

				// 6. Parse changes
				const updates = yield* parseMarkdownToBead(editedMarkdown, bead)

				// 7. Apply updates
				const hasChanges = Object.keys(updates).length > 0
				if (!hasChanges) {
					// No changes - just return
					return
				}

				// 8. Handle type change specially (requires delete+create)
				if (updates.type) {
					// TODO: Implement type change (delete old, create new, preserve deps)
					// For now, just throw an error
					yield* Effect.fail(
						new EditorError({
							message: "Type changes not yet implemented. Please use bd CLI directly.",
						}),
					)
				}

				// 9. Apply updates via bd update
				// bd update supports these flags:
				const updateArgs: {
					status?: string
					notes?: string
					priority?: number
					title?: string
					description?: string
					design?: string
					acceptance?: string
					assignee?: string
					estimate?: number
				} = {}

				if (updates.status) updateArgs.status = updates.status
				if (updates.notes) updateArgs.notes = updates.notes
				if (updates.priority !== undefined) updateArgs.priority = updates.priority
				if (updates.title) updateArgs.title = updates.title
				if (updates.description) updateArgs.description = updates.description
				if (updates.design) updateArgs.design = updates.design
				if (updates.acceptance) updateArgs.acceptance = updates.acceptance
				if (updates.assignee !== undefined) updateArgs.assignee = updates.assignee
				if (updates.estimate !== undefined) updateArgs.estimate = updates.estimate

				// Build bd update command args
				const args: string[] = ["update", bead.id]
				if (updateArgs.status) args.push("--status", updateArgs.status)
				if (updateArgs.priority !== undefined) args.push("--priority", String(updateArgs.priority))
				if (updateArgs.title) args.push("--title", updateArgs.title)
				if (updateArgs.description) args.push("--description", updateArgs.description)
				if (updateArgs.design) args.push("--design", updateArgs.design)
				if (updateArgs.notes) args.push("--notes", updateArgs.notes)
				if (updateArgs.acceptance) args.push("--acceptance", updateArgs.acceptance)
				if (updateArgs.assignee) args.push("--assignee", updateArgs.assignee)
				if (updateArgs.estimate !== undefined) args.push("--estimate", String(updateArgs.estimate))

				// Handle labels separately (--set-labels)
				if (updates.labels) {
					updates.labels.forEach((label) => {
						args.push("--set-labels", label)
					})
				}

				// Execute bd update
				const updateCommand = Command.make("bd", ...args)
				yield* Command.exitCode(updateCommand).pipe(
					Effect.mapError(
						(error) =>
							new EditorError({
								message: `Failed to update bead: ${error}`,
							}),
					),
				)

				// Clean up temp file
				yield* Effect.ignoreLogged(
					fs.remove(tempFile).pipe(
						Effect.mapError(
							(error) =>
								new EditorError({
									message: `Failed to remove temp file: ${error}`,
								}),
						),
					),
				)
			}),

		createBead: () =>
			Effect.gen(function* () {
				const fs = yield* FileSystem.FileSystem

				// 1. Create blank template
				const markdown = createBlankBeadTemplate()

				// 2. Write to temp file
				const tempFile = `/tmp/azedarach-new.md`
				yield* fs.writeFileString(tempFile, markdown).pipe(
					Effect.mapError(
						(error) =>
							new EditorError({
								message: `Failed to write temp file: ${error}`,
							}),
					),
				)

				// 3. Get $EDITOR from environment (default to vim)
				const editor = process.env.EDITOR || "vim"

				// 4. Open editor in tmux popup with synchronization
				// tmux display-popup returns immediately, so we use wait-for to block until editor exits:
				// 1. Create popup that signals a channel when done
				// 2. Wait on that channel
				const channel = `az-editor-${Date.now()}`

				// Track popup for cleanup on SIGINT (store both channel and tempFile for killing)
				activeEditorState = { channel, tempFile }

				// Launch popup - when editor exits, it signals the channel
				Bun.spawnSync(
					[
						"tmux",
						"display-popup",
						"-E", // Close popup when command exits
						"-w",
						"90%",
						"-h",
						"90%",
						"--",
						"sh",
						"-c",
						`${editor} "${tempFile}"; tmux wait-for -S ${channel}`,
					],
					{ stdin: "inherit", stdout: "inherit", stderr: "inherit" },
				)

				// Block until the channel is signaled (editor closed)
				const waitResult = Bun.spawnSync(["tmux", "wait-for", channel], {
					stdin: "inherit",
					stdout: "inherit",
					stderr: "inherit",
				})

				// Only clear tracking if wait completed successfully (exit code 0)
				// If interrupted (non-zero exit), leave state set so SIGINT handler can clean up
				if (waitResult.exitCode === 0) {
					activeEditorState = null
				}

				// 6. Read edited content
				const editedMarkdown = yield* fs.readFileString(tempFile).pipe(
					Effect.mapError(
						(error) =>
							new EditorError({
								message: `Failed to read edited file: ${error}`,
							}),
					),
				)

				// 6. Parse the template
				const fields = yield* parseNewBeadTemplate(editedMarkdown)

				// 7. Build bd create command
				// Note: bd create doesn't support --status or --notes flags
				const args: string[] = ["create", "--title", fields.title, "--type", fields.type]

				if (fields.priority !== undefined) args.push("--priority", String(fields.priority))
				if (fields.description) args.push("--description", fields.description)
				if (fields.design) args.push("--design", fields.design)
				if (fields.acceptance) args.push("--acceptance", fields.acceptance)
				if (fields.assignee) args.push("--assignee", fields.assignee)
				if (fields.estimate !== undefined) args.push("--estimate", String(fields.estimate))
				if (fields.labels && fields.labels.length > 0) {
					fields.labels.forEach((label) => args.push("--labels", label))
				}

				// 8. Execute bd create and capture output
				const createCommand = Command.make("bd", ...args)
				const output = yield* Command.string(createCommand).pipe(
					Effect.mapError(
						(error) =>
							new EditorError({
								message: `Failed to create bead: ${error}`,
							}),
					),
				)

				// 9. Parse the bead ID from output (format: "Created issue: az-xxx")
				const idMatch = output.match(/Created issue:\s*(\S+)/i)
				if (!idMatch) {
					yield* Effect.fail(
						new EditorError({
							message: `Could not parse bead ID from output: ${output}`,
						}),
					)
				}
				const beadId = idMatch![1]!

				// 10. If status or notes were set, update the bead (bd create doesn't support these)
				const needsUpdate =
					(fields.status && fields.status !== "open") || fields.notes
				if (needsUpdate) {
					const updateArgs: string[] = ["update", beadId]
					if (fields.status && fields.status !== "open") {
						updateArgs.push("--status", fields.status)
					}
					if (fields.notes) {
						updateArgs.push("--notes", fields.notes)
					}
					const updateCommand = Command.make("bd", ...updateArgs)
					yield* Command.exitCode(updateCommand).pipe(
						Effect.mapError(
							(error) =>
								new EditorError({
									message: `Failed to update bead status/notes: ${error}`,
								}),
						),
					)
				}

				// 11. Clean up temp file
				yield* Effect.ignoreLogged(
					fs.remove(tempFile).pipe(
						Effect.mapError(
							(error) =>
								new EditorError({
									message: `Failed to remove temp file: ${error}`,
								}),
						),
					),
				)

				return {
					id: beadId,
					title: fields.title,
					type: fields.type,
				}
			}),
	})
})

/**
 * Live EditorService layer
 */
export const EditorServiceLive = Layer.effect(EditorService, EditorServiceImpl)
