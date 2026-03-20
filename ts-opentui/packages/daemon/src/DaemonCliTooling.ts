import { getIssueSessionName } from "./DaemonSessionNames.js"

const AZ_NOTIFY_PATH = decodeURIComponent(
	new URL("../../../../bin/az-notify.sh", import.meta.url).pathname,
)
const AZ_PRE_COMPACT_PATH = decodeURIComponent(
	new URL("../../../../bin/az-pre-compact.sh", import.meta.url).pathname,
)

export type CliToolName = "claude" | "opencode" | "codex"
export type HookStrategy = "hooks+pty" | "events" | "pty"

export type JsonPrimitive = string | number | boolean | null
export interface JsonObject {
	readonly [key: string]: JsonValue
}
export type JsonArray = ReadonlyArray<JsonValue>
export type JsonValue = JsonPrimitive | JsonObject | JsonArray

export interface BuildCommandOptions {
	readonly initialPrompt?: string
	readonly imagePaths?: readonly string[]
	readonly issueId?: string
	readonly sessionName?: string
	readonly sessionEnv?: Readonly<Record<string, string>>
	readonly model?: string
	readonly dangerouslySkipPermissions?: boolean
	readonly sessionSettings?: Readonly<Record<string, JsonValue>>
	readonly continueConversation?: boolean
}

export interface CliToolDefinition {
	readonly name: CliToolName
	readonly executable: string
	readonly hookConfigDir: ".claude" | ".opencode" | ".codex"
	readonly sessionNamePrefix: "claude" | "opencode" | "codex"
	readonly hookStrategy: HookStrategy
	readonly buildCommand: (options: BuildCommandOptions) => string
	readonly getInitCommands: () => readonly string[]
}

export interface HookCommandDefinition {
	readonly type: "command"
	readonly command: string
}

export interface HookEntry {
	readonly matcher?: string
	readonly hooks: ReadonlyArray<HookCommandDefinition>
}

export interface SessionHookConfig {
	readonly permissions: {
		readonly allow: readonly string[]
	}
	readonly hooks: Readonly<Record<string, ReadonlyArray<HookEntry>>>
}

export interface HookConfigOptions {
	readonly preCompactEnabled?: boolean
	readonly projectPath?: string
	readonly azNotifyPath?: string
	readonly azPreCompactPath?: string
}

