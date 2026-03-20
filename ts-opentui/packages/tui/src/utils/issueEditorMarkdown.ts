import { Data, Effect } from "effect"
import type { Issue, IssueStatus, IssueType } from "../contracts.js"
import { formatIssueImplementations, parseIssueImplementations } from "./issueImplementations.js"

export class ParseMarkdownError extends Data.TaggedError("ParseMarkdownError")<{
	readonly message: string
	readonly markdown: string
}> {}

export interface UpdatedFields {
	readonly priority?: number
	readonly status?: IssueStatus
	readonly assignee?: string
	readonly labels?: readonly string[]
	readonly implementations?: readonly string[]
	readonly estimate?: number
	readonly title?: string
	readonly description?: string
	readonly design?: string
	readonly notes?: string
	readonly acceptance?: string
	readonly type?: IssueType
}

export interface NewIssueFields {
	readonly title: string
	readonly type: IssueType
	readonly priority: number
	readonly status: IssueStatus
	readonly assignee?: string
	readonly labels?: readonly string[]
	readonly implementations?: readonly string[]
	readonly estimate?: number
	readonly description?: string
	readonly design?: string
	readonly notes?: string
	readonly acceptance?: string
}

const priorityToLabel = (priority: number): string => `P${priority}`

const parsePriorityLabel = (label: string): number | undefined => {
	const match = label.match(/^P([0-4])$/i)
	if (match === null) {
		return undefined
	}
	return Number.parseInt(match[1], 10)
}

const parseTypeLabel = (label: string): IssueType | undefined => {
	const normalized = label.toLowerCase().trim()
	switch (normalized) {
		case "bug":
		case "feature":
		case "task":
		case "epic":
		case "chore":
			return normalized
		default:
			return undefined
	}
}

const parseStatusLabel = (label: string): IssueStatus | undefined => {
	const normalized = label.toLowerCase().trim()
	switch (normalized) {
		case "open":
		case "in_progress":
		case "blocked":
		case "closed":
		case "tombstone":
			return normalized
		default:
			return undefined
	}
}

const extractFieldValue = (line: string, fieldName: string): string => {
	const regex = new RegExp(`^${fieldName}:\\s*(.*)$`, "i")
	const match = line.match(regex)
	if (match === null) {
		return ""
	}
	return match[1].trim()
}

const parseSection = (markdown: string, sectionName: string): string => {
	const lines = markdown.split("\n")
	let inSection = false
	const content: string[] = []

	for (const line of lines) {
		if (line.trim() === `## ${sectionName}`) {
			inSection = true
			continue
		}

		if (inSection && (line.startsWith("##") || line.startsWith("───"))) {
			break
		}

		if (inSection && line.trim() !== "") {
			content.push(line)
		}
	}

	return content.join("\n").trim()
}

export const ISSUE_EDITOR_ANCHORS = {
	TITLE: "TITLE",
	DESCRIPTION: "DESCRIPTION",
	DESIGN: "DESIGN",
	NOTES: "NOTES",
	ACCEPTANCE: "ACCEPTANCE",
}

export const serializeIssueToMarkdown = (issue: Issue): string => {
	const lines: string[] = []

	lines.push(`# ${issue.id}: ${issue.title}`)
	lines.push("───────────────────────────────────────────────────")
	lines.push("")
	lines.push(`Type:     ${issue.issue_type}        (read-only - changing requires delete+create)`)
	lines.push(`Priority: ${priorityToLabel(issue.priority)}`)
	lines.push(`Status:   ${issue.status}`)
	lines.push(`Assignee: ${issue.assignee ?? ""}`)
	lines.push(`Labels:   ${(issue.labels ?? []).join(", ")}`)
	lines.push(`Impl:     ${formatIssueImplementations(issue.implementations)}`)
	lines.push(`Estimate: ${issue.estimate ?? ""}`)
	lines.push("")
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Description")
	lines.push("")
	lines.push(issue.description ?? "")
	lines.push("")
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Design")
	lines.push("")
	lines.push(issue.design ?? "")
	lines.push("")
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Notes")
	lines.push("")
	lines.push(issue.notes ?? "")
	lines.push("")
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Acceptance Criteria")
	lines.push("")
	lines.push(issue.acceptance ?? "")
	lines.push("")

	return lines.join("\n")
}

