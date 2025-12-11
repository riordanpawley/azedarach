/**
 * Board component - main Kanban board layout
 */
import { type Component, For, createMemo } from "solid-js"
import { Column } from "./Column"
import type { TaskWithSession, ColumnStatus, JumpTarget } from "./types"
import { COLUMNS } from "./types"

export interface BoardProps {
  tasks: TaskWithSession[]
  selectedTaskId?: string
  activeColumnIndex?: number
  activeTaskIndex?: number
  selectedIds?: Set<string>
  jumpLabels?: Map<string, JumpTarget> | null
  pendingJumpKey?: string | null
  terminalHeight?: number
}

/**
 * Board component
 *
 * Displays a horizontal flexbox layout of columns, one per status.
 * Tasks are grouped by status and displayed in their respective columns.
 */
export const Board: Component<BoardProps> = (props) => {
  // Group tasks by status for efficient rendering
  const tasksByStatus = createMemo(() => {
    const grouped = new Map<string, TaskWithSession[]>()

    // Initialize all columns with empty arrays
    COLUMNS.forEach((col) => {
      grouped.set(col.status, [])
    })

    // Group tasks by status
    props.tasks.forEach((task) => {
      const tasks = grouped.get(task.status) || []
      tasks.push(task)
      grouped.set(task.status, tasks)
    })

    return grouped
  })

  // Create a map from taskId to jump label for easy lookup
  const taskJumpLabels = createMemo(() => {
    const labels = props.jumpLabels
    if (!labels) return null

    const taskToLabel = new Map<string, string>()
    labels.forEach((target, label) => {
      taskToLabel.set(target.taskId, label)
    })
    return taskToLabel
  })

  return (
    <box flexDirection="row" width="100%" height="100%" padding={1}>
      <For each={COLUMNS}>
        {(column, index) => (
          <Column
            title={column.title}
            status={column.status as ColumnStatus}
            tasks={tasksByStatus().get(column.status) || []}
            selectedTaskId={props.selectedTaskId}
            selectedTaskIndex={props.activeColumnIndex === index() ? props.activeTaskIndex : undefined}
            isActiveColumn={props.activeColumnIndex === index()}
            selectedIds={props.selectedIds}
            taskJumpLabels={taskJumpLabels()}
            pendingJumpKey={props.pendingJumpKey}
            maxVisible={props.terminalHeight}
          />
        )}
      </For>
    </box>
  )
}
