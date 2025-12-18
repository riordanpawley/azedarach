/**
 * Hook Configuration for Azedarach Session State Detection
 *
 * Generates and manages Claude Code hook configuration that enables
 * session state detection via `az notify` commands.
 */

/**
 * Compute the absolute path to bin/az.ts at module load time.
 *
 * Uses standard URL APIs (not node:path/url) to parse import.meta.url
 * and navigate up to the project root.
 *
 * Path structure: src/core/hooks.ts → src/core → src → projectRoot → bin/az.ts
 */
const computeAzBinaryPath = (): string => {
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
	// Now at project root, add bin/az.ts
	return `${pathParts.join("/")}/bin/az.ts`
}

/** Cached absolute path to the az CLI script */
const AZ_BINARY_PATH = computeAzBinaryPath()

/**
 * Get the absolute path to the az CLI script
 *
 * Returns the pre-computed path to bin/az.ts.
 * This ensures hooks work even when az isn't in PATH.
 */
export const getAzBinaryPath = (): string => AZ_BINARY_PATH

/**
 * Build the az notify command with proper path handling
 *
 * Uses bun with absolute path to ensure the command works in /bin/sh
 * which doesn't have the user's PATH configured.
 *
 * @param event - Hook event type
 * @param beadId - Bead ID for the session
 * @param azBinaryPath - Optional absolute path to az binary (auto-detected if not provided)
 */
const buildNotifyCommand = (event: string, beadId: string, azBinaryPath?: string): string => {
	const azPath = azBinaryPath ?? getAzBinaryPath()
	// Use bun to run the TypeScript CLI directly
	return `bun run "${azPath}" notify ${event} ${beadId}`
}

/**
 * Generate Claude Code hook configuration for session state detection
 *
 * Creates hooks that call `az notify` when Claude enters specific states.
 * This enables authoritative state detection from Claude's native hook system.
 *
 * Hook events:
 * - PreToolUse - Claude is about to use a tool (busy detection)
 * - Notification (idle_prompt) - Claude is waiting for user input at the prompt
 * - PermissionRequest - Claude is waiting for permission approval
 * - Stop - Claude session stops (Ctrl+C, completion, etc.)
 * - SessionEnd - Claude session fully ends
 *
 * @param beadId - The bead ID to associate with this session
 * @param azBinaryPath - Optional absolute path to az binary (auto-detected if not provided)
 * @returns Hook configuration object to merge into settings.local.json
 */
export const generateHookConfig = (beadId: string, azBinaryPath?: string) => ({
	hooks: {
		PreToolUse: [
			{
				// Fires BEFORE permission check - immediate "busy" detection
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("pretooluse", beadId, azBinaryPath),
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
						command: buildNotifyCommand("idle_prompt", beadId, azBinaryPath),
					},
				],
			},
		],
		PermissionRequest: [
			{
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("permission_request", beadId, azBinaryPath),
					},
				],
			},
		],
		Stop: [
			{
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("stop", beadId, azBinaryPath),
					},
				],
			},
		],
		SessionEnd: [
			{
				hooks: [
					{
						type: "command",
						command: buildNotifyCommand("session_end", beadId, azBinaryPath),
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
