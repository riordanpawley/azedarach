# Go Concurrency Skill

**Version:** 1.0
**Purpose:** Idiomatic patterns for goroutines, channels, and context in Go
**Source:** Adapted from Effective Go, Go by Example, and production patterns

## Overview

Go provides built-in concurrency primitives via **goroutines** and **channels**. Unlike Effect's fibers, Go uses goroutines (actual OS threads in the Go scheduler) for concurrency.

## Goroutines vs Threads

| Aspect | OS Threads | Go Goroutines |
|--------|-----------|---------------|
| Memory | ~8MB stack each | ~2KB initial stack (grows as needed) |
| Limit | Hundreds (system limit) | Hundreds of thousands |
| Scheduling | OS kernel (preemptive) | Go runtime (cooperative + preemptive) |
| Best for | CPU-bound parallelism | Mixed I/O and CPU work |

**Key insight**: Goroutines are lightweight and scale well for I/O-bound work. Use them liberally.

## Goroutine Patterns

### Basic Goroutine

```go
// Spawn goroutine
go func() {
    fmt.Println("running in background")
}()

// Main continues immediately
fmt.Println("main continues")
```

### Goroutine with WaitGroup

```go
var wg sync.WaitGroup

// Spawn 3 workers
for i := 0; i < 3; i++ {
    wg.Add(1)  // Increment counter
    go func(id int) {
        defer wg.Done()  // Decrement when done
        doWork(id)
    }(i)
}

// Wait for all to complete
wg.Wait()
```

### Buffered Channels

```go
// Channel with 10-item buffer
ch := make(chan int, 10)

// Non-blocking send
select {
case ch <- value:
    fmt.Println("sent")
default:
    fmt.Println("channel full")
}
```

### Unbuffered Channels

```go
// Blocking channel (size 0)
ch := make(chan int)

// Send blocks until receiver ready
ch <- value

// Receive blocks until sender ready
value := <-ch
```

## Context for Cancellation

**CRITICAL**: Always pass `context.Context` as first argument to functions that:

- Do I/O operations
- Start goroutines
- Make HTTP requests
- Query databases

### Creating Contexts

```go
// Background context (no deadline, no cancellation)
ctx := context.Background()

// TODO context (when you don't know what to use yet)
ctx := context.TODO()

// With timeout
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()  // Always call to release resources

// With deadline
ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(5*time.Second))
defer cancel()

// With cancellation value
ctx, cancel := context.WithCancel(parentCtx)
// Cancel later: cancel()
```

### Checking for Cancellation

```go
func doWork(ctx context.Context) error {
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            // Context cancelled
            return ctx.Err()  // context.Canceled or context.DeadlineExceeded
        case <-ticker.C:
            // Do work
            if err := process(); err != nil {
                return err
            }
        }
    }
}
```

### Propagating Context

```go
// Parent context controls child
func (s *Service) Run(ctx context.Context) error {
    // Child inherits parent's deadline/cancellation
    childCtx, cancel := context.WithCancel(ctx)
    defer cancel()

    return s.doWork(childCtx)
}
```

## Channel Patterns

### Fan-In

```go
// Merge multiple channels into one
func fanIn(ch1, ch2 <-chan int) <-chan int {
    out := make(chan int)
    go func() {
        defer close(out)
        for {
            select {
            case v, ok := <-ch1:
                if !ok { ch1 = nil }  // Disable channel when closed
            case v, ok := <-ch2:
                if !ok { ch2 = nil }
            default:
                if ch1 == nil && ch2 == nil {
                    return  // Both closed
                }
            }
            if ch1 != nil || ch2 != nil {
                out <- v
            }
        }
    }()
    return out
}
```

### Fan-Out

```go
// Distribute work to multiple workers
func fanOut(work <-chan Task, workers int) {
    for i := 0; i < workers; i++ {
        go worker(work)
    }
}
```

### Worker Pool

