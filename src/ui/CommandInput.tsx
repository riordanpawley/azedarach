/**
 * CommandInput - Bottom bar command input for sending commands to VC REPL
 */
import { theme } from "./theme"

export interface CommandInputProps {
  input: string
}

const ATTR_BOLD = 1

/**
 * CommandInput component
 *
 * Displays at the bottom of the screen when in command mode.
 * Shows the current command input with a cursor.
 */
export const CommandInput = ({ input }: CommandInputProps) => {
  return (
    <box
      position="absolute"
      left={0}
      right={0}
      bottom={0}
      height={1}
      backgroundColor={theme.surface0}
      flexDirection="row"
    >
      <text fg={theme.mauve} attributes={ATTR_BOLD}>
        :
      </text>
      <text fg={theme.text}>{input}</text>
      <text fg={theme.mauve}>_</text>
      <box flexGrow={1} />
      <text fg={theme.overlay0}>Enter: send  Esc: cancel</text>
    </box>
  )
}
