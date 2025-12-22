/**
 * ClaudeCreatePrompt - Modal prompt for creating tasks via Claude
 *
 * Allows users to describe a task in natural language. Claude CLI runs
 * in non-interactive mode to create the bead, then exits.
 */

import { useAtomValue } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import { useState } from "react"
import { appConfigAtom } from "./atoms/index.js"
import { theme } from "./theme.js"

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
	const appConfigResult = useAtomValue(appConfigAtom)

	const appConfig = appConfigResult._tag === "Success" ? appConfigResult.value : null
	const cliTool = appConfig?.cliTool ?? "claude"
	const modelConfig = appConfig?.model
	const toolModelConfig = cliTool === "claude" ? modelConfig?.claude : modelConfig?.opencode
	const activeModel = modelConfig?.chat ?? toolModelConfig?.chat ?? "haiku"

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

		// Backspace to delete last character
		if (event.name === "backspace") {
			setDescription((prev) => prev.slice(0, -1))
			return
		}

		// Ctrl+U: Clear entire line
		if (event.ctrl && event.name === "u") {
			setDescription("")
			return
		}

		// Ctrl+W: Delete last word
		if (event.ctrl && event.name === "w") {
			setDescription((prev) => prev.replace(/\S+\s*$/, ""))
			return
		}

		// Tab: Convert to space (for accessibility)
		if (event.name === "tab") {
			setDescription((prev) => `${prev} `)
			return
		}

		// Handle paste and multi-character input (sequence > 1 char)
		if (event.sequence && event.sequence.length > 1 && !event.ctrl && !event.meta) {
			// Filter to printable characters only
			// biome-ignore lint/suspicious/noControlCharactersInRegex: Intentionally filtering control chars
			const printable = event.sequence.replace(/[\x00-\x1F\x7F]/g, "")
			if (printable) {
				setDescription((prev) => prev + printable)
			}
			return
		}

		// Regular single character input
		if (event.sequence && event.sequence.length === 1 && !event.ctrl && !event.meta) {
			// Only allow printable characters (ASCII 32-126 and extended)
			const charCode = event.sequence.charCodeAt(0)
			if (charCode >= 32 && charCode !== 127) {
				setDescription((prev) => prev + event.sequence)
			}
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
				flexDirection="column"
			>
				{/* Title */}
				<box>
					<text fg={theme.lavender} attributes={ATTR_BOLD}>
						Create Task with {cliTool === "claude" ? "Claude" : "OpenCode"}
						{"\n"}
					</text>
				</box>

				{/* Subtitle */}
				<box marginTop={1}>
					<text fg={theme.subtext0}>
						Describe what you want to do in natural language.{"\n"}
						The AI ({activeModel}) will create a bead with appropriate title/type/priority.
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
					<text fg={theme.overlay0}>
						Enter: submit Esc: cancel Ctrl-U: clear Ctrl-W: delete word
					</text>
				</box>
			</box>
		</box>
	)
}
