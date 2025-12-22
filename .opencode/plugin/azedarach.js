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

/**
 * Extract beadId from tmux session name
 *
 * Azedarach creates tmux sessions with the pattern:
 * - opencode-{beadId} (e.g., opencode-az-123)
 *
 * @returns {string | null} beadId if in an Azedarach-managed session, null otherwise
 */
const getBeadIdFromTmuxSession = () => {
	try {
		const sessionName = execSync("tmux display-message -p '#S'", {
			encoding: "utf-8",
			timeout: 1000,
		}).trim()

		if (sessionName.startsWith("opencode-")) {
			return sessionName.replace("opencode-", "")
		}

		if (sessionName.startsWith("claude-")) {
			return sessionName.replace("claude-", "")
		}

		return null
	} catch {
		return null
	}
}

/**
 * Get the path to az-notify.sh
 *
 * @returns {string | null}
 */
const getAzNotifyPath = () => {
	try {
		// Try project bin directory first
		const pluginDir = import.meta.dirname
		const projectRoot = pluginDir.replace("/.opencode/plugin", "")
		const azNotifyPath = `${projectRoot}/bin/az-notify.sh`

		execSync(`test -x "${azNotifyPath}"`, { timeout: 100 })
		return azNotifyPath
	} catch {
		try {
			return execSync("which az", { encoding: "utf-8", timeout: 100 }).trim()
		} catch {
			return null
		}
	}
}

/**
 * Send a notification to Azedarach via az notify
 *
 * @param {string} event
 * @param {string} beadId
 * @param {string | null} azNotifyPath
 */
const notify = (event, beadId, azNotifyPath) => {
	if (!azNotifyPath) return

	try {
		execSync(`"${azNotifyPath}" ${event} ${beadId}`, {
			timeout: 2000,
			stdio: "ignore",
		})
	} catch (error) {
		console.error(`[azedarach] Failed to notify: ${error}`)
	}
}

/**
 * Azedarach Plugin for OpenCode
 *
 * @type {import("@opencode/plugin").Plugin}
 */
// biome-ignore lint/suspicious/useAwait: OpenCode plugin API requires async
export const Azedarach = async ({ $: _$, project: _project, directory: _directory }) => {
	const beadId = getBeadIdFromTmuxSession()
	if (!beadId) {
		console.log("[azedarach] Not in an Azedarach tmux session, skipping status monitoring")
		return {}
	}

	const azNotifyPath = getAzNotifyPath()
	if (!azNotifyPath) {
		console.warn("[azedarach] Could not find az-notify.sh, status monitoring disabled")
		return {}
	}

	console.log(`[azedarach] Session monitoring enabled for bead: ${beadId}`)
	console.log(`[azedarach] Using notify script: ${azNotifyPath}`)

	// Initial busy status
	notify("user_prompt", beadId, azNotifyPath)

	return {
		// biome-ignore lint/suspicious/useAwait: OpenCode plugin API requires async
		event: async (event) => {
			switch (event.type) {
				case "session.created":
					notify("user_prompt", beadId, azNotifyPath)
					break
				case "session.idle":
					notify("idle_prompt", beadId, azNotifyPath)
					break
				case "session.error":
					notify("stop", beadId, azNotifyPath)
					break
				case "session.deleted":
					notify("session_end", beadId, azNotifyPath)
					break
			}
		},

		// biome-ignore lint/suspicious/useAwait: OpenCode plugin API requires async
		"tool.execute.before": async () => {
			notify("pretooluse", beadId, azNotifyPath)
		},

		// biome-ignore lint/suspicious/useAwait: OpenCode plugin API requires async
		"chat.message": async () => {
			notify("user_prompt", beadId, azNotifyPath)
		},
	}
}

export default Azedarach
