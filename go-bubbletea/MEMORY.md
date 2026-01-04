# MEMORY - go-bubbletea Testing Patterns

**Purpose:** Testing patterns and guardrails for Go/Bubbletea development

## Test Framework

**Primary:** Go's built-in `testing` package
- Table-driven tests for multiple cases
- Subtests for hierarchical organization
- Benchmarks for performance testing

**Secondary:** `testify` for assertions (optional, use sparingly)

## Testing Bubbletea Models

### Pattern: Model Initialization

```go
func TestBoardModelInit(t *testing.T) {
    m := NewBoardModel()
    cmd := m.Init()

    // Init should not return commands for simple models
    if cmd != nil {
        t.Errorf("Init() should return nil, got %v", cmd)
    }
}
```

### Pattern: Model Update

```go
func TestBoardModelUpdate(t *testing.T) {
    tests := []struct {
        name     string
        model    BoardModel
        msg      tea.Msg
        wantCmd  tea.Cmd
        check    func(BoardModel, tea.Cmd)
    }{
        {
            name:  "handles quit",
            model: NewBoardModel(),
            msg:   tea.KeyMsg{Type: tea.KeyCtrlC},
            wantCmd: tea.Quit,
            check: func(m BoardModel, cmd tea.Cmd) {
                // Verify state after update
                if m.quitting {
                    t.Error("Expected model to be quitting")
                }
            },
        },
        {
            name:  "handles down arrow",
            model: NewBoardModel(),
            msg:   tea.KeyMsg{Type: tea.KeyDown},
            check: func(m BoardModel, cmd tea.Cmd) {
                if m.cursor != 1 {
                    t.Errorf("Expected cursor 1, got %d", m.cursor)
                }
            },
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            newModel, cmd := tt.model.Update(tt.msg)
            if cmd != nil && !reflect.DeepEqual(cmd, tt.wantCmd) {
                t.Errorf("Update() cmd = %v, want %v", cmd, tt.wantCmd)
            }
            if tt.check != nil {
                tt.check(newModel, cmd)
            }
        })
    }
}
```

### Pattern: View Rendering

```go
func TestBoardModelView(t *testing.T) {
    m := NewBoardModel()
    m.tasks = []Task{{Title: "Task 1"}, {Title: "Task 2"}}

    got := m.View()
    want := "Task 1\nTask 2"

    if got != want {
        t.Errorf("View() = %q, want %q", got, want)
    }
}
```

## Testing Services with Mocks

### Pattern: Interface-Based Testing

```go
// Define interface
type CommandRunner interface {
    Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Mock for testing
type mockRunner struct {
    output []byte
    err    error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
    return m.output, m.err
}

// Test service
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
    if tasks[0].ID != "az-1" {
        t.Errorf("Expected id az-1, got %s", tasks[0].ID)
    }
}
```

### Pattern: Context Cancellation

```go
func TestPollingServiceStop(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()  // Ensure cleanup

    service := NewPollingService(ctx, &mockRunner{})

    go service.Start()

    // Immediate cancellation
    cancel()

    time.Sleep(100 * time.Millisecond)  // Let goroutine handle cancellation

    // Verify stopped
    if !service.Stopped() {
        t.Error("Service should be stopped after cancellation")
    }
}
```

### Pattern: Error Handling

```go
func TestBeadsClientError(t *testing.T) {
    runner := &mockRunner{err: errors.New("command failed")}
    client := NewClient(runner, slog.Default())

    _, err := client.List(context.Background())

    if err == nil {
        t.Fatal("Expected error, got nil")
    }

    // Check error type (custom error wrapping)
    var beadsErr *BeadsError
    if !errors.As(err, &beadsErr) {
        t.Errorf("Expected BeadsError, got %T", err)
    }
    if beadsErr.Op != "list" {
        t.Errorf("Expected op 'list', got %s", beadsErr.Op)
    }
}
```

## Testing with Testify (Optional)

### Using Assertions

