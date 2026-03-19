/**
 * ViewService - Manages view mode state (kanban/compact)
 *
 * Provides reactive state for switching between different UI view modes.
 */

import { Effect, SubscriptionRef } from "effect"

/**
 * View mode for the task board
 */
export type ViewMode = "kanban" | "compact"

export class ViewService extends Effect.Service<ViewService>()("ViewService", {
	effect: Effect.gen(function* () {
		const viewMode = yield* SubscriptionRef.make<ViewMode>("kanban")

		return {
			// Expose SubscriptionRef for atom subscription
			viewMode,

			/**
			 * Get current view mode
			 */
			getViewMode: () => SubscriptionRef.get(viewMode),

			/**
			 * Set view mode
			 */
			setViewMode: (mode: ViewMode) => SubscriptionRef.set(viewMode, mode),

			/**
			 * Toggle between kanban and compact view
			 */
			toggleViewMode: () =>
				SubscriptionRef.update(viewMode, (current) =>
					current === "kanban" ? "compact" : "kanban",
				),
		}
	}),
}) {}
