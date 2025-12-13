/**
 * ClaudeCreatePrompt - Modal prompt for creating tasks via Claude
 *
 * Allows users to describe a task in natural language, then spawns a Claude
 * session that creates the bead and stays open for immediate work.
 */

import { useKeyboard } from "@opentui/react"
import { useState } from "react"
import { theme } from "./theme"

export interface ClaudeCreatePromptProps {
	onSubmit: (description: string) => void
	onCancel: () => void
}

const ATTR_BOLD = 1

/**
 * ClaudeCreatePrompt component
 *
 * Simple modal for entering a natural language task description.
 * Claude will interpret this and create the appropriate bead.
 * Enter to submit, Esc to cancel.
 */
export const ClaudeCreatePrompt = (props: ClaudeCreatePromptProps) => {
	const [description, setDescription] = useState("")

	useKeyboard((event) => {
		// Escape to cancel
		if (event.name === "escape") {
			props.onCancel()
			return
		}

		// Enter to submit (only if description is not empty)
		if (event.name === "return") {
			if (description.trim()) {
				props.onSubmit(description)
			}
			return
		}

		// Backspace to delete
		if (event.name === "backspace") {
			setDescription((prev) => prev.slice(0, -1))
			return
		}

		// Regular character input
		if (event.sequence && event.sequence.length === 1 && !event.ctrl && !event.meta) {
			setDescription((prev) => prev + event.sequence)
		}
	})

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
			backgroundColor={theme.crust + "CC"}
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
				flexDirection="column"
			>
				{/* Title */}
				<box>
					<text fg={theme.lavender} attributes={ATTR_BOLD}>
						Create Task with Claude{"\n"}
					</text>
				</box>

				{/* Subtitle */}
				<box marginTop={1}>
					<text fg={theme.subtext0}>
						Describe what you want to do in natural language.{"\n"}
						Claude will create the bead and be ready to work on it.
					</text>
				</box>

				{/* Description input */}
				<box flexDirection="row" marginTop={1}>
					<text fg={theme.yellow}>{"❯ "}</text>
					<text fg={theme.text}>{description}</text>
					<text fg={theme.yellow}>_</text>
				</box>

				{/* Help text */}
				<box marginTop={1}>
					<text fg={theme.overlay0}>Enter: create and launch session Esc: cancel</text>
				</box>
			</box>
		</box>
	)
}
