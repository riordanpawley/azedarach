import { useState } from "react"

/**
 * Azedarach - TUI Kanban board for orchestrating parallel Claude Code sessions
 *
 * This is a placeholder entry point. The actual implementation will use:
 * - Effect for business logic and dependency injection
 * - OpenTUI + React for the terminal UI
 * - effect-atom for state management
 */

export function main() {
	console.log("Azedarach scaffolding initialized")
}

export function App() {
	const [greeting] = useState("Azedarach")
	return <div>{greeting}</div>
}
