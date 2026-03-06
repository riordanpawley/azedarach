# Effect Atom Interaction Skill

**Version:** 1.0  
**Purpose:** Move multi-step UI interaction behavior out of React handlers and into Effect/atom actions.

## When To Use

Use this skill when a React/OpenTUI handler is coordinating more than simple event normalization, especially when it:

- chains async actions (`await`, `.then`, mode transitions, follow-up key dispatch)
- applies navigation + mode + overlay logic in the component
- duplicates keyboard semantics in React land

## Core Rule

React event handlers should be **thin adapters**:

- normalize event payload (button, deltas, selected id snapshot)
- call a single atom action

The orchestration should live in `appRuntime.fn(...)` actions that use services.

## Implementation Pattern

1. Define a typed action input in `ts-opentui/src/ui/atoms/<domain>.ts`.
2. Implement orchestration with Effect services (for example: `NavigationService`, `EditorService`, `OverlayService`, `KeyboardService`).
3. Keep compatibility with keyboard behavior by delegating to `KeyboardService.handleKey(...)` when behavior parity matters.
4. Export atom actions from `ts-opentui/src/ui/atoms/index.ts`.
5. Replace React async logic with one atom dispatch call.

## Skeleton

```ts
export interface FooInteractionParams {
	taskId: string
	button: "left" | "right"
	selectedTaskId: string | undefined
}

export const handleFooInteractionAtom = appRuntime.fn((params: FooInteractionParams) =>
	Effect.gen(function* () {
		const overlay = yield* OverlayService
		if (yield* overlay.current()) return

		const nav = yield* NavigationService
		yield* nav.jumpToTask(params.taskId)

		const keyboard = yield* KeyboardService
		yield* keyboard.handleKey("space")
	}).pipe(Effect.catchAll(Effect.logError)),
)
```

## React Handler Standard

```ts
const handleMouseDown = (event: MouseEvent) => {
	if (event.button !== MouseButton.LEFT) return
	event.preventDefault()
	event.stopPropagation()
	void handleFooInteraction({ ...normalizedPayload })
}
```

No multi-step async control flow in the component.

## Anti-Patterns

- `await` orchestration in React handlers for navigation/mode/overlay transitions
- duplicating keyboard state machine logic directly in components
- mixing business rules across component and atom layers

## Verification

- `bun run type-check`
- `bun run build`
- manual UI behavior check in terminal for interaction parity
