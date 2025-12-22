/**
 * CLI Tool Registry - Abstraction layer for AI coding assistants
 *
 * Provides structured definitions for CLI tools like Claude Code and OpenCode,
 * handling tool-specific command construction, flag mappings, and session naming.
 *
 * This abstraction enables:
 * - Switching between AI coding tools via config
 * - Tool-specific command building with correct flags
 * - Future extensibility for new tools
 */

import { escapeForShellDoubleQuotes } from "./shell.js"

// ============================================================================
// Types
// ============================================================================

/**
 * Supported CLI tool identifiers
 */
export type CliToolName = "claude" | "opencode"

/**
 * Hook strategy for session status detection
 *
 * - "hooks+pty": Claude Code style - uses hooks with PTY pattern matching fallback
 * - "events": OpenCode style - rich plugin events for authoritative status
 */
export type HookStrategy = "hooks+pty" | "events"

/**
 * Options for building a CLI command
 */
export interface BuildCommandOptions {
	/** Initial prompt to send (e.g., "work on bead az-123") */
	readonly initialPrompt?: string
	/** Model to use (tool-specific format) */
	readonly model?: string
	/** Skip permission prompts (Claude: --dangerously-skip-permissions) */
	readonly dangerouslySkipPermissions?: boolean
	/** Session settings object (Claude: --settings) */
	readonly sessionSettings?: Record<string, unknown>
}

/**
 * Definition of a CLI tool's capabilities and command structure
 */
export interface CliToolDefinition {
	/** Tool identifier */
	readonly name: CliToolName

	/** Executable name (e.g., "claude", "opencode") */
	readonly executable: string

	/** Config directory for tool-specific hooks/plugins */
	readonly hookConfigDir: ".claude" | ".opencode"

	/** Session name prefix for tmux sessions */
	readonly sessionNamePrefix: "claude" | "opencode"

	/** How this tool handles session status detection */
	readonly hookStrategy: HookStrategy

	/**
	 * Build the complete command string for this tool
	 *
	 * @param options - Command options (prompt, model, etc.)
	 * @returns Complete command string to execute
	 */
	readonly buildCommand: (options: BuildCommandOptions) => string

	/**
	 * Get tool-specific init commands to inject at session start
	 *
	 * For tools without native hooks (like OpenCode), this returns
	 * commands that replicate hook behavior (e.g., "bd prime").
	 */
	readonly getInitCommands: () => readonly string[]
}

// ============================================================================
// Tool Definitions
// ============================================================================

/**
 * Claude Code CLI tool definition
 *
 * Claude Code uses:
 * - Positional argument for initial prompt
 * - --model for model selection (short names: haiku, sonnet, opus)
 * - --dangerously-skip-permissions for auto-approve
 * - --settings for session configuration
 * - Native hooks in .claude/settings.json
 */
const claudeToolDefinition: CliToolDefinition = {
	name: "claude",
	executable: "claude",
	hookConfigDir: ".claude",
	sessionNamePrefix: "claude",
	hookStrategy: "hooks+pty",

	buildCommand: (options) => {
		const parts: string[] = ["claude"]

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

	// Claude Code has native hooks, no extra init commands needed
	getInitCommands: () => [],
}

/**
 * OpenCode CLI tool definition
 *
 * OpenCode uses:
 * - --prompt flag for initial prompt (not positional)
 * - --model for model selection (provider/model format: anthropic/claude-sonnet)
 * - No equivalent to --dangerously-skip-permissions (non-interactive auto-approves)
 * - Plugin system in .opencode/plugin/ for hooks
 */
const openCodeToolDefinition: CliToolDefinition = {
	name: "opencode",
	executable: "opencode",
	hookConfigDir: ".opencode",
	sessionNamePrefix: "opencode",
	hookStrategy: "events",

	buildCommand: (options) => {
		const parts: string[] = ["opencode"]

		if (options.model) {
			parts.push(`--model ${options.model}`)
		}

		// OpenCode doesn't have --dangerously-skip-permissions
		// Non-interactive mode auto-approves by default

		// OpenCode doesn't have --settings equivalent
		// Configuration is done via opencode.json

		if (options.initialPrompt) {
			parts.push(`--prompt "${escapeForShellDoubleQuotes(options.initialPrompt)}"`)
		}

		return parts.join(" ")
	},

	getInitCommands: () => [],
}

// ============================================================================
// Registry
// ============================================================================

/**
 * Registry of all supported CLI tools
 */
const toolRegistry: ReadonlyMap<CliToolName, CliToolDefinition> = new Map([
	["claude", claudeToolDefinition],
	["opencode", openCodeToolDefinition],
])

/**
 * Get the tool definition for a given tool name
 *
 * @param name - Tool identifier ("claude" or "opencode")
 * @returns Tool definition
 * @throws Error if tool is not found (should not happen with typed CliToolName)
 */
export const getToolDefinition = (name: CliToolName): CliToolDefinition => {
	const tool = toolRegistry.get(name)
	if (!tool) {
		throw new Error(`Unknown CLI tool: ${name}`)
	}
	return tool
}

/**
 * Get all supported tool names
 */
export const getSupportedTools = (): readonly CliToolName[] => Array.from(toolRegistry.keys())

/**
 * Check if a tool name is valid
 */
export const isValidToolName = (name: string): name is CliToolName =>
	toolRegistry.has(name as CliToolName)

/**
 * Default CLI tool (for backwards compatibility)
 */
export const DEFAULT_CLI_TOOL: CliToolName = "claude"
