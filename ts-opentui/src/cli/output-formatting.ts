import type { Issue as TrackedIssue } from "../core/IssueTrackerClient.js"
import type { TmuxStatus } from "../core/TmuxSessionMonitor.js"

type DependencyCountLabel =
	| "blocking"
	| "blockedBy"
	| "children"
	| "parent"
	| "related"
	| "discoveredFrom"
	| "discoveredBy"
type RelationshipDependencyType = "blocks" | "related" | "parent-child" | "discovered-from"

type LinkedSpecRequirement = {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
	readonly title: string
	readonly kind: string
	readonly link_type: string
}

export const compactSingleLineText = (value: string): string => value.replace(/\s+/g, " ").trim()

export const formatIssueSummaryLine = (issue: TrackedIssue): string =>
	`${issue.id}: ${compactSingleLineText(issue.title)} [status=${issue.status} priority=${issue.priority} type=${issue.issue_type} updated_at=${issue.updated_at}]`

const normalizeIssueTextField = (value: string | undefined): string | undefined => {
	const normalized = value?.trim()
	return normalized && normalized.length > 0 ? normalized : undefined
}

export const formatSpecRequirementReference = (requirement: {
	readonly local_id: string
	readonly external_code: string | null
}): string =>
	requirement.external_code === null
		? requirement.local_id
		: `${requirement.local_id} (${requirement.external_code})`

const DEPENDENCY_COUNT_LABEL_ORDER: readonly DependencyCountLabel[] = [
	"blocking",
	"blockedBy",
	"children",
	"parent",
	"related",
	"discoveredFrom",
	"discoveredBy",
]

const normalizeDependencyIds = (
	refs: ReadonlyArray<{ readonly id: string }> | undefined,
): readonly string[] => {
	if (!refs || refs.length === 0) {
		return []
	}
	const seen = new Set<string>()
	const ids: string[] = []
	for (const ref of refs) {
		const normalized = ref.id.trim()
		if (normalized.length === 0 || seen.has(normalized)) continue
		seen.add(normalized)
		ids.push(normalized)
	}
	return ids
}

const formatIssueRelationshipSection = (
	label: "Dependencies" | "Dependents",
	refs: ReadonlyArray<{ readonly id: string }> | undefined,
	count: number | undefined,
): string | undefined => {
	const ids = normalizeDependencyIds(refs)
	if (ids.length > 0) {
		return `${label}:\n${ids.join(", ")}`
	}
	if (count !== undefined && count > 0) {
		return `${label}: ${count}`
	}
	return undefined
}

const dependencyCountLabelFromDependency = (
	dependencyType: RelationshipDependencyType,
): DependencyCountLabel => {
	switch (dependencyType) {
		case "blocks":
			return "blockedBy"
		case "parent-child":
			return "parent"
		case "discovered-from":
			return "discoveredFrom"
		default:
			return "related"
	}
}

const dependencyCountLabelFromDependent = (
	dependencyType: RelationshipDependencyType,
): DependencyCountLabel => {
	switch (dependencyType) {
		case "blocks":
			return "blocking"
		case "parent-child":
			return "children"
		case "discovered-from":
			return "discoveredBy"
		default:
			return "related"
	}
}

const formatIssueDependencyTypeCountsSection = (issue: TrackedIssue): string | undefined => {
	const counts = new Map<DependencyCountLabel, number>()
	for (const dependency of issue.dependencies ?? []) {
		const label = dependencyCountLabelFromDependency(dependency.dependency_type)
		counts.set(label, (counts.get(label) ?? 0) + 1)
	}
	for (const dependent of issue.dependents ?? []) {
		const label = dependencyCountLabelFromDependent(dependent.dependency_type)
		counts.set(label, (counts.get(label) ?? 0) + 1)
	}

	const parts = DEPENDENCY_COUNT_LABEL_ORDER.flatMap((label) => {
		const count = counts.get(label)
		return count && count > 0 ? `${label}: ${count}` : []
	})
	if (parts.length === 0) {
		return undefined
	}
	return `Dependency Counts: ${parts.join(", ")}`
}

const formatIssueLinkedSpecSection = (
	linkedSpecRequirements: ReadonlyArray<LinkedSpecRequirement> | undefined,
): string | undefined => {
	if (!linkedSpecRequirements || linkedSpecRequirements.length === 0) {
		return undefined
	}

	const lines = linkedSpecRequirements.map(
		(requirement) =>
			`${formatSpecRequirementReference(requirement)} [${requirement.kind}] (${requirement.link_type}) ${compactSingleLineText(requirement.title)}`,
	)
	return `Linked Spec Requirements:\n${lines.join("\n")}`
}