```go
import "github.com/stretchr/testify/assert"

func TestTaskCard(t *testing.T) {
    card := TaskCard{Title: "Test"}

    assert.Equal(t, "Test", card.Title())
    assert.NotNil(t, card.ID())
}
```

### Using Test Suite

```go
func TestTaskCardSuite(t *testing.T) {
    suite.Suite(&TaskCardSuite{})
    suite.Run(t, new(TaskCardSuite))
}

type TaskCardSuite struct {
    suite.Suite
    card TaskCard
}

func (s *TaskCardSuite) SetupTest() {
    s.card = NewTaskCard()
}

func (s *TaskCardSuite) TestTitle(t *testing.T) {
    assert.Equal(t, "Default", s.card.Title())
}

func (s *TaskCardSuite) TearDownTest() {
    // Cleanup
}
```

## Testing Concurrency

### Pattern: Race Detection

```bash
# Run tests with race detector
go test -race ./...

# Run specific test with race
go test -race -run TestSession ./...
```

### Pattern: Synchronization Testing

```go
func TestConcurrentAccess(t *testing.T) {
    var mu sync.Mutex
    counter := 0

    // Spawn 100 goroutines
    var wg sync.WaitGroup
    wg.Add(100)

    for i := 0; i < 100; i++ {
        go func() {
            defer wg.Done()
            mu.Lock()
            counter++
            mu.Unlock()
        }()
    }

    wg.Wait()

    if counter != 100 {
        t.Errorf("Expected 100, got %d", counter)
    }
}
```

### Pattern: Channel Testing

```go
func TestWorkerPool(t *testing.T) {
    pool := NewWorkerPool(2)  // 2 workers

    // Submit work
    for i := 0; i < 5; i++ {
        pool.Submit(Task{ID: fmt.Sprintf("task-%d", i)})
    }

    // Get results
    results := pool.Close()

    if len(results) != 5 {
        t.Errorf("Expected 5 results, got %d", len(results))
    }

    // Verify all tasks processed
    processedIDs := make(map[string]bool)
    for _, r := range results {
        processedIDs[r.TaskID] = true
    }

    for i := 0; i < 5; i++ {
        id := fmt.Sprintf("task-%d", i)
        if !processedIDs[id] {
            t.Errorf("Task %s not processed", id)
        }
    }
}
```

## Integration Testing

### Pattern: End-to-End

```go
func TestFullWorkflow(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }

    // Setup
    ctx := context.Background()
    service := NewService(ctx)

    // Create task
    task, err := service.CreateTask(ctx, "Test Task")
    if err != nil {
        t.Fatalf("CreateTask() error = %v", err)
    }

    // Update task
    err = service.UpdateStatus(ctx, task.ID, "in_progress")
    if err != nil {
        t.Fatalf("UpdateStatus() error = %v", err)
    }

    // Verify state
    updated, err := service.GetTask(ctx, task.ID)
    if err != nil {
        t.Fatalf("GetTask() error = %v", err)
    }

    if updated.Status != "in_progress" {
        t.Errorf("Expected status 'in_progress', got %s", updated.Status)
    }

    // Cleanup
    _ = service.DeleteTask(ctx, task.ID)
}
```

### Pattern: Using Testcontainers (Advanced)

```go
import "github.com/testcontainers/testcontainers-go"

func TestDatabaseIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping testcontainers test in short mode")
    }

    // Start test database
    ctx := context.Background()
    container, err := testcontainers.GenericContainer(ctx, ...containerConfig...)
    if err != nil {
        t.Fatalf("Failed to start container: %v", err)
    }
    defer container.Terminate(ctx)

    // Run tests against real database
    db, err := connectToDatabase(ctx, container)
    if err != nil {
        t.Fatalf("Failed to connect: %v", err)
    }

    // Test operations
    service := NewService(db)
    // ... actual tests
}
```

## Testing External Dependencies

### Pattern: Command Execution Mocking

