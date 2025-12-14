/**
 * BeadEditorService - Bead editor using $EDITOR
 *
 * Serializes beads to structured markdown, opens $EDITOR, parses changes,
 * and applies updates via BeadsClient.
 *
 * NOTE: Renamed from EditorService to avoid collision with ModeService
 * (src/services/EditorService.ts which handles editor modes in the UI)
 */

import { Command, type CommandExecutor, FileSystem, Path } from "@effect/platform"
import { Context, Data, Effect, Layer } from "effect"
import type { Issue } from "./BeadsClient"
import { BeadsClient } from "./BeadsClient"

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

export interface BeadEditorServiceImpl {
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
}

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
// Service Implementation
// ============================================================================

/**
 * BeadEditorService
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const editor = yield* BeadEditorService
 *   yield* editor.editBead(bead)
 * }).pipe(Effect.provide(BeadEditorService.Default))
 * ```
 */
export class BeadEditorService extends Effect.Service<BeadEditorService>()(
	"BeadEditorService",
	{
		dependencies: [BeadsClient.Default],
		effect: Effect.gen(function* () {
			return {
				editBead: (bead: Issue) =>
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

				// 4. Open editor (blocking)
				const command = Command.make(editor, tempFile)
				yield* Command.exitCode(command).pipe(
					Effect.mapError(
						(error) =>
							new EditorError({
								message: `Failed to open editor: ${error}`,
							}),
					),
				)

				// 5. Read edited content
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
			}
		}),
	},
) {}

/**
 * Legacy alias for BeadEditorService
 *
 * @deprecated Use BeadEditorService instead
 */
export const EditorService = BeadEditorService

/**
 * Legacy layer export
 *
 * @deprecated Use BeadEditorService.Default instead
 */
export const EditorServiceLive = BeadEditorService.Default
