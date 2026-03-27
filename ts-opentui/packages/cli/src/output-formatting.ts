import type { Issue as TrackedIssue } from "../../../src/core/IssueTrackerClient.js"
import type { SpecRequirementRef } from "../../../src/core/specTypes.js"
import type { TmuxStatus } from "../../../src/core/TmuxSessionMonitor.js"

type DependencyCountLabel =
	| "blocking"
	| "blockedBy"
	| "children"
	| "parent"
	| "related"
	| "discoveredFrom"
	| "discoveredBy"
type RelationshipDependencyType = "blocks" | "related" | "parent-child" | "discovered-from"

export interface PrimeImplementationContext {
	readonly implementations: ReadonlyArray<{
		readonly name: string
		readonly description?: string
		readonly directory?: string
		readonly is_default: boolean
		readonly is_builtin: boolean
	}>
}

export interface PrimeIssueContext {
	readonly issue: TrackedIssue
	readonly linkedSpecRequirements?: ReadonlyArray<SpecRequirementRef>
	readonly showImplementations?: boolean
}

export type PrimeMode = "default" | "question-first" | "subagent"

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
	linkedSpecRequirements: ReadonlyArray<SpecRequirementRef> | undefined,
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
		readonly linkedSpecRequirements?: ReadonlyArray<SpecRequirementRef> | undefined
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
		const normalized = implementation.name.trim()
		if (normalized.length === 0 || seen.has(normalized)) {
			continue
		}
		seen.add(normalized)
		implementations.push(normalized)
	}
	return implementations
}

const formatPrimeImplementationMetadata = (
	implementationContext: PrimeImplementationContext | undefined,
): string | undefined => {
	if (implementationContext === undefined || implementationContext.implementations.length === 0) {
		return undefined
	}

	const items = implementationContext.implementations.map((implementation) => {
		const metadata = [
			implementation.directory === undefined ? undefined : `dir=${implementation.directory}`,
			implementation.is_default ? "default" : undefined,
			implementation.is_builtin ? "builtin" : undefined,
		].filter((value): value is string => value !== undefined)
		const description =
			implementation.description === undefined
				? undefined
				: compactSingleLineText(implementation.description)
		const suffix = [metadata.length === 0 ? undefined : metadata.join(", "), description].filter(
			(value): value is string => value !== undefined,
		)
		return suffix.length === 0
			? implementation.name
			: `${implementation.name} (${suffix.join("; ")})`
	})

	return items.length === 0 ? undefined : items.join(", ")
}

const formatPrimeImplementationGuardrails = (
	implementationContext: PrimeImplementationContext | undefined,
	specEnabled: boolean,
): string | undefined => {
	const implementations = normalizePrimeImplementations(implementationContext)
	if (implementations.length <= 1) {
		return undefined
	}

	return [
		"- Implementation guardrails:",
		`  - This project has multiple implementations configured: ${implementations.join(", ")}.`,
		...(() => {
			const metadata = formatPrimeImplementationMetadata(implementationContext)
			return metadata === undefined ? [] : [`  - Implementation metadata: ${metadata}.`]
		})(),
		specEnabled
			? "  - New `az issue` and `az spec link` writes must include one or more `--impl <impl>` selections."
			: "  - New `az issue` writes must include one or more `--impl <impl>` selections.",
		"  - The implicit `default` fallback only applies while the project has exactly one implementation configured.",
		"  - Repeated `--impl` flags mean intentionally shared work, for example `--impl ts-opentui --impl go-bubbletea`.",
	].join("\n")
}

const PRIME_ISSUE_CONTEXT_MAX_LENGTH = 3200

const clipPrimeIssueContext = (value: string): string =>
	value.length > PRIME_ISSUE_CONTEXT_MAX_LENGTH
		? `${value.slice(0, PRIME_ISSUE_CONTEXT_MAX_LENGTH)}\n...`
		: value

const formatPrimeIssueSection = (
	issueId: string,
	issueContext: PrimeIssueContext | undefined,
): string =>
	issueContext === undefined
		? `

Active issue context (AZEDARACH_ISSUE_ID=${issueId}):
Could not load issue details automatically; run \`az issue get ${issueId}\`.`
		: (() => {
				const renderedIssueContext = [
					formatIssueSummaryLine(issueContext.issue, {
						showImplementations: issueContext.showImplementations,
					}),
					...formatIssueDetailSections(issueContext.issue, {
						linkedSpecRequirements: issueContext.linkedSpecRequirements,
						showImplementations: issueContext.showImplementations,
					}),
				].join("\n\n")

				return `

Active issue context (AZEDARACH_ISSUE_ID=${issueId}):
Refresh with \`az issue get ${issueId}\` if this looks stale.
\`\`\`
${clipPrimeIssueContext(renderedIssueContext)}
\`\`\``
			})()

