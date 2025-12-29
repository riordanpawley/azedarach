/**
 * FilterMenu component - filter options popover (bottom-right, like SortMenu)
 */
import type {
	FilterConfig,
	FilterField,
	FilterSessionState,
	IssueStatus,
	IssueType,
} from "../services/EditorService.js"
import { theme } from "./theme.js"

export interface FilterMenuProps {
	config: FilterConfig
	activeField: FilterField | null
}

const ATTR_BOLD = 1
const ATTR_DIM = 2

/**
 * Get display name for filter field
 */
const getFieldName = (field: FilterField): string => {
	switch (field) {
		case "status":
			return "Status"
		case "priority":
			return "Priority"
		case "type":
			return "Type"
		case "session":
			return "Session"
		case "age":
			return "Age"
	}
}

/**
 * Get short status display name
 */
const getStatusName = (status: IssueStatus): string => {
	switch (status) {
		case "open":
			return "Open"
		case "in_progress":
			return "In Prog"
		case "blocked":
			return "Blocked"
		case "closed":
			return "Closed"
	}
}

/**
 * Get short type display name
 */
const getTypeName = (type: IssueType): string => {
	switch (type) {
		case "bug":
			return "Bug"
		case "feature":
			return "Feature"
		case "task":
			return "Task"
		case "epic":
			return "Epic"
		case "chore":
			return "Chore"
	}
}

/**
 * Get session state display name
 */
const getSessionName = (state: FilterSessionState): string => {
	switch (state) {
		case "idle":
			return "Idle"
		case "initializing":
			return "Init"
		case "busy":
			return "Busy"
		case "waiting":
			return "Wait"
		case "done":
			return "Done"
		case "error":
			return "Err"
		case "paused":
			return "Pause"
	}
}

/**
 * FilterMenu component
 *
 * Displays a floating panel showing filter options.
 * Shows sub-menus when a filter field is selected.
 */
