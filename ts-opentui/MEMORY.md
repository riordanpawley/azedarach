# MEMORY - ts-opentui Testing Patterns

**Purpose:** Testing patterns and guardrails for Effect/OpenTUI development

## Test Framework

**Primary:** `bun:test` (built-in to Bun)
- Fast native TypeScript runner
- Built-in mock timers (`setImmediate`, `clearImmediate`)
- No external dependencies

**Secondary:** `@effect/experimental` for Effect testing utilities

## Testing Effect Services

### Pattern: Service with Test Context

```typescript
import { Effect, Context, TestClock, TestRandom } from "effect"

class MyService extends Effect.Service<MyService>()("MyService", {
  scoped: Effect.gen(function* () {
    return {
      // Methods here
      doWork: (value: string) => Effect.succeed(value.toUpperCase())
    }
  })
}) {}

describe("MyService", () => {
  it("processes input", async () => {
    const result = await Effect.runPromise(
      MyService.pipe(
        Effect.flatMap(service => service.doWork("hello")),
      ),
      {
        context: Context.make()
          .add(TestClock)  // Control time in tests
          .add(TestRandom) // Deterministic randomness
      }
    )

    expect(result).toBe("HELLO")
  })
})
```

### Pattern: Testing Async Operations

```typescript
import { Effect, TestClock } from "effect"

describe("async operations", () => {
  it("completes after delay", async () => {
    const result = await Effect.runPromise(
      Effect.gen(function* () {
        yield* Effect.sleep("1 second")
        return "done"
      }),
      { context: Context.make().add(TestClock) }
    )

    expect(result).toBe("done")
  })

  it("supports time travel", async () => {
    let processed = false

    const effect = Effect.gen(function* () {
      yield* Effect.delay("1 day")
      processed = true
    })

    await Effect.runPromise(effect, { context: Context.make().add(TestClock) })

    // TestClock automatically advances time - no real delay
    expect(processed).toBe(true)
  })
})
```

### Pattern: Error Handling

```typescript
import { Effect } from "effect"

describe("error handling", () => {
  it("fails with custom error", async () => {
    const CustomError = Schema.Struct({ message: Schema.String })
    const error = CustomError.make({ message: "test error" })

    const result = await Effect.runPromise(
      Effect.fail(error)
    )

    expect(result).toEqual({
      _tag: "Failure",
      cause: error
    })
  })

  it("recovers from error", async () => {
    const result = await Effect.runPromise(
      Effect.fail(new Error("fail"))
        .pipe(Effect.catchAll(() => Effect.succeed("recovered")))
    )

    expect(result).toBe("recovered")
  })
})
```

## Testing OpenTUI Components

### Pattern: Component Rendering

```typescript
import { describe, it, expect, mock } from "bun:test"
import { render } from "@testing-library/react"
import { TaskCard } from "./TaskCard"

describe("TaskCard", () => {
  it("renders task title", () => {
    const { getByText } = render(<TaskCard title="Test Task" />)
    expect(getByText("Test Task")).toBeTruthy()
  })

  it("renders status indicator", () => {
    const { getByTestId } = render(<TaskCard status="in_progress" />)
    const indicator = getByTestId("task-status")
    expect(indicator.textContent).toContain("🔵")
  })
})
```

### Pattern: Event Handling

```typescript
describe("TaskCard interactions", () => {
  it("calls onSelect when clicked", () => {
    const onSelect = mock()
    const { getByRole } = render(
      <TaskCard task={task} onSelect={onSelect} />
    )

    const card = getByRole("button")
    card.click()

    expect(onSelect).toHaveBeenCalled()
    expect(onSelect).toHaveBeenCalledWith(task)
  })
})
```

### Pattern: Keyboard Events

```typescript
describe("keyboard navigation", () => {
  it("handles down arrow", () => {
    const { getByTestId } = render(<Board tasks={tasks} />)
    const board = getByTestId("board")

    // Simulate keydown event
    board.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown" }))

    // Verify cursor moved
    expect(board.querySelector(".selected")).toBe(tasks[1].id)
  })
})
```

## Testing TUI Logic

### Pattern: State Transitions

```typescript
import { describe, it, expect } from "bun:test"

describe("Board state", () => {
  it("transitions from open to in_progress", () => {
    const board = createBoard()
    const task = { id: "az-1", status: "open" as const }

    const updated = board.handleSelect(task)
    expect(updated.status).toBe("in_progress")
  })
})
```

### Pattern: Mode Switching

```typescript
describe("mode transitions", () => {
  it("switches from normal to select", () => {
    const app = createApp()
    const result = app.handleKey("v")

    expect(result.mode).toBe("select")
    expect(result.cursor).toBe(app.cursor)  // Cursor preserved
  })

  it("clears selections on cancel", () => {
    const app = createApp({ mode: "select", selected: new Set(["az-1"]) })
    const result = app.handleKey("Esc")

    expect(result.mode).toBe("normal")
    expect(result.selected).toBeEmpty()
  })
})
```

