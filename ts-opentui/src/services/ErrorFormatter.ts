/**
 * ErrorFormatter - Service for converting technical errors into user-friendly messages
 *
 * Takes tagged errors from various services (Git, IssueTracker, Tmux, PR) and formats them
 * with actionable guidance. Preserves error context for debugging while showing
 * clear, helpful messages to users.
 *
 * Design principles:
 * - User message should be understandable without technical knowledge
 * - Include specific "Try:" suggestions when applicable
 * - Preserve original error for debugging (via Effect.logError)
 * - Different error categories get tailored guidance
 */

import { Effect } from "effect"

// ============================================================================
// Types
// ============================================================================

/**
 * Formatted error ready for display
 */
export interface FormattedError {
	/** User-friendly error message */
	readonly message: string
	/** Optional actionable suggestions (prefixed with "Try:") */
	readonly suggestion?: string
	/** Original error for logging/debugging */
	readonly original: unknown
	/** Category for potential future filtering */
	readonly category: ErrorCategory
}

export type ErrorCategory = "git" | "tracker" | "tmux" | "pr" | "session" | "system" | "unknown"

// ============================================================================
// Error Tag Registry
// ============================================================================

/**
 * Maps error _tag to formatting configuration
 * This is the central registry for all known error types
 */
const ERROR_FORMATTERS: Record<
	string,
	(error: Record<string, unknown>) => Omit<FormattedError, "original">