```go
type WorkerPool struct {
    tasks   chan Task
    results chan Result
    wg      sync.WaitGroup
}

func NewWorkerPool(workers int) *WorkerPool {
    p := &WorkerPool{
        tasks:   make(chan Task, 100),
        results: make(chan Result, 100),
    }

    p.wg.Add(workers)
    for i := 0; i < workers; i++ {
        go p.worker()
    }

    return p
}

func (p *WorkerPool) worker() {
    defer p.wg.Done()
    for task := range p.tasks {
        result := process(task)
        p.results <- result
    }
}

func (p *WorkerPool) Submit(task Task) {
    p.tasks <- task
}

func (p *WorkerPool) Close() []Result {
    close(p.tasks)
    p.wg.Wait()
    close(p.results)

    var results []Result
    for r := range p.results {
        results = append(results, r)
    }
    return results
}
```

## Error Handling in Goroutines

### Error Channels

```go
// Send errors back from goroutine
errCh := make(chan error, 1)

go func() {
    defer close(errCh)
    errCh <- doWork()
}()

select {
case err := <-errCh:
    if err != nil {
        log.Printf("work failed: %v", err)
    }
case <-time.After(5 * time.Second):
    log.Printf("timeout waiting for work")
}
```

### sync/errgroup

```go
// Collect errors from multiple goroutines
g, ctx := errgroup.WithContext(context.Background())

g.Go(func() error {
    return task1(ctx)
})

g.Go(func() error {
    return task2(ctx)
})

// Wait for all - returns first error (if any)
if err := g.Wait(); err != nil {
    log.Printf("one or more tasks failed: %v", err)
}
```

## Race Conditions

### Detect with `-race`

```bash
go test -race ./...
go run -race main.go
```

### Fix with Mutex

```go
type Counter struct {
    mu    sync.Mutex
    value int
}

func (c *Counter) Increment() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.value++
}
```

### Fix with Atomic

```go
type Counter struct {
    value int64  // Must be int64 for atomic
}

func (c *Counter) Increment() {
    atomic.AddInt64(&c.value, 1)
}

func (c *Counter) Get() int64 {
    return atomic.LoadInt64(&c.value)
}
```

## CRITICAL: Never Close Channels from Receiver

```go
// ❌ WRONG: Receiver closes channel
func consumer(ch <-chan int) {
    defer close(ch)  // PANIC!
    for v := range ch { ... }
}

// ✅ CORRECT: Sender owns and closes channel
func producer() <-chan int {
    ch := make(chan int)
    go func() {
        defer close(ch)  // Owner closes
        for i := 0; i < 10; i++ {
            ch <- i
        }
    }()
    return ch
}
```

## Common Patterns

### One-shot Goroutine with Result

```go
func asyncResult() <-chan int {
    ch := make(chan int, 1)  // Buffered so goroutine doesn't block
    go func() {
        defer close(ch)
        ch <- longRunningCalculation()
    }()
    return ch
}

// Use later
result := <-asyncResult()
```

### Timeout with Select

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

select {
case result := <-ch:
    fmt.Printf("got result: %v\n", result)
case <-ctx.Done():
    fmt.Println("timeout")
}
```

### Ticker for Periodic Work

```go
ticker := time.NewTicker(5 * time.Second)
defer ticker.Stop()

for range ticker.C {
    doPeriodicWork()
}
```

### Rate Limiting

```go
// Limit to 10 requests per second
limiter := time.NewTicker(100 * time.Millisecond)
defer limiter.Stop()

for _, item := range items {
    <-limiter.C  // Wait for next token
    process(item)
}
```

## Best Practices

1. **Always check context cancellation** in long-running operations
2. **Defer cancel()** when creating contexts with timeout/deadline
3. **Buffer channels** to avoid deadlocks when sender/receiver aren't ready
4. **Close channels** only by the sender
5. **Use sync.WaitGroup** to wait for goroutine groups
6. **Run tests with `-race` flag** to detect race conditions
7. **Prefer channels over shared memory** (CSP: "Don't communicate by sharing memory; share memory by communicating")
8. **Use atomic operations** for simple counters/flags instead of mutex

## References

- [Effective Go - Concurrency](https://go.dev/doc/effective_go#concurrency)
- [Go by Example - Goroutines](https://gobyexample.com/goroutines)
- [Go by Example - Channels](https://gobyexample.com/channels)
- [Go by Example - Context](https://gobyexample.com/context)
