# Go Testing Skill

**Version:** 1.0
**Purpose:** Test-Driven Development for Go/Bubbletea

## Overview

TDD with Go uses the built-in `testing` package. This skill covers patterns for testing Bubbletea models, services, and concurrent code.

**Core Principle:** Tests are **specifications**, not just coverage metrics.

## TDD Cycle

### 1. Write Failing Test (RED)

```go
func TestTaskCardTitle(t *testing.T) {
    card := TaskCard{Title: "Test Task"}
    want := "Test Task"
    got := card.GetTitle()

    if got != want {
        t.Errorf("GetTitle() = %q, want %q", got, want)
    }
}

// Run: go test
// Expected: FAIL (red) - no implementation exists yet
```

### 2. Write Minimal Implementation (GREEN)

**CRITICAL:** Write ONLY enough code to make test pass. Don't over-engineer.

```go
type TaskCard struct {
    Title string
}

func (c TaskCard) GetTitle() string {
    return c.Title
}

// Run: go test
// Expected: PASS (green)
```

### 3. Refactor (if needed)

Only refactor AFTER tests pass:

```go
// Extract common method if needed
func (c TaskCard) String() string {
    return fmt.Sprintf("Task: %s", c.Title)
}

// Run: go test
// Expected: Still PASS (green)
```

## Testing Bubbletea Models

### Testing Init

```go
func TestBoardModelInit(t *testing.T) {
    m := NewBoardModel()
    cmd := m.Init()

    if cmd != nil {
        t.Errorf("Init() should return nil, got %v", cmd)
    }
}
```

### Testing Update with Messages

```go
func TestBoardModelUpdate_KeyPress(t *testing.T) {
    m := NewBoardModel()
    msg := tea.KeyMsg{Type: tea.KeyDown}

    newModel, cmd := m.Update(msg)
    updated := newModel.(BoardModel)

    if updated.cursor != 1 {
        t.Errorf("cursor = %d, want 1", updated.cursor)
    }
    if cmd != nil {
        t.Errorf("cmd should be nil")
    }
}
```

### Testing State Transitions

```go
func TestBoardModel_EnterTask(t *testing.T) {
    m := NewBoardModel()
    m.tasks = []Task{{ID: "az-1", Title: "Test"}}
    
    msg := tea.KeyMsg{Type: tea.KeyEnter}
    newModel, _ := m.Update(msg)
    updated := newModel.(BoardModel)

    if updated.state != StateDetail {
        t.Errorf("state = %v, want StateDetail", updated.state)
    }
}
```

### Testing View Output

```go
func TestBoardModelView(t *testing.T) {
    m := NewBoardModel()
    m.tasks = []Task{{ID: "az-1", Title: "Test Task"}}

    view := m.View()

    if !strings.Contains(view, "Test Task") {
        t.Errorf("View() should contain task title")
    }
}
```

## Testing Services with Mocks

### Interface-Based Mocking

```go
// Define interface
type CommandRunner interface {
    Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Mock implementation
type mockRunner struct {
    output []byte
    err    error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
    return m.output, m.err
}

// Test using mock
func TestBeadsClientList(t *testing.T) {
    runner := &mockRunner{output: []byte(`[{"id":"az-1","title":"Test"}]`)}
    client := NewClient(runner, slog.Default())

    tasks, err := client.List(context.Background())

    if err != nil {
        t.Fatalf("List() error = %v", err)
    }
    if len(tasks) != 1 {
        t.Errorf("len(tasks) = %d, want 1", len(tasks))
    }
}
```

### Testing Error Scenarios

```go
func TestBeadsClientList_Error(t *testing.T) {
    runner := &mockRunner{err: errors.New("command failed")}
    client := NewClient(runner, slog.Default())

    _, err := client.List(context.Background())

    if err == nil {
        t.Error("expected error, got nil")
    }
}
```

## Testing Concurrent Code

### Using Race Detector

```bash
go test -race ./...
```

### Testing with Contexts

```go
func TestServiceWithContext(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    svc := NewService()
    result, err := svc.DoWork(ctx)

    if err != nil {
        t.Errorf("DoWork() error = %v", err)
    }
    if result != "expected" {
        t.Errorf("result = %v, want expected", result)
    }
}
```