> = {
	// ─────────────────────────────────────────────────────────────────────────
	// Git Errors
	// ─────────────────────────────────────────────────────────────────────────
	GitError: (error) => {
		const stderr = String(error.stderr || "")
		const command = String(error.command || "git")

		// Merge conflict detection
		if (stderr.includes("CONFLICT") || stderr.includes("merge conflict")) {
			return {
				message: "Merge conflict detected",
				suggestion:
					"Try: Open the worktree and resolve conflicts manually, then run 'git add' and 'git commit'",
				category: "git",
			}
		}

		// Dirty worktree
		if (stderr.includes("uncommitted changes") || stderr.includes("dirty")) {
			return {
				message: "Worktree has uncommitted changes",
				suggestion: "Try: Commit or stash changes in the worktree before this operation",
				category: "git",
			}
		}

		// Branch already exists
		if (stderr.includes("already exists")) {
			return {
				message: "Branch already exists",
				suggestion: "Try: Use a different branch name or delete the existing branch first",
				category: "git",
			}
		}

		// Not a git repo
		if (stderr.includes("not a git repository")) {
			return {
				message: "Not a git repository",
				suggestion: "Try: Run this command from within a git repository",
				category: "git",
			}
		}

		// Authentication/permission issues
		if (
			stderr.includes("Permission denied") ||
			stderr.includes("Authentication failed") ||
			stderr.includes("could not read Username")
		) {
			return {
				message: "Git authentication failed",
				suggestion: "Try: Check your SSH keys or run 'gh auth login' for GitHub authentication",
				category: "git",
			}
		}

		// Network issues
		if (
			stderr.includes("Could not resolve host") ||
			stderr.includes("Connection refused") ||
			stderr.includes("Network is unreachable")
		) {
			return {
				message: "Network error during git operation",
				suggestion: "Try: Check your internet connection and try again",
				category: "git",
			}
		}

		// Default git error
		return {
			message: `Git command failed: ${command}`,
			suggestion: stderr
				? `Error: ${stderr.slice(0, 100)}${stderr.length > 100 ? "..." : ""}`
				: undefined,
			category: "git",
		}
	},

	WorktreeNotFoundError: (error) => ({
		message: `Worktree not found for ${error.issueId}`,
		suggestion: "Try: The worktree may have been deleted. Start a new session to recreate it",
		category: "git",
	}),

	WorktreeExistsError: (error) => ({
		message: `Worktree already exists for ${error.issueId}`,
		suggestion: "Try: Use the existing worktree or clean it up first",
		category: "git",
	}),

	WorktreeNameClashError: (error) => {
		const conflictPath = String(error.conflictingWorktreePath || "")
		const conflictIssue = String(error.conflictingIssueId || "another issue")
		const ahead = Number(error.commitsAheadOfBase)
		const uncommitted = Number(error.uncommittedFileCount)
		const riskSummary =
			Number.isFinite(ahead) || Number.isFinite(uncommitted)
				? `Duplicate worktree risk: ${Number.isFinite(ahead) ? `${ahead} commit(s) ahead` : "ahead unknown"}, ${Number.isFinite(uncommitted) ? `${uncommitted} uncommitted file(s)` : "dirty unknown"}`
				: undefined

		return {
			message: `Worktree/branch name clash with ${conflictIssue}${conflictPath ? ` at ${conflictPath}` : ""}`,
			suggestion: riskSummary
				? `Try: Resolve or delete the duplicate worktree first. ${riskSummary}`
				: "Try: Resolve or delete the duplicate worktree first",
			category: "git",
		}
	},

	NotAGitRepoError: (error) => ({
		message: `Not a git repository: ${error.path}`,
		suggestion: "Try: Initialize a git repository with 'git init' or navigate to an existing repo",
		category: "git",
	}),

	// ─────────────────────────────────────────────────────────────────────────
	// IssueTracker Errors
	// ─────────────────────────────────────────────────────────────────────────
	IssueTrackerError: (error) => {
		const stderr = String(error.stderr || "")
		const command = String(error.command || "tracker")

		// Database locked
		if (stderr.includes("database is locked") || stderr.includes("SQLITE_BUSY")) {
			return {
				message: "IssueTracker database is locked",
				suggestion:
					"Try: Another process may be using the issue tracker backend. Wait a moment and retry your `az issue` command.",
				category: "tracker",
			}
		}

		// Not initialized
		if (stderr.includes("not initialized") || stderr.includes("No tracker directory")) {
			return {
				message: "IssueTracker not initialized in this project",
				suggestion:
					"Try: Ensure this project is initialized for issue tracking (`.azedarach` exists), then rerun `az issue`.",
				category: "tracker",
			}
		}

		// Sync conflicts
		if (stderr.includes("sync") && stderr.includes("conflict")) {
			return {
				message: "IssueTracker sync conflict detected",
				suggestion: "Try: Run `az sync`, then retry your `az issue` command.",
				category: "tracker",
			}
		}

		return {
			message: `IssueTracker command failed: ${command}`,
			suggestion: stderr
				? `Error: ${stderr.slice(0, 100)}${stderr.length > 100 ? "..." : ""}`
				: undefined,
			category: "tracker",
		}
	},

	NotFoundError: (error) => ({
		message: `Issue not found: ${error.issueId}`,
		suggestion: "Try: Check the issue ID or run `az issue list --limit 20` to find candidates.",
		category: "tracker",
	}),

	ParseError: (_error) => ({
		message: "Failed to parse tracker output",
		suggestion: "Try: Re-run with `az issue --help` to confirm the active command surface, then retry.",
		category: "tracker",
	}),

	// ─────────────────────────────────────────────────────────────────────────
	// Tmux Errors
	// ─────────────────────────────────────────────────────────────────────────
	TmuxNotFoundError: () => ({
		message: "tmux is not installed",
		suggestion: "Try: Install tmux with 'brew install tmux' (macOS) or your package manager",
		category: "tmux",
	}),

	SessionNotFoundError: (error) => ({
		message: `tmux session not found: ${error.session}`,
		suggestion: "Try: The session may have ended. Start a new session or check 'tmux ls'",
		category: "tmux",
	}),

	TmuxError: (error) => {
		const message = String(error.message || "")

		// Session already exists
		if (message.includes("duplicate session")) {
			return {
				message: "tmux session already exists",
				suggestion: "Try: Attach to the existing session or kill it first",
				category: "tmux",
			}
		}

		// No sessions
		if (message.includes("no server running") || message.includes("no sessions")) {
			return {
				message: "No tmux server running",
				suggestion: "Try: Start a new session - the tmux server will start automatically",
				category: "tmux",
			}
		}

		return {
			message: "tmux command failed",
			suggestion: message ? `Error: ${message.slice(0, 100)}` : undefined,
			category: "tmux",
		}
	},

	NotInsideTmuxError: () => ({
		message: "Not running inside tmux",
		suggestion: "Try: Start Azedarach from within a tmux session",
		category: "tmux",
	}),

	TmuxCommandError: (error) => ({
		message: `tmux command failed: ${error.command}`,
		suggestion: error.stderr ? `Error: ${String(error.stderr).slice(0, 100)}` : undefined,
		category: "tmux",
	}),

    // ─────────────────────────────────────────────────────────────────────────
    // PR Errors
    // ─────────────────────────────────────────────────────────────────────────
    GitOperationInProgressError: (error) => {
        const operation = String(error.operation || "git operation")
        const continueCommand = String(error.continueCommand || "git <operation> --continue")
        const abortCommand = String(error.abortCommand || "git <operation> --abort")

        return {
            message: `Target repository has ${operation} in progress`,
            suggestion: `Try: Continue with '${continueCommand}' after resolving conflicts, or abort with '${abortCommand}', then retry the action`,
            category: "pr",
        }
    },

    PRAlreadyExistsError: (error) => {
        const branch = String(error.branch || "")
        const baseBranch = String(error.baseBranch || "")

        return {
            message: "PR already exists for this branch",
            suggestion:
                branch && baseBranch
                    ? `Try: Open the existing PR for '${branch}' into '${baseBranch}' (Space+O), or continue updating that PR`
                    : "Try: Open the existing PR (Space+O) or continue updating it",
            category: "pr",
        }
    },

    PRBranchProtectionError: (error) => {
        const operation = String(error.operation || "")
        const branch = String(error.branch || "")

        if (operation === "push") {
            return {
                message: `Push blocked by branch protection${branch ? ` on '${branch}'` : ""}`,
                suggestion:
                    "Try: Push to a feature branch allowed by repository rules, then retry PR creation",
                category: "pr",
            }
        }

        return {
            message: "PR creation blocked by branch protection policy",
            suggestion: "Try: Follow repository branch rules, then retry PR creation",
            category: "pr",
        }
    },

    PRError: (error) => {
        const message = String(error.message || "")

        return {
            message: `PR operation failed: ${message}`,
            category: "pr",
        }
	},

	GHCLIError: (error) => {
		const message = String(error.message || "")

		// Not authenticated
		if (message.includes("not logged in") || message.includes("authentication")) {
			return {
				message: "GitHub CLI not authenticated",
				suggestion: "Try: Run 'gh auth login' to authenticate with GitHub",
				category: "pr",
			}
		}

		// CLI not installed
		if (message.includes("not found") || message.includes("not installed")) {
			return {
				message: "GitHub CLI (gh) not installed",
				suggestion: "Try: Install with 'brew install gh' (macOS) or visit https://cli.github.com",
				category: "pr",
			}
		}

		return {
			message: "GitHub CLI error",
			suggestion: message ? `Error: ${message.slice(0, 100)}` : undefined,
			category: "pr",
		}
	},

	PRNotFoundError: (error) => ({
		message: `No PR found for branch ${error.branch}`,
		suggestion: "Try: Create a PR first with the 'p' key or 'gh pr create'",
		category: "pr",
	}),

	MergeConflictError: (error) => ({
		message: `Merge conflict for ${error.issueId}`,
		suggestion: "Try: Resolve conflicts in the worktree, then commit and try again",
		category: "pr",
	}),

	TypeCheckError: (error) => ({
		message: `Type errors after merging ${error.issueId}`,
		suggestion: "Try: AI session started to fix. Run 'bun run type-check' after fixing",
		category: "pr",
	}),

	OfflineError: (error) => {
		const operation = String(error.operation || "Operation")
		const reason = error.reason as "config" | "offline" | "both" | undefined

		switch (reason) {
			case "config":
				return {
					message: `${operation} disabled in configuration`,
					suggestion: "Try: Check .azedarach.json settings",
					category: "pr",
				}
			case "offline":
				return {
					message: `${operation} unavailable (network offline)`,
					suggestion: "Try: Check your internet connection and retry",
					category: "pr",
				}
			case "both":
				return {
					message: `${operation} disabled (config + offline)`,
					suggestion: "Try: Check both .azedarach.json settings and network connection",
					category: "pr",
				}
			default:
				return {
					message: `${operation} unavailable`,
					category: "pr",
				}
		}
	},

	// ─────────────────────────────────────────────────────────────────────────
	// Session Errors
	// ─────────────────────────────────────────────────────────────────────────
	SessionError: (error) => ({
		message: `Session error: ${error.message}`,
		category: "session",
	}),

	SessionNotFoundError_Session: (error) => ({
		message: `Session not found: ${error.issueId}`,
		suggestion: "Try: The session may have ended. Start a new session with Space",
		category: "session",
	}),

	SessionExistsError: (error) => ({
		message: `Session already running for ${error.issueId}`,
		suggestion: "Try: Attach to the existing session or stop it first",
		category: "session",
	}),

	InvalidStateError: (error) => ({
		message: `Invalid state transition: ${error.from} → ${error.to}`,
		category: "session",
	}),

	// ─────────────────────────────────────────────────────────────────────────
	// VC Errors
	// ─────────────────────────────────────────────────────────────────────────
	VCNotInstalledError: () => ({
		message: "VC (vibe coder) not installed",
		suggestion: "Try: Install VC or disable the VC feature in settings",
		category: "system",
	}),

	VCError: (error) => ({
		message: `VC error: ${error.message}`,
		suggestion: error.stderr ? `Error: ${String(error.stderr).slice(0, 100)}` : undefined,
		category: "system",
	}),

	VCNotRunningError: () => ({
		message: "VC executor is not running",
		suggestion: "Try: Toggle VC off and on again to restart",
		category: "system",
	}),

	// ─────────────────────────────────────────────────────────────────────────
	// File/Lock Errors
	// ─────────────────────────────────────────────────────────────────────────
	LockError: (error) => ({
		message: `Lock error: ${error.message}`,
		suggestion: "Try: Another operation may be in progress. Wait and try again",
		category: "system",
	}),

	LockTimeoutError: (error) => ({
		message: `Lock timeout for ${error.resource}`,
		suggestion: "Try: The operation took too long. Check for stuck processes",
		category: "system",
	}),

	LockConflictError: (error) => ({
		message: `Lock conflict: ${error.holder} holds lock on ${error.resource}`,
		suggestion: "Try: Wait for the other operation to complete",
		category: "system",
	}),

	// ─────────────────────────────────────────────────────────────────────────
	// Attachment Errors
	// ─────────────────────────────────────────────────────────────────────────
	AttachmentError: (error) => ({
		message: `Attachment failed: ${error.message}`,
		category: "session",
	}),

	TerminalError: (error) => ({
		message: `Terminal error: ${error.message}`,
		category: "session",
	}),

	ImageAttachmentError: (error) => ({
		message: `Image attachment failed: ${error.message}`,
		category: "session",
	}),

	ClipboardError: (error) => ({
		message: `Clipboard error: ${error.message}`,
		suggestion: "Try: Check clipboard permissions in system settings",
		category: "system",
	}),

	FileNotFoundError: (error) => ({
		message: `File not found: ${error.path}`,
		category: "system",
	}),

	// ─────────────────────────────────────────────────────────────────────────
	// Editor Errors
	// ─────────────────────────────────────────────────────────────────────────
	ParseMarkdownError: (_error) => ({
		message: "Failed to parse markdown",
		category: "system",
	}),

	EditorError: (error) => ({
		message: `Editor error: ${error.message}`,
		category: "system",
	}),

	// ─────────────────────────────────────────────────────────────────────────
	// Other Errors
	// ─────────────────────────────────────────────────────────────────────────
	TmuxSessionMonitorError: (error) => ({
		message: `Session monitor error: ${error.message}`,
		category: "system",
	}),

	StateDetectionError: (error) => ({
		message: `State detection error: ${error.message}`,
		category: "session",
	}),
}

