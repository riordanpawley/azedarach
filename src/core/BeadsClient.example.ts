/**
 * BeadsClient Usage Examples
 *
 * This file demonstrates how to use the BeadsClient Effect service.
 * These are examples only - not executable tests.
 */

import { Effect, Console } from "effect"
import { BeadsClient, BeadsClientLiveWithPlatform } from "./BeadsClient"
import { BunRuntime } from "@effect/platform-bun"

// Example 1: Get all ready issues
const getReadyIssues = Effect.gen(function* () {
  const client = yield* BeadsClient
  const issues = yield* client.ready()

  yield* Console.log(`Found ${issues.length} ready issues`)
  for (const issue of issues) {
    yield* Console.log(`  [${issue.id}] ${issue.title}`)
  }

  return issues
})

// Example 2: Search for issues
const searchIssues = (query: string) =>
  Effect.gen(function* () {
    const client = yield* BeadsClient
    const results = yield* client.search(query)

    yield* Console.log(`Search "${query}" found ${results.length} results`)
    return results
  })

// Example 3: Update issue status
const startWorkOnIssue = (id: string) =>
  Effect.gen(function* () {
    const client = yield* BeadsClient

    // Get current state
    const issue = yield* client.show(id)
    yield* Console.log(`Starting work on: ${issue.title}`)

    // Update to in_progress
    yield* client.update(id, {
      status: "in_progress",
      notes: "Started working on this issue",
    })

    yield* Console.log(`Issue ${id} is now in progress`)
  })

// Example 4: Complete and close an issue
const completeIssue = (id: string) =>
  Effect.gen(function* () {
    const client = yield* BeadsClient

    // Close the issue
    yield* client.close(id, "Implementation complete")

    yield* Console.log(`Closed issue ${id}`)
  })

// Example 5: List issues with filters
const listInProgressTasks = Effect.gen(function* () {
  const client = yield* BeadsClient

  const tasks = yield* client.list({
    status: "in_progress",
    type: "task",
  })

  yield* Console.log(`Active tasks: ${tasks.length}`)
  return tasks
})

// Example 6: Sync beads database
const syncBeads = Effect.gen(function* () {
  const client = yield* BeadsClient

  const result = yield* client.sync()

  yield* Console.log(`Sync complete: ${result.pushed} pushed, ${result.pulled} pulled`)
  return result
})

// Example 7: Error handling
const safeShowIssue = (id: string) =>
  Effect.gen(function* () {
    const client = yield* BeadsClient

    const issue = yield* client.show(id)
    return issue
  }).pipe(
    Effect.catchTag("NotFoundError", (error) =>
      Console.log(`Issue ${error.issueId} not found`).pipe(
        Effect.as(null)
      )
    ),
    Effect.catchTag("BeadsError", (error) =>
      Console.error(`Beads command failed: ${error.message}`).pipe(
        Effect.as(null)
      )
    )
  )

// Example 8: Composing operations
const workflowExample = Effect.gen(function* () {
  const client = yield* BeadsClient

  // Find ready work
  const ready = yield* client.ready()

  if (ready.length === 0) {
    yield* Console.log("No ready issues found")
    return
  }

  // Start work on the first one
  const firstIssue = ready[0]!
  yield* Console.log(`Starting work on: ${firstIssue.title}`)

  yield* client.update(firstIssue.id, {
    status: "in_progress",
    notes: `Started: ${new Date().toISOString()}`,
  })

  // ... do work ...

  // Complete the work
  yield* client.close(firstIssue.id, "Work completed successfully")

  // Sync changes
  yield* client.sync()

  yield* Console.log("Workflow complete!")
})

// To run any example:
// const main = getReadyIssues.pipe(Effect.provide(BeadsClientLiveWithPlatform))
// BunRuntime.runMain(main)