### Testing Channel Behavior

```go
func TestWorkerPool(t *testing.T) {
    jobs := make(chan int, 3)
    results := make(chan int, 3)

    go worker(jobs, results)

    jobs <- 1
    jobs <- 2
    close(jobs)

    got := []int{<-results, <-results}
    want := []int{1, 2}

    if !reflect.DeepEqual(got, want) {
        t.Errorf("results = %v, want %v", got, want)
    }
}
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
```go
// BAD: Implementation exists, test written to pass
func CalculateDiscount(price float64) float64 {
    return price * 0.9
}

// Test written to match implementation (wrong order!)
func TestDiscount(t *testing.T) {
    got := CalculateDiscount(100)
    if got != 90 {
        t.Errorf("wrong")  // Locks in bug
    }
}
```

**✅ CORRECT: Write test FIRST as spec:**
```go
// GOOD: Test specifies behavior first
func TestCalculateDiscount_AppliesTenPercent(t *testing.T) {
    got := CalculateDiscount(100)
    want := 90.0
    if got != want {
        t.Errorf("CalculateDiscount(100) = %v, want %v", got, want)
    }
}

// Implementation written to satisfy spec
func CalculateDiscount(price float64) float64 {
    return price * 0.9
}
```

### Never Delete Failing Tests

```go
// ❌ WRONG: Delete test to "fix" build
func TestEdgeCase(t *testing.T) {
    got := handleEdgeCase()
    if got != "expected" {
        t.Errorf("wrong")
    }
}

// ✅ CORRECT: Fix implementation to satisfy test
func handleEdgeCase() string {
    return "expected"  // Actually implement the logic
}
```

## Test Quality Guidelines

### Table-Driven Tests

```go
func TestCalculateDiscount(t *testing.T) {
    tests := []struct {
        name  string
        price float64
        want  float64
    }{
        {"normal price", 100, 90},
        {"zero price", 0, 0},
        {"large price", 1000000, 900000},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := CalculateDiscount(tt.price)
            if got != tt.want {
                t.Errorf("CalculateDiscount(%v) = %v, want %v", 
                    tt.price, got, tt.want)
            }
        })
    }
}
```

### Clear Error Messages

```go
// ❌ BAD: Unhelpful
if got != want {
    t.Error("wrong")
}

// ✅ GOOD: Clear and informative
if got != want {
    t.Errorf("CreateWorktree(%q) = %v, want %v", input, got, want)
}
```

### Test Helpers

```go
func TestBoardModel(t *testing.T) {
    // Helper for creating test model
    newTestModel := func(t *testing.T) BoardModel {
        t.Helper()
        m := NewBoardModel()
        m.tasks = []Task{{ID: "az-1", Title: "Test"}}
        return m
    }

    t.Run("displays tasks", func(t *testing.T) {
        m := newTestModel(t)
        view := m.View()
        if !strings.Contains(view, "Test") {
            t.Error("view should contain task title")
        }
    })
}
```

## Running Tests

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

# Run specific test
go test -run TestBoardModel ./internal/app

# Run subtests
go test -run TestBoardModel/displays_tasks ./internal/app

# Coverage report
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## Checklist: AI-Generated Code

Before accepting AI-generated tests or implementation:

- [ ] Test **fails** before I implement the feature
- [ ] Test **passes** after minimal implementation
- [ ] Test name describes **behavior**, not implementation
- [ ] Error messages are **clear** (got/want pattern)
- [ ] Edge cases are **covered** (zero, nil, large values)
- [ ] Test is **independent** (doesn't rely on other tests)
- [ ] Implementation is **minimal** (only what test requires)
- [ ] **Race detector** passes (`go test -race`)
- [ ] Tests run **successfully** in CI

**If ANY fail:** Ask AI to fix before proceeding.

## References

- [Go Testing Package](https://go.dev/pkg/testing)
- [Table-Driven Tests](https://go.dev/wiki/TableDrivenTests)
- [Subtests and Sub-benchmarks](https://go.dev/blog/subtests)
- [Race Detector](https://go.dev/doc/articles/race_detector)
