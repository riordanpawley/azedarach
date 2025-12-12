/**
 * DetailPanel component - expandable detail view for selected task
 */
import { type Component, Show, createMemo } from "solid-js"
import type { TaskWithSession } from "./types"
import { theme, getPriorityColor } from "./theme"
import { SESSION_INDICATORS } from "./types"

export interface DetailPanelProps {
  task: TaskWithSession
}

/**
 * DetailPanel component
 *
 * Displays a centered modal overlay with full task details:
 * - Title, ID, type, priority, status
 * - Description (wrapped)
 * - Design notes (if present)
 * - Dependencies (future enhancement)
 * - Session status and recent output (future enhancement)
 * - Available actions based on state (future enhancement)
 */
export const DetailPanel: Component<DetailPanelProps> = (props) => {
  const ATTR_BOLD = 1

  const indicator = () => SESSION_INDICATORS[props.task.sessionState]

  // Priority label like P1, P2, P3, P4
  const priorityLabel = () => `P${props.task.priority}`

  // Format timestamps
  const formatDate = (dateStr: string) => {
    try {
      const date = new Date(dateStr)
      return date.toLocaleDateString() + " " + date.toLocaleTimeString()
    } catch {
      return dateStr
    }
  }

  // Status color based on task status
  const statusColor = () => {
    switch (props.task.status) {
      case "open":
        return theme.blue
      case "in_progress":
        return theme.mauve
      case "blocked":
        return theme.red
      case "closed":
        return theme.green
      default:
        return theme.text
    }
  }

  // Available actions based on task state
  const availableActions = createMemo(() => {
    const actions: string[] = []

    switch (props.task.status) {
      case "open":
        actions.push("Space h - Move to previous column")
        actions.push("Space l - Move to next column (Start)")
        actions.push("s - Start session (coming soon)")
        break
      case "in_progress":
        actions.push("Space h - Move to Open")
        actions.push("Space l - Move to Blocked")
        actions.push("a - Attach to session (coming soon)")
        actions.push("p - Pause session (coming soon)")
        break
      case "blocked":
        actions.push("Space h - Move to In Progress")
        actions.push("Space l - Move to Closed")
        break
      case "closed":
        actions.push("Space h - Reopen")
        break
    }

    return actions
  })

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
        minWidth={70}
        maxWidth={90}
        flexDirection="column"
      >
        {/* Header with task ID and type */}
        <text fg={theme.mauve} attributes={ATTR_BOLD}>
          {`━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n`}
          {"  "}
          {props.task.id}
          {" ["}
          {props.task.issue_type}
          {"]"}
          {indicator() ? " " + indicator() : ""}
          {"\n"}
          {"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"}
          {"\n"}
        </text>

        {/* Title */}
        <text fg={theme.text} attributes={ATTR_BOLD}>
          {props.task.title}
          {"\n\n"}
        </text>

        {/* Metadata row */}
        <box flexDirection="row" gap={2}>
          <text fg={getPriorityColor(props.task.priority)}>
            {"Priority: "}
            {priorityLabel()}
          </text>
          <text fg={statusColor()}>
            {"Status: "}
            {props.task.status}
          </text>
          <text fg={theme.subtext0}>
            {"Session: "}
            {props.task.sessionState}
          </text>
        </box>
        <text>{"\n"}</text>

        {/* Description */}
        <Show when={props.task.description}>
          <text fg={theme.blue} attributes={ATTR_BOLD}>
            {"Description:\n"}
          </text>
          <text fg={theme.text}>
            {props.task.description}
            {"\n\n"}
          </text>
        </Show>

        {/* Design notes */}
        <Show when={props.task.design}>
          <text fg={theme.blue} attributes={ATTR_BOLD}>
            {"Design:\n"}
          </text>
          <text fg={theme.text}>
            {props.task.design}
            {"\n\n"}
          </text>
        </Show>

        {/* Notes */}
        <Show when={props.task.notes}>
          <text fg={theme.blue} attributes={ATTR_BOLD}>
            {"Notes:\n"}
          </text>
          <text fg={theme.text}>
            {props.task.notes}
            {"\n\n"}
          </text>
        </Show>

        {/* Timestamps */}
        <text fg={theme.subtext0}>
          {"Created: "}
          {formatDate(props.task.created_at)}
          {"\n"}
          {"Updated: "}
          {formatDate(props.task.updated_at)}
          {"\n"}
        </text>
        <Show when={props.task.closed_at}>
          <text fg={theme.subtext0}>
            {"Closed: "}
            {formatDate(props.task.closed_at!)}
            {"\n"}
          </text>
        </Show>

        <text>{"\n"}</text>

        {/* Available actions */}
        <text fg={theme.blue} attributes={ATTR_BOLD}>
          {"Available Actions:\n"}
        </text>
        <text fg={theme.text}>
          {availableActions().map((action) => "  " + action).join("\n")}
          {"\n\n"}
        </text>

        {/* Footer instructions */}
        <text fg={theme.subtext0}>
          {"Press Enter or Esc to close..."}
        </text>
      </box>
    </box>
  )
}
