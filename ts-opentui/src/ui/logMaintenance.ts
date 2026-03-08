const DEFAULT_AZ_LOG_PATH = "az.log"
const DEFAULT_AZ_LOG_MAX_LINES = 50_000

const splitLogLines = (
	content: string,
): { readonly lines: ReadonlyArray<string>; readonly trailingNewline: boolean } => {
	const trailingNewline = content.endsWith("\n")
	const lines = trailingNewline ? content.slice(0, -1).split("\n") : content.split("\n")
	return { lines, trailingNewline }
}

export async function truncateAzLogOnStartup(
	logPath: string = DEFAULT_AZ_LOG_PATH,
	maxLines: number = DEFAULT_AZ_LOG_MAX_LINES,
): Promise<void> {
	if (maxLines < 1) return

	const logFile = Bun.file(logPath)
	if (!(await logFile.exists())) return

	const content = await logFile.text()
	const { lines, trailingNewline } = splitLogLines(content)
	if (lines.length <= maxLines) return

	const trimmed = lines.slice(-maxLines).join("\n")
	await Bun.write(logPath, trailingNewline ? `${trimmed}\n` : trimmed)
}
