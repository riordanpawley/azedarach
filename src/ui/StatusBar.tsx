/**
 * StatusBar component - bottom status bar with mode indicator and contextual keybinds
 */
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
export const StatusBar = (props: StatusBarProps) => {
  // Get terminal width, default to 80 if not available
  const terminalWidth = process.stdout.columns || 80

  // Mode colors matching Helix conventions
  const getModeColor = () => {
    switch (props.mode) {
      case "action":
        return theme.green
      case "goto":
        return theme.yellow
      case "normal":
        return theme.blue
      case "search":
        return theme.peach
      case "select":
        return theme.mauve
      default:
        return theme.text
    }
  }

  // Short mode label like Helix
  const getModeLabel = () => {
    switch (props.mode) {
      case "action":
        return "ACT"
      case "goto":
        return "GTO"
      case "normal":
        return "NOR"
      case "search":
        return "SRC"
      case "select":
        return "SEL"
      default:
        return "???"
    }
  }

  // Connection status indicator
  const getConnectionIndicator = () => {
    const connected = props.connected ?? true
    const icon = connected ? "●" : "○"
    const color = connected ? theme.green : theme.overlay0
    return { icon, color }
  }

  // Determine what to show based on terminal width
  const shouldShowKeybinds = terminalWidth >= 100
  const shouldShowModeDisplay = terminalWidth >= 80
  const shouldShowSelectedCount = terminalWidth >= 60
  const shouldShowPriorityLegend = terminalWidth >= 120

  const connIndicator = getConnectionIndicator()
  const modeColor = getModeColor()
  const modeLabel = getModeLabel()

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
          azedarach
        </text>
        <text fg={connIndicator.color}>
          {connIndicator.icon}
        </text>

        {/* Mode indicator */}
        <box
          backgroundColor={modeColor}
          paddingLeft={1}
          paddingRight={1}
        >
          <text fg={theme.base} attributes={ATTR_BOLD}>
            {modeLabel}
          </text>
        </box>

        {/* Mode detail (shows pending keys, selection count, etc) */}
        {shouldShowModeDisplay && props.modeDisplay && (
          <text fg={theme.subtext0}>{props.modeDisplay}</text>
        )}

        {/* Contextual keyboard shortcuts - hide on narrow terminals */}
        {shouldShowKeybinds && (
          <box flexDirection="row" gap={2}>
            {props.mode === "action" && (
              <>
                <KeyHint keyName="h/l" action="Move" />
                <KeyHint keyName="s" action="Start" />
                <KeyHint keyName="a" action="Attach" />
                <KeyHint keyName="p" action="Pause" />
                <KeyHint keyName="r" action="Resume" />
                <KeyHint keyName="x" action="Stop" />
                <KeyHint keyName="Esc" action="Cancel" />
              </>
            )}

            {props.mode === "goto" && (
              <>
                <KeyHint keyName="w" action="Jump" />
                <KeyHint keyName="g" action="First" />
                <KeyHint keyName="e" action="Last" />
                <KeyHint keyName="Esc" action="Cancel" />
              </>
            )}

            {props.mode === "normal" && (
              <>
                <KeyHint keyName="Space" action="Menu" />
                <KeyHint keyName="/" action="Search" />
                <KeyHint keyName="v" action="Select" />
                <KeyHint keyName="g" action="Goto" />
                <KeyHint keyName="q" action="Quit" />
              </>
            )}

            {props.mode === "search" && (
              <>
                <KeyHint keyName="Enter" action="Confirm" />
                <KeyHint keyName="Esc" action="Clear" />
              </>
            )}

            {props.mode === "select" && (
              <>
                <KeyHint keyName="Space" action="Toggle" />
                <KeyHint keyName="v" action="Exit" />
                <KeyHint keyName="Esc" action="Clear" />
              </>
            )}
          </box>
        )}

        {/* Priority legend - show on wide terminals */}
        {shouldShowPriorityLegend && (
          <box flexDirection="row" gap={1}>
            <text fg={theme.overlay0}>Priority:</text>
            <text fg={theme.red}>P1</text>
            <text fg={theme.peach}>P2</text>
            <text fg={theme.yellow}>P3</text>
            <text fg={theme.text}>P4</text>
          </box>
        )}

        {/* Stats - right aligned */}
        <box flexGrow={1} />
        <box flexDirection="row" gap={2}>
          {shouldShowSelectedCount && props.selectedCount > 0 && (
            <text fg={theme.mauve}>Selected: {props.selectedCount}</text>
          )}
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
  keyName: string
  action: string
}

const KeyHint = (props: KeyHintProps) => (
  <box flexDirection="row" gap={1}>
    <text fg={theme.mauve}>{props.keyName}</text>
    <text fg={theme.subtext0}>{props.action}</text>
  </box>
)

/**
 * Text attribute for bold
 */
const ATTR_BOLD = 1
