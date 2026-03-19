export const AZ_SESSION_NAME = process.env.AZ_TMUX_SESSION ?? "az"

const SESSION_NAME = AZ_SESSION_NAME

export function shouldWrapInTmux(): boolean {
	if (process.env.TMUX) return false
	if (process.env.AZ_NO_TMUX === "1") return false
	return true
}

export async function execInTmux(argv: string[]): Promise<never> {
	const proc = Bun.spawn(["tmux", "new-session", "-A", "-s", SESSION_NAME, ...argv], {
		stdin: "inherit",
		stdout: "inherit",
		stderr: "inherit",
	})

	const exitCode = await proc.exited
	process.exit(exitCode)
}
