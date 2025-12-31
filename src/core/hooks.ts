/**
 * Hook Configuration for Azedarach Session State Detection
 *
 * Generates and manages Claude Code hook configuration that enables
 * session state detection via `az notify` commands.
 *
 * Also provides pure helper functions for merging settings.local.json
 * (the Effect-based merge is in WorktreeManager which has the services).
 */

/**
 * Compute the absolute path to the project bin directory at module load time.
 *
 * Uses standard URL APIs (not node:path/url) to parse import.meta.url
 * and navigate up to the project root.
 *
 * Path structure: src/core/hooks.ts → src/core → src → projectRoot → bin/
 */
const computeBinPath = (): string => {
	// import.meta.url gives us file:///path/to/src/core/hooks.ts
	const url = new URL(import.meta.url)
	// URL.pathname gives /path/to/src/core/hooks.ts (or /C:/... on Windows)
	const pathParts = url.pathname.split("/")
	// Remove: hooks.ts (or hooks.js if compiled)
	pathParts.pop()
	// Remove: core
	pathParts.pop()
	// Remove: src
	pathParts.pop()
	// Now at project root, add bin/
	return `${pathParts.join("/")}/bin`
}

/** Cached absolute path to the bin directory */
const BIN_PATH = computeBinPath()

/** Cached absolute path to the az CLI script */
const AZ_BINARY_PATH = `${BIN_PATH}/az.ts`

/** Cached absolute path to the fast notify shell script */
const AZ_NOTIFY_PATH = `${BIN_PATH}/az-notify.sh`

/**
 * Get the absolute path to the az CLI script
 *
 * Returns the pre-computed path to bin/az.ts.
 * This ensures hooks work even when az isn't in PATH.
 */
export const getAzBinaryPath = (): string => AZ_BINARY_PATH

/**
 * Get the absolute path to the fast notify shell script
 *
 * Returns the pre-computed path to bin/az-notify.sh.
 * This script is ~100x faster than the full CLI because it
 * directly calls tmux without TypeScript compilation overhead.
 */
export const getAzNotifyPath = (): string => AZ_NOTIFY_PATH

/**
 * Build the az notify command with proper path handling
 *
 * Uses the lightweight shell script (az-notify.sh) instead of the full
 * TypeScript CLI for maximum speed. The shell script directly calls tmux
 * without any compilation overhead (~10ms vs ~600ms).
 *
 * @param event - Hook event type
 * @param beadId - Bead ID for the session
 * @param azNotifyPath - Optional absolute path to az-notify.sh (auto-detected if not provided)
 */
const buildNotifyCommand = (event: string, beadId: string, azNotifyPath?: string): string => {
	const notifyPath = azNotifyPath ?? getAzNotifyPath()
	// Use the shell script directly - no bun/node overhead
	return `"${notifyPath}" ${event} ${beadId}`
}

/**
 * Permissions to auto-grant in spawned worktree sessions
 *
 * These permissions are injected into settings.local.json so Claude sessions
 * can work smoothly without manual approval prompts for common operations.
 */
export const WORKTREE_PERMISSIONS = {
	permissions: {
		allow: [
			// View images attached to beads
			"Read(//**/.beads/images/**)",
			// Use beads CLI for issue management
			"Bash(bd:*)",
			// Use az CLI for session control (dev server, notify, etc.)
			"Bash(az:*)",
		],
	},
}

/**
 * Generate Claude Code hook configuration for session state detection
 *
 * Creates hooks that call `az-notify.sh` when Claude enters specific states.
 * This enables authoritative state detection from Claude's native hook system.
 *
 * Also injects essential permissions for:
 * - Viewing bead-attached images (.beads/images/**)
 * - Using the beads CLI (bd:*)
 * - Using the az CLI (az:*)
 *
 * Hook events:
 * - UserPromptSubmit - User sends a prompt (busy detection)
 * - PreToolUse - Claude is about to use a tool (busy detection)
 * - Notification (idle_prompt) - Claude is waiting for user input at the prompt
 * - PermissionRequest - Claude is waiting for permission approval
 * - Stop - Claude session stops (Ctrl+C, completion, etc.)
 * - SessionEnd - Claude session fully ends
 *
 * @param beadId - The bead ID to associate with this session
 * @param azNotifyPath - Optional absolute path to az-notify.sh (auto-detected if not provided)
 * @returns Hook and permission configuration object to merge into settings.local.json
 */
export const generateHookConfig = (beadId: string, azNotifyPath?: string) => ({
	...WORKTREE_PERMISSIONS,
	hooks: {
		UserPromptSubmit: [
			{
				// Fires immediately when user sends a prompt - instant "busy" detection
				// This is the earliest possible signal that Claude is working
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("user_prompt", beadId, azNotifyPath),
					},
				],
			},
		],
		PreToolUse: [
			{
				// Fires BEFORE permission check when Claude attempts tool use
				// Reinforces "busy" state during tool execution
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("pretooluse", beadId, azNotifyPath),
					},
				],
			},
		],
		Notification: [
			{
				matcher: "idle_prompt",
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("idle_prompt", beadId, azNotifyPath),
					},
				],
			},
		],
		PermissionRequest: [
			{
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("permission_request", beadId, azNotifyPath),
					},
				],
			},
		],
		Stop: [
			{
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("stop", beadId, azNotifyPath),
					},
				],
			},
		],
		SessionEnd: [
			{
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("session_end", beadId, azNotifyPath),
					},
				],
			},
		],
	},
})

