/**
 * BreakIntoEpicOverlay - Modal for breaking a task into an epic with child tasks
 *
 * Uses Claude AI to analyze the task and suggest parallelizable child tasks.
 * The user can review and confirm the suggestions before conversion.
 */

import { useKeyboard } from "@opentui/react"
import { useCallback, useEffect, useState } from "react"
import type { SuggestedChildTask } from "../services/OverlayService.js"
import { theme } from "./theme.js"

export interface BreakIntoEpicOverlayProps {
	taskId: string
	taskTitle: string
	taskDescription?: string
	onConfirm: (childTasks: SuggestedChildTask[]) => void
	onCancel: () => void
	/** Fetch suggestions from Claude - called on mount */
	fetchSuggestions: (title: string, description?: string) => Promise<SuggestedChildTask[] | "error">
}

type OverlayState =
	| { _tag: "loading" }
	| { _tag: "suggestions"; tasks: SuggestedChildTask[]; selectedIndex: number }
	| { _tag: "error"; message: string }

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
 * Navigation: j/k to move, Enter to confirm, Esc to cancel
 */
export const BreakIntoEpicOverlay = (props: BreakIntoEpicOverlayProps) => {
	const [state, setState] = useState<OverlayState>({ _tag: "loading" })

	// Fetch suggestions on mount
	useEffect(() => {
		let cancelled = false

		const fetch = async () => {
			const result = await props.fetchSuggestions(props.taskTitle, props.taskDescription)
			if (cancelled) return

			if (result === "error") {
				setState({ _tag: "error", message: "Failed to get suggestions from Claude" })
			} else if (result.length === 0) {
				setState({
					_tag: "error",
					message: "Claude couldn't suggest any subtasks for this task",
				})
			} else {
				setState({ _tag: "suggestions", tasks: result, selectedIndex: 0 })
			}
		}

		fetch()

		return () => {
			cancelled = true
		}
	}, [props.taskTitle, props.taskDescription, props.fetchSuggestions])

	// Keyboard handling
	const handleKeyboard = useCallback(
		(event: { name: string }) => {
			if (state._tag === "loading") {
				// Only allow escape during loading
				if (event.name === "escape") {
					props.onCancel()
				}
				return
			}

			if (state._tag === "error") {
				// Only allow escape on error
				if (event.name === "escape") {
					props.onCancel()
				}
				return
			}

			// Suggestions state
			switch (event.name) {
				case "escape":
					props.onCancel()
					break
				case "return":
					props.onConfirm(state.tasks)
					break
				case "j":
				case "down":
					setState((s) => {
						if (s._tag !== "suggestions") return s
						const newIndex = Math.min(s.selectedIndex + 1, s.tasks.length - 1)
						return { ...s, selectedIndex: newIndex }
					})
					break
				case "k":
				case "up":
					setState((s) => {
						if (s._tag !== "suggestions") return s
						const newIndex = Math.max(s.selectedIndex - 1, 0)
						return { ...s, selectedIndex: newIndex }
					})
					break
			}
		},
		[state, props],
	)

	useKeyboard(handleKeyboard)

	const modalWidth = 70

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
					<text fg={theme.text}>{props.taskTitle}</text>
				</box>

				{/* Content based on state */}
				{state._tag === "loading" && (
					<box marginTop={2}>
						<text fg={theme.yellow}>⏳ Asking Claude to suggest subtasks...</text>
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
