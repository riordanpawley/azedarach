/**
 * ImageAttachOverlay component - overlay for attaching images to tasks
 *
 * Provides two methods for attaching images:
 * 1. Paste from clipboard (if xclip/wl-paste available)
 * 2. Enter file path manually
 */

import { Result } from "@effect-atom/atom"
import { useAtomSet, useAtomValue } from "@effect-atom/atom-react"
import { useKeyboard, useTextInput } from "@opentui/react"
import { useCallback, useEffect, useState } from "react"
import {
	attachImageClipboardAtom,
	attachImageFileAtom,
	hasClipboardSupportAtom,
	showToastAtom,
} from "./atoms"
import { theme } from "./theme"

export interface ImageAttachOverlayProps {
	taskId: string
	onSuccess: () => void
	onCancel: () => void
}

const ATTR_BOLD = 1

/**
 * ImageAttachOverlay component
 *
 * Modal overlay for attaching images to a task.
 * Supports clipboard paste and file path entry.
 */
export const ImageAttachOverlay = (props: ImageAttachOverlayProps) => {
	const [mode, setMode] = useState<"menu" | "path">("menu")
	const [pathInput, setPathInput] = useState("")
	const [isAttaching, setIsAttaching] = useState(false)

	// Atoms
	const hasClipboardResult = useAtomValue(hasClipboardSupportAtom)
	const hasClipboard = Result.isSuccess(hasClipboardResult) ? hasClipboardResult.value : false

	const attachFromClipboard = useAtomSet(attachImageClipboardAtom, { mode: "promise" })
	const attachFromFile = useAtomSet(attachImageFileAtom, { mode: "promise" })
	const showToast = useAtomSet(showToastAtom, { mode: "promise" })

	// Text input for file path mode
	const { ref: inputRef, value, focus } = useTextInput({
		initialValue: "",
		onChange: setPathInput,
	})

	// Focus input when entering path mode
	useEffect(() => {
		if (mode === "path") {
			focus()
		}
	}, [mode, focus])

	// Handle clipboard paste
	const handleClipboardPaste = useCallback(async () => {
		if (isAttaching) return

		setIsAttaching(true)
		try {
			const attachment = await attachFromClipboard(props.taskId)
			if (attachment) {
				await showToast({ type: "success", message: `Image attached: ${attachment.filename}` })
				props.onSuccess()
			}
		} catch (error) {
			await showToast({
				type: "error",
				message: `Failed to attach from clipboard: ${error instanceof Error ? error.message : String(error)}`,
			})
		} finally {
			setIsAttaching(false)
		}
	}, [attachFromClipboard, props, showToast, isAttaching])

	// Handle file path submission
	const handleFileSubmit = useCallback(async () => {
		if (isAttaching || !pathInput.trim()) return

		setIsAttaching(true)
		try {
			const attachment = await attachFromFile({ taskId: props.taskId, filePath: pathInput.trim() })
			if (attachment) {
				await showToast({ type: "success", message: `Image attached: ${attachment.filename}` })
				props.onSuccess()
			}
		} catch (error) {
			await showToast({
				type: "error",
				message: `Failed to attach file: ${error instanceof Error ? error.message : String(error)}`,
			})
		} finally {
			setIsAttaching(false)
		}
	}, [attachFromFile, props, pathInput, showToast, isAttaching])

	// Keyboard handling
	useKeyboard((event) => {
		// Escape always cancels
		if (event.name === "escape") {
			if (mode === "path") {
				setMode("menu")
				setPathInput("")
			} else {
				props.onCancel()
			}
			return
		}

		if (mode === "menu") {
			// Menu mode: select action
			switch (event.name) {
				case "p":
				case "v": // 'v' for paste as alternative
					if (hasClipboard && !isAttaching) {
						handleClipboardPaste()
					}
					break
				case "f":
					setMode("path")
					break
				case "return":
				case "enter":
					// If clipboard is available, default to paste
					if (hasClipboard && !isAttaching) {
						handleClipboardPaste()
					}
					break
			}
		} else if (mode === "path") {
			// Path input mode
			if (event.name === "return" || event.name === "enter") {
				handleFileSubmit()
			}
		}
	})

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
				minWidth={50}
				maxWidth={70}
				flexDirection="column"
			>
				{/* Header */}
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{"Attach Image to " + props.taskId}
				</text>
				<text fg={theme.surface1}>{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}</text>
				<text> </text>

				{mode === "menu" ? (
					<>
						{/* Menu options */}
						<text fg={theme.text}>{"Choose attachment method:"}</text>
						<text> </text>

						<box flexDirection="row">
							<text fg={hasClipboard ? theme.lavender : theme.overlay0}>{"p"}</text>
							<text fg={hasClipboard ? theme.text : theme.overlay0}>
								{" - Paste from clipboard"}
								{!hasClipboard && " (unavailable)"}
							</text>
						</box>

						<box flexDirection="row">
							<text fg={theme.lavender}>{"f"}</text>
							<text fg={theme.text}>{" - Enter file path"}</text>
						</box>

						<text> </text>

						{isAttaching ? (
							<text fg={theme.yellow}>{"Attaching image..."}</text>
						) : (
							<text fg={theme.subtext0}>{"Press Esc to cancel"}</text>
						)}
					</>
				) : (
					<>
						{/* File path input mode */}
						<text fg={theme.text}>{"Enter image file path:"}</text>
						<text> </text>

						<box
							border={true}
							borderStyle="single"
							borderColor={theme.surface1}
							paddingLeft={1}
							paddingRight={1}
						>
							<text fg={theme.text}>{pathInput || " "}</text>
							<textInput ref={inputRef} />
						</box>

						<text> </text>

						{isAttaching ? (
							<text fg={theme.yellow}>{"Attaching image..."}</text>
						) : (
							<>
								<text fg={theme.subtext0}>{"Supported: .png, .jpg, .gif, .webp, .bmp, .svg"}</text>
								<text fg={theme.subtext0}>{"Press Enter to attach, Esc to go back"}</text>
							</>
						)}
					</>
				)}
			</box>
		</box>
	)
}
