/**
 * usePaste - Hook for handling terminal paste events
 *
 * OpenTUI's useKeyboard only handles keypress/keyrelease events.
 * Paste events (Cmd+V, bracketed paste) are emitted separately.
 * This hook subscribes to the paste event from the keyHandler.
 *
 * The PasteEvent contains the pasted text in its `text` property.
 */

import type { PasteEvent } from "@opentui/core"
import { useAppContext } from "@opentui/react"
import { useCallback, useEffect, useLayoutEffect, useRef } from "react"

/**
 * Subscribe to paste events.
 *
 * @param handler - Callback receiving PasteEvent with pasted text
 *
 * @example
 * ```tsx
 * const [text, setText] = useState("")
 *
 * usePaste((event) => {
 *   setText(prev => prev + event.text)
 * })
 * ```
 */
export function usePaste(handler: (event: PasteEvent) => void): void {
	const { keyHandler } = useAppContext()

	// Stable handler reference (mirrors useEffectEvent pattern from OpenTUI)
	const handlerRef = useRef(handler)
	useLayoutEffect(() => {
		handlerRef.current = handler
	})
	const stableHandler = useCallback((event: PasteEvent) => {
		handlerRef.current(event)
	}, [])

	useEffect(() => {
		keyHandler?.on("paste", stableHandler)
		return () => {
			keyHandler?.off("paste", stableHandler)
		}
	}, [keyHandler, stableHandler])
}
