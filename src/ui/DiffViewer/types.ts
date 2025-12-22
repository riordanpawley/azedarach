export interface ChangedFile {
	path: string
	status: "added" | "modified" | "deleted" | "renamed"
	oldPath?: string
}

export type PickerMode = "fzf" | "tree"

/**
 * Tree node for hierarchical file display
 *
 * Can represent either a directory (container) or a file (leaf).
 * Directories have children, files have the original ChangedFile data.
 */
export interface TreeNode {
	type: "file" | "directory"
	name: string
	path: string // Full path for directories, file path for files
	depth: number
	file?: ChangedFile // Only for file nodes
	children?: TreeNode[] // Only for directory nodes (sorted)
}

/**
 * Flattened tree node for rendering
 *
 * Includes all info needed to render a single line in tree mode,
 * whether it's a directory or file, and whether it's expanded.
 */
export interface FlatTreeNode {
	type: "file" | "directory"
	name: string
	path: string
	depth: number
	file?: ChangedFile
	isExpanded?: boolean // Only for directories
	hasChildren?: boolean // Only for directories
}
