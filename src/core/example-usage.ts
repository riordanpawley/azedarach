/**
 * Example usage of TmuxService, TerminalService, and path utilities
 *
 * This file demonstrates how to use the Effect-based services.
 */

import { Effect } from "effect"
import { BunRuntime } from "@effect/platform-bun"
import { TmuxService, TmuxServiceLive } from "./TmuxService"
import { TerminalService, TerminalServiceLive } from "./TerminalService"
import { getWorktreePath, getSessionName, getProjectName } from "./paths"

// Example 1: Create a new tmux session
const createSession = (beadId: string, projectPath: string) =>
  Effect.gen(function* () {
    const tmux = yield* TmuxService
    const sessionName = getSessionName(beadId)
    const worktreePath = getWorktreePath(projectPath, beadId)

    // Create tmux session in the worktree directory
    yield* tmux.newSession(sessionName, {
      cwd: worktreePath,
      command: "claude"
    })

    console.log(`Created tmux session: ${sessionName}`)
  })

// Example 2: List all tmux sessions
const listAllSessions = Effect.gen(function* () {
  const tmux = yield* TmuxService
  const sessions = yield* tmux.listSessions()

  console.log("Active tmux sessions:")
  for (const session of sessions) {
    console.log(`  - ${session.name} (${session.windows} windows, attached: ${session.attached})`)
  }

  return sessions
})

// Example 3: Open terminal with tmux attach command
const attachToSession = (beadId: string) =>
  Effect.gen(function* () {
    const tmux = yield* TmuxService
    const terminal = yield* TerminalService
    const sessionName = getSessionName(beadId)

    // Check if session exists
    const exists = yield* tmux.hasSession(sessionName)
    if (!exists) {
      return yield* Effect.fail(new Error(`Session ${sessionName} not found`))
    }

    // Get the attach command and open in a new tmux window
    const attachCmd = tmux.attachCommand(sessionName)
    yield* terminal.openInTmuxWindow(attachCmd, sessionName)

    console.log(`Opened tmux window with session: ${sessionName}`)
  })

// Example 4: Path utilities
const pathExample = () => {
  const projectPath = "/Users/riordan/prog/azedarach"
  const beadId = "az-001"

  console.log("Project name:", getProjectName(projectPath))  // "azedarach"
  console.log("Worktree path:", getWorktreePath(projectPath, beadId))  // "/Users/riordan/prog/azedarach-az-001"
  console.log("Session name:", getSessionName(beadId))  // "az-001"
}

// Run examples (uncomment to test)
// const program = createSession("az-001", "/Users/riordan/prog/azedarach")
//   .pipe(Effect.provide(TmuxServiceLive))
//
// BunRuntime.runMain(program)

export {
  createSession,
  listAllSessions,
  attachToSession,
  pathExample
}
