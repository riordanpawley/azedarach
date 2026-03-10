import type { DateTime } from "effect"

export type SpecRequirementKind = "functional" | "acceptance" | "other"
export type SpecLinkType = "implements" | "tests" | "blocks" | "relates"
export type SpecLinkFulfillmentStatus = "planned" | "partial" | "complete" | "verified"
export type SpecRequirementLookupSelector = "auto" | "id" | "local_id" | "external_code"

export interface SpecRequirement {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
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
	readonly requirement_local_id: string
	readonly requirement_external_code: string | null
	readonly link_type: SpecLinkType
	readonly fulfillment_status: SpecLinkFulfillmentStatus
	readonly fulfillment_percent: number | null
	readonly evidence_note: string | null
	readonly created_at: string
	readonly updated_at: string
}

export interface SpecIssueRef {
	readonly id: string
	readonly title?: string
	readonly status?: string
	readonly issue_type?: string
	readonly link_type: SpecLinkType
	readonly fulfillment_status: SpecLinkFulfillmentStatus
	readonly fulfillment_percent: number | null
	readonly evidence_note: string | null
}

export interface SpecRequirementRef {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
	readonly title: string
	readonly kind: SpecRequirementKind
	readonly link_type: SpecLinkType
	readonly fulfillment_status: SpecLinkFulfillmentStatus
	readonly fulfillment_percent: number | null
	readonly evidence_note: string | null
}

export interface SpecRequirementWithStats extends SpecRequirement {
	readonly linked_issue_count: number
	readonly implemented_issue_count: number
}

export interface SpecRequirementListFilters {
	readonly query?: string
	readonly kind?: SpecRequirementKind
	readonly status?: string
	readonly priority?: number
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
	readonly fully_implemented_requirement_ids: readonly string[]
	readonly partially_implemented_requirement_ids: readonly string[]
	readonly integrity_gaps: readonly SpecCoverageGap[]
}

export interface SpecLintResult {
	readonly ok: boolean
	readonly requirement_count: number
	readonly linked_requirement_count: number
	readonly unlinked_requirement_count: number
	readonly integrity_gap_count: number
	readonly gap_counts: {
		readonly unlinked_requirement: number
		readonly missing_issue: number
		readonly missing_requirement: number
	}
	readonly report: SpecCoverageReport
}

export type SpecSnapshotDocumentKey = "overview" | "requirements" | "acceptance" | "change_log"

export interface SpecMarkdownSyncDocumentResult {
	readonly key: SpecSnapshotDocumentKey
	readonly path: string
	readonly status: "unchanged" | "updated"
	readonly changed: boolean
}

export interface SpecMarkdownSyncResult {
	readonly out_dir: string
	readonly check: boolean
	readonly ok: boolean
	readonly total_documents: number
	readonly changed_documents: number
	readonly documents: readonly SpecMarkdownSyncDocumentResult[]
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
	readonly document_key: SpecSnapshotDocumentKey
	readonly title: string
	readonly status: "success" | "failed" | "skipped"
	readonly message: string
	readonly requirement_count: number
	readonly link_count: number
}

export interface SpecPublishOutcome {
	readonly started_at: DateTime.Utc
	readonly finished_at: DateTime.Utc
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
