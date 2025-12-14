/**
 * Runtime layer for new atomic Effect services
 *
 * This module provides the layer composition for the new Effect.Service-based
 * services (ToastService, OverlayService, NavigationService, etc.)
 *
 * The services use fine-grained Refs for state management, allowing atomic
 * updates and efficient UI subscription via effect-atom.
 */

import { Layer } from "effect"
import { BoardService } from "../services/BoardService"
// Alias to avoid collision with core/EditorService (external $EDITOR)
import { EditorService as ModeService } from "../services/EditorService"
import { KeyboardService } from "../services/KeyboardService"
import { NavigationService } from "../services/NavigationService"
import { OverlayService } from "../services/OverlayService"
import { SessionService } from "../services/SessionService"
import { ToastService } from "../services/ToastService"

// ============================================================================
// Layer Composition
// ============================================================================

/**
 * Independent services with no dependencies
 *
 * These can be created directly as they don't depend on other services.
 */
export const independentServicesLayer = Layer.mergeAll(
	ToastService.Default,
	OverlayService.Default,
	NavigationService.Default,
	ModeService.Default,
)

/**
 * BoardService layer
 *
 * Note: BoardService uses yield* BeadsClient and yield* SessionManager
 * inside its effect (not in dependencies array) because those services
 * use the old Context.Tag pattern. The layer must be provided with
 * BeadsClient and SessionManager in context.
 */
export const boardServiceLayer = BoardService.Default

/**
 * SessionService layer
 *
 * Has dependencies on ToastService, NavigationService, and BoardService
 * declared via the Effect.Service dependencies array.
 */
export const sessionServiceLayer = SessionService.Default

/**
 * KeyboardService layer
 *
 * Has dependencies on ToastService, OverlayService, NavigationService,
 * EditorService (ModeService), and BoardService.
 */
export const keyboardServiceLayer = KeyboardService.Default

/**
 * Combined layer for all new atomic services
 *
 * Usage: Merge with appLayer in atoms.ts to get a unified runtime.
 *
 * ```typescript
 * const fullAppLayer = appLayer.pipe(
 *   Layer.provideMerge(atomicServicesLayer)
 * )
 * ```
 */
export const atomicServicesLayer = Layer.mergeAll(
	independentServicesLayer,
	boardServiceLayer,
	sessionServiceLayer,
	keyboardServiceLayer,
)

/**
 * Re-export services for convenience
 */
export {
	ToastService,
	OverlayService,
	NavigationService,
	ModeService,
	BoardService,
	SessionService,
	KeyboardService,
}