```go
type CommandExecutor interface {
    Execute(name string, args ...string) ([]byte, error)
}

type mockExecutor struct {
    commands map[string][]byte
}

func (m *mockExecutor) Execute(name string, args ...string) ([]byte, error) {
    key := fmt.Sprintf("%s %v", name, args)
    output, ok := m.commands[key]
    if !ok {
        return nil, fmt.Errorf("unexpected command: %s", key)
    }
    return output, nil
}

func TestGitServiceCreateWorktree(t *testing.T) {
    executor := &mockExecutor{
        commands: map[string][]byte{
            "git worktree add ../test-az-1 -b test-az-1 main": []byte(""),
        },
    }
    service := NewGitService(executor)

    err := service.CreateWorktree(context.Background(), "test-az-1", "main")
    if err != nil {
        t.Fatalf("CreateWorktree() error = %v", err)
    }
}
```

### Pattern: File System Mocking

```go
// Use interface
type FileSystem interface {
    ReadFile(path string) ([]byte, error)
    WriteFile(path string, data []byte) error
}

// Mock for testing
type mockFS struct {
    files map[string][]byte
}

func (m *mockFS) ReadFile(path string) ([]byte, error) {
    data, ok := m.files[path]
    if !ok {
        return nil, fmt.Errorf("file not found: %s", path)
    }
    return data, nil
}

func (m *mockFS) WriteFile(path string, data []byte) error {
    m.files[path] = data
    return nil
}
```

## Benchmarking

### Pattern: Performance Tests

```go
func BenchmarkBoardView(b *testing.B) {
    m := NewBoardModel()
    m.tasks = make([]Task, 100)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _ = m.View()
    }
}

func BenchmarkModelUpdate(b *testing.B) {
    m := NewBoardModel()
    msg := tea.KeyMsg{Type: tea.KeyDown}

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, _ = m.Update(msg)
    }
}
```

## Running Tests

```bash
# Run all tests
go test ./...

# Run tests in verbose mode
go test -v ./...

# Run tests with race detector
go test -race ./...

# Run tests with coverage
go test -cover ./...
go test -coverprofile=coverage.out ./...

# View coverage report
go tool cover -html=coverage.out

# Run specific test
go test -run TestBoardModel ./...

# Run tests in specific package
go test ./internal/app

# Run benchmarks
go test -bench=. ./...

# Run with build tags
go test -tags=integration ./...
```

## Guardrails Checklist

Before accepting AI-generated code or tests:

- [ ] Tests **compile** without errors
- [ ] Tests run successfully (`go test`)
- [ ] No race conditions detected (`go test -race`)
- [ ] Code formatted (`gofmt`, `goimports`)
- [ ] Linting passes (`golangci-lint`)
- [ ] Test describes **behavior**, not implementation
- [ ] Test has **clear setup/expectations**
- [ ] External dependencies mocked (CLI, file system)
- [ ] Context cancellation tested
- [ ] Error cases covered
- [ ] Edge cases considered

## Troubleshooting

### Parallel Test Execution

```go
// Tests can run in parallel by default
// Use t.Parallel() for CPU-bound tests
func TestParallel(t *testing.T) {
    t.Parallel()  // Run with other tests concurrently
    // ... test code
}
```

### Test Helpers

```go
// Create helpers in test files (not exported)
func setupTestDB(t *testing.T) *sql.DB {
    db, err := sql.Open("sqlite3", ":memory:")
    if err != nil {
        t.Fatalf("Failed to open db: %v", err)
    }
    t.Cleanup(func() {
        db.Close()
    })
    return db
}
```

## References

- [Go Testing Package](https://go.dev/pkg/testing)
- [Testify](https://github.com/stretchr/testify)
- [Go Test Table-Driven Tests](https://dave.cheney.net/2019/05/07/prefer-table-driven-tests)
- [Go Subtests](https://go.dev/blog/subtests)
- [Testing Containers](https://testcontainers.com/)
