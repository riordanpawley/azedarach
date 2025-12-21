import { useAtomSet } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import { useCallback, useEffect, useState } from "react"
import { changedFilesAtom, fileDiffAtom, fullDiffAtom } from "../atoms/diff.js"
import { theme } from "../theme.js"
import { DiffContent } from "./DiffContent.js"
import { FilePicker } from "./FilePicker.js"
import type { DiffViewerState } from "./types.js"

interface DiffViewerProps {
	worktreePath: string
	baseBranch: string
	onClose: () => void
}

export const DiffViewer = ({ worktreePath, baseBranch, onClose }: DiffViewerProps) => {
	// Atom hooks for fetching data
	const getChangedFiles = useAtomSet(changedFilesAtom, { mode: "promise" })
	const getFileDiff = useAtomSet(fileDiffAtom, { mode: "promise" })
	const getFullDiff = useAtomSet(fullDiffAtom, { mode: "promise" })

	// Component state
	const [state, setState] = useState<DiffViewerState>({
		layout: "split",
		pickerMode: "fzf",
		files: [],
		selectedIndex: 0,
		filterText: "",
		currentFile: null,
		diffContent: "",
		scrollOffset: 0,
		focus: "picker",
		isLoading: true,
	})

	// Load files and initial diff on mount
	useEffect(() => {
		const loadInitialData = async () => {
			setState((s) => ({ ...s, isLoading: true }))

			// Load changed files
			const files = await getChangedFiles({ worktreePath, baseBranch })

			// Load full diff as initial view
			const diffContent = await getFullDiff({ worktreePath, baseBranch })

			setState((s) => ({
				...s,
				files,
				diffContent,
				currentFile: null,
				isLoading: false,
			}))
		}

		loadInitialData()
	}, [worktreePath, baseBranch, getChangedFiles, getFullDiff])

	// Load diff for selected file
	const loadFileDiff = useCallback(
		async (filePath: string) => {
			setState((s) => ({ ...s, isLoading: true, scrollOffset: 0 }))
			const diffContent = await getFileDiff({ worktreePath, baseBranch, filePath })
			setState((s) => ({
				...s,
				diffContent,
				currentFile: filePath,
				isLoading: false,
			}))
		},
		[worktreePath, baseBranch, getFileDiff],
	)

	// Load full diff (all files)
	const loadFullDiff = useCallback(async () => {
		setState((s) => ({ ...s, isLoading: true, scrollOffset: 0 }))
		const diffContent = await getFullDiff({ worktreePath, baseBranch })
		setState((s) => ({
			...s,
			diffContent,
			currentFile: null,
			isLoading: false,
		}))
	}, [worktreePath, baseBranch, getFullDiff])

	// Keyboard handling
	useKeyboard((event) => {
		if (event.name === "escape") {
			onClose()
			return
		}

		// Navigation in picker
		if (event.name === "j" && state.focus === "picker") {
			setState((s) => ({
				...s,
				selectedIndex: Math.min(s.selectedIndex + 1, s.files.length - 1),
			}))
		} else if (event.name === "k" && state.focus === "picker") {
			setState((s) => ({
				...s,
				selectedIndex: Math.max(s.selectedIndex - 1, 0),
			}))
		}

		// Scroll in diff view
		if (event.name === "j" && state.focus === "diff") {
			setState((s) => ({
				...s,
				scrollOffset: Math.min(s.scrollOffset + 1, s.diffContent.split("\n").length - 1),
			}))
		} else if (event.name === "k" && state.focus === "diff") {
			setState((s) => ({
				...s,
				scrollOffset: Math.max(s.scrollOffset - 1, 0),
			}))
		}

		// Toggle focus between panels
		if (event.name === "h") {
			setState((s) => ({ ...s, focus: "picker" }))
		} else if (event.name === "l") {
			setState((s) => ({ ...s, focus: "diff" }))
		}

		// Toggle picker visibility (maximize diff)
		if (event.name === "f") {
			setState((s) => {
				const layouts: DiffViewerState["layout"][] = ["split", "diff-only", "picker-only"]
				const currentIndex = layouts.indexOf(s.layout)
				const nextLayout = layouts[(currentIndex + 1) % layouts.length]
				return { ...s, layout: nextLayout }
			})
		}

		// Tab to toggle picker mode (fzf/tree)
		if (event.name === "tab") {
			setState((s) => ({
				...s,
				pickerMode: s.pickerMode === "fzf" ? "tree" : "fzf",
			}))
		}

		// Enter to select file and load its diff
		if (event.name === "return" && state.focus === "picker") {
			const selectedFile = state.files[state.selectedIndex]
			if (selectedFile) {
				loadFileDiff(selectedFile.path)
			}
		}

		// 'a' to show all files diff
		if (event.name === "a" && state.focus === "picker") {
			loadFullDiff()
		}
	})

	const showPicker = state.layout === "split" || state.layout === "picker-only"
	const showDiff = state.layout === "split" || state.layout === "diff-only"

	return (
		<box
			position="absolute"
			left={0}
			right={0}
			top={0}
			bottom={0}
			backgroundColor={`${theme.crust}EE`}
			alignItems="center"
			justifyContent="center"
		>
			<box flexDirection="column" width="95%" height="95%">
				{/* Header */}
				<box paddingLeft={1} paddingBottom={1}>
					<text fg={theme.mauve}>
						Diff: {baseBranch}...HEAD <span fg={theme.overlay0}>({worktreePath})</span>
					</text>
				</box>

				{/* Main content area */}
				<box flexGrow={1} flexDirection="row" gap={1}>
					{/* File picker - 30% width when shown */}
					{showPicker && (
						<box width="30%">
							<FilePicker
								files={state.files}
								selectedIndex={state.selectedIndex}
								filterText={state.filterText}
								mode={state.pickerMode}
								focused={state.focus === "picker"}
								height={100}
							/>
						</box>
					)}

					{/* Diff content - remaining width */}
					{showDiff && (
						<box flexGrow={1}>
							<DiffContent
								content={state.diffContent}
								scrollOffset={state.scrollOffset}
								height={100}
								width={100}
								focused={state.focus === "diff"}
								currentFile={state.currentFile}
								isLoading={state.isLoading}
							/>
						</box>
					)}
				</box>

				{/* Status bar with keybindings */}
				<text fg={theme.surface1}>{"─".repeat(80)}</text>
				<box paddingLeft={1} paddingRight={1}>
					<text fg={theme.overlay0}>
						<span fg={theme.blue}>h/l</span>
						<span> focus </span>
						<span fg={theme.blue}>j/k</span>
						<span> navigate </span>
						<span fg={theme.blue}>Enter</span>
						<span> select </span>
						<span fg={theme.blue}>a</span>
						<span> all </span>
						<span fg={theme.blue}>f</span>
						<span> layout </span>
						<span fg={theme.blue}>Esc</span>
						<span> close</span>
					</text>
				</box>
			</box>
		</box>
	)
}