export const formatIssueDetailSections = (
	issue: TrackedIssue,
	options?: {
		readonly linkedSpecRequirements?: ReadonlyArray<LinkedSpecRequirement> | undefined
	},
): readonly string[] => {
	const sections: string[] = []
	const description = normalizeIssueTextField(issue.description)
	const design = normalizeIssueTextField(issue.design)
	const acceptance = normalizeIssueTextField(issue.acceptance)
	const notes = normalizeIssueTextField(issue.notes)
	const dependencyTypeCounts = formatIssueDependencyTypeCountsSection(issue)
	const dependencies = formatIssueRelationshipSection(
		"Dependencies",
		issue.dependencies,
		issue.dependency_count,
	)
	const dependents = formatIssueRelationshipSection(
		"Dependents",
		issue.dependents,
		issue.dependent_count,
	)
	const linkedSpecs = formatIssueLinkedSpecSection(options?.linkedSpecRequirements)

	if (description) {
		sections.push(`Description:\n${description}`)
	}
	if (design) {
		sections.push(`Design:\n${design}`)
	}
	if (acceptance) {
		sections.push(`Acceptance:\n${acceptance}`)
	}
	if (notes) {
		sections.push(`Notes:\n${notes}`)
	}
	if (dependencyTypeCounts) {
		sections.push(dependencyTypeCounts)
	}
	if (dependencies) {
		sections.push(dependencies)
	}
	if (dependents) {
		sections.push(dependents)
	}
	if (linkedSpecs) {
		sections.push(linkedSpecs)
	}

	return sections
}

export const buildPrimeOutput = (issueId: string | undefined, issueContext: string): string => {
	const issueSection =
		issueId === undefined
			? ""
			: issueContext.length > 0
				? `

Active issue context (AZEDARACH_ISSUE_ID=${issueId}):
\`\`\`
${issueContext.length > 4000 ? `${issueContext.slice(0, 4000)}\n...` : issueContext}
\`\`\``
				: `

Active issue from AZEDARACH_ISSUE_ID=${issueId}.
Could not load issue details automatically; run \`az issue get ${issueId}\`.`

	const contextGuardrail =
		issueId === undefined
			? "- No active issue is preselected. When work starts, set `AZEDARACH_ISSUE_ID` or run `az issue get <issue-id>`."
			: `- \`AZEDARACH_ISSUE_ID\` is set to \`${issueId}\`; use it as the default issue scope and refresh stale context with \`az issue get ${issueId}\`.`

	return `Azedarach Session Primer

- Use \`az issue\` commands as the task-tracker interface for this repo.
- Start each session with: \`az prime\`
- Mandatory parenting rule: when working under a parent issue, create follow-up work with \`az issue child "Title"\` (or \`az issue create "Title"\`, which now defaults to active parent context unless \`--deferred\` is set).
- Dependency helpers: \`az issue dep add <issue-id> <depends-on-id> [--type blocks|related|parent-child|discovered-from]\` (defaults to \`blocks\`)
- Common issue commands:
  - \`az issue list --limit 20\` (lists most recently updated issues first)
  - \`az issue get <issue-id>\` (use \`--json\` when you need full structured output)
  - \`az issue child "Child task"\` (uses active parent context, or \`--parent <issue-id>\`)
  - \`az issue update <issue-id> --design "..."\`
  - \`az issue update <issue-id> --notes "..."\`
  - \`az issue update <issue-id> --status in_progress|blocked|open\`
  - \`az issue create "Title" --type task|bug|epic|chore --priority 1-5\`
  - \`az issue create "Child task" --parent <epic-id>\`
  - \`az issue close <issue-id> --reason "..."\` (guards against closing parents with open children)
  - \`az issue --help\`
- Issue-context guardrails:
  ${contextGuardrail}
  - Missing fields (for example description/design/acceptance/notes) are valid. Treat absent or empty fields as intentional and continue execution.
  - Do not go on history/log hunting tangents to backfill missing fields unless the user explicitly asks for that research.
- Keep issue context current as you work:
  - Update design/notes as implementation decisions change.
  - Use status/priority/labels flags when state changes materially.
  - Before implementing behavior changes, inspect relevant \`az spec\` requirements/links and align the plan to avoid spec drift.
  - After implementing behavior changes, run a spec compliance pass: verify behavior vs linked requirements and update requirement/link records if scope changed.
  - Spec sync discipline (ts-opentui behavior changes): update az spec requirement/link records in the same task, or record "Spec impact: none" with concrete file-based rationale.
  - For \`az spec\` commands, keep canonical Effect CLI ordering: options/flags before positional refs (for example \`az spec req get -j fr4203\`).
- Create follow-up/child work in the tracker instead of local TODOs.
- Prefer \`az issue\` operations over direct backend issue CLI commands in sessions.
- When work is complete:
  - Commit your changes first (\`git add -A && git commit -m "<issue-id>: ..."\`).
  - Always include the issue ID in the commit message.
  - Close the issue when implementation is ready for review (\`az issue close <issue-id>\`).
  - Review flow policy: reviews target closed tasks, not in-progress tasks.
  - If review finds remaining work, move the issue back to in-progress and continue.
${issueSection}
`
}

type WaitingAttentionPlan = {
	readonly ringBell: boolean
	readonly nextFlag: "0" | "1"
}

export const deriveWaitingAttentionPlan = (
	status: TmuxStatus,
	currentFlagRaw: string | null,
): WaitingAttentionPlan => {
	const normalizedFlag = currentFlagRaw?.trim() === "1" ? "1" : "0"
	if (status === "waiting") {
		return {
			ringBell: normalizedFlag !== "1",
			nextFlag: "1",
		}
	}
	return {
		ringBell: false,
		nextFlag: "0",
	}
}
