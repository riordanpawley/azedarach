/**
 * VCService Example Usage
 *
 * Demonstrates integration with steveyegge/vc for AI-supervised orchestration.
 *
 * Run with: bun run src/core/VCService.example.ts
 */

import { Effect, Console } from "effect"
import {
  VCService,
  VCServiceLive,
  type VCExecutorInfo,
} from "./VCService.js"

// ============================================================================
// Example 1: Check if VC is installed
// ============================================================================

const checkInstallation = Effect.gen(function* () {
  yield* Console.log("=== Checking VC Installation ===")

  const vc = yield* VCService
  const available = yield* vc.isAvailable()

  if (!available) {
    yield* Console.log("VC is not installed.")
    yield* Console.log("Install with:")
    yield* Console.log("  brew tap steveyegge/vc")
    yield* Console.log("  brew install vc")
    yield* Console.log("")
    yield* Console.log("Or build from source:")
    yield* Console.log("  git clone https://github.com/steveyegge/vc")
    yield* Console.log("  cd vc && go build -o vc ./cmd/vc")
    return false
  }

  const version = yield* vc.getVersion().pipe(
    Effect.catchTag("VCNotInstalledError", () => Effect.succeed("unknown"))
  )
  yield* Console.log(`VC is installed: ${version}`)
  return true
})

// ============================================================================
// Example 2: Toggle auto-pilot mode
// ============================================================================

const toggleAutoPilotExample = Effect.gen(function* () {
  yield* Console.log("\n=== Toggling Auto-Pilot ===")

  const vc = yield* VCService
  const status = yield* vc.getStatus()
  yield* Console.log(`Current status: ${status.status}`)

  if (status.status === "not_installed") {
    yield* Console.log("Cannot toggle - VC not installed")
    return
  }

  const newStatus = yield* vc.toggleAutoPilot().pipe(
    Effect.catchTag("VCNotInstalledError", () =>
      Effect.succeed({ status: "not_installed", sessionName: "" } as VCExecutorInfo)
    )
  )

  yield* Console.log(`New status: ${newStatus.status}`)

  if (newStatus.status === "running") {
    yield* Console.log("")
    yield* Console.log("VC auto-pilot is now running!")
    yield* Console.log("It will:")
    yield* Console.log("  - Poll for ready issues in Beads")
    yield* Console.log("  - Claim and execute work autonomously")
    yield* Console.log("  - Run quality gates (tests, lint, build)")
    yield* Console.log("  - Create/update issues as needed")
    yield* Console.log("")

    const attachCmd = yield* vc.getAttachCommand().pipe(
      Effect.catchAll(() => Effect.succeed("tmux attach -t vc-autopilot"))
    )
    yield* Console.log(`To view VC output: ${attachCmd}`)
  }
})

// ============================================================================
// Example 3: Send commands to VC REPL
// ============================================================================

const sendCommandsExample = Effect.gen(function* () {
  yield* Console.log("\n=== Sending Commands to VC ===")

  const vc = yield* VCService
  const status = yield* vc.getStatus()

  if (status.status !== "running") {
    yield* Console.log("VC is not running - starting auto-pilot first")
    yield* vc.toggleAutoPilot().pipe(
      Effect.catchAll(() => Effect.void)
    )
  }

  // Example commands you can send to VC's conversational REPL
  const commands = [
    "status",                           // Check current status
    "What's ready to work on?",         // Natural language query
    // "Let's continue working",        // Start executing (commented - would actually run!)
  ]

  for (const cmd of commands) {
    yield* Console.log(`Sending: "${cmd}"`)
    yield* vc.sendCommand(cmd).pipe(
      Effect.catchAll((e) => Console.log(`  Error: ${e.message}`))
    )
    yield* Effect.sleep("500 millis")
  }

  yield* Console.log("\nCommands sent. Check tmux session for output.")
})

// ============================================================================
// Example 4: Integration with Azedarach TUI
// ============================================================================

const tuiIntegrationExample = Effect.gen(function* () {
  yield* Console.log("\n=== TUI Integration Pattern ===")
  yield* Console.log(`
When integrating with Azedarach's TUI:

1. Status Bar Component:
   - Poll getStatus() every few seconds
   - Show: "VC: running" or "VC: stopped"
   - Show keybinding hint: "[a]uto-pilot"

2. Auto-Pilot Toggle (keybinding 'a'):
   - Call toggleAutoPilot()
   - Update status bar

3. Command Palette (keybinding ':'):
   - Accept natural language input
   - Call sendCommand(input)

4. Shared Beads DB:
   - Both Azedarach and VC read/write .beads/beads.db
   - TUI shows real-time updates as VC works
   - No special sync needed - same database!

Example TUI layout:

┌─────────────────────────────────────────────────────┐
│ Azedarach                                           │
├─────────────────────────────────────────────────────┤
│  Backlog  │  In Progress  │  Review  │  Done       │
│  ┌─────┐  │  ┌─────────┐  │          │  ┌─────┐    │
│  │ 📋  │  │  │ 🤖 VC   │  │          │  │ ✓   │    │
│  │ az-1│  │  │ az-2    │  │          │  │ az-0│    │
│  └─────┘  │  │ working │  │          │  └─────┘    │
│           │  └─────────┘  │          │             │
├─────────────────────────────────────────────────────┤
│ [a]uto-pilot: ON │ VC: running │ [:] command       │
└─────────────────────────────────────────────────────┘
`)
})

// ============================================================================
// Main
// ============================================================================

const main = Effect.gen(function* () {
  yield* Console.log("VCService Integration Examples")
  yield* Console.log("==============================")

  const installed = yield* checkInstallation

  if (installed) {
    yield* toggleAutoPilotExample
    // Uncomment to test command sending:
    // yield* sendCommandsExample
  }

  yield* tuiIntegrationExample

  yield* Console.log("\n=== Done ===")
}).pipe(Effect.provide(VCServiceLive))

// Run the example
Effect.runPromise(main).catch(console.error)
