import { theme } from "../theme.js"
import type { ChangedFile, FlatTreeNode, PickerMode } from "./types.js"

interface FilePickerProps {
	files: ChangedFile[]
	selectedIndex: number
	filterText: string
	mode: PickerMode
	focused: boolean
	/** Maximum number of items to show at once (virtual scrolling) */
	maxVisible?: number
	isSearching?: boolean
	// Tree mode props
	treeNodes?: FlatTreeNode[]
	// Jump label props
	jumpLabels?: Map<number, string> | null
	pendingJumpKey?: string | null
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

/**
 * Split a file path into directory and filename
 * "src/ui/DiffViewer.tsx" → { dir: "src/ui/", name: "DiffViewer.tsx" }
 */
const splitPath = (path: string): { dir: string; name: string } => {
	const lastSlash = path.lastIndexOf("/")
	if (lastSlash === -1) {
		return { dir: "", name: path }
	}
	return {
		dir: path.substring(0, lastSlash + 1),
		name: path.substring(lastSlash + 1),
	}
}

const FileItem = ({
	file,
	selected,
	focused,
	jumpLabel,
	pendingJumpKey,
}: {
	file: ChangedFile
	selected: boolean
	focused: boolean
	jumpLabel?: string
	pendingJumpKey?: string | null
}) => {
	const { symbol, color } = getStatusIndicator(file.status)
	const bg = selected ? (focused ? theme.surface0 : theme.surface1) : "transparent"

	// Split path into dim directory and bright filename
	const { dir, name } = splitPath(file.path)
	const dirColor = selected ? theme.subtext0 : theme.overlay0
	const nameColor = selected ? theme.text : theme.subtext1

	// Jump label styling - dim labels that don't match pending key
	const showLabel = jumpLabel !== undefined
	const labelMatchesPending = pendingJumpKey ? jumpLabel?.startsWith(pendingJumpKey) : true
	const labelColor = labelMatchesPending ? theme.yellow : theme.overlay0

	return (
		<box backgroundColor={bg} paddingLeft={1} paddingRight={1}>
			<text>
				{showLabel && (
					<>
						<span fg={labelColor}>{jumpLabel}</span>
						<span> </span>
					</>
				)}
				<span fg={color}>{symbol}</span>
				<span> </span>
				{dir && <span fg={dirColor}>{dir}</span>}
				<span fg={nameColor}>{name}</span>
				{file.status === "renamed" && file.oldPath && (
					<span fg={theme.subtext0}> ← {file.oldPath}</span>
				)}
			</text>
		</box>
	)
}

/**
 * Tree node item - renders a single node (file or directory) in tree mode
 */
const TreeItem = ({
	node,
	selected,
	focused,
	jumpLabel,
	pendingJumpKey,
}: {
	node: FlatTreeNode
	selected: boolean
	focused: boolean
	jumpLabel?: string
	pendingJumpKey?: string | null
}) => {
	const bg = selected ? (focused ? theme.surface0 : theme.surface1) : "transparent"
	const indent = "  ".repeat(node.depth)

	// Jump label styling
	const showLabel = jumpLabel !== undefined
	const labelMatchesPending = pendingJumpKey ? jumpLabel?.startsWith(pendingJumpKey) : true
	const labelColor = labelMatchesPending ? theme.yellow : theme.overlay0

	if (node.type === "directory") {
		// Directory node
		const arrow = node.isExpanded ? "▼" : "►"
		const nameColor = selected ? theme.text : theme.subtext1
		const arrowColor = selected ? theme.blue : theme.overlay1

		return (
			<box backgroundColor={bg} paddingLeft={1} paddingRight={1}>
				<text>
					{showLabel && (
						<>
							<span fg={labelColor}>{jumpLabel}</span>
							<span> </span>
						</>
					)}
					<span fg={theme.overlay0}>{indent}</span>
					<span fg={arrowColor}>{arrow}</span>
					<span fg={nameColor}> {node.name}/</span>
				</text>
			</box>
		)
	}

	// File node
	const { symbol, color } = getStatusIndicator(node.file!.status)
	const nameColor = selected ? theme.text : theme.subtext1

	return (
		<box backgroundColor={bg} paddingLeft={1} paddingRight={1}>
			<text>
				{showLabel && (
					<>
						<span fg={labelColor}>{jumpLabel}</span>
						<span> </span>
					</>
				)}
				<span fg={theme.overlay0}>{indent}</span>
				<span fg={color}>{symbol}</span>
				<span fg={nameColor}> {node.name}</span>
				{node.file?.status === "renamed" && node.file?.oldPath && (
					<span fg={theme.subtext0}> ← {splitPath(node.file.oldPath).name}</span>
				)}
			</text>
		</box>
	)
}

/** Default number of visible items in the file picker */
const DEFAULT_MAX_VISIBLE = 20

export const FilePicker = ({
	files,
	selectedIndex,
	filterText,
	mode,
	focused,
	maxVisible = DEFAULT_MAX_VISIBLE,
	isSearching = false,
	treeNodes = [],
	jumpLabels = null,
	pendingJumpKey = null,
}: FilePickerProps) => {
	// Determine what items to display based on mode
	const isTreeMode = mode === "tree"
	const itemCount = isTreeMode ? treeNodes.length : files.length

	// Calculate visible range for virtual scrolling (like Column.tsx)
	// Only render maxVisible items, keeping selection visible
	const visibleCount = Math.min(maxVisible, itemCount)
	let startIndex = 0

	if (itemCount > maxVisible) {
		// Scroll to keep selection centered when possible
		if (selectedIndex >= maxVisible - 1) {
			startIndex = Math.min(selectedIndex - maxVisible + 2, itemCount - maxVisible)
		}
		startIndex = Math.max(0, startIndex)
	}

	// Get visible items based on mode (computed separately to maintain types)
	const visibleFiles = isTreeMode ? [] : files.slice(startIndex, startIndex + visibleCount)
	const visibleTreeNodes = isTreeMode ? treeNodes.slice(startIndex, startIndex + visibleCount) : []

	// Calculate hidden counts for scroll indicators
	const hiddenBefore = startIndex
	const hiddenAfter = Math.max(0, itemCount - startIndex - visibleCount)

	return (
		<box
			flexDirection="column"
			flexGrow={1}
			borderStyle="rounded"
			border={true}
			borderColor={focused ? theme.mauve : theme.surface2}
		>
			{/* Header */}
			<box paddingLeft={1} paddingRight={1}>
				<text fg={theme.text}>
					Changed Files <span fg={theme.subtext0}>({files.length})</span>
					<span fg={theme.overlay0}> [{isTreeMode ? "tree" : "list"}]</span>
				</text>
			</box>
			<text fg={theme.surface1}>{"─".repeat(30)}</text>

			{/* Filter input (shown when searching or has filter) */}
			{(isSearching || filterText) && (
				<>
					<box paddingLeft={1} paddingRight={1}>
						<text>
							<span fg={theme.blue}>/</span>
							<span fg={theme.text}> {filterText}</span>
							{isSearching && <span fg={theme.blue}>█</span>}
						</text>
					</box>
					<text fg={theme.surface1}>{"─".repeat(30)}</text>
				</>
			)}

			{/* File/Tree list */}
			<box flexDirection="column" flexGrow={1} overflow="hidden">
				{itemCount === 0 ? (
					<box paddingLeft={1} paddingTop={1}>
						<text fg={theme.subtext0}>No files match filter</text>
					</box>
				) : isTreeMode ? (
					// Tree mode rendering
					visibleTreeNodes.map((node, index) => {
						const actualIndex = startIndex + index
						const label = jumpLabels?.get(actualIndex)
						return (
							<TreeItem
								key={node.path}
								node={node}
								selected={actualIndex === selectedIndex}
								focused={focused}
								jumpLabel={label}
								pendingJumpKey={pendingJumpKey}
							/>
						)
					})
				) : (
					// List mode rendering
					visibleFiles.map((file, index) => {
						const actualIndex = startIndex + index
						const label = jumpLabels?.get(actualIndex)
						return (
							<FileItem
								key={file.path}
								file={file}
								selected={actualIndex === selectedIndex}
								focused={focused}
								jumpLabel={label}
								pendingJumpKey={pendingJumpKey}
							/>
						)
					})
				)}
			</box>

			{/* Scroll indicator - shows position when there are hidden items */}
			{(hiddenBefore > 0 || hiddenAfter > 0) && (
				<>
					<text fg={theme.surface2}>{"─".repeat(30)}</text>
					<box paddingLeft={1} paddingRight={1}>
						<text fg={theme.subtext0}>
							{hiddenBefore > 0 && <span>↑{hiddenBefore} </span>}
							<span>
								{selectedIndex + 1}/{itemCount}
							</span>
							{hiddenAfter > 0 && <span> ↓{hiddenAfter}</span>}
						</text>
					</box>
				</>
			)}
		</box>
	)
}
