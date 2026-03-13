---
name: effect-schema
description: "Effect Schema Codec Usage Skill"
---

# Effect Schema Codec Usage Skill

**Version:** 1.0
**Purpose:** Canonical Effect Schema patterns for JSON boundaries, persistence, and typed domain conversion.

## Overview

Effect `Schema` is a **two-way codec**, not just a validator:

- decode: unknown/input JSON -> typed domain
- encode: typed domain -> output JSON

Use schemas as the single source of truth for data contracts at system boundaries.

## Core Rules

1. Treat schemas as codecs, not passive shape checks.
2. Use `Schema.parseJson(innerSchema)` for JSON-string boundaries.
3. Use `Schema.decodeUnknown*` for reads and `Schema.encode*` for writes.
4. Use domain schemas (`Schema.DateTimeUtc`, literals, unions, brands) instead of raw `Schema.String` when semantics are known.
5. Never hand-roll boundary serialization if a schema exists.

## JSON Boundary Pattern

Use `parseJson` for fields stored as JSON strings (DB metadata, queue payloads, file blobs).

```ts
import { Effect, Schema } from "effect"

const PayloadJsonSchema = Schema.parseJson(
	Schema.Struct({
		idempotencyKey: Schema.String,
		startedAt: Schema.DateTimeUtc,
	}),
)

const decodePayload = (value: string) => Schema.decodeUnknownSync(PayloadJsonSchema)(value)
const encodePayload = (value: Schema.Schema.Type<typeof PayloadJsonSchema>) =>
	Schema.encodeSync(PayloadJsonSchema)(value)
```

## Preferred Encode/Decode Helper Pattern

Wrap codec failures in typed domain errors at service/store boundaries.

```ts
const encodeConfig = (value: Config): Effect.Effect<string, StoreError> =>
	Effect.try({
		try: () => Schema.encodeSync(ConfigJsonSchema)(value),
		catch: (cause) => new StoreError({ message: "Failed to encode config", cause }),
	})

const decodeConfig = (raw: string | undefined): Config =>
	raw === undefined ? DEFAULT_CONFIG : Schema.decodeUnknownSync(ConfigJsonSchema)(raw)
```

## Date/Time Contracts

If a field is semantically a UTC timestamp, use `Schema.DateTimeUtc` and domain `DateTime.Utc`.

```ts
const OutcomeJsonSchema = Schema.parseJson(
	Schema.Struct({
		started_at: Schema.DateTimeUtc,
		finished_at: Schema.DateTimeUtc,
	}),
)
```

Do not model temporal fields as plain `Schema.String` unless they are truly opaque strings.

## Domain Modeling Guidelines

- IDs with constrained format: branded strings or regex-backed schema.
- Enums/state machines: `Schema.Literal(...)` unions.
- Optional nullable fields: prefer explicit `Schema.NullOr(...)` where API contract is nullable.
- Structured maps/collections: use `Schema.Struct`, `Schema.Array`, `Schema.HashMap` rather than `unknown`.

## Anti-Patterns

1. `JSON.stringify` / `JSON.parse` directly in boundary code when a schema exists.
2. Declaring known typed values (timestamps, constrained status) as bare `Schema.String`.
3. Decode-only flows where encode path is manual/custom.
4. Silent fallback that swallows decode failure without explicit defaulting policy.
5. Using `as`/`any` to coerce decoded values.

## Testing Schema Contracts

At minimum, add:

1. round-trip test (`encode` then `decode`) for valid payloads
2. invalid payload rejection test
3. boundary-specific test (for example `DateTimeUtc`, literals, nullable handling)

```ts
const encoded = Schema.encodeSync(PayloadJsonSchema)(payload)
const decoded = Schema.decodeUnknownSync(PayloadJsonSchema)(encoded)
expect(decoded).toEqual(payload)
```

## Quick Checklist

- Is this a boundary (DB/file/CLI/network/metadata)?
- Is there a schema that models both decode and encode?
- Are timestamps modeled as `Schema.DateTimeUtc` when appropriate?
- Are decode/encode errors mapped to typed domain errors?
- Are tests covering round-trip + invalid input?
