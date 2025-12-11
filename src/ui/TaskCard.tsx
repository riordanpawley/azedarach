/**
 * TaskCard component - displays a single task in the Kanban board
 */
import type { Component } from "solid-js"
import type { TaskWithSession } from "./types"
import { SESSION_INDICATORS } from "./types"
import { theme, getPriorityColor } from "./theme"

export interface TaskCardProps {
  task: TaskWithSession
  isSelected?: boolean
  isMultiSelected?: boolean
  jumpLabel?: string
  pendingJumpKey?: string | null
}

/**
 * TaskCard component
 *
 * Simple two-line card: ID line + title line
 */
export const TaskCard: Component<TaskCardProps> = (props) => {
  const indicator = () => SESSION_INDICATORS[props.task.sessionState]

  // Border color based on selection state
  const borderColor = () => {
    if (props.isMultiSelected) return theme.mauve
    if (props.isSelected) return theme.lavender
    return theme.surface1
  }

  // Background color based on selection state
  const backgroundColor = () => {
    if (props.isMultiSelected) return theme.surface1
    if (props.isSelected) return theme.surface0
    return undefined
  }

  // Build the header line: "az-xxx [type]" or "aa az-xxx [type]" in jump mode
  const headerLine = () => {
    let line = ""
    if (props.jumpLabel) {
      line += props.jumpLabel + " "
    }
    line += props.task.id + " [" + props.task.issue_type + "]"
    const ind = indicator()
    if (ind) {
      line += " " + ind
    }
    if (props.isMultiSelected) {
      line += " *"
    }
    return line
  }

  // Priority color for title - need to apply via ANSI since we use single text element
  const priorityColor = () => getPriorityColor(props.task.priority)

  return (
    <box
      borderStyle="single"
      border={true}
      borderColor={borderColor()}
      backgroundColor={backgroundColor()}
      paddingLeft={1}
      paddingRight={1}
      flexDirection="column"
    >
      {/* Header line - ID and type */}
      <text fg={theme.overlay0}>{headerLine()}</text>
      {/* Title line - separate text element for different color */}
      <text fg={priorityColor()}>{props.task.title}</text>
    </box>
  )
}