const renderPrimeSpecGuardrails =
	(): string => `  - In this repo, when guidance says \`spec\`, it means \`az spec\` requirement/link records, not README.md, AGENTS.md, or other internal docs.
  - Treat \`az spec link\` records as required traceability for behavior work: they are how planning, implementation, and review stay aligned to requirements.
  - Before coding behavior changes, confirm the issue has the right requirement links (or add/update them) so acceptance checks stay explicit.
  - Before implementing behavior changes, inspect relevant \`az spec\` requirements/links and align the plan to avoid spec drift.
  - Spec boundary for \`az spec\` usage:
    - Use \`az spec\` only for product behavior changes (user flows, API contracts, state rules, acceptance criteria).
    - Do NOT use \`az spec\` for infra-only work (hosting/VPS, DNS, TLS, CI/CD, cron host, vendor migration) when behavior is unchanged.
    - Track infra-only tasks in \`az issue\` only.
    - If unsure, default to no spec link and note: "Spec impact: none (infra-only)."
    - Example: Vercel -> Vultr with unchanged behavior => issue only, no spec link.
  - After implementing behavior changes, run an \`az spec\` compliance pass: verify behavior vs linked \`az spec\` requirements and update requirement/link records if scope changed.
  - Spec sync discipline (ts-opentui behavior changes): update az spec requirement/link records in the same task, or record "Spec impact: none" with concrete file-based rationale.
  - For \`az\` commands, always place options/flags before positional refs; positional-first ordering can be rejected by Effect CLI parsing.
  - Prefer named flag references over positional refs whenever available (for example \`--issue\`, \`--req\`, \`--id\`, \`--local-id\`, \`--external-code\`, \`--query\`).
  - For \`az spec link add\`, use either explicit refs (\`az spec link add --issue <issue-id> --req <requirement-ref> --type relates --fulfillment-status planned --impl <impl>\`) or place flags before positional refs (\`az spec link add --type relates --fulfillment-status planned --impl <impl> <issue-id> <requirement-ref>\`).
  - For \`az spec req update\`, prefer \`az spec req update --req <requirement-ref> --title "..." --body "..."\` over positional refs.
  - Avoid positional-first ordering like \`az spec link add <issue-id> <requirement-ref> -t relates -f planned\`; Effect CLI parsing can reject late flags as unknown arguments.
  - If this project should not use spec workflows, disable them with \`az config set spec.enabled false\` (or set \`spec.enabled\` to false in \`.azedarach/config.json\`).`

const renderQuestionFirstGuardrails =
	(): string => `- Question-first execution rules (Space+Q mode):
  - MUST ask follow-up questions immediately when the issue is underspecified or ambiguous.
  - MUST improve the current issue title and description before implementation work begins.
  - MUST record unknowns/open questions in the issue description so scope is explicit.`

const renderSubagentGuardrails = (): string => `- Subagent execution rules:
  - You are a leaf worker, not the orchestrator.
  - Keep work scoped to the assigned child issue and keep updates concise.
  - Do not fan out to additional subagents unless explicitly instructed.
  - If the task still needs decomposition, stop and return the boundary to the orchestrator.
  - If you need a fresh primer, run \`az prime subagent\` instead of \`az prime\`.
  - If asked to split work, use the \`single-window fanout\` rule: split until each child fits in one subagent context window, then hand the plan back up.`

