/**
 * useToasts - Hook for toast notification management
 *
 * Wraps ToastService atoms for convenient React usage.
 * Provides show/dismiss functions with proper typing.
 */

import { Result } from "@effect-atom/atom"
import { useAtom, useAtomValue } from "@effect-atom/atom-react"
import { useMemo } from "react"
import { dismissToastAtom, showToastAtom, toastsAtom } from "../atoms.js"
import type { ToastMessage } from "../Toast.js"

/**
 * Hook for managing toast notifications
 *
 * @example
 * ```tsx
 * const { toasts, showError, showSuccess, showInfo, dismissToast } = useToasts()
 *
 * // Show a toast
 * showSuccess("Task completed!")
 *
 * // Dismiss a toast
 * dismissToast(toastId)
 * ```
 */
export function useToasts() {
	const toastsResult = useAtomValue(toastsAtom)
	const [, showToast] = useAtom(showToastAtom, { mode: "promise" })
	const [, dismiss] = useAtom(dismissToastAtom, { mode: "promise" })

	// Unwrap Result with empty array default
	// Map Toast (createdAt) to ToastMessage (timestamp)
	const toasts: ToastMessage[] = useMemo(() => {
		if (!Result.isSuccess(toastsResult)) return []
		return toastsResult.value.map((t) => ({
			id: t.id,
			message: t.message,
			type: t.type,
			timestamp: t.createdAt,
		}))
	}, [toastsResult])

	// Actions - errors are logged in Effect layer
	const actions = useMemo(
		() => ({
			showError: (message: string) => {
				showToast({ type: "error", message })
			},

			showWarning: (message: string) => {
				showToast({ type: "warning", message })
			},

			showSuccess: (message: string) => {
				showToast({ type: "success", message })
			},

			showInfo: (message: string) => {
				showToast({ type: "info", message })
			},

			dismissToast: (id: string) => {
				dismiss(id)
			},
		}),
		[showToast, dismiss],
	)

	return {
		toasts,
		...actions,
	}
}
