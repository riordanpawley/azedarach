/**
 * TaskCard component - displays a single task in the Kanban board
 */
import type { Component } from "solid-js"
import type { TaskWithSession } from "./types"
import { SESSION_INDICATORS } from "./types"
import { theme } from "./theme"

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

  // Build the complete card content as a single string to avoid OpenTUI rendering bugs
  // Multiple <text> siblings cause character corruption
  const cardContent = () => {
    let header = ""
    if (props.jumpLabel) {
      header += props.jumpLabel + " "
    }
    header += props.task.id + " [" + props.task.issue_type + "]"
    const ind = indicator()
    if (ind) {
      header += " " + ind
    }
    if (props.isMultiSelected) {
      header += " *"
    }
    // Combine header and title with newline
    return header + "\n" + props.task.title
  }

  return (
    <box
      borderStyle="single"
      border={true}
      borderColor={borderColor()}
      backgroundColor={backgroundColor()}
      paddingLeft={1}
      paddingRight={1}
    >
      {/* Single text element to avoid OpenTUI sibling text rendering bugs */}
      <text fg={theme.overlay0}>{cardContent()}</text>
    </box>
  )
}
