# Effect Testing Skill

**Version:** 1.0
**Purpose:** Test-Driven Development for TypeScript/Effect with Bun

## Overview

TDD with Effect requires understanding how to test effectful computations. This skill covers patterns for testing Effect services, React/OpenTUI components, and async workflows.

**Core Principle:** Tests are **specifications**, not just coverage metrics.

## TDD Cycle

### 1. Write Failing Test (RED)

```typescript
import { describe, it, expect } from "bun:test"

describe("TaskCard component", () => {
    it("renders task title", () => {
        const result = render(<TaskCard title="Test Task" />)
        expect(result).toContain("Test Task")
    })
})

// Run: bun test
// Expected: FAIL (red) - no implementation exists yet
```

### 2. Write Minimal Implementation (GREEN)

**CRITICAL:** Write ONLY enough code to make test pass. Don't over-engineer.

```typescript
export const TaskCard = ({ title }: { title: string }) => {
    return <text>{title}</text>
}

// Run: bun test
// Expected: PASS (green)
```

### 3. Refactor (if needed)

Only refactor AFTER tests pass:

```typescript
const Text = ({ children }: { children: string }) => (
    <text>{children}</text>
)

export const TaskCard = ({ title }: { title: string }) => (
    <Text>{title}</Text>
)

// Run: bun test
// Expected: Still PASS (green)
```

## Testing Effect Services

### Basic Effect Test

```typescript
import { Effect } from "effect"
import { describe, it, expect } from "bun:test"

describe("SessionManager", () => {
    it("creates worktree", async () => {
        const program = Effect.gen(function* () {
            const result = yield* createWorktree("test-task")
            return result
        })

        const result = await Effect.runPromise(program)
        expect(result.success).toBe(true)
    })
})
```

### Testing with Service Dependencies

```typescript
import { Effect, Layer, Context } from "effect"

// Define test layer with mock
const TestGitService = Layer.succeed(
    GitService,
    GitService.of({
        createWorktree: () => Effect.succeed({ path: "/tmp/test" }),
        deleteWorktree: () => Effect.succeed(undefined)
    })
)

describe("WorktreeManager", () => {
    it("creates worktree using GitService", async () => {
        const program = Effect.gen(function* () {
            const manager = yield* WorktreeManager
            return yield* manager.create("test-task")
        })

        const result = await program.pipe(
            Effect.provide(TestGitService),
            Effect.runPromise
        )

        expect(result.path).toBe("/tmp/test")
    })
})
```

### Testing Error Scenarios

```typescript
import { Effect } from "effect"

describe("SessionManager errors", () => {
    it("fails gracefully on git error", async () => {
        const program = createSession("invalid-task").pipe(
            Effect.catchTag("GitError", (e) =>
                Effect.succeed({ error: e.message })
            )
        )

        const result = await Effect.runPromise(program)
        expect(result.error).toBeDefined()
    })
})
```

### Testing with TestClock

```typescript
import { Effect, TestClock, Fiber } from "effect"

describe("Scheduler", () => {
    it("schedules task with delay", async () => {
        const program = Effect.gen(function* () {
            const fiber = yield* scheduleTask("delayed", 1000).pipe(
                Effect.fork
            )
            
            // Fast-forward time
            yield* TestClock.adjust("1 second")
            
            return yield* Fiber.join(fiber)
        })

        const result = await Effect.runPromise(program)
        expect(result.executed).toBe(true)
    })
})
```

## Testing React/OpenTUI Components

### Basic Component Test

```typescript
import { render, screen } from "@testing-library/react"
import { TaskCard } from "./TaskCard"

describe("TaskCard", () => {
    it("renders task title", () => {
        render(<TaskCard title="Test Task" />)
        expect(screen.getByText("Test Task")).toBeInTheDocument()
    })

    it("shows status indicator", () => {
        render(<TaskCard title="Test" status="in_progress" />)
        expect(screen.getByRole("status")).toHaveTextContent("🔵")
    })
})
```

