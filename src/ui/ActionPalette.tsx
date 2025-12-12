/**
 * ActionPalette component - modal showing available actions
 */
import { type Component } from "solid-js"
import { theme } from "./theme"
import type { TaskWithSession } from "./types"

export interface ActionPaletteProps {
  task?: TaskWithSession
}

/**
 * ActionPalette component
 *
 * Displays a centered modal overlay with all available actions
 * grouped by category. Actions are grayed out based on session state.
 */
export const ActionPalette: Component<ActionPaletteProps> = (props) => {
  const ATTR_BOLD = 1
  const ATTR_DIM = 2

  // Helper to check if an action is available
  const isAvailable = (action: string): boolean => {
    const sessionState = props.task?.sessionState ?? "idle"

    switch (action) {
      case "s": // Start - only if idle
        return sessionState === "idle"
      case "a": // Attach - only if not idle
        return sessionState !== "idle"
      case "p": // Pause - only if busy
        return sessionState === "busy"
      case "r": // Resume - only if paused
        return sessionState === "paused"
      case "x": // Stop - only if not idle
        return sessionState !== "idle"
      case "h": // Move left - always available
      case "l": // Move right - always available
        return true
      default:
        return false
    }
  }

  // Helper to render an action line with conditional dimming
  const renderAction = (key: string, description: string) => {
    const available = isAvailable(key)
    const fgColor = available ? theme.text : theme.overlay0
    const keyColor = available ? theme.lavender : theme.overlay0
    const attrs = available ? 0 : ATTR_DIM

    return (
      <>
        {"  "}
        <text fg={keyColor} attributes={attrs}>{key}</text>
        {"  "}
        <text fg={fgColor} attributes={attrs}>{description}</text>
        {"\n"}
      </>
    )
  }

  return (
    <box
      position="absolute"
      left={0}
      right={0}
      top={0}
      bottom={0}
      alignItems="center"
      justifyContent="center"
      backgroundColor={theme.crust + "CC"} // Semi-transparent overlay
    >
      <box
        borderStyle="rounded"
        border={true}
        borderColor={theme.mauve}
        backgroundColor={theme.base}
        paddingLeft={2}
        paddingRight={2}
        paddingTop={1}
        paddingBottom={1}
        minWidth={40}
      >
        {/* Header */}
        <text fg={theme.mauve} attributes={ATTR_BOLD}>
          {"┌─ Actions ─────────────────────┐\n"}
          {"\n"}
          {/* Session actions */}
          <text fg={theme.blue} attributes={ATTR_BOLD}>{"Session\n"}</text>
          <text fg={theme.text}>
            {renderAction("s", "Start session")}
            {renderAction("a", "Attach to session")}
            {renderAction("p", "Pause session")}
            {renderAction("r", "Resume session")}
            {renderAction("x", "Stop session")}
            {"\n"}
            {/* Move actions */}
            <text fg={theme.blue} attributes={ATTR_BOLD}>{"Move\n"}</text>
            {renderAction("h", "Move left")}
            {renderAction("l", "Move right")}
            {"\n"}
            <text fg={theme.subtext0}>{"Press Esc to cancel"}</text>
          </text>
        </text>
      </box>
    </box>
  )
}
