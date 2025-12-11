import { createSignal } from "solid-js";

/**
 * Azedarach - TUI Kanban board for orchestrating parallel Claude Code sessions
 *
 * This is a placeholder entry point. The actual implementation will use:
 * - Effect for business logic and dependency injection
 * - OpenTUI + Solid for the terminal UI
 * - effect-atom for state management
 */

export function main() {
  const [greeting] = createSignal("Azedarach scaffolding initialized");
  console.log(greeting());
}
