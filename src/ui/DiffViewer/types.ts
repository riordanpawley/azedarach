export interface ChangedFile {
	path: string
	status: "added" | "modified" | "deleted" | "renamed"
	oldPath?: string
}

export type Layout = "split" | "diff-only" | "picker-only"
export type PickerMode = "fzf" | "tree"
export type Focus = "picker" | "diff"

export interface DiffViewerState {
	layout: Layout
	pickerMode: PickerMode
	files: ChangedFile[]
	selectedIndex: number
	filterText: string
	currentFile: string | null // null = all files
	diffContent: string
	scrollOffset: number
	focus: Focus
	isLoading: boolean
}