export const FilterMenu = (props: FilterMenuProps) => {
	const { config, activeField } = props

	// Count active filters for each field
	const statusCount = config.status.size
	const priorityCount = config.priority.size
	const typeCount = config.type.size
	const sessionCount = config.session.size

	// Field line component showing active filter count
	const FieldLine = ({
		keyName,
		field,
		count,
	}: {
		keyName: string
		field: FilterField
		count: number
	}) => {
		const isActive = activeField === field
		const hasFilters = count > 0
		const fgColor = isActive ? theme.lavender : hasFilters ? theme.green : theme.text
		const keyColor = isActive ? theme.green : theme.mauve
		const attrs = isActive || hasFilters ? ATTR_BOLD : 0

		return (
			<box flexDirection="row">
				<text fg={keyColor} attributes={attrs}>
					{keyName}
				</text>
				<text fg={fgColor} attributes={attrs}>
					{` ${getFieldName(field)}`}
				</text>
				{hasFilters && (
					<text fg={theme.yellow} attributes={attrs}>
						{` (${count})`}
					</text>
				)}
			</box>
		)
	}

	// Toggle line for individual filter values
	const ToggleLine = ({
		keyName,
		label,
		isSelected,
	}: {
		keyName: string
		label: string
		isSelected: boolean
	}) => {
		const fgColor = isSelected ? theme.green : theme.subtext0
		const keyColor = isSelected ? theme.green : theme.overlay0
		const indicator = isSelected ? "●" : "○"

		return (
			<box flexDirection="row" gap={1}>
				<text fg={keyColor}>{keyName}</text>
				<text fg={fgColor}>{indicator}</text>
				<text fg={fgColor}>{label}</text>
			</box>
		)
	}

	// Render sub-menu for selected field
	const renderSubMenu = () => {
		if (!activeField) return null

		switch (activeField) {
			case "status":
				return (
					<>
						<text fg={theme.surface1}>{"─────────"}</text>
						<ToggleLine
							keyName="o"
							label={getStatusName("open")}
							isSelected={config.status.has("open")}
						/>
						<ToggleLine
							keyName="i"
							label={getStatusName("in_progress")}
							isSelected={config.status.has("in_progress")}
						/>
						<ToggleLine
							keyName="b"
							label={getStatusName("blocked")}
							isSelected={config.status.has("blocked")}
						/>
						<ToggleLine
							keyName="d"
							label={getStatusName("closed")}
							isSelected={config.status.has("closed")}
						/>
					</>
				)
			case "priority":
				return (
					<>
						<text fg={theme.surface1}>{"─────────"}</text>
						<ToggleLine keyName="0" label="P0" isSelected={config.priority.has(0)} />
						<ToggleLine keyName="1" label="P1" isSelected={config.priority.has(1)} />
						<ToggleLine keyName="2" label="P2" isSelected={config.priority.has(2)} />
						<ToggleLine keyName="3" label="P3" isSelected={config.priority.has(3)} />
						<ToggleLine keyName="4" label="P4" isSelected={config.priority.has(4)} />
					</>
				)
			case "type":
				return (
					<>
						<text fg={theme.surface1}>{"─────────"}</text>
						<ToggleLine
							keyName="B"
							label={getTypeName("bug")}
							isSelected={config.type.has("bug")}
						/>
						<ToggleLine
							keyName="F"
							label={getTypeName("feature")}
							isSelected={config.type.has("feature")}
						/>
						<ToggleLine
							keyName="T"
							label={getTypeName("task")}
							isSelected={config.type.has("task")}
						/>
						<ToggleLine
							keyName="E"
							label={getTypeName("epic")}
							isSelected={config.type.has("epic")}
						/>
						<ToggleLine
							keyName="C"
							label={getTypeName("chore")}
							isSelected={config.type.has("chore")}
						/>
					</>
				)
			case "session":
				return (
					<>
						<text fg={theme.surface1}>{"─────────"}</text>
						<ToggleLine
							keyName="I"
							label={getSessionName("idle")}
							isSelected={config.session.has("idle")}
						/>
						<ToggleLine
							keyName="N"
							label={getSessionName("initializing")}
							isSelected={config.session.has("initializing")}
						/>
						<ToggleLine
							keyName="U"
							label={getSessionName("busy")}
							isSelected={config.session.has("busy")}
						/>
						<ToggleLine
							keyName="W"
							label={getSessionName("waiting")}
							isSelected={config.session.has("waiting")}
						/>
						<ToggleLine
							keyName="D"
							label={getSessionName("done")}
							isSelected={config.session.has("done")}
						/>
						<ToggleLine
							keyName="X"
							label={getSessionName("error")}
							isSelected={config.session.has("error")}
						/>
						<ToggleLine
							keyName="P"
							label={getSessionName("paused")}
							isSelected={config.session.has("paused")}
						/>
					</>
				)
		}
	}

	return (
		<box position="absolute" right={1} bottom={4}>
			<box
				borderStyle="rounded"
				border={true}
				borderColor={theme.surface1}
				backgroundColor={theme.base}
				paddingLeft={1}
				paddingRight={1}
				flexDirection="column"
			>
				<text fg={theme.subtext0} attributes={ATTR_DIM}>
					Filter by:
				</text>
				<text fg={theme.surface1}>{"─────────"}</text>

				{/* Filter fields */}
				<FieldLine keyName="s" field="status" count={statusCount} />
				<FieldLine keyName="p" field="priority" count={priorityCount} />
				<FieldLine keyName="t" field="type" count={typeCount} />
				<FieldLine keyName="S" field="session" count={sessionCount} />

				{/* Sub-menu for active field */}
				{renderSubMenu()}

				<text fg={theme.surface1}>{"─────────"}</text>

				{/* Epic subtasks toggle */}
				<box flexDirection="row" gap={1}>
					<text fg={theme.mauve}>e</text>
					<text fg={config.hideEpicSubtasks ? theme.green : theme.subtext0}>
						{config.hideEpicSubtasks ? "●" : "○"}
					</text>
					<text fg={config.hideEpicSubtasks ? theme.green : theme.subtext0}>
						Hide epic children
					</text>
				</box>

				<text fg={theme.surface1}>{"─────────"}</text>
				<box flexDirection="row" gap={1}>
					<text fg={theme.overlay0}>c</text>
					<text fg={theme.subtext0}>clear all</text>
				</box>
				<box flexDirection="row" gap={1}>
					<text fg={theme.overlay0}>Esc</text>
					<text fg={theme.subtext0}>cancel</text>
				</box>
			</box>
		</box>
	)
}
