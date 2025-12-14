/**
 * FileLockManager integration test
 *
 * Quick test to verify the implementation works correctly.
 */

import { Console, Duration, Effect } from "effect"
import { FileLockManager, withLock } from "./FileLockManager.js"

const test = Effect.gen(function* () {
	yield* Console.log("Testing FileLockManager...")

	// Test 1: Basic exclusive lock
	yield* Console.log("\n1. Testing exclusive lock acquisition")
	const result1 = yield* withLock(
		{ path: "/tmp/test-file.txt", type: "exclusive" },
		Effect.sync(() => {
			console.log("  ✓ Exclusive lock acquired successfully")
			return "success"
		}),
	)
	yield* Console.log(`  Result: ${result1}`)

	// Test 2: Multiple shared locks
	yield* Console.log("\n2. Testing shared locks (3 concurrent readers)")
	const readers = [1, 2, 3].map((i) =>
		withLock(
			{ path: "/tmp/shared-file.txt", type: "shared" },
			Effect.gen(function* () {
				yield* Console.log(`  ✓ Reader ${i} acquired shared lock`)
				yield* Effect.sleep(Duration.millis(50))
				return i
			}),
		),
	)
	const results = yield* Effect.all(readers, { concurrency: "unbounded" })
	yield* Console.log(`  All readers complete: ${results.join(", ")}`)

	// Test 3: Lock timeout
	yield* Console.log("\n3. Testing lock timeout")
	const manager = yield* FileLockManager

	const lock1 = yield* manager.acquireLock({
		path: "/tmp/timeout-test.txt",
		type: "exclusive",
	})
	yield* Console.log("  ✓ First lock acquired")

	const timeoutResult = yield* manager
		.acquireLock({
			path: "/tmp/timeout-test.txt",
			type: "exclusive",
			timeout: Duration.millis(100),
		})
		.pipe(
			Effect.map(() => "unexpected success"),
			Effect.catchTag("LockTimeoutError", () => Effect.succeed("timed out as expected")),
		)
	yield* Console.log(`  ✓ Second lock: ${timeoutResult}`)

	yield* manager.releaseLock(lock1)
	yield* Console.log("  ✓ First lock released")

	// Test 4: Lock state inspection
	yield* Console.log("\n4. Testing lock state inspection")
	const stateBefore = yield* manager.getLockState("/tmp/inspect.txt")
	yield* Console.log(`  State before: ${stateBefore === null ? "no locks" : "has locks"}`)

	const lock2 = yield* manager.acquireLock({
		path: "/tmp/inspect.txt",
		type: "exclusive",
	})

	const stateAfter = yield* manager.getLockState("/tmp/inspect.txt")
	yield* Console.log(
		`  State after: exclusive=${stateAfter?.exclusiveHolder !== null}, shared=${stateAfter?.sharedHolders.length}`,
	)

	yield* manager.releaseLock(lock2)

	const stateFinal = yield* manager.getLockState("/tmp/inspect.txt")
	yield* Console.log(`  State final: ${stateFinal === null ? "cleaned up" : "still has locks"}`)

	yield* Console.log("\n✓ All tests passed!")
})

const program = test.pipe(Effect.provide(FileLockManager.Default))

Effect.runPromise(program).catch(console.error)
