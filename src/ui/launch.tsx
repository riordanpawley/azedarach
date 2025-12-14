/**
 * TUI launcher - initializes OpenTUI and renders the app
 */
import { createCliRenderer } from "@opentui/core"
import { createRoot } from "@opentui/react"
import { killActivePopup } from "../core/EditorService"
import { App } from "./App"

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

	const renderer = await createCliRenderer()
	createRoot(renderer).render(<App />)
}
