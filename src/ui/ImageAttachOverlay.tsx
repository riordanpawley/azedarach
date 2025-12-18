/**
 * ImageAttachOverlay component - overlay for attaching images to tasks
 *
 * Provides two methods for attaching images:
 * 1. Paste from clipboard (if xclip/wl-paste available)
 * 2. Enter file path manually
 *
 * Note: Keyboard handling is in KeyboardService, this component just renders.
 */

import { Result } from "@effect-atom/atom"
import { useAtomValue } from "@effect-atom/atom-react"
import { hasClipboardSupportAtom, imageAttachOverlayStateAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

/**
 * ImageAttachOverlay component
 *
 * Modal overlay for attaching images to a task.
 * Supports clipboard paste and file path entry.
 * All state and keyboard handling is in the Effect layer.
 */
export const ImageAttachOverlay = () => {
	// Subscribe to overlay state from Effect layer
	const stateResult = useAtomValue(imageAttachOverlayStateAtom)
	const state = Result.isSuccess(stateResult)
		? stateResult.value
		: { mode: "menu" as const, pathInput: "", isAttaching: false, taskId: null }

	// Subscribe to clipboard support
	const hasClipboardResult = useAtomValue(hasClipboardSupportAtom)
	const hasClipboard = Result.isSuccess(hasClipboardResult) ? hasClipboardResult.value : false

	const { mode, pathInput, isAttaching, taskId } = state

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
					{`Attach Image to ${taskId ?? "task"}`}
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
