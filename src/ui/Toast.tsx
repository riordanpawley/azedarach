/**
 * Toast component - dismissible error/info notifications
 *
 * Supports multi-line messages (for error suggestions) and longer
 * duration for error toasts to give users time to read actionable guidance.
 */
import { useEffect } from "react"
import { theme } from "./theme.js"

export interface ToastMessage {
	readonly id: string
	readonly message: string
	readonly type: "error" | "info" | "success" | "warning"
	readonly timestamp: number
}

export interface ToastProps {
	toasts: readonly ToastMessage[]
	onDismiss: (id: string) => void
}

/** Default duration for info/success toasts */
const TOAST_DURATION_MS = 5000
/** Longer duration for error toasts (to read suggestions) */
const ERROR_TOAST_DURATION_MS = 8000
const ATTR_BOLD = 1

/**
 * Get toast styling based on type
 */
function getToastStyle(type: ToastMessage["type"]) {
	switch (type) {
		case "error":
			return { bg: theme.red, fg: theme.crust, icon: "!" }
		case "warning":
			return { bg: theme.yellow, fg: theme.crust, icon: "⚠" }
		case "success":
			return { bg: theme.green, fg: theme.crust, icon: "+" }
		default:
			return { bg: theme.blue, fg: theme.crust, icon: "i" }
	}
}

/**
 * Toast container - shows stacked toasts at bottom-right
 */
export const ToastContainer = (props: ToastProps) => {
	// Auto-dismiss toasts after duration (errors get longer to read suggestions)
	useEffect(() => {
		if (props.toasts.length === 0) return

		const timers: NodeJS.Timeout[] = []

		for (const toast of props.toasts) {
			const duration = toast.type === "error" ? ERROR_TOAST_DURATION_MS : TOAST_DURATION_MS
			const elapsed = Date.now() - toast.timestamp
			const remaining = duration - elapsed

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

		return () => {
			for (const timer of timers) {
				clearTimeout(timer)
			}
		}
	}, [props.toasts, props.onDismiss])

	return (
		<box position="absolute" right={2} bottom={2} flexDirection="column" gap={1}>
			{props.toasts.map((toast) => {
				const style = getToastStyle(toast.type)
				// Split message by newlines to support multi-line (error + suggestion)
				const lines = toast.message.split("\n")

				return (
					<box
						key={toast.id}
						backgroundColor={style.bg}
						paddingLeft={1}
						paddingRight={1}
						borderStyle="rounded"
						border={true}
						borderColor={style.bg}
						minWidth={30}
						maxWidth={70}
						flexDirection="column"
					>
						{lines.map((line, idx) => (
							<text
								key={`${toast.id}-line-${idx}`}
								fg={style.fg}
								attributes={idx === 0 ? ATTR_BOLD : undefined}
							>
								{idx === 0 ? ` ${style.icon} ${line} ` : `   ${line} `}
							</text>
						))}
					</box>
				)
			})}
		</box>
	)
}

/**
 * Generate unique toast ID
 */
export function generateToastId(): string {
	return `toast-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`
}
