import { theme } from "../theme.js"

interface DiffContentProps {
	content: string // Raw ANSI diff output
	scrollOffset: number
	height: number
	width: number
	focused: boolean
	currentFile: string | null // For header display
	isLoading: boolean
}

export const DiffContent = ({
	content,
	scrollOffset,
	height,
	focused,
	currentFile,
	isLoading,
}: DiffContentProps) => {
	// Split content into lines
	const lines = content ? content.split("\n") : []
	const visibleHeight = height - 4 // Account for header and footer
	const visibleLines = lines.slice(scrollOffset, scrollOffset + visibleHeight)

	// Calculate scroll percentage
	const scrollPercentage =
		lines.length > 0
			? Math.round((scrollOffset / Math.max(1, lines.length - visibleHeight)) * 100)
			: 0

	return (
		<box
			flexDirection="column"
			height={height}
			borderStyle="rounded"
			border={true}
			borderColor={focused ? theme.mauve : theme.surface2}
		>
			{/* Header */}
			<box paddingLeft={1} paddingRight={1}>
				<text fg={theme.text}>
					{currentFile ? (
						<>
							Diff: <span fg={theme.blue}>{currentFile}</span>
						</>
					) : (
						<>
							Diff: <span fg={theme.overlay0}>All changes</span>
						</>
					)}
				</text>
			</box>
			<text fg={theme.surface1}>{"─".repeat(60)}</text>

			{/* Content area */}
			<box flexDirection="column" flexGrow={1} overflow="hidden">
				{isLoading ? (
					<box paddingLeft={1} paddingTop={1}>
						<text fg={theme.yellow}>Loading diff...</text>
					</box>
				) : lines.length === 0 ? (
					<box paddingLeft={1} paddingTop={1}>
						<text fg={theme.overlay0}>No changes</text>
					</box>
				) : (
					visibleLines.map((line, index) => {
						const lineNumber = scrollOffset + index
						return (
							<box key={lineNumber} paddingLeft={1} paddingRight={1}>
								<text>{line}</text>
							</box>
						)
					})
				)}
			</box>

			{/* Status bar - scroll position */}
			{lines.length > visibleHeight && (
				<>
					<text fg={theme.surface1}>{"─".repeat(60)}</text>
					<box paddingLeft={1} paddingRight={1}>
						<text fg={theme.overlay0}>
							{scrollOffset + 1}-{Math.min(scrollOffset + visibleHeight, lines.length)}/
							{lines.length} ({scrollPercentage}%)
						</text>
					</box>
				</>
			)}
		</box>
	)
}
