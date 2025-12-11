/**
 * TUI launcher - initializes OpenTUI and renders the app
 */
import { render } from "@opentui/solid"
import { App } from "./App"
import { AtomProvider } from "../lib/effect-atom-solid"

/**
 * Launch the TUI application
 *
 * Initializes the OpenTUI renderer and starts the application.
 * Wraps App with AtomProvider for effect-atom state management.
 */
export async function launchTUI(): Promise<void> {
  await render(
    () => (
      <AtomProvider>
        <App />
      </AtomProvider>
    ),
    {
      // OpenTUI renderer config
      // Use default terminal size
    }
  )
}
