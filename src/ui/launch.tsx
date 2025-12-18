/**
 * TUI launcher - initializes OpenTUI and renders the app
 */
import { createCliRenderer } from "@opentui/core"
import { createRoot } from "@opentui/react"
import { killActivePopup } from "../core/BeadEditorService.js"
import { AZ_SESSION_NAME } from "../lib/tmux-wrap.js"
import { App } from "./App.js"

/**
 * Register a global tmux keybinding to return to the az session.
 * Binds Ctrl-a Ctrl-a (double-tap prefix) to switch back to the main az TUI session.
 * This works from any Claude session spawned by az.
 */
async function registerReturnBinding(): Promise<void> {
	// Only register if we're inside tmux
	if (!process.env.TMUX) return

	try {
		// Bind C-a (Ctrl-a after prefix) to return to az session
		// This creates a "double-tap prefix to go home" pattern
		const proc = Bun.spawn(["tmux", "bind-key", "C-a", "switch-client", "-t", AZ_SESSION_NAME], {
			stdout: "ignore",
			stderr: "ignore",
		})
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
	// Register SIGINT handler to clean up any active tmux popup
	// This prevents orphaned popups when user presses Ctrl-C during editor operations
	process.on("SIGINT", () => {
		killActivePopup()
		process.exit(0)
	})

	// Register Ctrl-a A to return to az session (fire-and-forget)
	registerReturnBinding()

	const renderer = await createCliRenderer()
	createRoot(renderer).render(<App />)
}
