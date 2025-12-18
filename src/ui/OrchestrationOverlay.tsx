/**
 * OrchestrationOverlay - Modal overlay for epic orchestration
 *
 * Allows multi-selecting child tasks from an epic and spawning
 * parallel Claude sessions for them. Only tasks with status="open"
 * and no existing session can be selected.
 *
 * Keyboard handling is in KeyboardService, this component just renders.
 */

import type { OrchestrationTask } from "../services/EditorService.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

export interface OrchestrationOverlayProps {
	epicId: string
	epicTitle: string
	childTasks: ReadonlyArray<OrchestrationTask>
	selectedIds: ReadonlyArray<string>
	focusIndex: number
}

/**
 * Get status indicator for a task
 * ○ = open (spawnable)
 * ● = in_progress or blocked or has session (not spawnable)
 * ✓ = closed
 */
const getStatusIndicator = (task: OrchestrationTask): string => {
	if (task.status === "closed") return "✓"
	if (task.status === "open" && !task.hasSession) return "○"
	return "●"
}

/**
 * Get selection indicator for a task
 * [✓] = selected
 * [ ] = not selected but selectable
 * [-] = not selectable
 */
const getSelectionIndicator = (task: OrchestrationTask, isSelected: boolean): string => {
	const isSpawnable = task.status === "open" && !task.hasSession
	if (!isSpawnable) return "[-]"
	return isSelected ? "[✓]" : "[ ]"
}

/**
 * OrchestrationOverlay component
 *
 * Displays epic title with child tasks below, allowing multi-selection
 * of spawnable tasks. Press Space to toggle selection, 'a' for select all,
 * 'n' for none, Enter to spawn selected tasks, Esc to exit.
 */
export const OrchestrationOverlay = (props: OrchestrationOverlayProps) => {
	// Count spawnable tasks
	const spawnableCount = props.childTasks.filter((t) => t.status === "open" && !t.hasSession).length

	const selectedCount = props.selectedIds.length

	return (
		<box
			position="absolute"
			left={0}
			right={0}
			top={0}
			bottom={0}
			alignItems="center"
			justifyContent="center"
			backgroundColor={`${theme.crust}CC`}
		>
			<box
				borderStyle="rounded"
				border={true}
				borderColor={theme.mauve}
				backgroundColor={theme.base}
				paddingLeft={2}
				paddingRight={2}
				paddingTop={1}
				paddingBottom={1}
				minWidth={70}
				maxWidth={90}
				flexDirection="column"
			>
				{/* Header with epic title */}
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{`─────────────────── Orchestrate: ${props.epicTitle} ───────────────────`}
				</text>
				<text> </text>

				{/* Task list */}
				<box flexDirection="column">
					{props.childTasks.map((task, index) => {
						const isFocused = index === props.focusIndex
						const isSelected = props.selectedIds.includes(task.id)
						const statusIndicator = getStatusIndicator(task)
						const selectionIndicator = getSelectionIndicator(task, isSelected)

						return (
							<text
								key={task.id}
								fg={isFocused ? theme.mauve : theme.text}
								attributes={isFocused ? ATTR_BOLD : 0}
							>
								{`  ${selectionIndicator}  ${task.id}  ${statusIndicator}  ${task.title}`}
							</text>
						)
					})}
				</box>
				<text> </text>

				{/* Selection summary */}
				<text fg={theme.subtext0}>
					{`  Selected: ${selectedCount}/${spawnableCount} spawnable`}
				</text>
				<text> </text>

				{/* Footer instructions */}
				<text fg={theme.subtext0}>
					{"  Space:toggle  a:all  n:none  Enter:spawn selected  Esc:exit"}
				</text>
			</box>
		</box>
	)
}
