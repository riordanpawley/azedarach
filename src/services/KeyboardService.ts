/**
 * KeyboardService - Data-driven keyboard handler in Effect-land
 *
 * Manages keyboard bindings using a data-driven approach with Effect.Service pattern.
 * Keybindings are stored as data structures with associated Effect actions,
 * allowing for dynamic registration, mode-based filtering, and overlay precedence.
 */

import { Effect, Ref } from "effect"
import { ToastService } from "./ToastService"
import { OverlayService } from "./OverlayService"
import { NavigationService } from "./NavigationService"
import { EditorService } from "./EditorService"
import { BoardService } from "./BoardService"

// ============================================================================
// Types
// ============================================================================

/**
 * Keybinding definition with mode-specific action
 */
export interface Keybinding {
	readonly key: string
	readonly mode: "normal" | "select" | "command" | "search" | "overlay" | "*"
	readonly description: string
	readonly action: Effect.Effect<void>
}

// ============================================================================
// Service Definition
// ============================================================================

export class KeyboardService extends Effect.Service<KeyboardService>()(
	"KeyboardService",
	{
		// Declare dependencies - Effect handles the wiring
		dependencies: [
			ToastService.Default,
			OverlayService.Default,
			NavigationService.Default,
			EditorService.Default,
			BoardService.Default,
		],

		effect: Effect.gen(function* () {
			// Inject all services we need for actions
			const toast = yield* ToastService
			const overlay = yield* OverlayService
			const nav = yield* NavigationService
			const editor = yield* EditorService
			const board = yield* BoardService

			/**
			 * Helper: Open detail overlay for current cursor position
			 */
			const openCurrentDetail = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const cursor = yield* nav.getCursor()
					const task = yield* board.getTaskAt(cursor.columnIndex, cursor.taskIndex)
					if (task) {
						yield* overlay.push({ _tag: "detail", taskId: task.id })
					}
				})

			/**
			 * Helper: Toggle selection for task at current cursor position
			 */
			const toggleCurrentSelection = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const cursor = yield* nav.getCursor()
					const task = yield* board.getTaskAt(cursor.columnIndex, cursor.taskIndex)
					if (task) {
						yield* editor.toggleSelection(task.id)
					}
				})

			/**
			 * Helper: Handle escape key based on current context
			 *
			 * Priority:
			 * 1. Close overlay if one is open
			 * 2. Exit to normal mode if in another mode
			 */
			const handleEscape = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const hasOverlay = yield* overlay.isOpen()
					if (hasOverlay) {
						yield* overlay.pop()
						return
					}
					const mode = yield* editor.getMode()
					if (mode._tag !== "normal") {
						yield* editor.exitToNormal()
					}
				})

			// Default keybindings - defined as data, not switch statements
			// Actions are Effect values that can be executed when the key is pressed
			const defaultBindings: ReadonlyArray<Keybinding> = [
				// Navigation (normal mode)
				{
					key: "j",
					mode: "normal",
					description: "Move down",
					action: Effect.suspend(() => nav.move("down")),
				},
				{
					key: "k",
					mode: "normal",
					description: "Move up",
					action: Effect.suspend(() => nav.move("up")),
				},
				{
					key: "h",
					mode: "normal",
					description: "Move left",
					action: Effect.suspend(() => nav.move("left")),
				},
				{
					key: "l",
					mode: "normal",
					description: "Move right",
					action: Effect.suspend(() => nav.move("right")),
				},
				{
					key: "g",
					mode: "normal",
					description: "Go to top",
					action: Effect.suspend(() => nav.jumpTo(0, 0)),
				},
				{
					key: "G",
					mode: "normal",
					description: "Go to bottom",
					action: Effect.suspend(() => nav.jumpToEnd()),
				},

				// Overlays
				{
					key: "?",
					mode: "normal",
					description: "Show help",
					action: Effect.suspend(() => overlay.push({ _tag: "help" })),
				},
				{
					key: "c",
					mode: "normal",
					description: "Create task",
					action: Effect.suspend(() => overlay.push({ _tag: "create" })),
				},
				{
					key: "Enter",
					mode: "normal",
					description: "View detail",
					action: Effect.suspend(() => openCurrentDetail()),
				},
				{
					key: ",",
					mode: "normal",
					description: "Settings",
					action: Effect.suspend(() => overlay.push({ _tag: "settings" })),
				},

				// Mode transitions
				{
					key: "v",
					mode: "normal",
					description: "Select mode",
					action: Effect.suspend(() => editor.enterSelect()),
				},
				{
					key: ":",
					mode: "normal",
					description: "Command mode",
					action: Effect.suspend(() => editor.enterCommand()),
				},
				{
					key: "/",
					mode: "normal",
					description: "Search",
					action: Effect.suspend(() => editor.enterSearch()),
				},

				// Universal escape
				{
					key: "Escape",
					mode: "*",
					description: "Exit/cancel",
					action: Effect.suspend(() => handleEscape()),
				},

				// Select mode
				{
					key: "Space",
					mode: "select",
					description: "Toggle selection",
					action: Effect.suspend(() => toggleCurrentSelection()),
				},
				{
					key: "j",
					mode: "select",
					description: "Move down",
					action: Effect.suspend(() => nav.move("down")),
				},
				{
					key: "k",
					mode: "select",
					description: "Move up",
					action: Effect.suspend(() => nav.move("up")),
				},

				// Overlay mode
				{
					key: "Escape",
					mode: "overlay",
					description: "Close overlay",
					action: Effect.suspend(() => overlay.pop()),
				},
			]

			const keybindings = yield* Ref.make<ReadonlyArray<Keybinding>>(
				defaultBindings,
			)

			/**
			 * Helper: Get current context for keybinding matching
			 */
			const getContext = () =>
				Effect.gen(function* () {
					const mode = yield* editor.getMode()
					const hasOverlay = yield* overlay.isOpen()
					return { mode, hasOverlay }
				})

			/**
			 * Helper: Find matching keybinding for key and context
			 *
			 * Priority: overlay > specific mode > wildcard "*"
			 */
			const findBinding = (
				key: string,
				mode: string,
				hasOverlay: boolean,
			): Effect.Effect<Keybinding | undefined> =>
				Effect.gen(function* () {
					const bindings = yield* Ref.get(keybindings)

					// Priority: overlay > specific mode > wildcard
					const effectiveMode = hasOverlay ? "overlay" : mode

					return (
						bindings.find((b) => b.key === key && b.mode === effectiveMode) ??
						bindings.find((b) => b.key === key && b.mode === "*")
					)
				})

			return {
				// State refs
				keybindings,

				/**
				 * Handle a key press
				 *
				 * Gets current context (mode, overlay status), finds matching binding,
				 * and executes its action if found.
				 */
				handleKey: (key: string): Effect.Effect<void> =>
					Effect.gen(function* () {
						const { mode, hasOverlay } = yield* getContext()
						const binding = yield* findBinding(key, mode._tag, hasOverlay)

						if (binding) {
							yield* binding.action
						}
						// Unknown key - ignore (or could show toast in debug mode)
					}),

				/**
				 * Register a new keybinding
				 *
				 * Adds the binding to the end of the keybindings array.
				 * If you want to override an existing binding, unregister it first.
				 */
				register: (binding: Keybinding): Effect.Effect<void> =>
					Ref.update(keybindings, (bs) => [...bs, binding]),

				/**
				 * Unregister a keybinding
				 *
				 * Removes all keybindings matching the given key and mode.
				 */
				unregister: (key: string, mode: Keybinding["mode"]): Effect.Effect<void> =>
					Ref.update(keybindings, (bs) =>
						bs.filter((b) => !(b.key === key && b.mode === mode)),
					),

				/**
				 * Get all registered keybindings
				 */
				getBindings: (): Effect.Effect<ReadonlyArray<Keybinding>> =>
					Ref.get(keybindings),
			}
		}),
	},
) {}
