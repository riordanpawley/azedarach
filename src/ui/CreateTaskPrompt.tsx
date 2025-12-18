/**
 * CreateTaskPrompt - Modal prompt for creating new tasks
 */

import { useKeyboard } from "@opentui/react"
import { useState } from "react"
import { theme } from "./theme.js"

export interface CreateTaskPromptProps {
	onSubmit: (params: { title: string; type: string; priority: number }) => void
	onCancel: () => void
}

const TASK_TYPES = ["task", "bug", "feature", "epic", "chore"] as const
const PRIORITIES = [1, 2, 3, 4] as const
const ATTR_BOLD = 1

/**
 * CreateTaskPrompt component
 *
 * Simple modal for creating new tasks with title, type, and priority inputs.
 * Uses tab/shift-tab to cycle through fields, Enter to submit, Esc to cancel.
 */
export const CreateTaskPrompt = (props: CreateTaskPromptProps) => {
	const [title, setTitle] = useState("")
	const [typeIndex, setTypeIndex] = useState(0) // default: task
	const [priorityIndex, setPriorityIndex] = useState(1) // default: P2
	const [focusedField, setFocusedField] = useState<"title" | "type" | "priority">("title")

	useKeyboard((event) => {
		const field = focusedField

		// Escape to cancel
		if (event.name === "escape") {
			props.onCancel()
			return
		}

		// Tab/Shift-Tab to cycle through fields
		if (event.name === "tab") {
			if (event.shift) {
				// Shift-tab: previous field
				if (field === "priority") setFocusedField("type")
				else if (field === "type") setFocusedField("title")
				else setFocusedField("priority")
			} else {
				// Tab: next field
				if (field === "title") setFocusedField("type")
				else if (field === "type") setFocusedField("priority")
				else setFocusedField("title")
			}
			return
		}

		// Enter to submit (only if title is not empty)
		if (event.name === "return") {
			if (title.trim()) {
				props.onSubmit({
					title: title,
					type: TASK_TYPES[typeIndex],
					priority: PRIORITIES[priorityIndex],
				})
			}
			return
		}

		// Handle field-specific input
		if (field === "title") {
			// Backspace to delete
			if (event.name === "backspace") {
				setTitle((prev) => prev.slice(0, -1))
			}
			// Regular character input
			else if (event.sequence && event.sequence.length === 1 && !event.ctrl && !event.meta) {
				setTitle((prev) => prev + event.sequence)
			}
		} else if (field === "type") {
			// Left/right arrows or h/l to cycle type
			if (event.name === "left" || event.name === "h") {
				setTypeIndex((prev) => (prev - 1 + TASK_TYPES.length) % TASK_TYPES.length)
			} else if (event.name === "right" || event.name === "l") {
				setTypeIndex((prev) => (prev + 1) % TASK_TYPES.length)
			}
		} else if (field === "priority") {
			// Left/right arrows or h/l to cycle priority
			if (event.name === "left" || event.name === "h") {
				setPriorityIndex((prev) => (prev - 1 + PRIORITIES.length) % PRIORITIES.length)
			} else if (event.name === "right" || event.name === "l") {
				setPriorityIndex((prev) => (prev + 1) % PRIORITIES.length)
			}
		}
	})

	const selectedType = TASK_TYPES[typeIndex]
	const selectedPriority = PRIORITIES[priorityIndex]

	const modalWidth = 60

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
				backgroundColor={theme.surface0}
				paddingLeft={2}
				paddingRight={2}
				paddingTop={1}
				paddingBottom={1}
				minWidth={modalWidth}
				flexDirection="column"
			>
				{/* Title */}
				<box>
					<text fg={theme.mauve} attributes={ATTR_BOLD}>
						Create New Task{"\n"}
					</text>
				</box>

				{/* Title input */}
				<box flexDirection="row" marginTop={1}>
					<text fg={focusedField === "title" ? theme.yellow : theme.subtext0}>Title: </text>
					<text fg={theme.text}>{title}</text>
					{focusedField === "title" && <text fg={theme.yellow}>_</text>}
				</box>

				{/* Type selector */}
				<box flexDirection="row" marginTop={1}>
					<text fg={focusedField === "type" ? theme.yellow : theme.subtext0}>Type: </text>
					{focusedField === "type" && <text fg={theme.subtext0}>{"< "}</text>}
					<text fg={theme.text} attributes={focusedField === "type" ? ATTR_BOLD : undefined}>
						{selectedType}
					</text>
					{focusedField === "type" && <text fg={theme.subtext0}>{" >"}</text>}
				</box>

				{/* Priority selector */}
				<box flexDirection="row" marginTop={1}>
					<text fg={focusedField === "priority" ? theme.yellow : theme.subtext0}>Priority: </text>
					{focusedField === "priority" && <text fg={theme.subtext0}>{"< "}</text>}
					<text fg={theme.text} attributes={focusedField === "priority" ? ATTR_BOLD : undefined}>
						P{selectedPriority}
					</text>
					{focusedField === "priority" && <text fg={theme.subtext0}>{" >"}</text>}
				</box>

				{/* Help text */}
				<box marginTop={1}>
					<text fg={theme.overlay0}>Tab: next field Enter: create Esc: cancel</text>
				</box>
			</box>
		</box>
	)
}
