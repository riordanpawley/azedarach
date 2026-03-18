/**
 * ImagePreviewOverlay component - DEPRECATED
 *
 * This component is no longer used for image preview. Images are now displayed
 * using viu in a tmux popup (see ImageAttachmentService.openPreview), which
 * supports Kitty graphics protocol for high-quality rendering.
 *
 * OpenTUI's text component renders character-by-character and strips ANSI
 * escape sequences, making it incompatible with terminal image protocols.
 * The tmux popup approach bypasses OpenTUI entirely.
 *
 * This component remains in case we need fallback error display.
 */

import { Result } from "@effect-atom/atom"
import { useAtomValue } from "@effect-atom/atom-react"
import { currentAttachmentsAtom, imagePreviewStateAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

/**
 * ImagePreviewOverlay component
 *
 * Full-screen overlay that displays the currently selected image.
 * The image is pre-rendered to terminal-compatible text by ImageAttachmentService.
 */
export const ImagePreviewOverlay = () => {
	// Subscribe to preview state from Effect layer
	const previewResult = useAtomValue(imagePreviewStateAtom)
	const preview = Result.isSuccess(previewResult)
		? previewResult.value
		: {
				taskId: null,
				attachmentId: null,
				filename: null,
				renderedImage: null,
				isLoading: false,
				error: null,
			}

	// Get current attachments for navigation info
	const attachmentsResult = useAtomValue(currentAttachmentsAtom)
	const { attachments, selectedIndex } = Result.isSuccess(attachmentsResult)
		? (attachmentsResult.value ?? { attachments: [], selectedIndex: -1 })
		: { attachments: [], selectedIndex: -1 }

	const { filename, renderedImage, isLoading, error } = preview

	// Navigation info (e.g., "2 / 5")
	const navInfo =
		attachments.length > 0 && selectedIndex >= 0
			? `${selectedIndex + 1} / ${attachments.length}`
			: ""

	return (
		<box
			position="absolute"
			left={0}
			right={0}
			top={0}
			bottom={0}
			alignItems="center"
			justifyContent="center"
			backgroundColor={theme.crust}
		>
			<box
				borderStyle="rounded"
				border={true}
				borderColor={theme.mauve}
				backgroundColor={theme.base}
				paddingLeft={1}
				paddingRight={1}
				paddingTop={0}
				paddingBottom={0}
				flexDirection="column"
				alignItems="center"
			>
				{/* Header with filename and navigation */}
				<box flexDirection="row" justifyContent="space-between" width="100%">
					<text fg={theme.mauve} attributes={ATTR_BOLD}>
						{filename ? `📷 ${filename}` : "Image Preview"}
					</text>
					{navInfo && <text fg={theme.subtext0}>{navInfo}</text>}
				</box>

				{/* Image content */}
				{isLoading ? (
					<box paddingTop={2} paddingBottom={2}>
						<text fg={theme.yellow}>{"Loading image..."}</text>
					</box>
				) : error ? (
					<box paddingTop={2} paddingBottom={2} flexDirection="column" alignItems="center">
						<text fg={theme.red}>{"Failed to load image"}</text>
						<text fg={theme.subtext0}>{error}</text>
					</box>
				) : renderedImage ? (
					<box paddingTop={1} paddingBottom={1}>
						{/* The rendered image contains control sequences that OpenTUI passes through */}
						<text>{renderedImage}</text>
					</box>
				) : (
					<box paddingTop={2} paddingBottom={2}>
						<text fg={theme.subtext0}>{"No image to display"}</text>
					</box>
				)}

				{/* Footer with keybindings */}
				<text fg={theme.subtext0}>
					{attachments.length > 1
						? "j/k:navigate  o:open  Esc:close"
						: "o:open in viewer  Esc:close"}
				</text>
			</box>
		</box>
	)
}
