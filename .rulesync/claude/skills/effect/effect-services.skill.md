# Effect Services & Layers Skill

**Version:** 1.0
**Purpose:** Idiomatic patterns for Effect services, layers, and dependency injection
**Source:** Adapted from [Effect Patterns Hub](https://github.com/PaulJPhilp/EffectPatterns)

## Overview

Effect's service architecture uses **Layers** for dependency injection, providing composable, testable, and type-safe service construction. This skill covers the patterns used in this codebase.

## Core Concepts

### The Three Type Parameters

Every `Effect<A, E, R>` has:
- **A (Success)**: The value produced on success
- **E (Error)**: The typed error that can occur
- **R (Requirements)**: Services needed from the environment

Every `Layer<ROut, E, RIn>` has:
- **ROut**: Services this layer provides
- **E**: Errors that can occur during construction
- **RIn**: Services this layer requires to construct

### Effect.Service Pattern

The modern way to define services:

```typescript
// ✅ CORRECT: Service class pattern
export class UserService extends Effect.Service<UserService>()("UserService", {
  effect: Effect.gen(function* () {
    const state = yield* SubscriptionRef.make<User[]>([])

    return {
      state,
      getUsers: () => SubscriptionRef.get(state),
      addUser: (user: User) => SubscriptionRef.update(state, (users) => [...users, user]),
    }
  }),
}) {}

// Usage: UserService provides both the Tag and the Default layer
// - UserService (the Tag for dependency injection)
// - UserService.Default (the Layer that constructs the service)
```

### Service with Dependencies

When a service needs other services, declare them in `dependencies`:

```typescript
// ✅ CORRECT: Declare dependencies explicitly
export class NotificationService extends Effect.Service<NotificationService>()("NotificationService", {
  dependencies: [UserService.Default, LoggerService.Default],
  effect: Effect.gen(function* () {
    // Grab dependencies at construction time
    const userService = yield* UserService
    const logger = yield* LoggerService

    return {
      notifyUser: (userId: string, message: string) =>
        Effect.gen(function* () {
          const users = yield* userService.getUsers()
          const user = users.find((u) => u.id === userId)
          if (!user) return yield* Effect.fail(new UserNotFoundError({ userId }))

          yield* logger.info(`Notifying ${user.name}: ${message}`)
          // ... send notification
        }),
    }
  }),
}) {}

// NotificationService.Default now has NO unsatisfied requirements
// It can be used directly in Layer.mergeAll without ordering issues
```

### Why `dependencies` Matters

```typescript
// ❌ BAD: Missing dependencies - leaks requirements
export class BrokenService extends Effect.Service<BrokenService>()("BrokenService", {
  effect: Effect.gen(function* () {
    const logger = yield* LoggerService  // ❌ Requirement leaks to consumers!
    return { /* ... */ }
  }),
}) {}
// BrokenService.Default requires LoggerService from the layer composition

// ✅ GOOD: Dependencies declared - self-contained
export class GoodService extends Effect.Service<GoodService>()("GoodService", {
  dependencies: [LoggerService.Default],
  effect: Effect.gen(function* () {
    const logger = yield* LoggerService  // ✅ Provided by dependencies
    return { /* ... */ }
  }),
}) {}
// GoodService.Default has no external requirements
```

## Scoped vs Effect Services

### When to Use `effect:`

Use for services that only have state and synchronous methods:

```typescript
export class ConfigService extends Effect.Service<ConfigService>()("ConfigService", {
  effect: Effect.gen(function* () {
    const config = yield* SubscriptionRef.make({ theme: "dark", locale: "en" })

    return {
      config,
      setTheme: (theme: string) => SubscriptionRef.update(config, (c) => ({ ...c, theme })),
      getTheme: () => Effect.map(SubscriptionRef.get(config), (c) => c.theme),
    }
  }),
}) {}
```

### When to Use `scoped:`

Use when the service spawns background fibers or needs cleanup:

```typescript
export class PollingService extends Effect.Service<PollingService>()("PollingService", {
  scoped: Effect.gen(function* () {
    const data = yield* SubscriptionRef.make<Data | null>(null)

    // Background polling fiber - lives for service lifetime
    yield* Effect.gen(function* () {
      const freshData = yield* fetchData()
      yield* SubscriptionRef.set(data, freshData)
    }).pipe(
      Effect.repeat(Schedule.spaced("30 seconds")),
      Effect.forkScoped,  // ✅ Tied to service scope
    )

    return {
      data,
      getData: () => SubscriptionRef.get(data),
    }
  }),
}) {}
```

### Decision Matrix

| Scenario | Use |
|----------|-----|
| Only state (SubscriptionRef/Ref) and methods | `effect:` |
| Spawns `Effect.forkScoped` fibers | `scoped:` |
| Uses `Effect.scheduleForked` | `scoped:` |
| Needs cleanup via `Effect.addFinalizer` | `scoped:` |
| Methods spawn fibers that outlive the call | `scoped:` |

## Layer Composition

### Layer.mergeAll

Combine independent layers:

```typescript
const appLayer = Layer.mergeAll(
  ConfigService.Default,
  UserService.Default,
  NotificationService.Default,  // Its dependencies are self-contained
  LoggerService.Default,
)
```

### Layer.provideMerge

When layers have ordering dependencies (rare with proper `dependencies:` usage):

```typescript
// Only needed if services don't declare their own dependencies
const appLayer = LoggerService.Default.pipe(
  Layer.provideMerge(UserService.Default),
  Layer.provideMerge(NotificationService.Default),
)
```

## Anti-Patterns

### Never Provide Inside Service Methods

```typescript
// ❌ WRONG: Providing inside a method
export class BadService extends Effect.Service<BadService>()("BadService", {
  effect: Effect.succeed({
    doSomething: () =>
      Effect.gen(function* () {
        const logger = yield* LoggerService
        yield* logger.info("doing something")
      }).pipe(Effect.provide(LoggerService.Default)),  // ❌ WRONG!
  }),
}) {}

// ✅ CORRECT: Grab dependency at construction
export class GoodService extends Effect.Service<GoodService>()("GoodService", {
  dependencies: [LoggerService.Default],
  effect: Effect.gen(function* () {
    const logger = yield* LoggerService  // ✅ At construction

    return {
      doSomething: () =>
        Effect.gen(function* () {
          yield* logger.info("doing something")  // ✅ Use directly
        }),
    }
  }),
}) {}
```

### Never Create Global Effect-Returning Functions

```typescript
// ❌ WRONG: Global function with service requirements
export const getUser = (id: string) =>
  Effect.gen(function* () {
    const userService = yield* UserService
    return yield* userService.getById(id)
  })
// This leaks UserService requirement to every caller

// ✅ CORRECT: Method on a service
export class UserOperations extends Effect.Service<UserOperations>()("UserOperations", {
  dependencies: [UserService.Default],
  effect: Effect.gen(function* () {
    const userService = yield* UserService

    return {
      getUser: (id: string) => userService.getById(id),
    }
  }),
}) {}
```

### Don't Wrap One-Liners

```typescript
// ❌ WRONG: Unnecessary wrapper
const getPath = (pathService: Path.Path, segments: string[]) =>
  pathService.join(...segments)

// ✅ CORRECT: Just use it directly
const fullPath = pathService.join("src", "components", "App.tsx")
```

## State Management with SubscriptionRef

### Basic Pattern

```typescript
export class CounterService extends Effect.Service<CounterService>()("CounterService", {
  effect: Effect.gen(function* () {
    const count = yield* SubscriptionRef.make(0)

    return {
      count,  // Expose for subscriptions
      increment: () => SubscriptionRef.update(count, (n) => n + 1),
      decrement: () => SubscriptionRef.update(count, (n) => n - 1),
      reset: () => SubscriptionRef.set(count, 0),
    }
  }),
}) {}
```

### Subscribing to Changes

```typescript
// In atoms (for React integration)
export const countAtom = appRuntime.subscriptionRef(
  Effect.gen(function* () {
    const counter = yield* CounterService
    return counter.count
  }),
)

// Direct subscription in Effect code
const changes = yield* SubscriptionRef.changes(counter.count)
yield* Stream.runForEach(changes, (value) =>
  Effect.log(`Count changed to ${value}`)
)
```

## Testing Services

### Creating Test Layers

```typescript
// Production service
export class ApiService extends Effect.Service<ApiService>()("ApiService", {
  effect: Effect.succeed({
    fetchData: () => Effect.tryPromise(() => fetch("/api/data").then((r) => r.json())),
  }),
}) {}

// Test layer with mock
const TestApiService = Layer.succeed(ApiService, {
  fetchData: () => Effect.succeed({ items: ["mock1", "mock2"] }),
})

// In tests
const result = yield* myEffect.pipe(
  Effect.provide(TestApiService),
)
```

## Summary

| Pattern | Description |
|---------|-------------|
| `Effect.Service` | Modern service definition with Tag + Default layer |
| `dependencies: []` | Declare service dependencies for self-contained layers |
| `effect:` | Services with state only |
| `scoped:` | Services with background fibers or cleanup |
| `SubscriptionRef` | Reactive state that can be subscribed to |
| `Layer.mergeAll` | Combine independent layers |
| Grab deps at construction | `yield* OtherService` in the effect/scoped block |
| Use deps directly | Don't wrap service methods in helpers |
