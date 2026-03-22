import { getIssueSessionName } from "@azedarach/shared/session-names"

const AZ_NOTIFY_PATH = decodeURIComponent(
	new URL("../../../../bin/az-notify.sh", import.meta.url).pathname,
)
const AZ_PRE_COMPACT_PATH = decodeURIComponent(
	new URL("../../../../bin/az-pre-compact.sh", import.meta.url).pathname,
)

const buildNotifyCommand = (
	event: string,
	issueId: string,
	projectPath?: string,
	azNotifyPath?: string,
): string => {
	const notifyPath = azNotifyPath ?? AZ_NOTIFY_PATH
	const sessionName = getIssueSessionName(issueId, projectPath)
	return `"${notifyPath}" ${event} "${issueId}" "${sessionName}"`
}

const buildPreCompactCommand = (issueId: string, azPreCompactPath?: string): string => {
	const preCompactPath = azPreCompactPath ?? AZ_PRE_COMPACT_PATH
	return `"${preCompactPath}" ${issueId}`
}

export interface HookConfigOptions {
	readonly preCompactEnabled?: boolean
	readonly projectPath?: string
	readonly azNotifyPath?: string
	readonly azPreCompactPath?: string
}

export const WORKTREE_PERMISSIONS = {
	permissions: {
		allow: ["Read(//**/.azedarach/tmp/attachments/**)", "Bash(tracker:*)", "Bash(az:*)"],
	},
}

export const generateHookConfig = (issueId: string, options: HookConfigOptions = {}) => {
	const { preCompactEnabled = true, projectPath, azNotifyPath, azPreCompactPath } = options

	const hooks: Record<string, unknown[]> = {
		UserPromptSubmit: [
			{
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

	if (preCompactEnabled) {
		hooks.PreCompact = [
			{
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

const isPlainObject = (value: unknown): value is Record<string, unknown> =>
	value !== null && typeof value === "object" && !Array.isArray(value)

export const deepMerge = (
	target: Record<string, unknown>,
	source: Record<string, unknown>,
): Record<string, unknown> => {
	const result = { ...target }

	for (const key of Object.keys(source)) {
		const sourceValue = source[key]
		const targetValue = target[key]

		if (isPlainObject(sourceValue) && isPlainObject(targetValue)) {
			result[key] = deepMerge(targetValue, sourceValue)
		} else if (Array.isArray(sourceValue) && Array.isArray(targetValue)) {
			result[key] = [...targetValue, ...sourceValue]
		} else {
			result[key] = sourceValue
		}
	}

	return result
}
