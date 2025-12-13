/**
 * WorktreeManager Usage Examples
 *
 * Demonstrates common patterns for working with git worktrees via Effect.
 */

import { BunRuntime } from "@effect/platform-bun"
import { Console, Effect } from "effect"
import * as WorktreeManager from "./WorktreeManager.js"
import { WorktreeManagerLiveWithPlatform } from "./WorktreeManager.js"

// ============================================================================
// Example 1: Create and List Worktrees
// ============================================================================

const example1 = Effect.gen(function* () {
	const manager = yield* WorktreeManager.WorktreeManager

	yield* Console.log("Creating worktree for task az-05y...")

	const worktree = yield* manager.create({
		beadId: "az-05y",
		baseBranch: "main",
		projectPath: process.cwd(),
	})

	yield* Console.log(`✓ Created worktree at: ${worktree.path}`)
	yield* Console.log(`  Branch: ${worktree.branch}`)
	yield* Console.log(`  HEAD: ${worktree.head}`)

	yield* Console.log("\nListing all worktrees...")
	const worktrees = yield* manager.list(process.cwd())

	for (const wt of worktrees) {
		yield* Console.log(`  - ${wt.beadId}: ${wt.path} (${wt.branch})`)
	}
}).pipe(Effect.provide(WorktreeManagerLiveWithPlatform))

// ============================================================================
// Example 2: Idempotent Create
// ============================================================================

const example2 = Effect.gen(function* () {
	const manager = yield* WorktreeManager.WorktreeManager

	yield* Console.log("Creating worktree (first time)...")
	const wt1 = yield* manager.create({
		beadId: "az-abc",
		baseBranch: "main",
		projectPath: process.cwd(),
	})
	yield* Console.log(`✓ Created at: ${wt1.path}`)

	yield* Console.log("\nCreating same worktree again (should be idempotent)...")
	const wt2 = yield* manager.create({
		beadId: "az-abc",
		baseBranch: "main",
		projectPath: process.cwd(),
	})
	yield* Console.log(`✓ Returned existing: ${wt2.path}`)

	yield* Console.log(`\nPaths match: ${wt1.path === wt2.path}`)
}).pipe(Effect.provide(WorktreeManagerLiveWithPlatform))

// ============================================================================
// Example 3: Scoped Worktree with Auto-Cleanup
// ============================================================================

const example3 = Effect.gen(function* () {
	yield* Console.log("Creating scoped worktree...")

	yield* Effect.scoped(
		Effect.gen(function* () {
			const worktree = yield* WorktreeManager.acquireWorktree({
				beadId: "az-temp",
				baseBranch: "main",
				projectPath: process.cwd(),
			})

			yield* Console.log(`✓ Worktree created at: ${worktree.path}`)
			yield* Console.log("  Doing work in worktree...")

			// Simulate some work
			yield* Effect.sleep("1 second")

			yield* Console.log("  Work complete. Exiting scope...")
			// Worktree will be automatically removed when scope closes
		}),
	)

	yield* Console.log("✓ Worktree automatically cleaned up")
}).pipe(Effect.provide(WorktreeManagerLiveWithPlatform))

// ============================================================================
// Example 4: Check if Worktree Exists
// ============================================================================

const example4 = Effect.gen(function* () {
	const manager = yield* WorktreeManager.WorktreeManager

	const beadId = "az-check"

	const exists = yield* manager.exists({
		beadId,
		projectPath: process.cwd(),
	})

	if (exists) {
		yield* Console.log(`✓ Worktree for ${beadId} exists`)

		const worktree = yield* manager.get({
			beadId,
			projectPath: process.cwd(),
		})

		if (worktree) {
			yield* Console.log(`  Path: ${worktree.path}`)
			yield* Console.log(`  Branch: ${worktree.branch}`)
		}
	} else {
		yield* Console.log(`✗ Worktree for ${beadId} does not exist`)
	}
}).pipe(Effect.provide(WorktreeManagerLiveWithPlatform))

// ============================================================================
// Example 5: Error Handling
// ============================================================================

const example5 = Effect.gen(function* () {
	yield* Console.log("Attempting to create worktree in non-git directory...")

	yield* WorktreeManager.create({
		beadId: "az-error",
		baseBranch: "main",
		projectPath: "/tmp",
	}).pipe(
		Effect.catchTag("NotAGitRepoError", (error) =>
			Console.log(`✓ Caught expected error: ${error._tag} - ${error.path}`),
		),
		Effect.catchTag("GitError", (error) => Console.log(`✗ Git error: ${error.message}`)),
	)
}).pipe(Effect.provide(WorktreeManagerLiveWithPlatform))

// ============================================================================
// Example 6: Cleanup Pattern
// ============================================================================

const example6 = Effect.gen(function* () {
	const manager = yield* WorktreeManager.WorktreeManager

	const beadId = "az-cleanup"

	// Create worktree
	yield* Console.log(`Creating worktree for ${beadId}...`)
	yield* manager.create({
		beadId,
		baseBranch: "main",
		projectPath: process.cwd(),
	})
	yield* Console.log("✓ Created")

	// Verify it exists
	const exists = yield* manager.exists({
		beadId,
		projectPath: process.cwd(),
	})
	yield* Console.log(`Exists: ${exists}`)

	// Clean up
	yield* Console.log("Removing worktree...")
	yield* manager.remove({
		beadId,
		projectPath: process.cwd(),
	})
	yield* Console.log("✓ Removed")

	// Verify it's gone
	const existsAfter = yield* manager.exists({
		beadId,
		projectPath: process.cwd(),
	})
	yield* Console.log(`Exists after removal: ${existsAfter}`)
}).pipe(Effect.provide(WorktreeManagerLiveWithPlatform))

// ============================================================================
// Example 7: Using Convenience Functions
// ============================================================================

const example7 = Effect.gen(function* () {
	yield* Console.log("Using convenience functions...")

	// Create using top-level function
	const worktree = yield* WorktreeManager.create({
		beadId: "az-conv",
		baseBranch: "main",
		projectPath: process.cwd(),
	})

	yield* Console.log(`✓ Created: ${worktree.path}`)

	// List using top-level function
	const worktrees = yield* WorktreeManager.list(process.cwd())
	yield* Console.log(`Total worktrees: ${worktrees.length}`)

	// Check using top-level function
	const exists = yield* WorktreeManager.exists({
		beadId: "az-conv",
		projectPath: process.cwd(),
	})
	yield* Console.log(`Exists: ${exists}`)

	// Remove using top-level function
	yield* WorktreeManager.remove({
		beadId: "az-conv",
		projectPath: process.cwd(),
	})
	yield* Console.log("✓ Removed")
}).pipe(Effect.provide(WorktreeManagerLiveWithPlatform))

// ============================================================================
// Run Examples
// ============================================================================

// Uncomment the example you want to run:

// BunRuntime.runMain(example1)
// BunRuntime.runMain(example2)
// BunRuntime.runMain(example3)
// BunRuntime.runMain(example4)
// BunRuntime.runMain(example5)
// BunRuntime.runMain(example6)
// BunRuntime.runMain(example7)
