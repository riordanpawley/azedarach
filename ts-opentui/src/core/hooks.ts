/**
 * Hook Configuration for Azedarach Session State Detection
 *
 * Generates and manages Claude Code hook configuration that enables
 * session state detection via `az notify` commands.
 *
 * Also provides pure helper functions for merging settings.local.json
 * (the Effect-based merge is in WorktreeManager which has the services).
 */

import { getIssueSessionName } from "./paths.js"

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

/** Cached absolute path to the az CLI binary */
const AZ_BINARY_PATH = `${BIN_PATH}/az`

/** Cached absolute path to the fast notify shell script */
const AZ_NOTIFY_PATH = `${BIN_PATH}/az-notify.sh`

/** Cached absolute path to the pre-compact hook script */
const AZ_PRE_COMPACT_PATH = `${BIN_PATH}/az-pre-compact.sh`

/**
 * Get the absolute path to the az CLI binary
 *
 * Returns the pre-computed path to bin/az.
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
 * Get the absolute path to the pre-compact hook script
 *
 * Returns the pre-computed path to bin/az-pre-compact.sh.
 * This script outputs a reminder for Claude to update tracker
 * before context compaction.
 */
export const getAzPreCompactPath = (): string => AZ_PRE_COMPACT_PATH

/**
 * Build the az notify command with proper path handling
 *
 * Uses the lightweight shell script (az-notify.sh) instead of the full
 * TypeScript CLI for maximum speed. The shell script directly calls tmux
 * without any compilation overhead (~10ms vs ~600ms).
 *
 * @param event - Hook event type
 * @param issueId - Bead ID for the session
 * @param projectPath - Optional project path for project-prefixed session IDs
 * @param azNotifyPath - Optional absolute path to az-notify.sh (auto-detected if not provided)
 */
const buildNotifyCommand = (
	event: string,
	issueId: string,
	projectPath?: string,
	azNotifyPath?: string,
): string => {
	const notifyPath = azNotifyPath ?? getAzNotifyPath()
	const sessionName = getIssueSessionName(issueId, projectPath)
	// Use the shell script directly - no bun/node overhead
	return `"${notifyPath}" ${event} "${issueId}" "${sessionName}"`
}

/**
 * Build the pre-compact command with proper path handling
 *
 * @param issueId - Bead ID for the session
 * @param azPreCompactPath - Optional absolute path to az-pre-compact.sh (auto-detected if not provided)
 */
const buildPreCompactCommand = (issueId: string, azPreCompactPath?: string): string => {
	const preCompactPath = azPreCompactPath ?? getAzPreCompactPath()
	return `"${preCompactPath}" ${issueId}`
}

/**
 * Options for hook configuration generation
 */
export interface HookConfigOptions {
	/** Whether to include the PreCompact hook (default: true) */
	preCompactEnabled?: boolean
	/** Optional project path for project-prefixed session IDs */
	projectPath?: string
	/** Optional absolute path to az-notify.sh (auto-detected if not provided) */
	azNotifyPath?: string
	/** Optional absolute path to az-pre-compact.sh (auto-detected if not provided) */
	azPreCompactPath?: string
}

const CODEX_HOOK_BLOCK_START = "# >>> azedarach-codex-hooks"
const CODEX_HOOK_BLOCK_END = "# <<< azedarach-codex-hooks"

/**
 * Generate a managed TOML block for Codex SessionStart/Stop hooks.
 */
export const generateCodexSessionHookTomlBlock = (
	issueId: string,
	options: {
		readonly projectPath?: string
		readonly azNotifyPath?: string
	} = {},
): string => {
	const { projectPath, azNotifyPath } = options
	const sessionStartCommand = buildNotifyCommand("user_prompt", issueId, projectPath, azNotifyPath)
	const stopCommand = buildNotifyCommand("session_end", issueId, projectPath, azNotifyPath)

	const tomlEscape = (value: string): string =>
		value.replaceAll("\\", "\\\\").replaceAll('"', '\\"')

	const sessionStartEscaped = tomlEscape(sessionStartCommand)
	const stopEscaped = tomlEscape(stopCommand)

	return `${CODEX_HOOK_BLOCK_START}
[[hooks.SessionStart]]
hooks = [{ command = "${sessionStartEscaped}" }]

[[hooks.Stop]]
hooks = [{ command = "${stopEscaped}" }]
${CODEX_HOOK_BLOCK_END}
`
}

/**
 * Merge (or replace) the managed Azedarach Codex hook block in a config TOML string.
 */