## Testing External Dependencies

### Pattern: Mocking CLI Calls

```typescript
import { describe, it, expect, mock } from "bun:test"
import { Effect } from "effect"
import { Command } from "@effect/platform"

describe("BeadsClient", () => {
  it("lists tasks via bd CLI", async () => {
    // Mock Command.run
    const commandRun = mock(() => Effect.succeed(`[{"id":"az-1","title":"Test"}]`))
    mock.module("@effect/platform", {
      Command: {
        run: commandRun
      }
    })

    const result = await Effect.runPromise(
      listTasks(),
      { context: Context.make() }
    )

    expect(result).toEqual([{ id: "az-1", title: "Test" }])
    expect(commandRun).toHaveBeenCalledWith({
      name: "bd",
      args: ["list", "--format=json"]
    })
  })
})
```

### Pattern: Testing Tmux Integration

```typescript
describe("TmuxService", () => {
  it("creates session with correct name", async () => {
    const commandRun = mock(() => Effect.succeed(""))
    mock.module("@effect/platform", {
      Command: { run: commandRun }
    })

    const result = await Effect.runPromise(
      createSession("az-1"),
      { context: Context.make() }
    )

    expect(commandRun).toHaveBeenCalledWith({
      name: "tmux",
      args: ["new-session", "-d", "-s", "az-1"]
    })
  })
})
```

## Common Test Patterns

### Testing Schema Validation

```typescript
import { Schema } from "effect"
import { describe, it, expect } from "bun:test"

describe("ConfigSchema", () => {
  it("validates required port", async () => {
    const invalidConfig = { port: "not a number" }
    const result = await Effect.runPromise(
      ConfigSchema.decodeUnknown(invalidConfig)
    )

    expect(result._tag).toBe("Left")
    if (result._tag === "Left") {
      expect(result.left).toContain("port")
    }
  })

  it("accepts valid config", async () => {
    const validConfig = { port: 8080 }
    const result = await Effect.runPromise(
      ConfigSchema.decodeUnknown(validConfig)
    )

    expect(result._tag).toBe("Right")
    if (result._tag === "Right") {
      expect(result.right.port).toBe(8080)
    }
  })
})
```

### Testing State Machines

```typescript
describe("session state machine", () => {
  it("transitions idle → busy on start", () => {
    const state = createSessionState("idle")
    const next = handleStart(state)

    expect(next.value).toBe("busy")
    expect(next.timestamp).toBeGreaterThan(0)
  })

  it("transitions busy → done on completion", () => {
    const state = createSessionState("busy")
    const next = handleDone(state)

    expect(next.value).toBe("done")
    expect(next.result).toBeDefined()
  })
})
```

## Running Tests

```bash
# Run all tests
bun test

# Run specific file
bun test src/core/SessionManager.test.ts

# Run with coverage
bun test --coverage

# Watch mode (rerun on changes)
bun test --watch

# Run tests matching pattern
bun test --test-name-pattern="Session"
```

## Guardrails Checklist

Before accepting AI-generated code or tests:

- [ ] Tests **fail** before implementation (RED)
- [ ] Tests **pass** after minimal implementation (GREEN)
- [ ] No type errors (`bun run type-check`)
- [ ] No lint warnings (`biome check src/`)
- [ ] Tests are **independent** (no side effects between tests)
- [ ] Tests have **clear names** describing behavior
- [ ] Edge cases covered (zero, negative, boundaries)
- [ ] External dependencies mocked (CLI, tmux, git)
- [ ] Effect services use TestContext (TestClock, TestRandom)
- [ ] No `any` types (always specify types)
- [ ] No suppressed errors (@ts-ignore, as any)

## Troubleshooting

### Test Timeout

```typescript
// Increase timeout for slow tests
it("handles long operation", async () => {
  // bun test default: 5000ms
  // Use done callback with custom timeout
}, { timeout: 10000 })
```

### Flaky Tests (Non-deterministic)

```typescript
// GOOD: TestRandom with fixed seed for determinism
import { TestRandom, Effect } from "effect"

describe("random selection", () => {
  it("produces consistent results with fixed seed", async () => {
    // Create TestRandom service with fixed seed
    const random = TestRandom(42)  // Fixed seed = deterministic

    const result = await Effect.runPromise(
      Effect.gen(function* () {
        const r = yield* Effect.random
        return r.nextIntBetween(1, 100)
      }),
      { context: Context.make().add(random) }
    )

    // Result is ALWAYS the same (deterministic)
    expect(result).toBe(73)  // Seed 42 always produces 73
  })
})
```

## References

- [Bun Testing Documentation](https://bun.sh/docs/test)
- [Effect Testing Guide](https://effect.website/docs/testing)
- [Testing Library for React](https://testing-library.com/docs/react-testing-library/intro)
- [React Testing Best Practices](https://kentcdodds.com/blog/common-mistakes-with-react-testing-library/)