// ============================================================================
// Service Implementation
// ============================================================================

/**
 * Format any error into a user-friendly message with actionable guidance
 *
 * @param error - Any error (tagged or not)
 * @returns FormattedError with message, optional suggestion, and category
 */
export const format = (error: unknown): FormattedError => {
	// Handle tagged errors (Data.TaggedError instances)
	if (error && typeof error === "object" && "_tag" in error) {
		const tag = String((error as { _tag: string })._tag)
		const formatter = ERROR_FORMATTERS[tag]

		if (formatter) {
			return {
				...formatter(error as Record<string, unknown>),
				original: error,
			}
		}

		// Unknown tagged error - use message if available
		const message =
			"message" in error ? String((error as { message: string }).message) : `Unknown error: ${tag}`
		return {
			message,
			original: error,
			category: "unknown",
		}
	}

	// Handle standard Error objects
	if (error instanceof Error) {
		return {
			message: error.message,
			original: error,
			category: "unknown",
		}
	}

	// Handle string errors
	if (typeof error === "string") {
		return {
			message: error,
			original: error,
			category: "unknown",
		}
	}

	// Fallback for anything else
	return {
		message: String(error),
		original: error,
		category: "unknown",
	}
}

/**
 * Format error and combine message with suggestion for display
 *
 * @param error - Any error
 * @returns Single string ready for toast display
 */