/**
 * Type guard to check if value is a plain object (not array)
 */
const isPlainObject = (value: unknown): value is Record<string, unknown> =>
	value !== null && typeof value === "object" && !Array.isArray(value)

/**
 * Deep merge two objects (for merging hook configs with existing settings)
 *
 * Arrays are concatenated rather than replaced to preserve both
 * existing hooks and new hooks.
 */
export const deepMerge = (
	target: Record<string, unknown>,
	source: Record<string, unknown>,
): Record<string, unknown> => {
	const result = { ...target }

	for (const key of Object.keys(source)) {
		const sourceValue = source[key]
		const targetValue = target[key]

		if (isPlainObject(sourceValue) && isPlainObject(targetValue)) {
			// Both are objects - recursively merge
			result[key] = deepMerge(targetValue, sourceValue)
		} else if (Array.isArray(sourceValue) && Array.isArray(targetValue)) {
			// Both are arrays - concatenate
			result[key] = [...targetValue, ...sourceValue]
		} else {
			// Otherwise, source wins
			result[key] = sourceValue
		}
	}

	return result
}

/**
 * Deduplicate an array by JSON stringification
 *
 * Works for arrays of primitives or objects - uses full equality via JSON.stringify.
 * Preserves order, keeping the first occurrence of each unique value.
 */
const deduplicateArray = (arr: unknown[]): unknown[] => {
	const seen = new Set<string>()
	return arr.filter((item) => {
		const key = JSON.stringify(item)
		if (seen.has(key)) return false
		seen.add(key)
		return true
	})
}

/**
 * Deep merge with deduplication (for merging permission settings)
 *
 * Like deepMerge, but deduplicates arrays instead of just concatenating.
 * This prevents duplicate entries in allowedTools, trustedPaths, etc.
 */
export const deepMergeWithDedup = (
	target: Record<string, unknown>,
	source: Record<string, unknown>,
): Record<string, unknown> => {
	const result = { ...target }

	for (const key of Object.keys(source)) {
		const sourceValue = source[key]
		const targetValue = target[key]

		if (isPlainObject(sourceValue) && isPlainObject(targetValue)) {
			// Both are objects - recursively merge
			result[key] = deepMergeWithDedup(targetValue, sourceValue)
		} else if (Array.isArray(sourceValue) && Array.isArray(targetValue)) {
			// Both are arrays - concatenate and deduplicate
			result[key] = deduplicateArray([...targetValue, ...sourceValue])
		} else if (Array.isArray(sourceValue)) {
			// Source is array, target is not - deduplicate source
			result[key] = deduplicateArray(sourceValue)
		} else {
			// Otherwise, source wins
			result[key] = sourceValue
		}
	}

	return result
}

/**
 * Keys to exclude when merging settings from worktree to main
 *
 * These are bead-specific configurations that should not be copied
 * from the worktree to the main settings.
 */
const EXCLUDED_KEYS = new Set(["hooks"])

/**
 * Extract permission-related settings from a settings object
 *
 * Filters out excluded keys (like hooks) that are bead-specific
 * and shouldn't be merged back to main.
 */
export const extractMergeableSettings = (
	settings: Record<string, unknown>,
): Record<string, unknown> => {
	const result: Record<string, unknown> = {}
	for (const key of Object.keys(settings)) {
		if (!EXCLUDED_KEYS.has(key)) {
			result[key] = settings[key]
		}
	}
	return result
}

/**
 * Generate worktree-specific skill content with bead ID context
 *
 * This skill is injected into worktrees so Claude sessions know their
 * bead ID and how to use the az CLI without having to discover it.
 *
 * @param beadId - The bead ID for this worktree session
 * @returns Markdown skill content
 */
export const generateWorktreeSkill = (beadId: string): string => `# Azedarach Worktree Context

**This is an Azedarach-managed worktree session.**

## Your Session

- **Bead ID:** \`${beadId}\`
- **Branch:** \`${beadId}\`

## Dev Server Commands

Control dev servers without breaking TUI state tracking:

\`\`\`bash
# Start the dev server
az dev start ${beadId}

# Stop the dev server
az dev stop ${beadId}

# Restart after config changes
az dev restart ${beadId}

# Check server status
az dev status ${beadId}
\`\`\`

**Why use az CLI?** Direct commands (npm run dev, ctrl-c) break TUI state tracking.
The \`az dev\` commands sync state via tmux metadata.

## Session Lifecycle

1. **You're here** - TUI spawned your session in this worktree
2. **Do your work** - Use \`az dev\` for server control
3. **Sync beads** - Run \`bd sync\` before finishing
4. **Complete** - Clean exit triggers TUI completion workflow (PR creation)

## Quick Reference

| Command | Description |
|---------|-------------|
| \`az dev start ${beadId}\` | Start dev server |
| \`az dev stop ${beadId}\` | Stop dev server |
| \`az dev restart ${beadId}\` | Restart dev server |
| \`az dev status ${beadId}\` | Check server status |
| \`bd sync\` | Sync beads changes |
| \`bd close ${beadId}\` | Mark bead complete |
`
