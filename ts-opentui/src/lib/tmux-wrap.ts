/**
 * tmux Auto-Wrapper
 *
 * Checks if running outside tmux and re-execs inside a tmux session.
 * Must be called BEFORE any Effect runtime initialization.
 */

/**
 * The tmux session name for the main az TUI.
 * Configurable via AZ_TMUX_SESSION env var, defaults to "az".
 */
export const AZ_SESSION_NAME = process.env.AZ_TMUX_SESSION ?? "az"

// Keep internal alias for backwards compatibility within this file
const SESSION_NAME = AZ_SESSION_NAME

/**
 * Check if we should wrap the current process in tmux
 */
export function shouldWrapInTmux(): boolean {
	// Already inside tmux
	if (process.env.TMUX) return false

	// User explicitly disabled auto-wrap
	if (process.env.AZ_NO_TMUX === "1") return false

	return true
}

/**
 * Re-execute this process inside a tmux session
 *
 * Uses `tmux new-session -A` (attach-or-create) so:
 * - If session exists: attaches to it
 * - If session doesn't exist: creates it
 *
 * This function never returns - it exits after the spawned process completes.
 *
 * @param argv - Full process.argv to pass through
 */
export async function execInTmux(argv: string[]): Promise<never> {
	// Build the command to run inside tmux
	// argv[0] is bun, argv[1] is the script path, rest are args
	const command = argv.join(" ")

	const proc = Bun.spawn(["tmux", "new-session", "-A", "-s", SESSION_NAME, command], {
		stdin: "inherit",
		stdout: "inherit",
		stderr: "inherit",
	})

	const exitCode = await proc.exited
	process.exit(exitCode)
}
