# TDD Workflow Skill

**Version:** 1.0
**Purpose:** Test-Driven Development workflow with AI guardrails to prevent automation bias

## Overview

TDD (Test-Driven Development) with AI assistance requires **human-in-the-loop verification** to prevent "automation bias" - the tendency to trust AI-generated code/tests uncritically.

**Core Principle:** Tests are **specifications**, not just coverage metrics.

## TDD Cycle

### 1. Write Failing Test (RED)

```typescript
// Effect/TypeScript example
import { describe, it } from "bun:test"

describe("TaskCard component", () => {
    it("renders task title", () => {
        const result = render(<TaskCard title="Test Task" />)
        expect(result).toContain("Test Task")
    })
})

// Run: bun test
// Expected: FAIL (red) - no implementation exists yet
```

```go
// Go example
func TestTaskCardTitle(t *testing.T) {
    card := TaskCard{Title: "Test Task"}
    want := "Test Task"
    got := card.Title()

    if got != want {
        t.Errorf("Title() = %q, want %q", got, want)
    }
}

// Run: go test
// Expected: FAIL (red) - no implementation exists yet
```

### 2. Write Minimal Implementation (GREEN)

**CRITICAL:** Write ONLY enough code to make test pass. Don't over-engineer.

```typescript
// Effect/TypeScript - Minimal implementation
export const TaskCard = ({ title }: { title: string }) => {
    return <text>{title}</text>
}

// Run: bun test
// Expected: PASS (green)
```

```go
// Go - Minimal implementation
type TaskCard struct {
    Title string
}

func (c TaskCard) Title() string {
    return c.Title
}

// Run: go test
// Expected: PASS (green)
```

### 3. Refactor (if needed)

Only refactor AFTER tests pass, with test safety net:

```typescript
// Extract reusable component
const Text = ({ children }: { children: string }) => (
    <text>{children}</text>
)

export const TaskCard = ({ title }: { title: string }) => (
    <Text>{title}</Text>
)

// Run: bun test
// Expected: Still PASS (green)
```

```go
// Extract method
func Title(card TaskCard) string {
    return card.Title
}

// Run: go test
// Expected: Still PASS (green)
```

### 4. Repeat

Add next test case, repeat cycle.

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

### Avoid "Testing Implementation Details"

```typescript
// ❌ BAD: Tests internal structure
test("component has state", () => {
    const component = new Component()
    expect(component.state.title).toBeDefined()
})

// ✅ GOOD: Tests behavior (black-box)
test("displays task title", () => {
    const result = render(<Component title="Task 1" />)
    expect(result).toContain("Task 1")
})
```

## Testing Patterns

### Effect/TypeScript (ts-opentui)

**Test Framework:** `bun:test` (built-in to Bun)

**Testing Effect Services:**
```typescript
import { Effect, TestClock, Context } from "effect"

describe("SessionManager", () => {
    it("creates worktree", async () => {
        const result = await Effect.runPromise(
            createWorktree("test-task"),
            {
                context: Context.make().add(TestClock),
            }
        )
        expect(result.success).toBe(true)
    })
})
```

**Testing React Components:**
```typescript
import { render, screen } from "@testing-library/react"
import { TaskCard } from "./TaskCard"

describe("TaskCard", () => {
    it("renders task title", () => {
        render(<TaskCard title="Test Task" />)
        expect(screen.getByText("Test Task")).toBeInTheDocument()
    })
})
```

### Go (go-bubbletea)

**Test Framework:** Built-in `testing` package

**Testing Bubbletea Models:**
```go
func TestBoardModelInit(t *testing.T) {
    m := NewBoardModel()
    cmd := m.Init()

    if cmd != nil {
        t.Errorf("Init() should return nil, got %v", cmd)
    }
}
```

**Testing Commands:**
```go
func TestUpdate(t *testing.T) {
    m := NewBoardModel()
    msg := tea.KeyMsg{Type: tea.KeyEnter}

    newModel, cmd := m.Update(msg)

    // Verify state changed
    if newModel.state != StateDetail {
        t.Errorf("Expected state %d, got %d", StateDetail, newModel.state)
    }
}
```

**Testing Services with Mocks:**
```go
func TestBeadsClientList(t *testing.T) {
    runner := &mockRunner{output: []byte(`[{"id":"az-1"}]`)}
    client := NewClient(runner, slog.Default())

    tasks, err := client.List(context.Background())

    if err != nil {
        t.Fatalf("List() error = %v", err)
    }
    if len(tasks) != 1 {
        t.Errorf("Expected 1 task, got %d", len(tasks))
    }
}
```

## When to Test

### Test When:

- **Public API functions/methods** - External contracts matter
- **Business logic** - Algorithms, calculations, transformations
- **State transitions** - UI state changes, model updates
- **Error handling** - Edge cases, invalid inputs
- **Integration points** - Service boundaries, external dependencies

### Don't Test When:

- **Trivial getters/setters** - Unless they have logic
- **Generated code** - Unless you control the generator
- **External libraries** - Trust their tests (report bugs instead)
- **Private implementation details** - Test public behavior

## Test Quality Guidelines

### ONE: Clear Test Names

```typescript
// ❌ BAD: Vague
test("works", () => { ... })

// ✅ GOOD: Specific behavior
test("creates worktree with correct naming", () => { ... })
```

### TWO: Arrange-Act-Assert

```typescript
// Clear AAA pattern
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

### THREE: One Assertion Per Test

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

### FOUR: Test Edge Cases

```typescript
describe("calculateDiscount", () => {
    it("handles zero price", () => {
        expect(calculateDiscount(0)).toBe(0)
    })

    it("handles negative price", () => {
        expect(calculateDiscount(-10)).toBe(0)  // No negative discounts
    })

    it("handles very large price", () => {
        expect(calculateDiscount(1000000)).toBe(900000)
    })
})
```

## Running Tests

### Effect/TypeScript (ts-opentui)

```bash
# Run all tests
bun test

# Run specific file
bun test src/core/SessionManager.test.ts

# Run with coverage
bun test --coverage

# Watch mode
bun test --watch
```

### Go (go-bubbletea)

```bash
# Run all tests
go test ./...

# Run specific package
go test ./internal/app

# Run with race detector
go test -race ./...

# Run with coverage
go test -cover ./...

# Verbose output
go test -v ./...
```

## Checklist: AI-Generated Code

Before accepting AI-generated tests or implementation, verify:

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
- [Go Testing Package](https://go.dev/pkg/testing)
- [Testing Library for React](https://testing-library.com/docs/react-testing-library/intro)
- [Test-Driven Development by Example](https://www.oreilly.com/library/view/test-driven-development/0321146530)
