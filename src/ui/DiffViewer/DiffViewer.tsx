import { useAtomSet } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import { useEffect, useState } from "react"
import { changedFilesAtom, showDiffPopupAtom } from "../atoms/diff.js"
import { theme } from "../theme.js"
import { FilePicker } from "./FilePicker.js"
import type { ChangedFile } from "./types.js"

interface DiffViewerProps {
	worktreePath: string
	baseBranch: string
	onClose: () => void
}

interface DiffViewerState {
	files: ChangedFile[]
	selectedIndex: number
	isLoading: boolean
}

export const DiffViewer = ({ worktreePath, baseBranch, onClose }: DiffViewerProps) => {
	// Atom hooks
	const getChangedFiles = useAtomSet(changedFilesAtom, { mode: "promise" })
	const showDiffPopup = useAtomSet(showDiffPopupAtom, { mode: "promise" })

	// Component state - simplified, no more diff content
	const [state, setState] = useState<DiffViewerState>({
		files: [],
		selectedIndex: 0,
		isLoading: true,
	})

	// Load file list on mount
	useEffect(() => {
		const loadFiles = async () => {
			setState((s) => ({ ...s, isLoading: true }))
			const files = await getChangedFiles({ worktreePath, baseBranch })
			setState({ files, selectedIndex: 0, isLoading: false })
		}
		loadFiles()
	}, [worktreePath, baseBranch, getChangedFiles])

	// Show diff for selected file in tmux popup
	const showFileDiff = async (filePath: string) => {
		await showDiffPopup({ worktreePath, baseBranch, filePath })
	}

	// Show diff for all files in tmux popup
	const showAllDiff = async () => {
		await showDiffPopup({ worktreePath, baseBranch })
	}

	// Keyboard handling
	useKeyboard((event) => {
		// Close on q or Escape
		if (event.name === "escape" || event.name === "q") {
			onClose()
			return
		}

		// Navigation (j/k or up/down arrows)
		if (event.name === "j" || event.name === "down") {
			setState((s) => ({
				...s,
				selectedIndex: Math.min(s.selectedIndex + 1, s.files.length - 1),
			}))
		} else if (event.name === "k" || event.name === "up") {
			setState((s) => ({
				...s,
				selectedIndex: Math.max(s.selectedIndex - 1, 0),
			}))
		}

		// Enter to view selected file diff in tmux popup
		if (event.name === "return") {
			const selectedFile = state.files[state.selectedIndex]
			if (selectedFile) {
				showFileDiff(selectedFile.path)
			}
		}

		// 'a' to view all files diff in tmux popup
		if (event.name === "a") {
			showAllDiff()
		}
	})

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
					</text>
				</box>

				{/* File picker - full width */}
				<box flexGrow={1}>
					<FilePicker
						files={state.files}
						selectedIndex={state.selectedIndex}
						filterText=""
						mode="fzf"
						focused={true}
						height={100}
					/>
				</box>

				{/* Status bar with keybindings */}
				<text fg={theme.surface2}>{"─".repeat(60)}</text>
				<box paddingLeft={1} paddingRight={1}>
					<text fg={theme.subtext0}>
						<span fg={theme.blue}>↑/↓</span>
						<span> navigate </span>
						<span fg={theme.blue}>Enter</span>
						<span> view file </span>
						<span fg={theme.blue}>a</span>
						<span> all files </span>
						<span fg={theme.blue}>q</span>
						<span> close</span>
					</text>
				</box>
			</box>
		</box>
	)
}
