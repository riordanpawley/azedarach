/**
 * Hook Configuration for Azedarach Session State Detection
 *
 * Generates and manages Claude Code hook configuration that enables
 * session state detection via `az notify` commands.
 */

/**
 * Generate Claude Code hook configuration for session state detection
 *
 * Creates hooks that call `az notify` when Claude enters specific states.
 * This enables authoritative state detection from Claude's native hook system.
 *
 * Hook events:
 * - Notification (idle_prompt) - Claude is waiting for user input at the prompt
 * - PermissionRequest - Claude is waiting for permission approval
 * - Stop - Claude session stops (Ctrl+C, completion, etc.)
 * - SessionEnd - Claude session fully ends
 *
 * @param beadId - The bead ID to associate with this session
 * @returns Hook configuration object to merge into settings.local.json
 */
export const generateHookConfig = (beadId: string) => ({
	hooks: {
		Notification: [
			{
				matcher: "idle_prompt",
				hooks: [
					{
						type: "command",
						command: `az notify idle_prompt ${beadId}`,
					},
				],
			},
		],
		PermissionRequest: [
			{
				hooks: [
					{
						type: "command",
						command: `az notify permission_request ${beadId}`,
					},
				],
			},
		],
		Stop: [
			{
				hooks: [
					{
						type: "command",
						command: `az notify stop ${beadId}`,
					},
				],
			},
		],
		SessionEnd: [
			{
				hooks: [
					{
						type: "command",
						command: `az notify session_end ${beadId}`,
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
