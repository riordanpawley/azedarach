/**
 * Singleton empty collections for use with Effect Refs/SubscriptionRefs
 *
 * These ensure reference equality for empty collections, which is important for:
 * - Avoiding unnecessary re-renders in reactive systems
 * - Proper equality checks in SubscriptionRef
 *
 * Usage:
 *   import { emptyRecord, emptyArray } from "../lib/empty"
 *   const ref = yield* SubscriptionRef.make(emptyRecord<string, Task>())
 */

import type { Record as R } from "effect"

// Singleton frozen empty record - all calls return the same reference
const EMPTY_RECORD: R.ReadonlyRecord<string, never> = Object.freeze({})

/**
 * Returns a singleton empty record (same reference on every call)
 *
 * @example
 * emptyRecord<Task>() === emptyRecord<Task>() // true
 */
export const emptyRecord = <V>(): R.ReadonlyRecord<string, V> =>
	EMPTY_RECORD as R.ReadonlyRecord<string, V>

// Singleton frozen empty array - all calls return the same reference
const EMPTY_ARRAY: readonly never[] = Object.freeze([])

/**
 * Returns a singleton empty readonly array (same reference on every call)
 *
 * @example
 * emptyArray<Task>() === emptyArray<Task>() // true
 */
export const emptyArray = <T>(): readonly T[] => EMPTY_ARRAY as readonly T[]
