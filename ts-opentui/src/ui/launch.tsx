/**
 * TUI launcher - initializes OpenTUI and renders the app
 */
import { createCliRenderer } from "@opentui/core"
import { createRoot } from "@opentui/react"
import { killActivePopup } from "../core/IssueEditorService.js"
import { AZ_SESSION_NAME } from "../lib/tmux-wrap.js"
import { App } from "./App.js"
import { truncateAzLogOnStartup } from "./logMaintenance.js"

const AZ_RETURN_KEY = process.env.AZ_RETURN_KEY?.trim() || "g"

/**
 * Register a global tmux keybinding to return to the az session.
 * Binds Ctrl-a g (by default) to switch back to the main az TUI session.
 * This works from any Claude session spawned by az.
 */
async function registerReturnBinding(): Promise<void> {
	// Only register if we're inside tmux
	if (!process.env.TMUX) return

	try {
		// Preserve native double-prefix behavior for users who rely on send-prefix.
		const prefixProc = Bun.spawn(["tmux", "bind-key", "C-a", "send-prefix"], {
			stdout: "ignore",
			stderr: "ignore",
		})
		await prefixProc.exited

		// Bind a dedicated key for "return to board" navigation.
		const proc = Bun.spawn(
			["tmux", "bind-key", AZ_RETURN_KEY, "switch-client", "-t", AZ_SESSION_NAME],
			{
				stdout: "ignore",
				stderr: "ignore",
			},
		)
		await proc.exited
	} catch {
		// Silently ignore - binding is nice-to-have, not critical
	}
}

/**
 * Launch the TUI application
 *
 * Initializes the OpenTUI renderer and starts the application.
 * Uses React's createRoot pattern for rendering.
 */
export async function launchTUI(): Promise<void> {
	await truncateAzLogOnStartup()

	// Register SIGINT handler to clean up any active tmux popup
	// This prevents orphaned popups when user presses Ctrl-C during editor operations
	process.on("SIGINT", () => {
		killActivePopup()
		process.exit(0)
	})

	// Register return-to-board tmux keybinding (fire-and-forget)
	registerReturnBinding()

	const renderer = await createCliRenderer({
		useMouse: true,
	})
	createRoot(renderer).render(<App />)
}
