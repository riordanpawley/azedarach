export type TmuxStatus = "busy" | "waiting" | "idle"

export interface TrackedIssueRelationshipRef {
	readonly id: string
	readonly dependency_type: "blocks" | "related" | "parent-child" | "discovered-from"
}

export interface TrackedIssue {
	readonly id: string
	readonly title: string
	readonly status: "open" | "in_progress" | "blocked" | "closed" | "tombstone"
	readonly priority: number
	readonly issue_type: "bug" | "feature" | "task" | "epic" | "chore"
	readonly created_at: string
	readonly updated_at: string
	readonly description?: string
	readonly design?: string
	readonly acceptance?: string
	readonly notes?: string
	readonly implementations?: ReadonlyArray<string>
	readonly dependencies?: ReadonlyArray<TrackedIssueRelationshipRef>
	readonly dependents?: ReadonlyArray<TrackedIssueRelationshipRef>
	readonly dependency_count?: number
	readonly dependent_count?: number
}

export interface SpecRequirementRef {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
	readonly kind: string
	readonly link_type: string
	readonly title: string
	readonly implementations?: ReadonlyArray<string>
	readonly fulfillment_status?: string
	readonly fulfillment_percent?: number | null
	readonly evidence_note?: string | null
}

export interface ResolvedConfig {
	readonly issueTracker:
		| {
				readonly linear: {
					readonly team?: string
				}
		  }
		| {
				readonly tracker: Readonly<Record<string, unknown>>
		  }
		| {
				readonly legacy: Readonly<Record<string, unknown>>
		  }
		| {
				readonly local: Readonly<Record<string, unknown>>
		  }
}