const escapeForShellDoubleQuotes = (value: string): string =>
	value
		.replace(/\\/g, "\\\\")
		.replace(/"/g, '\\"')
		.replace(/\$/g, "\\$")
		.replace(/`/g, "\\`")
		.replace(/!/g, "\\!")

const buildCommandEnvironmentPrefix = (options: BuildCommandOptions): string[] => {
	const prefixed: string[] = []

	if (options.issueId) {
		prefixed.push(`AZEDARACH_ISSUE_ID="${escapeForShellDoubleQuotes(options.issueId)}"`)
	}

	if (options.sessionEnv) {
		const entries = Object.entries(options.sessionEnv).filter(
			([key]) => key !== "AZEDARACH_ISSUE_ID",
		)
		entries.sort(([left], [right]) => left.localeCompare(right))
		for (const [key, value] of entries) {
			prefixed.push(`${key}="${escapeForShellDoubleQuotes(value)}"`)
		}
	}

	return prefixed
}

const buildCodexNotifyCommand = (params: {
	readonly event: "user_prompt" | "session_end"
	readonly issueId: string
	readonly sessionName: string
	readonly azNotifyPath: string
}): string => `"${params.azNotifyPath}" ${params.event} "${params.issueId}" "${params.sessionName}"`

const buildCodexConfigOverrideArg = (key: string, command: string): string => {
	const tomlCommand = command.replaceAll("\\", "\\\\").replaceAll('"', '\\"')
	const override = `${key}=[{hooks=[{command="${tomlCommand}"}]}]`
	return `-c "${escapeForShellDoubleQuotes(override)}"`
}

const claudeToolDefinition: CliToolDefinition = {
	name: "claude",
	executable: "claude",
	hookConfigDir: ".claude",
	sessionNamePrefix: "claude",
	hookStrategy: "hooks+pty",
	buildCommand: (options) => {
		const parts: string[] = buildCommandEnvironmentPrefix(options)

		parts.push("claude")

		if (options.continueConversation) {
			parts.push("-c")
		}
		if (options.model) {
			parts.push(`--model ${options.model}`)
		}
		if (options.dangerouslySkipPermissions) {
			parts.push("--dangerously-skip-permissions")
		}
		if (options.sessionSettings && Object.keys(options.sessionSettings).length > 0) {
			parts.push(`--settings '${JSON.stringify(options.sessionSettings)}'`)
		}
		if (options.initialPrompt) {
			parts.push(`"${escapeForShellDoubleQuotes(options.initialPrompt)}"`)
		}

		return parts.join(" ")
	},
	getInitCommands: () => [],
}

const openCodeToolDefinition: CliToolDefinition = {
	name: "opencode",
	executable: "opencode",
	hookConfigDir: ".opencode",
	sessionNamePrefix: "opencode",
	hookStrategy: "events",
	buildCommand: (options) => {
		const parts: string[] = buildCommandEnvironmentPrefix(options)

		parts.push("opencode")

		if (options.model) {
			parts.push(`--model ${options.model}`)
		}
		if (options.initialPrompt) {
			parts.push(`--prompt "${escapeForShellDoubleQuotes(options.initialPrompt)}"`)
		}

		return parts.join(" ")
	},
	getInitCommands: () => [],
}

const codexToolDefinition: CliToolDefinition = {
	name: "codex",
	executable: "codex",
	hookConfigDir: ".codex",
	sessionNamePrefix: "codex",
	hookStrategy: "pty",
	buildCommand: (options) => {
		const parts: string[] = buildCommandEnvironmentPrefix(options)

		parts.push("codex")

		if (options.issueId && options.sessionName) {
			const sessionStartCommand = buildCodexNotifyCommand({
				event: "user_prompt",
				issueId: options.issueId,
				sessionName: options.sessionName,
				azNotifyPath: AZ_NOTIFY_PATH,
			})
			const stopCommand = buildCodexNotifyCommand({
				event: "session_end",
				issueId: options.issueId,
				sessionName: options.sessionName,
				azNotifyPath: AZ_NOTIFY_PATH,
			})
			parts.push(buildCodexConfigOverrideArg("hooks.SessionStart", sessionStartCommand))
			parts.push(buildCodexConfigOverrideArg("hooks.Stop", stopCommand))
		}

		if (options.model) {
			parts.push(`--model ${options.model}`)
		}
		if (options.imagePaths && options.imagePaths.length > 0) {
			for (const imagePath of options.imagePaths) {
				if (imagePath.trim().length === 0) continue
				parts.push(`--image "${escapeForShellDoubleQuotes(imagePath)}"`)
			}
		}
		if (options.dangerouslySkipPermissions) {
			parts.push("--dangerously-bypass-approvals-and-sandbox")
		}
		if (options.continueConversation) {
			parts.push("resume", "--last")
		} else if (options.initialPrompt) {
			parts.push("--", `"${escapeForShellDoubleQuotes(options.initialPrompt)}"`)
		}

		return parts.join(" ")
	},
	getInitCommands: () => [],
}

const toolRegistry: ReadonlyMap<CliToolName, CliToolDefinition> = new Map([
	["claude", claudeToolDefinition],
	["opencode", openCodeToolDefinition],
	["codex", codexToolDefinition],
])

export const getToolDefinition = (name: CliToolName): CliToolDefinition => {
	const tool = toolRegistry.get(name)
	if (!tool) {
		throw new Error(`Unknown CLI tool: ${name}`)
	}
	return tool
}

export const getSupportedTools = (): readonly CliToolName[] => Array.from(toolRegistry.keys())

export const isValidToolName = (name: string): name is CliToolName =>
	toolRegistry.has(name as CliToolName)

export const DEFAULT_CLI_TOOL: CliToolName = "claude"

export const WORKTREE_PERMISSIONS: SessionHookConfig = {
	permissions: {
		allow: ["Read(//**/.azedarach/tmp/attachments/**)", "Bash(tracker:*)", "Bash(az:*)"],
	},
	hooks: {},
}

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

export const generateHookConfig = (
	issueId: string,
	options: HookConfigOptions = {},
): SessionHookConfig => {
	const { preCompactEnabled = true, projectPath, azNotifyPath, azPreCompactPath } = options

	const hooks: Record<string, HookEntry[]> = {
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

const isPlainObject = (value: JsonValue | null): value is JsonObject =>
	value !== null && typeof value === "object" && !Array.isArray(value)

export const deepMerge = (target: JsonObject, source: JsonObject): JsonObject => {
	const result: Record<string, JsonValue> = { ...target }

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
