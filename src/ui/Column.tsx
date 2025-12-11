/**
 * Column component - displays a vertical list of tasks for a single status
 */
import { type Component, For, Show, createMemo } from "solid-js"
import { TaskCard } from "./TaskCard"
import type { TaskWithSession, ColumnStatus } from "./types"
import { theme, columnColors } from "./theme"

export interface ColumnProps {
  title: string
  status: ColumnStatus
  tasks: TaskWithSession[]
  selectedTaskId?: string
  selectedTaskIndex?: number
  isActiveColumn?: boolean
  selectedIds?: Set<string>
  taskJumpLabels?: Map<string, string> | null
  pendingJumpKey?: string | null
  maxVisible?: number
}

/**
 * Column component
 *
 * Displays a column header and a windowed list of tasks.
 * Only shows maxVisible tasks at a time, scrolling to keep selection visible.
 */
export const Column: Component<ColumnProps> = (props) => {
  const taskCount = () => props.tasks.length
  const headerColor = () => columnColors[props.status as keyof typeof columnColors] || theme.blue
  const maxVisible = () => props.maxVisible ?? 5

  // Combined header text to avoid multi-text rendering issues
  const headerText = () => `${props.title} (${taskCount()})`

  // Calculate visible window based on selected task index
  const visibleTasks = createMemo(() => {
    const tasks = props.tasks
    const max = maxVisible()

    if (tasks.length <= max) {
      return { tasks, startIndex: 0, hasMore: false, hasPrev: false }
    }

    // Find selected task index in this column
    const selectedIdx = props.selectedTaskIndex ?? 0

    // Calculate window to keep selection visible
    let startIndex = 0
    if (selectedIdx >= max - 1) {
      // Scroll so selection is near bottom of window
      startIndex = Math.min(selectedIdx - max + 2, tasks.length - max)
    }
    startIndex = Math.max(0, startIndex)

    return {
      tasks: tasks.slice(startIndex, startIndex + max),
      startIndex,
      hasMore: startIndex + max < tasks.length,
      hasPrev: startIndex > 0,
    }
  })

  return (
    <box flexDirection="column" width="25%" marginRight={1}>
      {/* Column header */}
      <box paddingLeft={1}>
        <text fg={headerColor()} attributes={props.isActiveColumn ? ATTR_BOLD : 0}>
          {headerText()}
        </text>
      </box>

      {/* Scroll indicator - top */}
      <Show when={visibleTasks().hasPrev}>
        <box paddingLeft={1}>
          <text fg={theme.overlay0}>{"  ↑ " + visibleTasks().startIndex + " more"}</text>
        </box>
      </Show>

      {/* Task list - windowed */}
      <box flexDirection="column" flexGrow={1}>
        <For each={visibleTasks().tasks}>
          {(task) => (
            <TaskCard
              task={task}
              isSelected={props.selectedTaskId === task.id}
              isMultiSelected={props.selectedIds?.has(task.id)}
              jumpLabel={props.taskJumpLabels?.get(task.id)}
              pendingJumpKey={props.pendingJumpKey}
            />
          )}
        </For>
      </box>

      {/* Scroll indicator - bottom */}
      <Show when={visibleTasks().hasMore}>
        <box paddingLeft={1}>
          <text fg={theme.overlay0}>{"  ↓ " + (taskCount() - visibleTasks().startIndex - maxVisible()) + " more"}</text>
        </box>
      </Show>
    </box>
  )
}

/**
 * Text attribute for bold
 */
const ATTR_BOLD = 1
