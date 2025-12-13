/**
 * SearchInput - Bottom bar search input for filtering tasks
 */
import { theme } from "./theme"

export interface SearchInputProps {
  query: string
}

const ATTR_BOLD = 1

/**
 * SearchInput component
 *
 * Displays at the bottom of the screen when in search mode.
 * Shows the current search query with a cursor.
 */
export const SearchInput = ({ query }: SearchInputProps) => {
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
      <text fg={theme.yellow} attributes={ATTR_BOLD}>
        /
      </text>
      <text fg={theme.text}>{query}</text>
      <text fg={theme.yellow}>_</text>
      <box flexGrow={1} />
      <text fg={theme.overlay0}>Enter: search  Esc: cancel</text>
    </box>
  )
}
