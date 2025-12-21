export interface ChangedFile {
	path: string
	status: "added" | "modified" | "deleted" | "renamed"
	oldPath?: string
}

// Note: PickerMode kept for FilePicker props, but tree mode not yet implemented
export type PickerMode = "fzf" | "tree"
