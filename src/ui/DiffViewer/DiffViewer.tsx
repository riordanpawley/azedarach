import { useAtomSet } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import { useEffect, useMemo, useState } from "react"
import { changedFilesAtom, showDiffPopupAtom } from "../atoms/diff.js"
import { theme } from "../theme.js"
import { generateJumpLabels } from "../types.js"
import { FilePicker } from "./FilePicker.js"
import { buildFileTree, flattenTree, getAllDirectoryPaths, getParentPath } from "./fileTree.js"
import type { ChangedFile, PickerMode } from "./types.js"

interface DiffViewerProps {
	worktreePath: string
	baseBranch: string
	onClose: () => void
}

type InputMode = "normal" | "search" | "goto" | "jump"

interface DiffViewerState {
	files: ChangedFile[]
	selectedIndex: number
	isLoading: boolean
	// View and input mode
	viewMode: PickerMode
	inputMode: InputMode
	filterText: string
	// Jump labels (index in filtered list → label)
	jumpLabels: Map<number, string> | null
	pendingJumpKey: string | null
	// Tree mode state
	expandedDirs: Set<string>
}

export const DiffViewer = ({ worktreePath, baseBranch, onClose }: DiffViewerProps) => {
	// Atom hooks
	const getChangedFiles = useAtomSet(changedFilesAtom, { mode: "promise" })
	const showDiffPopup = useAtomSet(showDiffPopupAtom, { mode: "promise" })

	// Component state
	const [state, setState] = useState<DiffViewerState>({
		files: [],
		selectedIndex: 0,
		isLoading: true,
		viewMode: "fzf",
		inputMode: "normal",
		filterText: "",
		jumpLabels: null,
		pendingJumpKey: null,
		expandedDirs: new Set(),
	})

	// Filter files based on search text
	const filteredFiles = useMemo(() => {
		if (!state.filterText) return state.files
		const query = state.filterText.toLowerCase()
		return state.files.filter((f) => f.path.toLowerCase().includes(query))
	}, [state.files, state.filterText])

	// Build tree structure from filtered files
	const fileTree = useMemo(() => buildFileTree(filteredFiles), [filteredFiles])

	// Flatten tree based on expansion state (for rendering)
	const flattenedTree = useMemo(
		() => flattenTree(fileTree, state.expandedDirs),
		[fileTree, state.expandedDirs],
	)

	// Get current item count based on view mode
	const itemCount = state.viewMode === "tree" ? flattenedTree.length : filteredFiles.length

	// Load file list on mount
	useEffect(() => {
		const loadFiles = async () => {
			setState((s) => ({ ...s, isLoading: true }))
			const files = await getChangedFiles({ worktreePath, baseBranch })
			setState((s) => ({ ...s, files, selectedIndex: 0, isLoading: false }))
		}
		loadFiles()
	}, [worktreePath, baseBranch, getChangedFiles])

	// Reset selection when item count changes (keep selection in bounds)
	useEffect(() => {
		setState((s) => ({
			...s,
			selectedIndex: Math.min(s.selectedIndex, Math.max(0, itemCount - 1)),
		}))
	}, [itemCount])

	// Show diff for selected file in tmux popup
	const showFileDiff = async (filePath: string) => {
		await showDiffPopup({ worktreePath, baseBranch, filePath })
	}

	// Show diff for all files in tmux popup
	const showAllDiff = async () => {
		await showDiffPopup({ worktreePath, baseBranch })
	}

	// Tree navigation helpers
	const toggleExpand = (dirPath: string) => {
		setState((s) => {
			const newExpanded = new Set(s.expandedDirs)
			if (newExpanded.has(dirPath)) {
				newExpanded.delete(dirPath)
			} else {
				newExpanded.add(dirPath)
			}
			return { ...s, expandedDirs: newExpanded }
		})
	}

	const expandAll = () => {
		const allDirs = getAllDirectoryPaths(fileTree)
		setState((s) => ({ ...s, expandedDirs: new Set(allDirs) }))
	}

	const collapseAll = () => {
		setState((s) => ({ ...s, expandedDirs: new Set() }))
	}

	// Get the currently selected item (for tree operations)
	const getSelectedTreeNode = () => {
		if (state.viewMode !== "tree") return null
		return flattenedTree[state.selectedIndex] ?? null
	}

	// Get the currently selected file (for diff operations)
	const getSelectedFile = (): ChangedFile | null => {
		if (state.viewMode === "tree") {
			const node = flattenedTree[state.selectedIndex]
			return node?.type === "file" ? (node.file ?? null) : null
		}
		return filteredFiles[state.selectedIndex] ?? null
	}

	// Jump label helpers
	const enterJumpMode = () => {
		// Generate labels for all visible items
		const labels = generateJumpLabels(itemCount)
		const labelMap = new Map<number, string>()
		labels.forEach((label, index) => {
			if (index < itemCount) {
				labelMap.set(index, label)
			}
		})
		setState((s) => ({
			...s,
			inputMode: "jump",
			jumpLabels: labelMap,
			pendingJumpKey: null,
		}))
	}

	const exitJumpMode = () => {
		setState((s) => ({
			...s,
			inputMode: "normal",
			jumpLabels: null,
			pendingJumpKey: null,
		}))
	}

	// Find index by label
	const findIndexByLabel = (label: string): number | null => {
		if (!state.jumpLabels) return null
		for (const [index, l] of state.jumpLabels.entries()) {
			if (l === label) return index
		}
		return null
	}

	// Keyboard handling - mode-based routing
	useKeyboard((event) => {
		const { inputMode } = state

		// === SEARCH MODE ===
		if (inputMode === "search") {
			// Escape: exit search, clear filter
			if (event.name === "escape") {
				setState((s) => ({ ...s, inputMode: "normal", filterText: "" }))
				return
			}
			// Enter: exit search, keep filter
			if (event.name === "return") {
				setState((s) => ({ ...s, inputMode: "normal" }))
				return
			}
			// Backspace: remove last char
			if (event.name === "backspace") {
				setState((s) => ({ ...s, filterText: s.filterText.slice(0, -1) }))
				return
			}
			// Printable characters: append to filter
			if (event.sequence && event.sequence.length === 1 && !event.ctrl && !event.meta) {
				setState((s) => ({ ...s, filterText: s.filterText + event.sequence }))
				return
			}
			// Navigation works in search mode too
			if (event.name === "down" || (event.ctrl && event.name === "n")) {
				setState((s) => ({
					...s,
					selectedIndex: Math.min(s.selectedIndex + 1, filteredFiles.length - 1),
				}))
				return
			}
			if (event.name === "up" || (event.ctrl && event.name === "p")) {
				setState((s) => ({
					...s,
					selectedIndex: Math.max(s.selectedIndex - 1, 0),
				}))
				return
			}
			return
		}

		// === GOTO MODE (waiting for second key after 'g') ===
		if (inputMode === "goto") {
			// Escape: exit goto mode
			if (event.name === "escape") {
				setState((s) => ({ ...s, inputMode: "normal" }))
				return
			}
			// w: enter jump mode with labels
			if (event.name === "w") {
				enterJumpMode()
				return
			}
			// g: go to first item
			if (event.name === "g") {
				setState((s) => ({ ...s, inputMode: "normal", selectedIndex: 0 }))
				return
			}
			// G or e: go to last item
			if (event.name === "e" || event.shift) {
				setState((s) => ({ ...s, inputMode: "normal", selectedIndex: itemCount - 1 }))
				return
			}
			// Any other key: exit goto mode
			setState((s) => ({ ...s, inputMode: "normal" }))
			return
		}

		// === JUMP MODE (labels visible, waiting for 2-char combo) ===
		if (inputMode === "jump") {
			// Escape: exit jump mode
			if (event.name === "escape") {
				exitJumpMode()
				return
			}

			// Handle label character input
			if (event.sequence && event.sequence.length === 1 && !event.ctrl && !event.meta) {
				const char = event.sequence

				if (!state.pendingJumpKey) {
					// First character of label
					setState((s) => ({ ...s, pendingJumpKey: char }))
				} else {
					// Second character - lookup and jump
					const label = state.pendingJumpKey + char
					const targetIndex = findIndexByLabel(label)
					if (targetIndex !== null) {
						setState((s) => ({
							...s,
							inputMode: "normal",
							jumpLabels: null,
							pendingJumpKey: null,
							selectedIndex: targetIndex,
						}))
					} else {
						// Invalid label, exit jump mode
						exitJumpMode()
					}
				}
				return
			}
			return
		}

		// === NORMAL MODE ===
		// Close on q or Escape
		if (event.name === "escape" || event.name === "q") {
			onClose()
			return
		}

		// Enter goto mode with g
		if (event.name === "g") {
			setState((s) => ({ ...s, inputMode: "goto" }))
			return
		}

		// Enter search mode with /
		if (event.sequence === "/") {
			setState((s) => ({ ...s, inputMode: "search" }))
			return
		}

		// Tab to toggle between list and tree view
		if (event.name === "tab") {
			setState((s) => ({
				...s,
				viewMode: s.viewMode === "fzf" ? "tree" : "fzf",
				selectedIndex: 0, // Reset selection on mode switch
			}))
			return
		}

		// Navigation (j/k or up/down arrows)
		if (event.name === "j" || event.name === "down") {
			setState((s) => ({
				...s,
				selectedIndex: Math.min(s.selectedIndex + 1, itemCount - 1),
			}))
			return
		}
		if (event.name === "k" || event.name === "up") {
			setState((s) => ({
				...s,
				selectedIndex: Math.max(s.selectedIndex - 1, 0),
			}))
			return
		}

		// Tree-specific navigation
		if (state.viewMode === "tree") {
			const selectedNode = getSelectedTreeNode()

			// Space or Enter on directory: toggle expand
			if (selectedNode?.type === "directory") {
				if (event.name === "space" || event.name === "return") {
					toggleExpand(selectedNode.path)
					return
				}
				// l or right: expand directory
				if (event.name === "l" || event.name === "right") {
					if (!state.expandedDirs.has(selectedNode.path)) {
						toggleExpand(selectedNode.path)
					}
					return
				}
			}

			// h or left: collapse current or go to parent
			if (event.name === "h" || event.name === "left") {
				if (selectedNode) {
					if (selectedNode.type === "directory" && state.expandedDirs.has(selectedNode.path)) {
						// Collapse current directory
						toggleExpand(selectedNode.path)
					} else {
						// Go to parent directory
						const parentPath = getParentPath(selectedNode.path)
						if (parentPath) {
							// Find parent in flattened tree and select it
							const parentIndex = flattenedTree.findIndex((n) => n.path === parentPath)
							if (parentIndex !== -1) {
								setState((s) => ({ ...s, selectedIndex: parentIndex }))
							}
						}
					}
				}
				return
			}

			// - collapse all
			if (event.sequence === "-") {
				collapseAll()
				return
			}

			// + or = expand all
			if (event.sequence === "+" || event.sequence === "=") {
				expandAll()
				return
			}
		}

		// Enter to view selected file diff in tmux popup
		if (event.name === "return") {
			const selectedFile = getSelectedFile()
			if (selectedFile) {
				showFileDiff(selectedFile.path)
			}
			return
		}

		// 'a' to view all files diff in tmux popup
		if (event.name === "a") {
			showAllDiff()
			return
		}
	})

	const isSearching = state.inputMode === "search"
	const isGotoMode = state.inputMode === "goto"
	const isJumpMode = state.inputMode === "jump"

	return (
		<box
			position="absolute"
			left={0}
			right={0}
			top={0}
			bottom={0}
			backgroundColor={`${theme.crust}CC`}
			alignItems="center"
			justifyContent="center"
		>
			<box flexDirection="column" width="50%" height="80%" backgroundColor={theme.base}>
				{/* Header */}
				<box paddingLeft={1} paddingBottom={1}>
					<text fg={theme.mauve}>
						Changed Files <span fg={theme.subtext0}>({baseBranch}...HEAD)</span>
						{state.filterText && (
							<span fg={theme.subtext0}>
								{" "}
								- {filteredFiles.length}/{state.files.length} matches
							</span>
						)}
					</text>
				</box>

				{/* File picker - full width */}
				<box flexGrow={1}>
					<FilePicker
						files={filteredFiles}
						selectedIndex={state.selectedIndex}
						filterText={state.filterText}
						mode={state.viewMode}
						focused={true}
						height={100}
						isSearching={isSearching}
						treeNodes={flattenedTree}
						jumpLabels={state.jumpLabels}
						pendingJumpKey={state.pendingJumpKey}
					/>
				</box>

				{/* Status bar with keybindings */}
				<text fg={theme.surface2}>{"─".repeat(60)}</text>
				<box paddingLeft={1} paddingRight={1}>
					{isSearching ? (
						<text fg={theme.subtext0}>
							<span fg={theme.yellow}>SEARCH </span>
							<span fg={theme.blue}>Enter</span>
							<span> confirm </span>
							<span fg={theme.blue}>Esc</span>
							<span> cancel </span>
							<span fg={theme.blue}>↑/↓</span>
							<span> navigate</span>
						</text>
					) : isGotoMode ? (
						<text fg={theme.subtext0}>
							<span fg={theme.yellow}>GOTO </span>
							<span fg={theme.blue}>w</span>
							<span> jump labels </span>
							<span fg={theme.blue}>g</span>
							<span> first </span>
							<span fg={theme.blue}>G</span>
							<span> last </span>
							<span fg={theme.blue}>Esc</span>
							<span> cancel</span>
						</text>
					) : isJumpMode ? (
						<text fg={theme.subtext0}>
							<span fg={theme.yellow}>JUMP </span>
							<span>type 2-char label to jump </span>
							<span fg={theme.blue}>Esc</span>
							<span> cancel</span>
							{state.pendingJumpKey && <span fg={theme.yellow}> [{state.pendingJumpKey}_]</span>}
						</text>
					) : state.viewMode === "tree" ? (
						<text fg={theme.subtext0}>
							<span fg={theme.blue}>Tab</span>
							<span> list </span>
							<span fg={theme.blue}>h/l</span>
							<span> ±node </span>
							<span fg={theme.blue}>-/+</span>
							<span> ±all </span>
							<span fg={theme.blue}>gw</span>
							<span> jump </span>
							<span fg={theme.blue}>q</span>
							<span> close</span>
						</text>
					) : (
						<text fg={theme.subtext0}>
							<span fg={theme.blue}>Tab</span>
							<span> tree </span>
							<span fg={theme.blue}>↑/↓</span>
							<span> nav </span>
							<span fg={theme.blue}>/</span>
							<span> search </span>
							<span fg={theme.blue}>gw</span>
							<span> jump </span>
							<span fg={theme.blue}>q</span>
							<span> close</span>
						</text>
					)}
				</box>
			</box>
		</box>
	)
}