export const mergeCodexSessionHooksIntoConfig = (
	existingConfig: string,
	hookBlock: string,
): string => {
	const startIndex = existingConfig.indexOf(CODEX_HOOK_BLOCK_START)
	const endIndex = existingConfig.indexOf(CODEX_HOOK_BLOCK_END)

	if (startIndex >= 0 && endIndex >= startIndex) {
		const endWithMarker = endIndex + CODEX_HOOK_BLOCK_END.length
		const before = existingConfig.slice(0, startIndex).replace(/\s*$/, "")
		const after = existingConfig.slice(endWithMarker).replace(/^\s*/, "")
		const mergedMiddle = [before, hookBlock.trimEnd(), after]
			.filter((part) => part.length > 0)
			.join("\n\n")
		return `${mergedMiddle}\n`
	}

	const trimmed = existingConfig.trimEnd()
	if (trimmed.length === 0) {
		return `${hookBlock.trimEnd()}\n`
	}

	return `${trimmed}\n\n${hookBlock.trimEnd()}\n`
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
			// View materialized issue attachment images
			"Read(//**/.azedarach/tmp/attachments/**)",
			// Use tracker CLI for issue management
			"Bash(tracker:*)",
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
 * - Viewing issue-attached images (.azedarach/tmp/attachments/**)
 * - Using the tracker CLI (tracker:*)
 * - Using the az CLI (az:*)
 *
 * Hook events:
 * - UserPromptSubmit - User sends a prompt (busy detection)
 * - PreToolUse - Claude is about to use a tool (busy detection)
 * - PreCompact - Before context compaction (optional, enabled by default)
 * - Notification (idle_prompt) - Claude is waiting for user input at the prompt
 * - PermissionRequest - Claude is waiting for permission approval
 * - Stop - Claude session stops (Ctrl+C, completion, etc.)
 * - SessionEnd - Claude session fully ends
 *
 * @param issueId - The bead ID to associate with this session
 * @param options - Optional configuration for hook generation
 * @returns Hook and permission configuration object to merge into settings.local.json
 */
export const generateHookConfig = (issueId: string, options: HookConfigOptions = {}) => {
	const { preCompactEnabled = true, projectPath, azNotifyPath, azPreCompactPath } = options

	// Build the hooks object with required hooks
	const hooks: Record<string, unknown[]> = {
		UserPromptSubmit: [
			{
				// Fires immediately when user sends a prompt - instant "busy" detection
				// This is the earliest possible signal that Claude is working
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("user_prompt", issueId, projectPath, azNotifyPath),
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
						command: buildNotifyCommand("pretooluse", issueId, projectPath, azNotifyPath),
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
						command: buildNotifyCommand("idle_prompt", issueId, projectPath, azNotifyPath),
					},
				],
			},
		],
		PermissionRequest: [
			{
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("permission_request", issueId, projectPath, azNotifyPath),
					},
				],
			},
		],
		Stop: [
			{
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("stop", issueId, projectPath, azNotifyPath),
					},
				],
			},
		],
		SessionEnd: [
			{
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("session_end", issueId, projectPath, azNotifyPath),
					},
				],
			},
		],
	}

	// Add PreCompact hook if enabled
	if (preCompactEnabled) {
		hooks.PreCompact = [
			{
				// Fires before context compaction - outputs reminder to update tracker
				// This ensures session progress is preserved before context is lost
				hooks: [
					{
						type: "command",
						command: buildPreCompactCommand(issueId, azPreCompactPath),
					},
				],
			},
		]
	}

	return {
		...WORKTREE_PERMISSIONS,
		hooks,
	}
}

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
 * @param issueId - The bead ID for this worktree session
 * @returns Markdown skill content
 */
export const generateWorktreeSkill = (issueId: string): string => `# Azedarach Worktree Context

**This is an Azedarach-managed worktree session.**

## Your Session

- **Bead ID:** \`${issueId}\`
- **Branch:** \`${issueId}\`

## Dev Server Commands

Control dev servers without breaking TUI state tracking:

\`\`\`bash
# Start the dev server
az dev start ${issueId}

# Stop the dev server
az dev stop ${issueId}

# Restart after config changes
az dev restart ${issueId}

# Check server status
az dev status ${issueId}
\`\`\`

**Why use az CLI?** Direct commands (npm run dev, ctrl-c) break TUI state tracking.
The \`az dev\` commands sync state via tmux metadata.

## Session Lifecycle

1. **You're here** - TUI spawned your session in this worktree
2. **Do your work** - Use \`az dev\` for server control
3. **Sync tracker** - Run \`tracker sync\` before finishing
4. **Complete** - Clean exit triggers TUI completion workflow (PR creation)

## Quick Reference

| Command | Description |
|---------|-------------|
| \`az dev start ${issueId}\` | Start dev server |
| \`az dev stop ${issueId}\` | Stop dev server |
| \`az dev restart ${issueId}\` | Restart dev server |
| \`az dev status ${issueId}\` | Check server status |
| \`tracker sync\` | Sync tracker changes |
| \`tracker close ${issueId}\` | Mark bead complete |
`
