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
}

/**
 * StatusBar component
 *
 * Displays:
 * - Current mode indicator (NOR/SEL/ACT/etc) like Helix
 * - Contextual keyboard shortcuts based on current mode
 * - Application stats (tasks, active sessions)
 */
export const StatusBar: Component<StatusBarProps> = (props) => {
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
        {/* Mode indicator - left side */}
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
        <text fg={theme.subtext0}>{props.modeDisplay}</text>

        {/* Contextual keyboard shortcuts */}
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
              <KeyHint key="Esc" action="Cancel" />
            </Match>
          </Switch>
        </box>

        {/* Stats - right aligned */}
        <box flexGrow={1} />
        <box flexDirection="row" gap={2}>
          <Show when={props.selectedCount > 0}>
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
