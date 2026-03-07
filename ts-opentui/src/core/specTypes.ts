export type SpecRequirementKind = "functional" | "acceptance" | "other"
export type SpecLinkType = "implements" | "tests" | "blocks" | "relates"

export interface SpecRequirement {
	readonly id: string
	readonly title: string
	readonly body: string
	readonly kind: SpecRequirementKind
	readonly status: string
	readonly priority: number
	readonly created_at: string
	readonly updated_at: string
}

export interface SpecIssueLink {
	readonly issue_id: string
	readonly requirement_id: string
	readonly link_type: SpecLinkType
	readonly created_at: string
	readonly updated_at: string
}

export interface SpecIssueRef {
	readonly id: string
	readonly title?: string
	readonly status?: string
	readonly issue_type?: string
	readonly link_type: SpecLinkType
}

export interface SpecRequirementRef {
	readonly id: string
	readonly title: string
	readonly kind: SpecRequirementKind
	readonly link_type: SpecLinkType
}

export interface SpecRequirementWithStats extends SpecRequirement {
	readonly linked_issue_count: number
}

export interface SpecCoverageGap {
	readonly kind: "unlinked_requirement" | "missing_issue" | "missing_requirement"
	readonly requirement_id?: string
	readonly issue_id?: string
	readonly message: string
}

export interface SpecCoverageReport {
	readonly requirements: readonly SpecRequirementWithStats[]
	readonly unlinked_requirement_ids: readonly string[]
	readonly integrity_gaps: readonly SpecCoverageGap[]
}

export interface SpecPublishConfig {
	readonly enabled: boolean
	readonly debounce_ms: number
	readonly target_project: string | null
	readonly documents: {
		readonly overview: string
		readonly requirements: string
		readonly acceptance: string
		readonly change_log: string
	}
}

export interface SpecPublishDocumentOutcome {
	readonly document_key: "overview" | "requirements" | "acceptance" | "change_log"
	readonly title: string
	readonly status: "success" | "failed" | "skipped"
	readonly message: string
	readonly requirement_count: number
	readonly link_count: number
}

export interface SpecPublishOutcome {
	readonly started_at: string
	readonly finished_at: string
	readonly status: "success" | "partial" | "failed"
	readonly total_requirements: number
	readonly total_links: number
	readonly outcomes: readonly SpecPublishDocumentOutcome[]
}

export const DEFAULT_SPEC_PUBLISH_CONFIG: SpecPublishConfig = {
	enabled: false,
	debounce_ms: 2000,
	target_project: null,
	documents: {
		overview: "Spec Overview",
		requirements: "Requirements Index",
		acceptance: "Acceptance Index",
		change_log: "Change Log",
	},
}

