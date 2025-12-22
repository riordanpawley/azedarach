/**
 * Azedarach OpenCode Plugin - Session Status Notifications
 *
 * This plugin bridges OpenCode session events to Azedarach's TmuxSessionMonitor.
 * It calls `az notify` when session state changes, which sets tmux session options
 * that Azedarach polls to detect state.
 *
 * Event Mapping:
 * - session.created → busy (session started working)
 * - session.idle → waiting (awaiting user input)
 * - tool.execute.before → busy (tool execution started)
 * - tool.execute.after → busy (tool completed, may continue)
 * - session.error → waiting (error occurred, needs attention)
 *
 * IMPORTANT: This plugin works alongside opencode-beads which handles:
 * - bd prime on session start
 * - /bd-* slash commands
 * - beads task agent
 *
 * This plugin ONLY handles session status for Azedarach TUI monitoring.
 *
 * @see https://opencode.ai/docs/plugins/
 */

import { execSync } from "node:child_process"
import type { Plugin } from "@opencode/plugin"

/**
 * Extract beadId from tmux session name
 *
 * Azedarach creates tmux sessions with the pattern:
 * - opencode-{beadId} (e.g., opencode-az-123)
 *
 * @returns beadId if in an Azedarach-managed session, null otherwise
 */
const getBeadIdFromTmuxSession = (): string | null => {
	try {
		// Get the current tmux session name
		const sessionName = execSync("tmux display-message -p '#S'", {
			encoding: "utf-8",
			timeout: 1000,
		}).trim()

		// Check if it matches our pattern
		if (sessionName.startsWith("opencode-")) {
			return sessionName.replace("opencode-", "")
		}

		// Could also be a claude session (for testing)
		if (sessionName.startsWith("claude-")) {
			return sessionName.replace("claude-", "")
		}

		return null
	} catch {
		// Not in a tmux session, or tmux command failed
		return null
	}
}

/**
 * Get the path to az-notify.sh
 *
 * The script is located relative to the project where this plugin is installed.
 * We traverse up from the plugin location to find the bin directory.
 */
const getAzNotifyPath = (): string | null => {
	try {
		// Try to find az-notify.sh in the project's bin directory
		// The plugin is in .opencode/plugin/, so we go up to project root
		const pluginDir = import.meta.dirname ?? __dirname
		const projectRoot = pluginDir.replace("/.opencode/plugin", "")
		const azNotifyPath = `${projectRoot}/bin/az-notify.sh`

		// Verify it exists
		execSync(`test -x "${azNotifyPath}"`, { timeout: 100 })
		return azNotifyPath
	} catch {
		// Fall back to looking for az in PATH
		try {
			const azPath = execSync("which az", { encoding: "utf-8", timeout: 100 }).trim()
			return azPath
		} catch {
			return null
		}
	}
}

/**
 * Send a notification to Azedarach via az notify
 *
 * Uses the lightweight shell script for speed (~10ms vs ~600ms for full CLI).
 */
const notify = (event: string, beadId: string, azNotifyPath: string | null): void => {
	if (!azNotifyPath) {
		// Can't notify without az-notify.sh
		return
	}

	try {
		// Use the shell script directly - no bun/node overhead
		execSync(`"${azNotifyPath}" ${event} ${beadId}`, {
			timeout: 2000,
			stdio: "ignore",
		})
	} catch (error) {
		// Silently fail - notifications are best-effort
		console.error(`[azedarach] Failed to notify: ${error}`)
	}
}

/**
 * Azedarach Plugin for OpenCode
 *
 * Provides session status monitoring for Azedarach TUI.
 */
export const Azedarach: Plugin = async ({ $: _$, project: _project, directory: _directory }) => {
	// Check if we're in an Azedarach-managed session
	const beadId = getBeadIdFromTmuxSession()
	if (!beadId) {
		// Not an Azedarach session, disable monitoring
		console.log("[azedarach] Not in an Azedarach tmux session, skipping status monitoring")
		return {}
	}

	// Find the az-notify script
	const azNotifyPath = getAzNotifyPath()
	if (!azNotifyPath) {
		console.warn("[azedarach] Could not find az-notify.sh, status monitoring disabled")
		return {}
	}

	console.log(`[azedarach] Session monitoring enabled for bead: ${beadId}`)
	console.log(`[azedarach] Using notify script: ${azNotifyPath}`)

	// Send initial "busy" status since session just started
	notify("user_prompt", beadId, azNotifyPath)

	return {
		/**
		 * Session events for lifecycle tracking
		 */
		event: async (event) => {
			switch (event.type) {
				case "session.created":
					// Session started - mark as busy
					notify("user_prompt", beadId, azNotifyPath)
					break

				case "session.idle":
					// Session is waiting for user input
					notify("idle_prompt", beadId, azNotifyPath)
					break

				case "session.error":
					// Error occurred - treat as waiting (needs attention)
					notify("stop", beadId, azNotifyPath)
					break

				case "session.deleted":
					// Session ended
					notify("session_end", beadId, azNotifyPath)
					break
			}
		},

		/**
		 * Tool execution hooks for busy state detection
		 */
		"tool.execute.before": async (_event) => {
			// Tool is about to execute - definitely busy
			notify("pretooluse", beadId, azNotifyPath)
		},

		"tool.execute.after": async (_event) => {
			// Tool completed - still busy (more work may follow)
			// Don't notify here - let session.idle handle the transition
		},

		/**
		 * Message hooks for activity tracking
		 */
		"chat.message": async (_event) => {
			// New message in chat - mark as busy
			notify("user_prompt", beadId, azNotifyPath)
		},
	}
}

// Default export for plugin loading
export default Azedarach
