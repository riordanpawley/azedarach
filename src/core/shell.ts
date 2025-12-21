/**
 * Shell utility functions for safe command construction
 */

/**
 * Escape a string for safe use inside shell double quotes
 *
 * When constructing shell commands like: `claude "escaped_prompt"`,
 * the prompt must have special characters escaped to prevent:
 * - Command injection via backticks or $()
 * - History expansion via !
 * - Variable expansion via $
 * - Quote escaping issues
 *
 * Order matters: escape backslashes first since they're the escape character,
 * then escape other special characters.
 *
 * @example
 * ```ts
 * const prompt = 'Check !task and run $(whoami)'
 * const safe = escapeForShellDoubleQuotes(prompt)
 * // Result: 'Check \\!task and run \\$(whoami)'
 * const command = `claude "${safe}"`
 * ```
 */
export function escapeForShellDoubleQuotes(s: string): string {
	return s
		.replace(/\\/g, "\\\\") // Backslash → \\ (must be first)
		.replace(/"/g, '\\"') // Double quote → \"
		.replace(/\$/g, "\\$") // Dollar sign → \$ (variable expansion)
		.replace(/`/g, "\\`") // Backtick → \` (command substitution)
		.replace(/!/g, "\\!") // Exclamation → \! (history expansion in bash/zsh)
}
