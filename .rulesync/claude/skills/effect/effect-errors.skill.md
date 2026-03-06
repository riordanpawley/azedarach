# Effect Error Handling Skill

**Version:** 1.0
**Purpose:** Idiomatic patterns for typed errors, recovery, retries, and timeouts
**Source:** Adapted from [Effect Patterns Hub](https://github.com/PaulJPhilp/EffectPatterns)

## Overview

Effect makes errors **explicit in types**. Every `Effect<A, E, R>` declares its possible errors in the `E` channel, enabling compile-time safety and precise error handling.

## Core Principles

1. **Typed errors**: Errors are part of the type signature
2. **Mandatory handling**: Compiler ensures errors are addressed
3. **Declarative recovery**: No try/catch, use combinators
4. **Defect separation**: Typed errors vs unexpected failures (defects)

## Defining Tagged Errors

### Using Data.TaggedError

```typescript
import { Data } from "effect"

// ✅ CORRECT: Tagged error with structured data
export class UserNotFoundError extends Data.TaggedError("UserNotFoundError")<{
  readonly userId: string
}> {}

export class ValidationError extends Data.TaggedError("ValidationError")<{
  readonly field: string
  readonly message: string
}> {}

export class NetworkError extends Data.TaggedError("NetworkError")<{
  readonly url: string
  readonly status: number
}> {}
```

### Why Tagged Errors

- **Discriminated unions**: The `_tag` property enables type-safe pattern matching
- **Structural equality**: Errors are comparable with `Equal.equals`
- **Serializable**: Can be logged, stored, transmitted
- **Stack traces**: Automatic capture when created

## Failing with Typed Errors

```typescript
// Fail with a typed error
const getUser = (id: string): Effect.Effect<User, UserNotFoundError> =>
  Effect.gen(function* () {
    const user = yield* database.findUser(id)
    if (!user) {
      return yield* Effect.fail(new UserNotFoundError({ userId: id }))
    }
    return user
  })

// The error type is tracked in the signature
// Callers MUST handle UserNotFoundError
```

## Catching Errors

### catchAll - Handle Any Error

```typescript
// Catch all errors, return fallback
const safeGetUser = getUser(id).pipe(
  Effect.catchAll((error) =>
    Effect.succeed({ id: "guest", name: "Guest User" })
  ),
)
// Type: Effect<User, never, ...> - error channel is now `never`
```

### catchTag - Handle Specific Error

```typescript
// ✅ PREFERRED: Target specific error types
const result = getUser(id).pipe(
  Effect.catchTag("UserNotFoundError", (error) =>
    Effect.succeed({ id: error.userId, name: "Unknown User" })
  ),
)
// Only UserNotFoundError is handled, others propagate
```

### catchTags - Handle Multiple Errors

```typescript
const result = fetchData(url).pipe(
  Effect.catchTags({
    NetworkError: (error) =>
      Effect.succeed({ data: null, error: `Network failed: ${error.status}` }),
    ValidationError: (error) =>
      Effect.succeed({ data: null, error: `Invalid ${error.field}: ${error.message}` }),
    // Other errors propagate unchanged
  }),
)
```

### When to Use Each

| Combinator | Use When |
|------------|----------|
| `catchAll` | Need uniform fallback for any error |
| `catchTag` | Handle one specific error type |
| `catchTags` | Handle multiple specific errors differently |
| `catchSome` | Conditionally handle based on error predicate |

## Mapping Errors

### mapError - Transform Error Type

```typescript
// Map internal error to domain error
const fetchUser = httpGet(url).pipe(
  Effect.mapError((httpError) =>
    new UserServiceError({ cause: httpError, operation: "fetchUser" })
  ),
)
```

### orElseFail - Replace with Different Error

```typescript
// Replace error entirely
const getConfig = readFile(path).pipe(
  Effect.orElseFail(() => new ConfigNotFoundError({ path })),
)
```

## Retry and Timeout

### Basic Retry

```typescript
import { Schedule } from "effect"

// Retry 3 times with no delay
const resilient = fetchData.pipe(
  Effect.retry(Schedule.recurs(3)),
)
```

### Exponential Backoff

```typescript
// ✅ RECOMMENDED: Exponential backoff with jitter
const retryPolicy = Schedule.exponential("100 millis").pipe(
  Schedule.jittered,                    // Add randomness to prevent thundering herd
  Schedule.intersect(Schedule.recurs(5)), // Max 5 retries
  Schedule.whileOutput((delay) => delay < Duration.seconds(10)), // Cap delay
)

const resilient = fetchData.pipe(
  Effect.retry(retryPolicy),
)
```

### Timeout

```typescript
// Fail if operation takes too long
const bounded = fetchData.pipe(
  Effect.timeout("5 seconds"),
)
// Returns Option<A> - None if timed out

// Or fail with specific error on timeout
const boundedWithError = fetchData.pipe(
  Effect.timeoutFail({
    duration: "5 seconds",
    onTimeout: () => new TimeoutError({ operation: "fetchData" }),
  }),
)
```

### Combined: Timeout + Retry

```typescript
// ✅ BEST PRACTICE: Timeout per attempt, then retry
const resilient = fetchExternalApi(url).pipe(
  Effect.timeout("3 seconds"),           // Timeout single attempt
  Effect.retry(
    Schedule.exponential("100 millis").pipe(
      Schedule.jittered,
      Schedule.intersect(Schedule.recurs(3)),
    ),
  ),
  Effect.catchTag("TimeoutException", () =>
    Effect.fail(new ApiUnavailableError({ url })),
  ),
)
```

## Retry Based on Error Type

```typescript
// Only retry transient errors
const retryTransient = Schedule.recurWhile<Error>((error) =>
  error._tag === "NetworkError" || error._tag === "TimeoutError"
).pipe(
  Schedule.intersect(Schedule.exponential("100 millis")),
  Schedule.intersect(Schedule.recurs(5)),
)

const resilient = fetchData.pipe(
  Effect.retry(retryTransient),
)
// ValidationError won't be retried, fails immediately
```

## Typed vs Untyped Errors

### Typed Errors (Expected)

Created with `Effect.fail()`, tracked in the `E` type:

```typescript
const mayFail: Effect.Effect<User, UserNotFoundError> =
  Effect.fail(new UserNotFoundError({ userId: "123" }))

// Caught by catchAll, catchTag, catchTags
```

### Defects (Unexpected)

Created with `Effect.die()` or thrown exceptions - NOT in `E` type:

```typescript
// Defect - not in error channel
const defect = Effect.die(new Error("Unexpected state"))

// Thrown exceptions become defects
const unsafe = Effect.sync(() => {
  throw new Error("Oops")  // Becomes a defect
})

// Defects are NOT caught by catchAll/catchTag
// They propagate as "defects" and crash unless caught with:
Effect.catchAllDefect((defect) => Effect.succeed("recovered"))
```

### When to Use Each

| Situation | Use |
|-----------|-----|
| Expected failure (not found, validation) | `Effect.fail(new TypedError())` |
| Programmer error, impossible state | `Effect.die()` |
| External library throws | Wrap with `Effect.try` or `Effect.tryPromise` |

## Error Accumulation

### Collect All Errors

```typescript
// Validate multiple fields, collect all errors
const validateUser = (input: unknown) =>
  Effect.all([
    validateName(input.name),
    validateEmail(input.email),
    validateAge(input.age),
  ], { mode: "either" })
// Returns Either<[ValidationError, ...], [Name, Email, Age]>
```

### Using Effect.validate

```typescript
const result = Effect.validate([
  validateName(input.name),
  validateEmail(input.email),
], { concurrency: "unbounded" })
// Runs all validations, accumulates errors
```

## Anti-Patterns

### Don't Use Try/Catch

```typescript
// ❌ WRONG: Imperative error handling
const bad = Effect.sync(() => {
  try {
    return JSON.parse(data)
  } catch (e) {
    return null  // Error information lost!
  }
})

// ✅ CORRECT: Use Effect.try
const good = Effect.try({
  try: () => JSON.parse(data),
  catch: (error) => new ParseError({ cause: error }),
})
```

### Don't Swallow Errors Silently

```typescript
// ❌ WRONG: Silent failure
const bad = fetchUser(id).pipe(
  Effect.catchAll(() => Effect.succeed(null)),
)

// ✅ CORRECT: Log or track errors
const good = fetchUser(id).pipe(
  Effect.catchAll((error) =>
    Effect.gen(function* () {
      yield* Effect.logError("Failed to fetch user", error)
      return null
    }),
  ),
)
```

### Don't Retry Permanent Errors

```typescript
// ❌ WRONG: Retrying auth errors wastes resources
const bad = callApi().pipe(
  Effect.retry(Schedule.recurs(5)),  // Will retry 401/403 errors!
)

// ✅ CORRECT: Only retry transient failures
const good = callApi().pipe(
  Effect.retry(
    Schedule.recurWhile((e) => e._tag === "NetworkError"),
  ),
)
```

## Summary

| Pattern | Description |
|---------|-------------|
| `Data.TaggedError` | Define discriminated union errors with `_tag` |
| `Effect.fail` | Create typed error in failure channel |
| `catchTag` | Handle specific error type |
| `catchTags` | Handle multiple error types |
| `mapError` | Transform error type |
| `Effect.retry` | Retry with schedule |
| `Schedule.exponential` | Exponential backoff |
| `Schedule.jittered` | Add randomness to schedule |
| `Effect.timeout` | Bound operation duration |
| `Effect.die` | Unrecoverable defect (not in E channel) |
