/**
 * StatusBar component - bottom status bar with mode indicator and contextual keybinds
 */
import { type Component, Show, Switch, Match } from "solid-js"
import type { EditorMode } from "./types"
import { theme } from "./theme"

export interface StatusBarProps {
  totalTasks: number
  activeSessions: number
  mode: EditorMode
  modeDisplay: string
  selectedCount: number
  connected?: boolean
}

/**
 * StatusBar component
 *
 * Displays:
 * - Project name and connection status
 * - Current mode indicator (NOR/SEL/ACT/etc) like Helix
 * - Contextual keyboard shortcuts based on current mode (responsive)
 * - Application stats (tasks, active sessions)
 */
export const StatusBar: Component<StatusBarProps> = (props) => {
  // Get terminal width, default to 80 if not available
  const terminalWidth = () => process.stdout.columns || 80

  // Mode colors matching Helix conventions
  const modeColor = () => {
    switch (props.mode) {
      case "normal":
        return theme.blue
      case "select":
        return theme.mauve
      case "goto":
        return theme.yellow
      case "action":
        return theme.green
      default:
        return theme.text
    }
  }

  // Short mode label like Helix
  const modeLabel = () => {
    switch (props.mode) {
      case "normal":
        return "NOR"
      case "select":
        return "SEL"
      case "goto":
        return "GTO"
      case "action":
        return "ACT"
      default:
        return "???"
    }
  }

  // Connection status indicator
  const connectionIndicator = () => {
    const connected = props.connected ?? true
    const icon = connected ? "●" : "○"
    const color = connected ? theme.green : theme.overlay0
    return { icon, color }
  }

  // Determine what to show based on terminal width
  const shouldShowKeybinds = () => terminalWidth() >= 100
  const shouldShowModeDisplay = () => terminalWidth() >= 80
  const shouldShowSelectedCount = () => terminalWidth() >= 60
  const shouldShowPriorityLegend = () => terminalWidth() >= 120

  // Build status line as single string to avoid rendering bugs
  const statusLine = () => {
    const conn = connectionIndicator()
    const parts: string[] = []

    // Left section: project name + connection
    parts.push(`azedarach ${conn.icon}`)

    // Mode indicator is always shown
    // Stats section: always show on right
    const stats: string[] = []
    if (shouldShowSelectedCount() && props.selectedCount > 0) {
      stats.push(`Selected: ${props.selectedCount}`)
    }
    stats.push(`Tasks: ${props.totalTasks}`)
    stats.push(`Active: ${props.activeSessions}`)

    return { projectInfo: parts.join(" "), stats: stats.join("  ") }
  }

  return (
    <box
      borderStyle="single"
      border={true}
      borderColor={theme.surface1}
      backgroundColor={theme.mantle}
      paddingLeft={1}
      paddingRight={1}
    >
      <box flexDirection="row" gap={2} width="100%">
        {/* Project name and connection status - left side */}
        <text fg={theme.text} attributes={ATTR_BOLD}>
          {statusLine().projectInfo.split(" ")[0]}
        </text>
        <text fg={connectionIndicator().color}>
          {connectionIndicator().icon}
        </text>

        {/* Mode indicator */}
        <box
          backgroundColor={modeColor()}
          paddingLeft={1}
          paddingRight={1}
        >
          <text fg={theme.base} attributes={ATTR_BOLD}>
            {modeLabel()}
          </text>
        </box>

        {/* Mode detail (shows pending keys, selection count, etc) */}
        <Show when={shouldShowModeDisplay() && props.modeDisplay}>
          <text fg={theme.subtext0}>{props.modeDisplay}</text>
        </Show>

        {/* Contextual keyboard shortcuts - hide on narrow terminals */}
        <Show when={shouldShowKeybinds()}>
          <box flexDirection="row" gap={2}>
            <Switch>
              <Match when={props.mode === "normal"}>
                <KeyHint key="Space" action="Menu" />
                <KeyHint key="v" action="Select" />
                <KeyHint key="g" action="Goto" />
                <KeyHint key="q" action="Quit" />
              </Match>

              <Match when={props.mode === "select"}>
                <KeyHint key="Space" action="Toggle" />
                <KeyHint key="v" action="Exit" />
                <KeyHint key="Esc" action="Clear" />
              </Match>

              <Match when={props.mode === "goto"}>
                <KeyHint key="w" action="Jump" />
                <KeyHint key="g" action="First" />
                <KeyHint key="e" action="Last" />
                <KeyHint key="Esc" action="Cancel" />
              </Match>

              <Match when={props.mode === "action"}>
                <KeyHint key="h/l" action="Move" />
                <KeyHint key="s" action="Start" />
                <KeyHint key="a" action="Attach" />
                <KeyHint key="p" action="Pause" />
                <KeyHint key="r" action="Resume" />
                <KeyHint key="x" action="Stop" />
                <KeyHint key="Esc" action="Cancel" />
              </Match>
            </Switch>
          </box>
        </Show>

        {/* Priority legend - show on wide terminals */}
        <Show when={shouldShowPriorityLegend()}>
          <box flexDirection="row" gap={1}>
            <text fg={theme.overlay0}>Priority:</text>
            <text fg={theme.red}>P1</text>
            <text fg={theme.peach}>P2</text>
            <text fg={theme.yellow}>P3</text>
            <text fg={theme.text}>P4</text>
          </box>
        </Show>

        {/* Stats - right aligned */}
        <box flexGrow={1} />
        <box flexDirection="row" gap={2}>
          <Show when={shouldShowSelectedCount() && props.selectedCount > 0}>
            <text fg={theme.mauve}>Selected: {props.selectedCount}</text>
          </Show>
          <text fg={theme.green}>Tasks: {props.totalTasks}</text>
          <text fg={theme.blue}>Active: {props.activeSessions}</text>
        </box>
      </box>
    </box>
  )
}

/**
 * KeyHint - displays a keyboard shortcut hint
 */
interface KeyHintProps {
  key: string
  action: string
}

const KeyHint: Component<KeyHintProps> = (props) => (
  <box flexDirection="row" gap={1}>
    <text fg={theme.mauve}>{props.key}</text>
    <text fg={theme.subtext0}>{props.action}</text>
  </box>
)

/**
 * Text attribute for bold
 */
const ATTR_BOLD = 1
