/**
 * BreakIntoEpicOverlay - Modal for breaking a task into an epic with child tasks
 *
 * Uses Claude AI to analyze the task and suggest parallelizable child tasks.
 * The user can review and confirm the suggestions before conversion.
 *
 * Note: All state is managed by BreakIntoEpicService via SubscriptionRef.
 * Keyboard handling is in KeyboardService/InputHandlersService.
 * This component is pure render only - just useAtomValue and JSX.
 */

import { Result } from "@effect-atom/atom"
import { useAtomValue } from "@effect-atom/atom-react"
import { breakIntoEpicStateAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1
const ATTR_DIM = 2

/**
 * BreakIntoEpicOverlay component
 *
 * Displays a modal that:
 * 1. Shows loading state while fetching Claude suggestions
 * 2. Lists suggested child tasks with navigation
 * 3. Allows confirming (Enter) or canceling (Esc)
 *
 * All state comes from BreakIntoEpicService via breakIntoEpicStateAtom.
 * All keyboard handling is in the Effect layer (InputHandlersService).
 */
export const BreakIntoEpicOverlay = () => {
	// Subscribe to overlay state from Effect layer
	const stateResult = useAtomValue(breakIntoEpicStateAtom)
	const state = Result.isSuccess(stateResult) ? stateResult.value : { _tag: "closed" as const }

	// If closed, don't render anything (should be handled by parent but safety check)
	if (state._tag === "closed") {
		return null
	}

	const modalWidth = 70

	// Extract taskTitle for display (available in loading, suggestions, and error states)
	const taskTitle =
		state._tag === "loading" || state._tag === "suggestions" || state._tag === "error"
			? state.taskTitle
			: "task"

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
				borderColor={theme.lavender}
				backgroundColor={theme.surface0}
				paddingLeft={2}
				paddingRight={2}
				paddingTop={1}
				paddingBottom={1}
				minWidth={modalWidth}
				maxWidth={modalWidth}
				flexDirection="column"
			>
				{/* Title */}
				<box>
					<text fg={theme.lavender} attributes={ATTR_BOLD}>
						🔀 Break into Epic{"\n"}
					</text>
				</box>

				{/* Task being converted */}
				<box marginTop={1}>
					<text fg={theme.subtext0}>Converting: </text>
					<text fg={theme.text}>{taskTitle}</text>
				</box>

				{/* Content based on state */}
				{state._tag === "loading" && (
					<box marginTop={2}>
						<text fg={theme.yellow}>⏳ Asking Claude to suggest subtasks...</text>
					</box>
				)}

				{state._tag === "executing" && (
					<box marginTop={2}>
						<text fg={theme.yellow}>⏳ Creating subtasks...</text>
					</box>
				)}

				{state._tag === "error" && (
					<box marginTop={2}>
						<text fg={theme.red}>❌ {state.message}</text>
					</box>
				)}

				{state._tag === "suggestions" && (
					<>
						<box marginTop={1}>
							<text fg={theme.subtext0}>Suggested child tasks ({state.tasks.length}):</text>
						</box>

						{/* Task list */}
						<box marginTop={1} flexDirection="column">
							{state.tasks.map((task, index) => {
								const isSelected = index === state.selectedIndex
								const prefix = isSelected ? "▸ " : "  "
								const fg = isSelected ? theme.lavender : theme.text
								const attrs = isSelected ? ATTR_BOLD : 0

								return (
									<box key={task.title} flexDirection="column">
										<box>
											<text fg={fg} attributes={attrs}>
												{prefix}
												{task.title}
											</text>
										</box>
										{task.description && (
											<box marginLeft={4}>
												<text fg={theme.overlay0} attributes={ATTR_DIM}>
													{task.description}
												</text>
											</box>
										)}
									</box>
								)
							})}
						</box>
					</>
				)}

				{/* Help text */}
				<box marginTop={2}>
					{state._tag === "suggestions" ? (
						<>
							<text fg={theme.green}>Enter</text>
							<text fg={theme.overlay0}>: confirm </text>
							<text fg={theme.red}>Esc</text>
							<text fg={theme.overlay0}>: cancel </text>
							<text fg={theme.blue}>j/k</text>
							<text fg={theme.overlay0}>: navigate</text>
						</>
					) : (
						<>
							<text fg={theme.red}>Esc</text>
							<text fg={theme.overlay0}>: cancel</text>
						</>
					)}
				</box>
			</box>
		</box>
	)
}