### Testing User Interactions

```typescript
import { render, fireEvent } from "@testing-library/react"

describe("TaskCard interactions", () => {
    it("calls onSelect when clicked", () => {
        const onSelect = vi.fn()
        render(<TaskCard title="Test" onSelect={onSelect} />)
        
        fireEvent.click(screen.getByRole("button"))
        
        expect(onSelect).toHaveBeenCalledTimes(1)
    })
})
```

## AI Guardrails

### CRITICAL: Human Verification Required

**NEVER trust AI-generated tests uncritically.**

After AI generates tests or implementation code:

1. **Review the test** - Does it actually test the requirement?
2. **Run the test** - Verify it fails before implementing
3. **Review implementation** - Is it minimal? Does it match the test intent?
4. **Run again** - Verify test now passes
5. **Check edge cases** - Did we miss any scenarios?

### Anti-Patterns to Avoid

**❌ Write tests AFTER implementation:**
```typescript
// BAD: Implementation exists, test written to pass
export function calculateDiscount(price: number): number {
    return price * 0.9
}

// Test written to match implementation (wrong order!)
test("discount", () => {
    expect(calculateDiscount(100)).toBe(90)  // Locks in bug
})
```

**✅ CORRECT: Write test FIRST as spec:**
```typescript
// GOOD: Test specifies behavior first
test("applies 10% discount", () => {
    expect(calculateDiscount(100)).toBe(90)
})

// Implementation written to satisfy spec
export function calculateDiscount(price: number): number {
    return price * 0.9
}
```

### Never Delete Failing Tests

```typescript
// ❌ WRONG: Delete test to "fix" build
test("edge case", () => {
    expect(handleEdgeCase()).toBe("expected")
})

// ✅ CORRECT: Fix implementation to satisfy test
function handleEdgeCase(): string {
    return "expected"  // Actually implement the logic
}
```

## Test Quality Guidelines

### Clear Test Names

```typescript
// ❌ BAD: Vague
test("works", () => { ... })

// ✅ GOOD: Specific behavior
test("creates worktree with correct naming convention", () => { ... })
```

### Arrange-Act-Assert

```typescript
test("applies discount for premium users", () => {
    // Arrange
    const user = createPremiumUser()
    const price = 100

    // Act
    const discounted = applyDiscount(user, price)

    // Assert
    expect(discounted).toBe(80)  // 20% off
})
```

### One Assertion Per Test

```typescript
// ❌ BAD: Multiple assertions obscures failures
test("task", () => {
    const task = createTask()
    expect(task.title).toBeDefined()
    expect(task.status).toBe("open")
    expect(task.assignee).toBe("user")  // Which one failed?
})

// ✅ GOOD: One assertion each
test("task has title", () => {
    const task = createTask()
    expect(task.title).toBeDefined()
})

test("task has open status", () => {
    const task = createTask()
    expect(task.status).toBe("open")
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

# Watch mode
bun test --watch

# Run tests matching pattern
bun test --test-name-pattern "SessionManager"
```

## Checklist: AI-Generated Code

Before accepting AI-generated tests or implementation:

- [ ] Test **fails** before I implement the feature
- [ ] Test **passes** after minimal implementation
- [ ] Test name describes **behavior**, not implementation
- [ ] Test has **one assertion** (or logically related group)
- [ ] Edge cases are **covered** (zero, negative, large values)
- [ ] Test is **independent** (doesn't rely on previous tests)
- [ ] Implementation is **minimal** (only what test requires)
- [ ] No **type errors** or lint warnings
- [ ] Tests run **successfully** in CI

**If ANY fail:** Ask AI to fix before proceeding.

## References

- [Bun Test Documentation](https://bun.sh/docs/test)
- [Effect Testing Patterns](https://effect.website/docs/guides/testing)
- [Testing Library for React](https://testing-library.com/docs/react-testing-library/intro)
