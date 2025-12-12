/**
 * App component - root component with Helix-style modal keybindings
 */
import { type Component, createSignal, createMemo, batch, Show } from "solid-js"
import { Result } from "@effect-atom/atom"
import { useKeyboard } from "@opentui/solid"
import { Board } from "./Board"
import { StatusBar } from "./StatusBar"
import { HelpOverlay } from "./HelpOverlay"
import { ActionPalette } from "./ActionPalette"
import { DetailPanel } from "./DetailPanel"
import { CreateTaskPrompt } from "./CreateTaskPrompt"
import { ToastContainer, type ToastMessage, generateToastId } from "./Toast"
import { TASK_CARD_HEIGHT } from "./TaskCard"
import {
  tasksAtom,
  appRuntime,
  moveTaskAtom,
  moveTasksAtom,
  attachExternalAtom,
  attachInlineAtom,
  startSessionAtom,
  pauseSessionAtom,
  resumeSessionAtom,
  stopSessionAtom,
  createTaskAtom,
  editBeadAtom,
  createPRAtom,
  cleanupAtom,
} from "./atoms"
import { useAtomValue, useAtomMount, useAtomRefresh, useAtomSet } from "../lib/effect-atom-solid"
import {
  COLUMNS,
  type TaskWithSession,
  type NavigationState,
  type EditorMode,
  type GotoSubMode,
  type JumpTarget,
  generateJumpLabels,
} from "./types"
import { theme } from "./theme"

/**
 * App component
 *
 * Root component implementing Helix-style modal editing:
 * - Normal mode: hjkl navigation, g for goto prefix, v for select mode
 * - Goto mode: 'gw' shows 2-char labels for instant jumping
 * - Select mode: multi-selection with space to toggle
 * - Action mode: space in normal opens command palette
 */
// Header (1) + status bar (3) + padding (2)
const CHROME_HEIGHT = 6