export const buildPrimeOutput = (
	issueId: string | undefined,
	issueContext: PrimeIssueContext | undefined,
	implementationContext?: PrimeImplementationContext,
	specEnabled = false,
	primeMode: PrimeMode = "default",
): string => {
	const issueSection = issueId === undefined ? "" : formatPrimeIssueSection(issueId, issueContext)
	const issueFetchCommand =
		issueId === undefined ? "`az issue get <issue-id>`" : `\`az issue get ${issueId}\``
	const specCheckStep = specEnabled
		? "`az spec` (inspect linked requirements before behavior changes)"
		: "Spec workflows are disabled for this project (skip spec checks)."
	const primerTitle =
		primeMode === "subagent" ? "Azedarach Subagent Primer" : "Azedarach Session Primer"
	const firstCommandsHeader =
		primeMode === "subagent"
			? "- First 3 commands for this subagent:"
			: "- First 3 commands for this session:"
	const issueChildStep =
		primeMode === "subagent"
			? '`az issue child "Title"` (only when explicitly told to split work further)'
			: '`az issue child "Title"` (when you need follow-up scope under the active parent)'

	const contextGuardrail =
		issueId === undefined
			? "- No active issue is preselected. When work starts, set `AZEDARACH_ISSUE_ID` or run `az issue get <issue-id>`."
			: `- \`AZEDARACH_ISSUE_ID\` is set to \`${issueId}\`; use it as the default issue scope and refresh stale context with \`az issue get ${issueId}\`.`
	const implementationGuardrails = formatPrimeImplementationGuardrails(
		implementationContext,
		specEnabled,
	)
	const specGuardrails = specEnabled ? renderPrimeSpecGuardrails() : undefined
	const modeGuardrails =
		primeMode === "question-first"
			? renderQuestionFirstGuardrails()
			: primeMode === "subagent"
				? renderSubagentGuardrails()
				: undefined

	return `${primerTitle}

- ${firstCommandsHeader}
  - ${issueFetchCommand}
  - ${specCheckStep}
  - ${issueChildStep}
- Use \`az issue\` commands as the task-tracker interface for this repo.
- Prefer \`az issue\` operations over direct backend issue CLI commands in sessions.
- Create follow-up/child work in the tracker instead of local TODOs.
${issueSection}
${modeGuardrails === undefined ? "" : `${modeGuardrails}\n`}
- Follow-up and dependency rules:
  - When working under a parent issue, create follow-up work with \`az issue child "Title"\`.
  - When fanning out to subagents, split work until each child issue is independently actionable and fits within a single subagent context window.
  - Then assign one subagent per child issue, tell each subagent to use \`az issue\` and create/maintain its own child issue under the active parent, and reserve \`az prime\` for the orchestrator unless a subagent explicitly needs a fresh primer.
  - Shorthand: \`single-window fanout\` means split until each child is ready for one subagent, then fan out one subagent per child.
  - \`az issue create "Title"\` defaults to the active parent context (including \`AZEDARACH_ISSUE_ID\`) unless \`--deferred\` is set.
  - There is no top-level \`az dep\` command; use \`az issue dep ...\`.
  - Use \`az issue dep add <issue-id> <depends-on-id> [--type blocks|related|parent-child|discovered-from]\` to record dependency relationships (\`blocks\` is the default type).
  - Use \`az issue dep remove <issue-id> <depends-on-id> [--type blocks|related|parent-child|discovered-from]\` to remove dependency relationships.
- Fanout and mailbox quickstart:
  - \`az issue fanout --input ./fanout.json\` (plan nested child issues from spec)
  - \`az issue fanout --input ./fanout.json --apply\` (create issues + dependency edges)
  - \`az issue fanout ready --root <issue-id> --json\` (show runnable leaf issues)
  - \`az issue fanout drift --issue <issue-id> --worktree <path> --fail-on-out\` (detect out-of-budget changes)
  - \`az mail send --parent <parent-issue> --type dependency-ready --body "..."\`
  - \`az mail list --parent <parent-issue> --since <seq> --json\`
  - \`az mail watch --parent <parent-issue> --since <seq> --jsonl\`
- Issue-context guardrails:
  ${contextGuardrail}
  - Missing fields (for example description/design/acceptance/notes) are valid. Treat absent or empty fields as intentional and continue execution.
  - Do not go on history/log hunting tangents to backfill missing fields unless the user explicitly asks for that research.
${implementationGuardrails === undefined ? "" : `${implementationGuardrails}\n`}
- Keep issue context current as you work:
  - Update design/notes as implementation decisions change.
  - Use status/priority/labels flags when state changes materially.
- High-signal issue commands:
  - \`az issue list --limit 20\` (lists the most recently updated issues first)
  - \`az issue get <issue-id>\` (use \`--json\` when you need full structured output)
  - \`az issue child "Child task"\` (uses active parent context, or \`--parent <issue-id>\`)
  - \`az issue bulk-create --input issues.json --json\` (for example, \`issues.json\` can contain \`[{"title":"Agent-created task"}]\`)
  - \`az issue bulk-update --input updates.json --json\` (for example, \`updates.json\` can contain \`[{"id":"az-123","status":"blocked"}]\`)
  - \`az issue update <issue-id> --design "..."\`
  - \`az issue update <issue-id> --notes "..."\`
  - \`az issue update <issue-id> --append-notes "..."\` (adds to existing notes without overwriting previous notes)
  - \`az issue update <issue-id> --status in_progress|blocked|open\`
  - \`az issue close <issue-id> --reason "..."\` (guards against closing parents with open children)
  - \`az issue --help\`
${specGuardrails === undefined ? "" : `- Spec workflow:\n${specGuardrails}\n`}
- When work is complete:
  - Commit your changes first (\`git add -A && git commit -m "<issue-id>: ..."\`).
  - Always include the issue ID in the commit message.
  - Close the issue when implementation is ready for review (\`az issue close <issue-id>\`).
  - Review flow policy: reviews target closed tasks, not in-progress tasks.
  - If review finds remaining work, move the issue back to in-progress and continue.
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
