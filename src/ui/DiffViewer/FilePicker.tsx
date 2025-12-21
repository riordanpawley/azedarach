import { theme } from "../theme.js"
import type { ChangedFile, PickerMode } from "./types.js"

interface FilePickerProps {
	files: ChangedFile[]
	selectedIndex: number
	filterText: string
	mode: PickerMode
	focused: boolean
	height: number
}

const getStatusIndicator = (status: ChangedFile["status"]): { symbol: string; color: string } => {
	switch (status) {
		case "added":
			return { symbol: "A", color: theme.green }
		case "modified":
			return { symbol: "M", color: theme.blue }
		case "deleted":
			return { symbol: "D", color: theme.red }
		case "renamed":
			return { symbol: "R", color: theme.yellow }
	}
}

const FileItem = ({
	file,
	selected,
	focused,
}: {
	file: ChangedFile
	selected: boolean
	focused: boolean
}) => {
	const { symbol, color } = getStatusIndicator(file.status)
	const bg = selected ? (focused ? theme.surface0 : theme.surface1) : "transparent"
	const fg = selected ? theme.text : theme.subtext1

	return (
		<box backgroundColor={bg} paddingLeft={1} paddingRight={1}>
			<text>
				<span fg={color}>{symbol}</span>
				<span fg={fg}> {file.path}</span>
				{file.status === "renamed" && file.oldPath && (
					<span fg={theme.overlay0}> ← {file.oldPath}</span>
				)}
			</text>
		</box>
	)
}

export const FilePicker = ({
	files,
	selectedIndex,
	filterText,
	focused,
	height,
}: FilePickerProps) => {
	// Calculate visible range for scrolling
	const visibleHeight = height - 4 // Account for header and footer
	const startIndex = Math.max(
		0,
		Math.min(selectedIndex - Math.floor(visibleHeight / 2), files.length - visibleHeight),
	)
	const visibleFiles = files.slice(startIndex, startIndex + visibleHeight)

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
					Changed Files <span fg={theme.overlay0}>({files.length})</span>
				</text>
			</box>
			<text fg={theme.surface1}>{"─".repeat(30)}</text>

			{/* Filter input (shown when searching) */}
			{filterText && (
				<>
					<box paddingLeft={1} paddingRight={1}>
						<text>
							<span fg={theme.blue}>/</span>
							<span fg={theme.text}> {filterText}</span>
							<span fg={theme.blue}>█</span>
						</text>
					</box>
					<text fg={theme.surface1}>{"─".repeat(30)}</text>
				</>
			)}

			{/* File list */}
			<box flexDirection="column" flexGrow={1} overflow="hidden">
				{visibleFiles.length > 0 ? (
					visibleFiles.map((file, index) => {
						const actualIndex = startIndex + index
						return (
							<FileItem
								key={file.path}
								file={file}
								selected={actualIndex === selectedIndex}
								focused={focused}
							/>
						)
					})
				) : (
					<box paddingLeft={1} paddingTop={1}>
						<text fg={theme.overlay0}>No files match filter</text>
					</box>
				)}
			</box>

			{/* Scroll indicator */}
			{files.length > visibleHeight && (
				<>
					<text fg={theme.surface1}>{"─".repeat(30)}</text>
					<box paddingLeft={1} paddingRight={1}>
						<text fg={theme.overlay0}>
							{selectedIndex + 1}/{files.length}
						</text>
					</box>
				</>
			)}
		</box>
	)
}