export const App: Component = () => {
  // Mount the runtime (starts the Effect runtime lifecycle)
  useAtomMount(appRuntime)

  // Subscribe to tasks atom - returns Result.Result<TaskWithSession[], Error>
  const tasksResult = useAtomValue(tasksAtom)

  // Refresh function to re-fetch tasks after mutations
  const refreshTasks = useAtomRefresh(tasksAtom)

  // Action atoms for session lifecycle (using effect-atom fn pattern)
  const moveTask = useAtomSet(moveTaskAtom, { mode: "promise" })
  const moveTasks = useAtomSet(moveTasksAtom, { mode: "promise" })
  const attachExternal = useAtomSet(attachExternalAtom, { mode: "promise" })
  const attachInline = useAtomSet(attachInlineAtom, { mode: "promise" })
  const startSession = useAtomSet(startSessionAtom, { mode: "promise" })
  const pauseSession = useAtomSet(pauseSessionAtom, { mode: "promise" })
  const resumeSession = useAtomSet(resumeSessionAtom, { mode: "promise" })
  const stopSession = useAtomSet(stopSessionAtom, { mode: "promise" })
  const createTask = useAtomSet(createTaskAtom, { mode: "promise" })
  const editBead = useAtomSet(editBeadAtom, { mode: "promise" })
  const createPR = useAtomSet(createPRAtom, { mode: "promise" })
  const cleanup = useAtomSet(cleanupAtom, { mode: "promise" })

  // Calculate how many tasks can fit based on terminal height
  const maxVisibleTasks = () => {
    const rows = process.stdout.rows || 24
    const availableHeight = rows - CHROME_HEIGHT
    return Math.max(1, Math.floor(availableHeight / TASK_CARD_HEIGHT))
  }

  // Navigation state
  const [nav, setNav] = createSignal<NavigationState>({ columnIndex: 0, taskIndex: 0 })

  // Modal editing state
  const [mode, setMode] = createSignal<EditorMode>("normal")
  const [gotoSubMode, setGotoSubMode] = createSignal<GotoSubMode | null>(null)
  const [selectedIds, setSelectedIds] = createSignal<Set<string>>(new Set())
  const [jumpLabels, setJumpLabels] = createSignal<Map<string, JumpTarget> | null>(null)
  const [pendingJumpKey, setPendingJumpKey] = createSignal<string | null>(null)
  const [showHelp, setShowHelp] = createSignal(false)
  const [showDetail, setShowDetail] = createSignal(false)
  const [showCreatePrompt, setShowCreatePrompt] = createSignal(false)

  // Toast notifications state
  const [toasts, setToasts] = createSignal<ToastMessage[]>([])

  // Helper to show an error toast
  const showError = (message: string) => {
    setToasts((prev) => [
      ...prev,
      { id: generateToastId(), message, type: "error", timestamp: Date.now() },
    ])
  }

  // Helper to show a success toast
  const showSuccess = (message: string) => {
    setToasts((prev) => [
      ...prev,
      { id: generateToastId(), message, type: "success", timestamp: Date.now() },
    ])
  }

  // Helper to show an info toast
  const showInfo = (message: string) => {
    setToasts((prev) => [
      ...prev,
      { id: generateToastId(), message, type: "info", timestamp: Date.now() },
    ])
  }

  // Dismiss a toast by ID
  const dismissToast = (id: string) => {
    setToasts((prev) => prev.filter((t) => t.id !== id))
  }

  // Group tasks by column for navigation
  const tasksByColumn = createMemo(() => {
    const result = tasksResult()
    if (!Result.isSuccess(result)) return []

    return COLUMNS.map((col) =>
      result.value.filter((task) => task.status === col.status)
    )
  })

  // Get all tasks as flat list for jump label generation
  const allTasks = createMemo(() => {
    const columns = tasksByColumn()
    const tasks: Array<{ task: TaskWithSession; columnIndex: number; taskIndex: number }> = []

    columns.forEach((column, columnIndex) => {
      column.forEach((task, taskIndex) => {
        tasks.push({ task, columnIndex, taskIndex })
      })
    })

    return tasks
  })

  // Generate jump labels when entering goto mode
  const computeJumpLabels = () => {
    const tasks = allTasks()
    const labels = generateJumpLabels(tasks.length)
    const labelMap = new Map<string, JumpTarget>()

    tasks.forEach(({ task, columnIndex, taskIndex }, i) => {
      if (labels[i]) {
        labelMap.set(labels[i], { taskId: task.id, columnIndex, taskIndex })
      }
    })

    return labelMap
  }

  // Get currently selected task
  const selectedTask = createMemo((): TaskWithSession | undefined => {
    const columns = tasksByColumn()
    const { columnIndex, taskIndex } = nav()
    const column = columns[columnIndex]
    return column?.[taskIndex]
  })

  // Helper to clamp task index to column bounds
  const clampTaskIndex = (columnIndex: number, preferredIndex: number): number => {
    const columns = tasksByColumn()
    const column = columns[columnIndex]
    if (!column || column.length === 0) return 0
    return Math.min(preferredIndex, column.length - 1)
  }

  // Navigate to a specific position
  const navigateTo = (columnIndex: number, taskIndex: number) => {
    const clampedColumnIndex = Math.max(0, Math.min(columnIndex, COLUMNS.length - 1))
    const clampedTaskIndex = clampTaskIndex(clampedColumnIndex, taskIndex)
    setNav({ columnIndex: clampedColumnIndex, taskIndex: clampedTaskIndex })
  }

  // Toggle selection of current task
  const toggleSelection = (taskId: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev)
      if (next.has(taskId)) {
        next.delete(taskId)
      } else {
        next.add(taskId)
      }
      return next
    })
  }

  // Exit any special mode back to normal
  const exitToNormal = () => {
    batch(() => {
      setMode("normal")
      setGotoSubMode(null)
      setJumpLabels(null)
      setPendingJumpKey(null)
    })
  }

  // Handle jump label input
  const handleJumpInput = (key: string) => {
    const pending = pendingJumpKey()
    const labels = jumpLabels()

    if (!labels) {
      exitToNormal()
      return
    }

    if (pending) {
      // Second key - try to complete the jump
      const fullLabel = pending + key
      const target = labels.get(fullLabel)

      if (target) {
        navigateTo(target.columnIndex, target.taskIndex)
      }
      exitToNormal()
    } else {
      // First key - check if any labels start with this key
      const hasMatch = Array.from(labels.keys()).some((label) => label.startsWith(key))
      if (hasMatch) {
        setPendingJumpKey(key)
      } else {
        exitToNormal()
      }
    }
  }

  // Keyboard navigation with Helix-style modal bindings
  useKeyboard((event) => {
    const columns = tasksByColumn()
    const { columnIndex, taskIndex } = nav()
    const currentMode = mode()

    // Help overlay handling - dismiss on any key
    if (showHelp()) {
      setShowHelp(false)
      return
    }

    // Detail panel handling - dismiss on Enter or Escape
    if (showDetail()) {
      if (event.name === "return" || event.name === "escape") {
        setShowDetail(false)
      }
      return
    }

    // Create prompt handling - CreateTaskPrompt handles its own keyboard input
    if (showCreatePrompt()) {
      return
    }

    // Escape always returns to normal mode
    if (event.name === "escape") {
      if (currentMode === "select") {
        // Clear selections when exiting select mode
        setSelectedIds(new Set<string>())
      }
      exitToNormal()
      return
    }

    // Handle goto mode
    if (currentMode === "goto") {
      const subMode = gotoSubMode()

      if (subMode === "pending") {
        // Waiting for second key after 'g'
        switch (event.name) {
          case "w": {
            // Enter jump mode with labels
            const labels = computeJumpLabels()
            batch(() => {
              setJumpLabels(labels)
              setGotoSubMode("jump")
            })
            break
          }
          case "g": {
            // Go to first task in first column
            navigateTo(0, 0)
            exitToNormal()
            break
          }
          case "e": {
            // Go to last task in last column
            const lastColIdx = COLUMNS.length - 1
            const lastCol = columns[lastColIdx]
            navigateTo(lastColIdx, lastCol ? lastCol.length - 1 : 0)
            exitToNormal()
            break
          }
          case "h": {
            // Go to first column, keep task index
            navigateTo(0, taskIndex)
            exitToNormal()
            break
          }
          case "l": {
            // Go to last column, keep task index
            navigateTo(COLUMNS.length - 1, taskIndex)
            exitToNormal()
            break
          }
          default:
            exitToNormal()
        }
        return
      }

      if (subMode === "jump") {
        // In jump mode - handle label input
        handleJumpInput(event.name)
        return
      }
      return
    }

    // Handle select mode
    if (currentMode === "select") {
      switch (event.name) {
        case "up":
        case "k": {
          const column = columns[columnIndex]
          if (column && taskIndex > 0) {
            setNav({ columnIndex, taskIndex: taskIndex - 1 })
          }
          break
        }
        case "down":
        case "j": {
          const column = columns[columnIndex]
          if (column && taskIndex < column.length - 1) {
            setNav({ columnIndex, taskIndex: taskIndex + 1 })
          }
          break
        }
        case "left":
        case "h": {
          if (columnIndex > 0) {
            navigateTo(columnIndex - 1, taskIndex)
          }
          break
        }
        case "right":
        case "l": {
          if (columnIndex < COLUMNS.length - 1) {
            navigateTo(columnIndex + 1, taskIndex)
          }
          break
        }
        case "space": {
          // Toggle selection of current task
          const task = selectedTask()
          if (task) {
            toggleSelection(task.id)
          }
          break
        }
        case "v": {
          // Exit select mode (back to normal)
          exitToNormal()
          break
        }
      }
      return
    }

    // Handle action mode (Space menu)
    // Stay in action mode after moves so user can continue moving h/l
    // Press Escape to exit action mode
    if (currentMode === "action") {
      switch (event.name) {
        case "left":
        case "h": {
          // Move selected tasks (or current task) to previous column
          if (columnIndex > 0) {
            const targetStatus = COLUMNS[columnIndex - 1]?.status
            if (targetStatus) {
              const ids = selectedIds()
              const task = selectedTask()
              if (ids.size > 0) {
                moveTasks({ taskIds: [...ids], newStatus: targetStatus })
                  .then(() => {
                    refreshTasks()
                    navigateTo(columnIndex - 1, taskIndex)
                  })
                  .catch((error) => showError(`Move failed: ${error}`))
              } else if (task) {
                moveTask({ taskId: task.id, newStatus: targetStatus })
                  .then(() => {
                    refreshTasks()
                    navigateTo(columnIndex - 1, taskIndex)
                  })
                  .catch((error) => showError(`Move failed: ${error}`))
              }
            }
          }
          // Stay in action mode
          break
        }
        case "right":
        case "l": {
          // Move selected tasks (or current task) to next column
          if (columnIndex < COLUMNS.length - 1) {
            const targetStatus = COLUMNS[columnIndex + 1]?.status
            if (targetStatus) {
              const ids = selectedIds()
              const task = selectedTask()
              if (ids.size > 0) {
                moveTasks({ taskIds: [...ids], newStatus: targetStatus })
                  .then(() => {
                    refreshTasks()
                    navigateTo(columnIndex + 1, taskIndex)
                  })
                  .catch((error) => showError(`Move failed: ${error}`))
              } else if (task) {
                moveTask({ taskId: task.id, newStatus: targetStatus })
                  .then(() => {
                    refreshTasks()
                    navigateTo(columnIndex + 1, taskIndex)
                  })
                  .catch((error) => showError(`Move failed: ${error}`))
              }
            }
          }
          // Stay in action mode
          break
        }
        case "s": {
          // Start a new session (only for idle tasks)
          const task = selectedTask()
          if (task) {
            if (task.sessionState !== "idle") {
              showError(`Cannot start: task is ${task.sessionState}`)
            } else {
              startSession(task.id)
                .then(() => {
                  refreshTasks()
                  showSuccess(`Started session for ${task.id}`)
                })
                .catch((error) => {
                  showError(`Failed to start: ${error}`)
                })
            }
          }
          exitToNormal()
          break
        }
        case "a": {
          // Attach to session - switches tmux client to Claude session
          // Claude sessions use Ctrl-a prefix, so Ctrl-a ) switches back
          const task = selectedTask()
          if (task) {
            attachExternal(task.id)
              .then(() => {
                // Switched! Show reminder about how to get back
                showInfo(`Switched! Ctrl-a ) to return`)
              })
              .catch((error) => {
                const msg = error && typeof error === "object" && error._tag === "SessionNotFoundError"
                  ? `No session for ${task.id} - press Space+s to start`
                  : `Failed to attach: ${error}`
                showError(msg)
              })
          }
          exitToNormal()
          break
        }
        case "A": {
          // Attach to session inline (replace TUI)
          // Don't pre-check sessionState - it may be stale. Let AttachmentService check tmux directly.
          const task = selectedTask()
          if (task) {
            attachInline(task.id).catch((error) => {
              const msg = error && typeof error === "object" && error._tag === "SessionNotFoundError"
                ? `No session for ${task.id} - press Space+s to start`
                : `Failed to attach: ${error}`
              showError(msg)
            })
          }
          exitToNormal()
          break
        }
        case "p": {
          // Pause a running session (only for busy tasks)
          const task = selectedTask()
          if (task) {
            if (task.sessionState !== "busy") {
              showError(`Cannot pause: task is ${task.sessionState}`)
            } else {
              pauseSession(task.id)
                .then(() => {
                  refreshTasks()
                  showSuccess(`Paused session for ${task.id}`)
                })
                .catch((error) => {
                  showError(`Failed to pause: ${error}`)
                })
            }
          }
          exitToNormal()
          break
        }
        case "r": {
          // Resume a paused session (only for paused tasks)
          const task = selectedTask()
          if (task) {
            if (task.sessionState !== "paused") {
              showError(`Cannot resume: task is ${task.sessionState}`)
            } else {
              resumeSession(task.id)
                .then(() => {
                  refreshTasks()
                  showSuccess(`Resumed session for ${task.id}`)
                })
                .catch((error) => {
                  showError(`Failed to resume: ${error}`)
                })
            }
          }
          exitToNormal()
          break
        }
        case "x": {
          // Stop/kill a running session
          const task = selectedTask()
          if (task) {
            if (task.sessionState === "idle") {
              showError(`No session to stop`)
            } else {
              stopSession(task.id)
                .then(() => {
                  refreshTasks()
                  showSuccess(`Stopped session for ${task.id}`)
                })
                .catch((error) => {
                  showError(`Failed to stop: ${error}`)
                })
            }
          }
          exitToNormal()
          break
        }
        case "e": {
          // Edit bead in $EDITOR
          const task = selectedTask()
          if (task) {
            editBead(task)
              .then(() => {
                refreshTasks()
                showSuccess(`Updated ${task.id}`)
              })
              .catch((error) => {
                const msg = error && typeof error === "object" && error._tag === "ParseMarkdownError"
                  ? `Invalid format: ${error.message}`
                  : error && typeof error === "object" && error._tag === "EditorError"
                  ? `Editor error: ${error.message}`
                  : `Failed to edit: ${error}`
                showError(msg)
              })
          }
          exitToNormal()
          break
        }
        case "P": {
          // Create PR (push branch + gh pr create)
          const task = selectedTask()
          if (task) {
            if (task.sessionState === "idle") {
              showError(`No worktree for ${task.id} - start a session first`)
            } else {
              showInfo(`Creating PR for ${task.id}...`)
              createPR(task.id)
                .then((pr) => {
                  showSuccess(`PR created: ${pr.url}`)
                })
                .catch((error) => {
                  const msg = error && typeof error === "object" && error._tag === "GHCLIError"
                    ? error.message
                    : `Failed to create PR: ${error}`
                  showError(msg)
                })
            }
          }
          exitToNormal()
          break
        }
        case "d": {
          // Cleanup/delete worktree and branches
          const task = selectedTask()
          if (task) {
            if (task.sessionState === "idle") {
              showError(`No worktree to delete for ${task.id}`)
            } else {
              // TODO: Add confirmation dialog for destructive action
              showInfo(`Cleaning up ${task.id}...`)
              cleanup(task.id)
                .then(() => {
                  refreshTasks()
                  showSuccess(`Cleaned up ${task.id}`)
                })
                .catch((error) => {
                  showError(`Failed to cleanup: ${error}`)
                })
            }
          }
          exitToNormal()
          break
        }
        default:
          // Unknown key in action mode - ignore (don't exit)
          break
      }
      return
    }

    // Normal mode
    switch (event.name) {
      case "up":
      case "k": {
        // Move up in current column
        const column = columns[columnIndex]
        if (column && taskIndex > 0) {
          setNav({ columnIndex, taskIndex: taskIndex - 1 })
        }
        break
      }
      case "down":
      case "j": {
        // Move down in current column
        const column = columns[columnIndex]
        if (column && taskIndex < column.length - 1) {
          setNav({ columnIndex, taskIndex: taskIndex + 1 })
        }
        break
      }
      case "left":
      case "h": {
        // Move to previous column
        if (columnIndex > 0) {
          navigateTo(columnIndex - 1, taskIndex)
        }
        break
      }
      case "right":
      case "l": {
        // Move to next column
        if (columnIndex < COLUMNS.length - 1) {
          navigateTo(columnIndex + 1, taskIndex)
        }
        break
      }
      case "g": {
        // Enter goto mode (waiting for next key)
        batch(() => {
          setMode("goto")
          setGotoSubMode("pending")
        })
        break
      }
      case "v": {
        // Enter select mode
        setMode("select")
        break
      }
      case "space": {
        // Enter action mode (command palette)
        setMode("action")
        break
      }
      case "q": {
        // Quit
        process.exit(0)
        break
      }
      case "?": {
        // Toggle help overlay
        setShowHelp(true)
        break
      }
      case "return": {
        // Show detail panel for selected task
        const task = selectedTask()
        if (task) {
          setShowDetail(true)
        }
        break
      }
      case "c": {
        // Create new task
        setShowCreatePrompt(true)
        break
      }
    }

    // Ctrl-d: half page down
    if (event.ctrl && event.name === "d") {
      const column = columns[columnIndex]
      if (column) {
        const halfPage = Math.floor(column.length / 2)
        const newIndex = Math.min(taskIndex + halfPage, column.length - 1)
        setNav({ columnIndex, taskIndex: newIndex })
      }
    }

    // Ctrl-u: half page up
    if (event.ctrl && event.name === "u") {
      const column = columns[columnIndex]
      if (column) {
        const halfPage = Math.floor(column.length / 2)
        const newIndex = Math.max(taskIndex - halfPage, 0)
        setNav({ columnIndex, taskIndex: newIndex })
      }
    }
  })

  // Computed values (only when we have success)
  const totalTasks = () => {
    const result = tasksResult()
    return Result.isSuccess(result) ? result.value.length : 0
  }

  const activeSessions = () => {
    const result = tasksResult()
    if (!Result.isSuccess(result)) return 0
    return result.value.filter(
      (t) => t.sessionState === "busy" || t.sessionState === "waiting"
    ).length
  }

  // Mode display text
  const modeDisplay = createMemo(() => {
    const currentMode = mode()
    const subMode = gotoSubMode()
    const pending = pendingJumpKey()

    switch (currentMode) {
      case "goto":
        if (subMode === "pending") return "g..."
        if (subMode === "jump") return pending ? `g w ${pending}_` : "g w ..."
        return "goto"
      case "select":
        return `select (${selectedIds().size})`
      case "action":
        return "action"
      default:
        return "normal"
    }
  })

  // Render based on Result state
  return (
    <box flexDirection="column" width="100%" height="100%" backgroundColor={theme.base}>
      {Result.match(tasksResult(), {
        onInitial: () => (
          <box flexDirection="column" alignItems="center" justifyContent="center" flexGrow={1}>
            <text fg={theme.sky}>Loading tasks...</text>
          </box>
        ),
        onFailure: (failure) => (
          <box flexDirection="column" alignItems="center" justifyContent="center" flexGrow={1}>
            <text fg={theme.red}>Error loading tasks:</text>
            <text fg={theme.red}>{String(failure.cause)}</text>
          </box>
        ),
        onSuccess: (success) => (
          <box flexGrow={1}>
            <Board
              tasks={success.value}
              selectedTaskId={selectedTask()?.id}
              activeColumnIndex={nav().columnIndex}
              activeTaskIndex={nav().taskIndex}
              selectedIds={selectedIds()}
              jumpLabels={mode() === "goto" && gotoSubMode() === "jump" ? jumpLabels() : null}
              pendingJumpKey={pendingJumpKey()}
              terminalHeight={maxVisibleTasks()}
            />
          </box>
        ),
      })}

      {/* Status bar at bottom */}
      <StatusBar
        totalTasks={totalTasks}
        activeSessions={activeSessions}
        mode={mode}
        modeDisplay={modeDisplay}
        selectedCount={() => selectedIds().size}
      />

      {/* Help overlay */}
      <Show when={showHelp()}>
        <HelpOverlay />
      </Show>

      {/* Action palette */}
      <Show when={mode() === "action"}>
        <ActionPalette task={selectedTask()} />
      </Show>

      {/* Detail panel */}
      <Show when={showDetail() && selectedTask()}>
        {(task) => <DetailPanel task={task()} />}
      </Show>

      {/* Create task prompt */}
      <Show when={showCreatePrompt()}>
        <CreateTaskPrompt
          onSubmit={(params) => {
            createTask(params)
              .then((issue) => {
                setShowCreatePrompt(false)
                refreshTasks()
                showSuccess(`Created task: ${issue.id}`)
              })
              .catch((error) => {
                setShowCreatePrompt(false)
                showError(`Failed to create task: ${error}`)
              })
          }}
          onCancel={() => setShowCreatePrompt(false)}
        />
      </Show>

      {/* Toast notifications */}
      <ToastContainer toasts={toasts()} onDismiss={dismissToast} />
    </box>
  )
}
