import type { ScrollCommand } from "../contracts.js"

export const shouldResetScrollCommandOnPush = (
	overlay:
		| { readonly _tag: "detail"; readonly taskId: string }
		| { readonly _tag: "diagnostics" }
		| { readonly _tag: "help" }
		| { readonly _tag: "settings" }
		| { readonly _tag: "projectSelector" }
		| { readonly _tag: "waitingSessionPicker" }
		| { readonly _tag: "diffViewer"; readonly worktreePath: string; readonly baseBranch: string },
): boolean => overlay._tag === "detail" || overlay._tag === "diagnostics"

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
