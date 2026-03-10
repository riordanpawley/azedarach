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
	readonly implementations: readonly string[]
}

export interface PrimeImplementationContext {
	readonly implementations: readonly string[]
}

export const compactSingleLineText = (value: string): string => value.replace(/\s+/g, " ").trim()

const shouldShowIssueImplementations = (
	issue: TrackedIssue,
	showImplementations: boolean | undefined,
): boolean => {
	const implementations = issue.implementations ?? []
	return (
		(showImplementations === true && implementations.length > 0) ||
		implementations.length > 1 ||
		implementations.some((implementation) => implementation !== "default")
	)
}

export const formatIssueSummaryLine = (
	issue: TrackedIssue,
	options?: {
		readonly showImplementations?: boolean
	},
): string => {
	const implementations = issue.implementations ?? []
	return `${issue.id}: ${compactSingleLineText(issue.title)} [status=${issue.status} priority=${issue.priority} type=${issue.issue_type}${shouldShowIssueImplementations(issue, options?.showImplementations) ? ` impl=${implementations.join(",")}` : ""} updated_at=${issue.updated_at}]`
}

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

const formatIssueImplementationsSection = (
	issue: TrackedIssue,
	showImplementations: boolean | undefined,
): string | undefined =>
	shouldShowIssueImplementations(issue, showImplementations)
		? `Implementations:\n${(issue.implementations ?? []).join(", ")}`
		: undefined

export const formatIssueDetailSections = (
	issue: TrackedIssue,
	options?: {
		readonly linkedSpecRequirements?: ReadonlyArray<LinkedSpecRequirement> | undefined
		readonly showImplementations?: boolean
	},
): readonly string[] => {
	const sections: string[] = []
	const description = normalizeIssueTextField(issue.description)
	const design = normalizeIssueTextField(issue.design)
	const acceptance = normalizeIssueTextField(issue.acceptance)
	const notes = normalizeIssueTextField(issue.notes)
	const implementations = formatIssueImplementationsSection(issue, options?.showImplementations)
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
	if (implementations) {
		sections.push(implementations)
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

const normalizePrimeImplementations = (
	implementationContext: PrimeImplementationContext | undefined,
): readonly string[] => {
	if (implementationContext === undefined) {
		return []
	}

	const seen = new Set<string>()
	const implementations: string[] = []
	for (const implementation of implementationContext.implementations) {
		const normalized = implementation.trim()
		if (normalized.length === 0 || seen.has(normalized)) {
			continue
		}
		seen.add(normalized)
		implementations.push(normalized)
	}
	return implementations
}

const formatPrimeImplementationGuardrails = (
	implementationContext: PrimeImplementationContext | undefined,
): string | undefined => {
	const implementations = normalizePrimeImplementations(implementationContext)
	if (implementations.length <= 1) {
		return undefined
	}

	return [
		"- Implementation guardrails:",
		`  - This project has multiple implementations configured: ${implementations.join(", ")}.`,
		"  - New `az issue` and `az spec link` writes must include one or more `--impl <impl>` selections.",
		"  - The implicit `default` fallback only applies while the project has exactly one implementation configured.",
		"  - Repeated `--impl` flags mean intentionally shared work, for example `--impl ts-opentui --impl go-bubbletea`.",
	].join("\n")
}

export const buildPrimeOutput = (
	issueId: string | undefined,
	issueContext: string,
	implementationContext?: PrimeImplementationContext,
): string => {
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
	const implementationGuardrails = formatPrimeImplementationGuardrails(implementationContext)

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
${implementationGuardrails === undefined ? "" : `${implementationGuardrails}\n`}
- Keep issue context current as you work:
  - Update design/notes as implementation decisions change.
  - Use status/priority/labels flags when state changes materially.
  - In this repo, when guidance says \`spec\`, it means \`az spec\` requirement/link records, not README.md, AGENTS.md, or other internal docs.
  - Before implementing behavior changes, inspect relevant \`az spec\` requirements/links and align the plan to avoid spec drift.
  - Spec boundary for \`az spec\` usage:
    - Use \`az spec\` only for product behavior changes (user flows, API contracts, state rules, acceptance criteria).
    - Do NOT use \`az spec\` for infra-only work (hosting/VPS, DNS, TLS, CI/CD, cron host, vendor migration) when behavior is unchanged.
    - Track infra-only tasks in \`az issue\` only.
    - If unsure, default to no spec link and note: "Spec impact: none (infra-only)."
    - Example: Vercel -> Vultr with unchanged behavior => issue only, no spec link.
  - After implementing behavior changes, run an \`az spec\` compliance pass: verify behavior vs linked \`az spec\` requirements and update requirement/link records if scope changed.
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
