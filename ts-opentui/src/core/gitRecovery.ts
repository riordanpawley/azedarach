/**
 * Structured recovery hints for git command failures.
 */
export type GitRecoveryHint = {
	readonly _tag: "stale-lock-file"
	readonly lockFilePath: string
}

const LOCK_FILE_ERROR_PATTERN = /Unable to create ['"]([^'"]+\.lock)['"]/

export const createStaleLockRecoveryHint = (lockFilePath: string): GitRecoveryHint => ({
	_tag: "stale-lock-file",
	lockFilePath,
})

/**
 * Extract a stale lock-file recovery hint from git stderr output.
 *
 * Git commonly reports this as:
 * "fatal: Unable to create '<repo>/.git/index.lock': File exists."
 */
export const extractGitRecoveryHint = (stderr: string): GitRecoveryHint | undefined => {
	const match = stderr.match(LOCK_FILE_ERROR_PATTERN)
	const lockFilePath = match?.[1]?.trim()
	if (!lockFilePath) {
		return undefined
	}

	return createStaleLockRecoveryHint(lockFilePath)
}
