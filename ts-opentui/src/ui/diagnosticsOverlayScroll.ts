import type { ScrollCommand } from "../services/OverlayService.js"

/**
 * Scroll commands are delivered via SubscriptionRef (last-value).
 * Ignore commands emitted before overlay mount and already-handled commands.
 */
export const shouldApplyDiagnosticsScrollCommand = (
	command: ScrollCommand | null,
	overlayOpenedAtMs: number,
	lastHandledTimestamp: number | null,
): command is ScrollCommand => {
	if (command === null) return false
	if (command.target !== "diagnostics") return false
	if (command.timestamp < overlayOpenedAtMs) return false
	if (lastHandledTimestamp !== null && command.timestamp === lastHandledTimestamp) return false
	return true
}
