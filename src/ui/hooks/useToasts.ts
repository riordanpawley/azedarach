/**
 * useToasts - Hook for toast notification management
 *
 * Wraps ToastService atoms for convenient React usage.
 * Provides show/dismiss functions with proper typing.
 */

import { Result } from "@effect-atom/atom"
import { useAtom, useAtomValue } from "@effect-atom/atom-react"
import { useMemo } from "react"
import { dismissToastAtom, showToastAtom, toastsAtom } from "../atoms"
import type { ToastMessage } from "../Toast"

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

	const actions = useMemo(
		() => ({
			showError: (message: string) => {
				showToast({ type: "error", message }).catch(console.error)
			},

			showSuccess: (message: string) => {
				showToast({ type: "success", message }).catch(console.error)
			},

			showInfo: (message: string) => {
				showToast({ type: "info", message }).catch(console.error)
			},

			dismissToast: (id: string) => {
				dismiss(id).catch(console.error)
			},
		}),
		[showToast, dismiss],
	)

	return {
		toasts,
		...actions,
	}
}
