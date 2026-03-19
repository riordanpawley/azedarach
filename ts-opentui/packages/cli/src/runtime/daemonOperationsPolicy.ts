export const resolveDaemonIntervalMsFromEnv = (
	env: Readonly<Record<string, string | undefined>>,
): { readonly intervalMs: number | undefined; readonly warning: string | undefined } => {
	const raw = env.AZEDARACH_DAEMON_INTERVAL_MS
	if (raw === undefined || raw.trim().length === 0) {
		return { intervalMs: undefined, warning: undefined }
	}
	const parsed = Number.parseInt(raw, 10)
	if (!Number.isFinite(parsed) || parsed <= 0) {
		return {
			intervalMs: undefined,
			warning: `Ignoring invalid AZEDARACH_DAEMON_INTERVAL_MS='${raw}'. Expected a positive integer.`,
		}
	}
	return {
		intervalMs: parsed,
		warning: undefined,
	}
}
