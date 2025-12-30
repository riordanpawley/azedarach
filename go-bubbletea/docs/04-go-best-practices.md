# Go Best Practices

## Project Layout Rationale

Following the [Standard Go Project Layout](https://github.com/golang-standards/project-layout):

```
cmd/           # Main applications - minimal, just wiring
internal/      # Private code - can't be imported by other projects
pkg/           # Public libraries - reusable by others (use sparingly)
testdata/      # Test fixtures - ignored by go build
```

**Why `internal/`?**
- Compiler-enforced encapsulation
- Free to refactor without breaking external consumers
- Clear signal: "this is private implementation"

**Why thin `cmd/`?**
```go
// cmd/az/main.go - GOOD: minimal wiring only
func main() {
    cfg := config.Load()
    app := app.New(cfg)
    if err := app.Run(); err != nil {
        log.Fatal(err)
    }
}

// cmd/az/main.go - BAD: business logic in main
func main() {
    tasks, _ := beads.List()  // Don't do this
    // ...
}
```

## Dependency Injection via Interfaces

**Accept interfaces, return structs:**

```go
// internal/services/beads/client.go

// CommandRunner abstracts exec.Command for testing
type CommandRunner interface {
    Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Client wraps the bd CLI
type Client struct {
    runner CommandRunner
    logger *slog.Logger
}

// NewClient creates a Client with the given dependencies
func NewClient(runner CommandRunner, logger *slog.Logger) *Client {
    return &Client{
        runner: runner,
        logger: logger,
    }
}

// List fetches all beads
func (c *Client) List(ctx context.Context) ([]domain.Task, error) {
    out, err := c.runner.Run(ctx, "bd", "list", "--format=json")
    if err != nil {
        return nil, &domain.BeadsError{Op: "list", Err: err}
    }
    return parseTasksJSON(out)
}
```

**Test with mock:**

```go
// internal/services/beads/client_test.go

type mockRunner struct {
    output []byte
    err    error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
    return m.output, m.err
}

func TestClient_List(t *testing.T) {
    runner := &mockRunner{output: []byte(`[{"id":"az-1"}]`)}
    client := NewClient(runner, slog.Default())

    tasks, err := client.List(context.Background())

    require.NoError(t, err)
    assert.Len(t, tasks, 1)
}
```

## Functional Options Pattern

For complex constructors with optional configuration:

```go
// internal/services/monitor/session.go

type Monitor struct {
    pollInterval time.Duration
    patterns     map[SessionState][]*regexp.Regexp
    logger       *slog.Logger
}

type Option func(*Monitor)

func WithPollInterval(d time.Duration) Option {
    return func(m *Monitor) {
        m.pollInterval = d
    }
}

func WithLogger(l *slog.Logger) Option {
    return func(m *Monitor) {
        m.logger = l
    }
}

func NewMonitor(opts ...Option) *Monitor {
    m := &Monitor{
        pollInterval: 500 * time.Millisecond, // sensible default
        patterns:     defaultPatterns(),
        logger:       slog.Default(),
    }
    for _, opt := range opts {
        opt(m)
    }
    return m
}

// Usage:
monitor := NewMonitor(
    WithPollInterval(1 * time.Second),
    WithLogger(customLogger),
)
```

## Context Propagation

**Always pass context as first parameter:**

```go
// GOOD: Context flows through entire call chain
func (c *Client) List(ctx context.Context) ([]domain.Task, error) {
    out, err := c.runner.Run(ctx, "bd", "list")
    // ...
}

func (c *Client) Create(ctx context.Context, task domain.Task) error {
    // ...
}

// BAD: No context - can't cancel, no deadline
func (c *Client) List() ([]domain.Task, error) {
    // ...
}
```

**Bubbletea context pattern:**

```go
// Pass context via message for cancellation
type startMonitorMsg struct {
    ctx context.Context
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case startMonitorMsg:
        return m, m.startPolling(msg.ctx)
    }
    // ...
}
```

## Error Handling Idioms

**Wrap errors with context:**

```go
import "fmt"

func (c *Client) Create(ctx context.Context, task domain.Task) error {
    out, err := c.runner.Run(ctx, "bd", "create", "--title", task.Title)
    if err != nil {
        // Wrap with context about what we were doing
        return fmt.Errorf("creating bead %q: %w", task.Title, err)
    }
    return nil
}
```

**Sentinel errors for expected conditions:**

```go
// internal/domain/errors.go

var (
    ErrNotFound     = errors.New("not found")
    ErrConflict     = errors.New("conflict")
    ErrOffline      = errors.New("offline")
    ErrUserCanceled = errors.New("user canceled")
)

// Usage:
if errors.Is(err, domain.ErrNotFound) {
    // Handle not found case
}
```

**Custom error types with `Is()` and `As()`:**

```go
type BeadsError struct {
    Op      string
    BeadID  string
    Message string
    Err     error
}

func (e *BeadsError) Error() string {
    if e.BeadID != "" {
        return fmt.Sprintf("beads %s [%s]: %s", e.Op, e.BeadID, e.Message)
    }
    return fmt.Sprintf("beads %s: %s", e.Op, e.Message)
}

func (e *BeadsError) Unwrap() error { return e.Err }

// Is allows errors.Is(err, target) to work
func (e *BeadsError) Is(target error) bool {
    t, ok := target.(*BeadsError)
    if !ok {
        return false
    }
    return e.Op == t.Op // Match on operation type
}
```

## Concurrency Patterns

**Goroutine lifecycle management:**

```go
// internal/services/monitor/session.go

type SessionMonitor struct {
    mu       sync.RWMutex
    sessions map[string]*monitoredSession
    done     chan struct{}
    wg       sync.WaitGroup
}

type monitoredSession struct {
    beadID string
    cancel context.CancelFunc
}

func (m *SessionMonitor) Start(ctx context.Context, beadID string, program *tea.Program) {
    m.mu.Lock()
    defer m.mu.Unlock()

    // Don't start duplicate monitors
    if _, exists := m.sessions[beadID]; exists {
        return
    }

    ctx, cancel := context.WithCancel(ctx)
    m.sessions[beadID] = &monitoredSession{beadID: beadID, cancel: cancel}

    m.wg.Add(1)
    go func() {
        defer m.wg.Done()
        m.poll(ctx, beadID, program)
    }()
}

func (m *SessionMonitor) Stop(beadID string) {
    m.mu.Lock()
    defer m.mu.Unlock()

    if session, ok := m.sessions[beadID]; ok {
        session.cancel()
        delete(m.sessions, beadID)
    }
}

func (m *SessionMonitor) Shutdown() {
    close(m.done)
    m.mu.Lock()
    for _, s := range m.sessions {
        s.cancel()
    }
    m.mu.Unlock()
    m.wg.Wait() // Wait for all goroutines to finish
}
```

**Channel patterns:**

```go
// Fan-out: Multiple goroutines reading from one channel
func pollSessions(ctx context.Context, beadIDs <-chan string) {
    for beadID := range beadIDs {
        go pollOne(ctx, beadID)
    }
}

// Fan-in: Multiple goroutines writing to one channel
func collectResults(ctx context.Context, results chan<- SessionState) {
    var wg sync.WaitGroup
    for _, id := range activeSessionIDs {
        wg.Add(1)
        go func(id string) {
            defer wg.Done()
            state := detectState(id)
            select {
            case results <- state:
            case <-ctx.Done():
            }
        }(id)
    }
    go func() {
        wg.Wait()
        close(results)
    }()
}
```

## Package Design Principles

**1. Small, focused packages:**

```
internal/services/beads/     # Only beads CLI interaction
internal/services/git/       # Only git operations
internal/domain/             # Only data types, no I/O
```

**2. Avoid circular dependencies:**

```
domain/  ←──  services/  ←──  app/
   ↑             ↑              ↑
   └─────────────┴──────────────┘
              ui/

# domain/ imports nothing internal
# services/ imports domain/
# app/ imports services/, domain/
# ui/ imports domain/ (for types), never services/
```

**3. Package by feature, not layer (for complex features):**

```
# Instead of:
handlers/
  beads.go
  git.go
services/
  beads.go
  git.go

# Consider:
beads/
  client.go      # Service
  handlers.go    # UI handlers
  types.go       # Domain types
git/
  client.go
  worktree.go
  handlers.go
```

## Table-Driven Tests

```go
func TestDetectState(t *testing.T) {
    tests := []struct {
        name   string
        output string
        want   SessionState
    }{
        {
            name:   "waiting for confirmation",
            output: "Do you want to proceed? [y/n]",
            want:   StateWaiting,
        },
        {
            name:   "task completed",
            output: "Task completed successfully",
            want:   StateDone,
        },
        {
            name:   "error state",
            output: "Error: something went wrong",
            want:   StateError,
        },
        {
            name:   "active output",
            output: "Building project...",
            want:   StateBusy,
        },
        {
            name:   "empty output",
            output: "",
            want:   StateIdle,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := DetectState(tt.output)
            assert.Equal(t, tt.want, got)
        })
    }
}
```

## Structured Logging

```go
import "log/slog"

// Create logger with context
logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{
    Level: slog.LevelDebug,
}))

// Add fields progressively
logger = logger.With("component", "beads")

// Log with structured fields
logger.Info("fetching beads",
    "count", len(tasks),
    "filter", filter.String(),
)

logger.Error("beads command failed",
    "error", err,
    "command", "bd list",
)
```

## Graceful Shutdown

```go
// cmd/az/main.go

func main() {
    ctx, cancel := signal.NotifyContext(context.Background(),
        syscall.SIGINT, syscall.SIGTERM)
    defer cancel()

    app := app.New(cfg)

    // Run with graceful shutdown
    errCh := make(chan error, 1)
    go func() {
        errCh <- app.Run(ctx)
    }()

    select {
    case err := <-errCh:
        if err != nil {
            log.Fatal(err)
        }
    case <-ctx.Done():
        // Give app time to cleanup
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        app.Shutdown(shutdownCtx)
    }
}
```

## Configuration Best Practices

**Environment variable precedence:**

```go
// 1. Defaults (in code)
// 2. Config file (.azedarach.json)
// 3. Environment variables (AZ_*)
// 4. CLI flags

func Load() (*Config, error) {
    cfg := DefaultConfig()

    // Load from file
    if err := loadFromFile(cfg); err != nil && !os.IsNotExist(err) {
        return nil, err
    }

    // Override with env vars
    if v := os.Getenv("AZ_CLI_TOOL"); v != "" {
        cfg.CLITool = v
    }
    if v := os.Getenv("AZ_SKIP_PERMISSIONS"); v == "true" {
        cfg.Session.DangerouslySkipPermissions = true
    }

    return cfg, nil
}
```

**Validate configuration early:**

```go
func (c *Config) Validate() error {
    var errs []error

    if c.CLITool != "claude" && c.CLITool != "opencode" {
        errs = append(errs, fmt.Errorf("invalid cliTool: %s", c.CLITool))
    }

    if c.DevServer.Ports == nil {
        c.DevServer.Ports = make(map[string]PortConfig)
    }

    return errors.Join(errs...)
}
```
