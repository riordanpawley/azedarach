/**
 * File Tree Utilities
 *
 * Functions for building and manipulating file trees from flat file lists.
 * Used by the DiffViewer to display files in a hierarchical tree structure.
 */

import type { ChangedFile, FlatTreeNode, TreeNode } from "./types.js"

/**
 * Build a tree structure from a flat list of changed files
 *
 * Groups files by directory, creating intermediate directory nodes.
 * Directories are sorted alphabetically before files at each level.
 */
export function buildFileTree(files: ChangedFile[]): TreeNode[] {
	// Use a nested Map structure to build the tree
	interface TreeBuildNode {
		node: TreeNode
		children: Map<string, TreeBuildNode>
	}

	const root = new Map<string, TreeBuildNode>()

	for (const file of files) {
		const parts = file.path.split("/")
		let currentPath = ""
		let currentLevel = root

		for (let i = 0; i < parts.length; i++) {
			const part = parts[i]!
			const isFile = i === parts.length - 1
			currentPath += (currentPath ? "/" : "") + part

			if (!currentLevel.has(part)) {
				const node: TreeNode = {
					type: isFile ? "file" : "directory",
					name: part,
					path: currentPath,
					depth: i,
					file: isFile ? file : undefined,
					children: isFile ? undefined : [],
				}
				currentLevel.set(part, { node, children: new Map() })
			}

			if (!isFile) {
				currentLevel = currentLevel.get(part)!.children
			}
		}
	}

	// Convert Map structure to sorted TreeNode array (recursive)
	function mapToSortedArray(map: Map<string, TreeBuildNode>): TreeNode[] {
		const entries = Array.from(map.values())

		// Sort: directories first, then files, alphabetically within each group
		entries.sort((a, b) => {
			if (a.node.type !== b.node.type) {
				return a.node.type === "directory" ? -1 : 1
			}
			return a.node.name.localeCompare(b.node.name)
		})

		return entries.map(({ node, children }) => ({
			...node,
			children: node.type === "directory" ? mapToSortedArray(children) : undefined,
		}))
	}

	return mapToSortedArray(root)
}

/**
 * Flatten a tree into a list of visible nodes based on expansion state
 *
 * Only includes children of expanded directories.
 * Used for rendering the tree in a scrollable list.
 */
export function flattenTree(tree: TreeNode[], expandedDirs: Set<string>): FlatTreeNode[] {
	const result: FlatTreeNode[] = []

	function traverse(nodes: TreeNode[]) {
		for (const node of nodes) {
			const isExpanded = node.type === "directory" && expandedDirs.has(node.path)
			const hasChildren = node.type === "directory" && (node.children?.length ?? 0) > 0

			result.push({
				type: node.type,
				name: node.name,
				path: node.path,
				depth: node.depth,
				file: node.file,
				isExpanded: node.type === "directory" ? isExpanded : undefined,
				hasChildren: node.type === "directory" ? hasChildren : undefined,
			})

			// Only recurse into expanded directories
			if (isExpanded && node.children) {
				traverse(node.children)
			}
		}
	}

	traverse(tree)
	return result
}

/**
 * Get all directory paths in a tree (for expand all / collapse all)
 */
export function getAllDirectoryPaths(tree: TreeNode[]): string[] {
	const paths: string[] = []

	function traverse(nodes: TreeNode[]) {
		for (const node of nodes) {
			if (node.type === "directory") {
				paths.push(node.path)
				if (node.children) {
					traverse(node.children)
				}
			}
		}
	}

	traverse(tree)
	return paths
}

/**
 * Find the parent directory path for a given path
 * Returns null if at root level
 */
export function getParentPath(path: string): string | null {
	const lastSlash = path.lastIndexOf("/")
	if (lastSlash === -1) {
		return null
	}
	return path.substring(0, lastSlash)
}

/**
 * Check if a path is a descendant of another path
 */
export function isDescendantOf(childPath: string, parentPath: string): boolean {
	return childPath.startsWith(`${parentPath}/`)
}
