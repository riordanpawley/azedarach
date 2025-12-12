/**
 * Toast component - dismissible error/info notifications
 */
import { type Component, For, createEffect, onCleanup } from "solid-js"
import { theme } from "./theme"

export interface ToastMessage {
  readonly id: string
  readonly message: string
  readonly type: "error" | "info" | "success"
  readonly timestamp: number
}

export interface ToastProps {
  toasts: readonly ToastMessage[]
  onDismiss: (id: string) => void
}

const TOAST_DURATION_MS = 5000
const ATTR_BOLD = 1

/**
 * Get toast styling based on type
 */
function getToastStyle(type: ToastMessage["type"]) {
  switch (type) {
    case "error":
      return { bg: theme.red, fg: theme.crust, icon: "!" }
    case "success":
      return { bg: theme.green, fg: theme.crust, icon: "+" }
    case "info":
    default:
      return { bg: theme.blue, fg: theme.crust, icon: "i" }
  }
}

/**
 * Toast container - shows stacked toasts at bottom-right
 */
export const ToastContainer: Component<ToastProps> = (props) => {
  // Auto-dismiss toasts after duration
  createEffect(() => {
    const toasts = props.toasts
    if (toasts.length === 0) return

    const timers: NodeJS.Timeout[] = []

    for (const toast of toasts) {
      const elapsed = Date.now() - toast.timestamp
      const remaining = TOAST_DURATION_MS - elapsed

      if (remaining > 0) {
        const timer = setTimeout(() => {
          props.onDismiss(toast.id)
        }, remaining)
        timers.push(timer)
      } else {
        // Already expired, dismiss immediately
        props.onDismiss(toast.id)
      }
    }

    onCleanup(() => {
      for (const timer of timers) {
        clearTimeout(timer)
      }
    })
  })

  return (
    <box
      position="absolute"
      right={2}
      bottom={2}
      flexDirection="column"
      gap={1}
    >
      <For each={props.toasts}>
        {(toast) => {
          const style = getToastStyle(toast.type)
          return (
            <box
              backgroundColor={style.bg}
              paddingLeft={1}
              paddingRight={1}
              borderStyle="rounded"
              border={true}
              borderColor={style.bg}
              minWidth={30}
              maxWidth={60}
            >
              <text fg={style.fg} attributes={ATTR_BOLD}>
                {` ${style.icon} ${toast.message} `}
              </text>
            </box>
          )
        }}
      </For>
    </box>
  )
}

/**
 * Generate unique toast ID
 */
export function generateToastId(): string {
  return `toast-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`
}
