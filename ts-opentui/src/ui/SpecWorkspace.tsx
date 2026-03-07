import { DateTime } from "effect"
import type { SpecSubview } from "../services/EditorService.js"
import type { SpecCoverageGap } from "../core/specTypes.js"
import type { SpecWorkspaceState } from "./atoms/spec.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

const subviewLabels: ReadonlyArray<{ readonly id: SpecSubview; readonly label: string }> = [
    { id: "requirements", label: "Requirements" },
    { id: "coverage", label: "Coverage" },
    { id: "publish", label: "Publish" },
]

const formatTimestamp = (timestamp: string | null): string =>
    timestamp === null ? "never" : new Date(timestamp).toLocaleString()

const formatDateTimeUtc = (timestamp: DateTime.Utc): string =>
    new Date(DateTime.formatIso(timestamp)).toLocaleString()

const publishStatusColor = (status: "success" | "partial" | "failed"): string => {
    switch (status) {
        case "success":
            return theme.green
        case "partial":
            return theme.yellow
        case "failed":
            return theme.red
    }
}

const outcomeColor = (status: "success" | "failed" | "skipped"): string => {
    switch (status) {
        case "success":
            return theme.green
        case "failed":
            return theme.red
        case "skipped":
            return theme.overlay1
    }
}

const gapLabel = (gap: SpecCoverageGap): string => {
    if (gap.kind === "unlinked_requirement") {
        return `Unlinked ${gap.requirement_id ?? ""}`.trim()
    }
    if (gap.kind === "missing_issue") {
        return `Missing issue ${gap.issue_id ?? ""}`.trim()
    }
    return `Missing requirement ${gap.requirement_id ?? ""}`.trim()
}

const requirementReference = (requirement: {
	readonly local_id: string
	readonly external_code: string | null
}): string =>
	requirement.external_code === null
		? requirement.local_id
		: `${requirement.local_id} (${requirement.external_code})`

interface SpecWorkspaceProps {
    readonly subview: SpecSubview
    readonly state: SpecWorkspaceState
}

export const SpecWorkspace = ({ subview, state }: SpecWorkspaceProps) => {
    const requirements = state.coverageReport.requirements
    const unlinkedCount = state.coverageReport.unlinked_requirement_ids.length
    const integrityGapCount = state.coverageReport.integrity_gaps.length

    return (
        <box flexGrow={1} flexDirection="column" paddingLeft={1} paddingRight={1} paddingTop={1}>
            <box flexDirection="row" gap={1} paddingBottom={1}>
                {subviewLabels.map((item) => {
                    const active = item.id === subview
                    return (
                        <box
                            key={item.id}
                            backgroundColor={active ? theme.pink : theme.surface0}
                            paddingLeft={1}
                            paddingRight={1}
                        >
                            <text fg={active ? theme.base : theme.subtext0} attributes={ATTR_BOLD}>
                                {item.label}
                            </text>
                        </box>
                    )
                })}
                <box flexGrow={1} />
                <text fg={theme.subtext0}>Last refresh: {formatTimestamp(state.refreshedAt)}</text>
            </box>

            {state.isLoading && <text fg={theme.yellow}>Loading spec workspace...</text>}
            {state.error !== null && <text fg={theme.red}>Error: {state.error}</text>}

            {!state.isLoading && state.error === null && (
                <box flexDirection="column" gap={1}>
                    {subview === "requirements" && (
                        <>
                            <text fg={theme.subtext1}>
                                {requirements.length} requirements with linked issue counts
                            </text>
                            {requirements.length === 0 && (
                                <text fg={theme.overlay0}>
                                    No requirements found. Use `az spec req create ...` to add one.
                                </text>
                            )}
                            {requirements.map((requirement) => (
                                <box key={requirement.id} flexDirection="column" paddingBottom={1}>
                                    <text fg={theme.text} attributes={ATTR_BOLD}>
                                        {requirementReference(requirement)} [{requirement.kind}] links=
                                        {requirement.linked_issue_count}
                                    </text>
                                    <text fg={theme.subtext1}>{requirement.title}</text>
                                    <text fg={theme.overlay1}>
                                        id={requirement.id} status={requirement.status} priority={requirement.priority}
                                    </text>
                                </box>
                            ))}
                        </>
                    )}

                    {subview === "coverage" && (
                        <>
                            <text fg={theme.subtext1}>
                                unlinked={unlinkedCount} integrity-gaps={integrityGapCount}
                            </text>
                            {state.coverageReport.unlinked_requirement_ids.length > 0 && (
                                <box flexDirection="column">
                                    <text fg={theme.yellow} attributes={ATTR_BOLD}>
                                        Unlinked Requirements
                                    </text>
                                    {state.coverageReport.unlinked_requirement_ids.map((id) => (
                                        <text key={id} fg={theme.text}>
                                            {id}
                                        </text>
                                    ))}
                                </box>
                            )}
                            {state.coverageReport.integrity_gaps.length > 0 && (
                                <box flexDirection="column">
                                    <text fg={theme.red} attributes={ATTR_BOLD}>
                                        Integrity Gaps
                                    </text>
                                    {state.coverageReport.integrity_gaps.map((gap, index) => (
                                        <text key={`${gap.kind}-${index}`} fg={theme.subtext1}>
                                            {gapLabel(gap)}: {gap.message}
                                        </text>
                                    ))}
                                </box>
                            )}
                            {state.coverageReport.unlinked_requirement_ids.length === 0 &&
                                state.coverageReport.integrity_gaps.length === 0 && (
                                    <text fg={theme.green}>No coverage gaps detected.</text>
                                )}
                        </>
                    )}

                    {subview === "publish" && (
                        <>
                            <text fg={theme.subtext1}>Target and publish status</text>
                            <text fg={state.publishConfig.enabled ? theme.green : theme.overlay1}>
                                auto-publish={state.publishConfig.enabled ? "enabled" : "disabled"}
                            </text>
                            <text fg={theme.text}>
                                debounce_ms={state.publishConfig.debounce_ms} target_project=
                                {state.publishConfig.target_project ?? "unset"}
                            </text>
                            <text fg={theme.overlay1}>
                                docs: overview="{state.publishConfig.documents.overview}" requirements="
                                {state.publishConfig.documents.requirements}" acceptance="
                                {state.publishConfig.documents.acceptance}" changelog="
                                {state.publishConfig.documents.change_log}"
                            </text>

                            {state.lastPublishOutcome === null && (
                                <text fg={theme.overlay0}>
                                    No publish has run yet. Use `az spec publish run` to publish.
                                </text>
                            )}

                            {state.lastPublishOutcome !== null && (
                                <box flexDirection="column" gap={1}>
                                    <text fg={publishStatusColor(state.lastPublishOutcome.status)} attributes={ATTR_BOLD}>
                                        last_status={state.lastPublishOutcome.status} finished=
                                        {formatDateTimeUtc(state.lastPublishOutcome.finished_at)}
                                    </text>
                                    <text fg={theme.subtext1}>
                                        requirements={state.lastPublishOutcome.total_requirements} links=
                                        {state.lastPublishOutcome.total_links}
                                    </text>
                                    {state.lastPublishOutcome.outcomes.map((outcome) => (
                                        <text key={outcome.document_key} fg={outcomeColor(outcome.status)}>
                                            {outcome.document_key}: {outcome.status} ({outcome.title}) -{" "}
                                            {outcome.message}
                                        </text>
                                    ))}
                                </box>
                            )}
                        </>
                    )}
                </box>
            )}
        </box>
    )
}