export const formatForToast = (error: unknown): string => {
	const formatted = format(error)
	if (formatted.suggestion) {
		return `${formatted.message}\n${formatted.suggestion}`
	}
	return formatted.message
}

/**
 * Create an Effect that formats the error and logs it
 *
 * @param prefix - Prefix for the log message (e.g., "Failed to start session")
 * @param error - The error to format
 * @returns Effect that logs the formatted error
 */
export const logFormatted = (prefix: string, error: unknown): Effect.Effect<FormattedError> =>
	Effect.gen(function* () {
		const formatted = format(error)
		yield* Effect.logError(`${prefix}: ${formatted.message}`, {
			error: formatted.original,
			category: formatted.category,
			suggestion: formatted.suggestion,
		})
		return formatted
	})

// ============================================================================
// Service Definition
// ============================================================================

/**
 * ErrorFormatter service - stateless utility for error formatting
 *
 * While this could be a simple module export, wrapping it as a service:
 * 1. Keeps consistent with other services in the codebase
 * 2. Allows future extension (e.g., error tracking, custom formatters)
 * 3. Makes it easy to mock in tests
 */
export class ErrorFormatter extends Effect.Service<ErrorFormatter>()("ErrorFormatter", {
	effect: Effect.succeed({
		format,
		formatForToast,
		logFormatted,
	}),
}) {}
