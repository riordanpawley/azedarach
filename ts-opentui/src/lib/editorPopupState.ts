export interface ActiveEditorPopupState {
	readonly channel: string
	readonly tempFile: string
}

let activeEditorState: ActiveEditorPopupState | null = null

export const setActiveEditorPopup = (state: ActiveEditorPopupState): void => {
	activeEditorState = state
}

export const clearActiveEditorPopup = (): void => {
	activeEditorState = null
}

export const killActiveEditorPopup = (): void => {
	if (!activeEditorState) return

	const { tempFile } = activeEditorState

	try {
		Bun.spawnSync(["pkill", "-f", tempFile], {
			stdin: "ignore",
			stdout: "ignore",
			stderr: "ignore",
		})
	} catch {
		try {
			const result = Bun.spawnSync(["pgrep", "-f", tempFile], {
				stdin: "ignore",
				stdout: "pipe",
				stderr: "ignore",
			})

			if (result.stdout) {
				const output = Buffer.isBuffer(result.stdout)
					? result.stdout.toString()
					: String(result.stdout)
				const pids = output.trim().split("\n").filter(Boolean)

				for (const pidStr of pids) {
					const pid = Number.parseInt(pidStr, 10)
					if (!Number.isNaN(pid) && pid > 0 && pid !== process.pid) {
						try {
							process.kill(pid, "SIGTERM")
						} catch {
							// Process may already have exited.
						}
					}
				}
			}
		} catch {
			// Best effort fallback failed.
		}
	}

	activeEditorState = null
}