export const parseMarkdownToIssue = (
	markdown: string,
	original: Issue,
): Effect.Effect<UpdatedFields, ParseMarkdownError> =>
	Effect.try({
		try: () => {
			const lines = markdown.split("\n")
			const updates: {
				priority?: number
				status?: IssueStatus
				assignee?: string
				labels?: readonly string[]
				implementations?: readonly string[]
				estimate?: number
				title?: string
				description?: string
				design?: string
				notes?: string
				acceptance?: string
				type?: IssueType
			} = {}

			const headerLine = lines[0]
			if (headerLine === undefined || !headerLine.startsWith("#")) {
				throw new Error("Missing header line")
			}
			const headerMatch = headerLine.match(/^#\s+([^:]+):\s+(.+)$/)
			if (headerMatch === null) {
				throw new Error("Invalid header format. Expected: # {id}: {title}")
			}
			const parsedTitle = headerMatch[2].trim()
			if (parsedTitle !== original.title) {
				updates.title = parsedTitle
			}

			const metadataLines: string[] = []
			let inMetadata = false
			let separatorCount = 0
			for (const line of lines) {
				if (line.startsWith("───")) {
					separatorCount += 1
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

			for (const line of metadataLines) {
				if (line.startsWith("Type:")) {
					const typeValue = extractFieldValue(line, "Type").split("(")[0].trim()
					const parsedType = parseTypeLabel(typeValue)
					if (parsedType !== undefined && parsedType !== original.issue_type) {
						updates.type = parsedType
					}
				}

				if (line.startsWith("Priority:")) {
					const priorityValue = extractFieldValue(line, "Priority")
					const parsedPriority = parsePriorityLabel(priorityValue)
					if (parsedPriority !== undefined && parsedPriority !== original.priority) {
						updates.priority = parsedPriority
					}
				}

				if (line.startsWith("Status:")) {
					const statusValue = extractFieldValue(line, "Status")
					const parsedStatus = parseStatusLabel(statusValue)
					if (parsedStatus !== undefined && parsedStatus !== original.status) {
						updates.status = parsedStatus
					}
				}

				if (line.startsWith("Assignee:")) {
					const assigneeValue = extractFieldValue(line, "Assignee")
					const originalAssignee = original.assignee ?? ""
					if (assigneeValue !== originalAssignee) {
						updates.assignee = assigneeValue.length > 0 ? assigneeValue : undefined
					}
				}

				if (line.startsWith("Labels:")) {
					const labelsValue = extractFieldValue(line, "Labels")
					const parsedLabels =
						labelsValue.length > 0
							? labelsValue
									.split(",")
									.map((label) => label.trim())
									.filter((label) => label.length > 0)
							: []
					const originalLabels = original.labels ?? []
					const labelsChanged =
						parsedLabels.length !== originalLabels.length ||
						parsedLabels.some((label, index) => label !== originalLabels[index])
					if (labelsChanged) {
						updates.labels = parsedLabels
					}
				}

				if (line.startsWith("Impl:")) {
					const implementationValue = extractFieldValue(line, "Impl").split("(")[0].trim()
					const parsedImplementations = parseIssueImplementations(implementationValue)
					if (parsedImplementations !== undefined) {
						const originalImplementations = original.implementations
						const changed =
							parsedImplementations.length !== originalImplementations.length ||
							parsedImplementations.some(
								(implementation, index) => implementation !== originalImplementations[index],
							)
						if (changed) {
							updates.implementations = parsedImplementations
						}
					}
				}

				if (line.startsWith("Estimate:")) {
					const estimateValue = extractFieldValue(line, "Estimate")
					const parsedEstimate =
						estimateValue.length > 0 ? Number.parseInt(estimateValue, 10) : undefined
					const nextEstimate =
						parsedEstimate !== undefined && !Number.isNaN(parsedEstimate)
							? parsedEstimate
							: undefined
					if (nextEstimate !== (original.estimate ?? undefined)) {
						updates.estimate = nextEstimate
					}
				}
			}

			const description = parseSection(markdown, "Description")
			if (description !== (original.description ?? "")) {
				updates.description = description
			}

			const design = parseSection(markdown, "Design")
			if (design !== (original.design ?? "")) {
				updates.design = design
			}

			const notes = parseSection(markdown, "Notes")
			if (notes !== (original.notes ?? "")) {
				updates.notes = notes
			}

			const acceptance = parseSection(markdown, "Acceptance Criteria")
			if (acceptance !== (original.acceptance ?? "")) {
				updates.acceptance = acceptance
			}

			return updates
		},
		catch: (error) =>
			new ParseMarkdownError({
				message: error instanceof Error ? error.message : String(error),
				markdown,
			}),
	})

export const createBlankIssueTemplate = (
	defaultImplementation: string,
	availableImplementations: readonly string[],
): string => {
	const lines: string[] = []
	const implementationOptions =
		availableImplementations.length > 0
			? availableImplementations.join(" | ")
			: defaultImplementation

	lines.push(`# ${ISSUE_EDITOR_ANCHORS.TITLE}`)
	lines.push("───────────────────────────────────────────────────")
	lines.push("")
	lines.push("Type:     task        (task | bug | feature | epic | chore)")
	lines.push("Priority: P2          (P0 = highest, P4 = lowest)")
	lines.push("Status:   open        (open | in_progress | blocked | closed)")
	lines.push("Assignee: ")
	lines.push("Labels:   ")
	lines.push(`Impl:     ${defaultImplementation}        (${implementationOptions})`)
	lines.push("Estimate: ")
	lines.push("")
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Description")
	lines.push("")
	lines.push(ISSUE_EDITOR_ANCHORS.DESCRIPTION)
	lines.push("")
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Design")
	lines.push("")
	lines.push(ISSUE_EDITOR_ANCHORS.DESIGN)
	lines.push("")
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Notes")
	lines.push("")
	lines.push(ISSUE_EDITOR_ANCHORS.NOTES)
	lines.push("")
	lines.push("───────────────────────────────────────────────────")
	lines.push("## Acceptance Criteria")
	lines.push("")
	lines.push(ISSUE_EDITOR_ANCHORS.ACCEPTANCE)
	lines.push("")

	return lines.join("\n")
}

export const parseMarkdownToNewIssue = (
	markdown: string,
): Effect.Effect<NewIssueFields, ParseMarkdownError> =>
	Effect.try({
		try: () => {
			const lines = markdown.split("\n")
			const partial: {
				title?: string
				type?: IssueType
				priority?: number
				status?: IssueStatus
				assignee?: string
				labels?: readonly string[]
				implementations?: readonly string[]
				estimate?: number
				description?: string
				design?: string
				notes?: string
				acceptance?: string
			} = {}

			const headerLine = lines[0]
			if (headerLine === undefined || !headerLine.startsWith("#")) {
				throw new Error("Missing header line")
			}
			const headerMatch = headerLine.match(/^#\s+(.+)$/)
			if (headerMatch === null) {
				throw new Error("Invalid header format. Expected: # Title")
			}
			const parsedTitle = headerMatch[1].trim()
			if (parsedTitle.length === 0 || parsedTitle === ISSUE_EDITOR_ANCHORS.TITLE) {
				throw new Error("Title is required")
			}
			partial.title = parsedTitle

			const metadataLines: string[] = []
			let inMetadata = false
			let separatorCount = 0
			for (const line of lines) {
				if (line.startsWith("───")) {
					separatorCount += 1
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

			for (const line of metadataLines) {
				if (line.startsWith("Type:")) {
					const typeValue = extractFieldValue(line, "Type").split("(")[0].trim()
					const parsedType = parseTypeLabel(typeValue)
					if (parsedType === undefined) {
						throw new Error(`Invalid type: ${typeValue}`)
					}
					partial.type = parsedType
				}

				if (line.startsWith("Priority:")) {
					const priorityValue = extractFieldValue(line, "Priority").split("(")[0].trim()
					const parsedPriority = parsePriorityLabel(priorityValue)
					if (parsedPriority === undefined) {
						throw new Error(`Invalid priority: ${priorityValue}`)
					}
					partial.priority = parsedPriority
				}

				if (line.startsWith("Status:")) {
					const statusValue = extractFieldValue(line, "Status").split("(")[0].trim()
					const parsedStatus = parseStatusLabel(statusValue)
					if (parsedStatus !== undefined) {
						partial.status = parsedStatus
					}
				}

				if (line.startsWith("Assignee:")) {
					const assigneeValue = extractFieldValue(line, "Assignee")
					if (assigneeValue.length > 0) {
						partial.assignee = assigneeValue
					}
				}

				if (line.startsWith("Labels:")) {
					const labelsValue = extractFieldValue(line, "Labels")
					if (labelsValue.length > 0) {
						const labels = labelsValue
							.split(",")
							.map((label) => label.trim())
							.filter((label) => label.length > 0)
						if (labels.length > 0) {
							partial.labels = labels
						}
					}
				}

				if (line.startsWith("Impl:")) {
					const implementationValue = extractFieldValue(line, "Impl").split("(")[0].trim()
					const parsedImplementations = parseIssueImplementations(implementationValue)
					if (parsedImplementations !== undefined) {
						partial.implementations = parsedImplementations
					}
				}

				if (line.startsWith("Estimate:")) {
					const estimateValue = extractFieldValue(line, "Estimate")
					if (estimateValue.length > 0) {
						const parsedEstimate = Number.parseInt(estimateValue, 10)
						if (!Number.isNaN(parsedEstimate)) {
							partial.estimate = parsedEstimate
						}
					}
				}
			}

			const description = parseSection(markdown, "Description")
			if (description.length > 0 && description !== ISSUE_EDITOR_ANCHORS.DESCRIPTION) {
				partial.description = description
			}

			const design = parseSection(markdown, "Design")
			if (design.length > 0 && design !== ISSUE_EDITOR_ANCHORS.DESIGN) {
				partial.design = design
			}

			const notes = parseSection(markdown, "Notes")
			if (notes.length > 0 && notes !== ISSUE_EDITOR_ANCHORS.NOTES) {
				partial.notes = notes
			}

			const acceptance = parseSection(markdown, "Acceptance Criteria")
			if (acceptance.length > 0 && acceptance !== ISSUE_EDITOR_ANCHORS.ACCEPTANCE) {
				partial.acceptance = acceptance
			}

			if (
				partial.title === undefined ||
				partial.type === undefined ||
				partial.priority === undefined ||
				partial.status === undefined
			) {
				throw new Error("Missing required issue fields")
			}

			return {
				title: partial.title,
				type: partial.type,
				priority: partial.priority,
				status: partial.status,
				assignee: partial.assignee,
				labels: partial.labels,
				implementations: partial.implementations,
				estimate: partial.estimate,
				description: partial.description,
				design: partial.design,
				notes: partial.notes,
				acceptance: partial.acceptance,
			}
		},
		catch: (error) =>
			new ParseMarkdownError({
				message: error instanceof Error ? error.message : String(error),
				markdown,
			}),
	})
