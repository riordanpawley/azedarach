/**
 * GitActionHandlersService
 *
 * Git/PR/worktree authority was removed from TUI runtime during the daemon hard-cut.
 * These actions must be implemented via daemon RPC before being fully re-enabled.
 */

import type { CommandExecutor } from "@effect/platform"
import { Effect } from "effect"
import { ToastService } from "../ToastService.js"

const prUnavailable = (action: string) =>
	`PR action '${action}' is unavailable in daemon-rpc runtime; use daemon-backed PR RPC commands.`

export class GitActionHandlersService extends Effect.Service<GitActionHandlersService>()(
	"GitActionHandlersService",
	{
	dependencies: [ToastService.Default],
	effect: Effect.gen(function* () {
		const toast = yield* ToastService

		const unavailable = (
			action: string,
		): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
			toast.show("warning", prUnavailable(action))

		return {
			createPR: () => unavailable("create"),
			updateFromBase: () => unavailable("update-from-base"),
			merge: () => unavailable("merge"),
			cleanup: () => unavailable("cleanup"),
			abortMerge: () => unavailable("abort-merge"),
			showDiff: () => unavailable("show-diff"),
			openPR: () => unavailable("open-pr"),
			doMerge: () => unavailable("merge"),
			enterMergeSelect: () => unavailable("enter-merge-select"),
			confirmMergeSelect: () => unavailable("confirm-merge-select"),
			cancelMergeSelect: () => unavailable("cancel-merge-select"),
		}
	}),
	},
) {}
