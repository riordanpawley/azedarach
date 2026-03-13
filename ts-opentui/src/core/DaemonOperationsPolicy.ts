export type DaemonAutoStartCommand = "tui-default" | "sync"

export interface ResolveDaemonOperationsPolicyInput {
	readonly command: DaemonAutoStartCommand
	readonly noDaemonFlag: boolean
	readonly env: Readonly<Record<string, string | undefined>>
}

export interface ResolvedDaemonOperationsPolicy {
	readonly autoDaemonize: boolean
	readonly decision:
		| "enabled-by-default"
		| "disabled-by-cli-flag"
		| "disabled-by-env"
		| "enabled-by-env"
		| "ignored-invalid-env"
}

const parseBooleanLike = (value: string): boolean | undefined => {
	const normalized = value.trim().toLowerCase()
	if (["1", "true", "yes", "on"].includes(normalized)) return true
	if (["0", "false", "no", "off"].includes(normalized)) return false
	return undefined
}

export const resolveDaemonOperationsPolicy = (
	input: ResolveDaemonOperationsPolicyInput,
): ResolvedDaemonOperationsPolicy => {
	if (input.noDaemonFlag) {
		return {
			autoDaemonize: false,
			decision: "disabled-by-cli-flag",
		}
	}

	const modeRaw = input.env.AZEDARACH_DAEMON_MODE
	if (modeRaw !== undefined && modeRaw.trim().length > 0) {
		const mode = modeRaw.trim().toLowerCase()
		if (mode === "off" || mode === "disabled") {
			return {
				autoDaemonize: false,
				decision: "disabled-by-env",
			}
		}
		if (mode === "on" || mode === "enabled" || mode === "auto") {
			return {
				autoDaemonize: true,
				decision: "enabled-by-env",
			}
		}
	}

	const explicitDisable = input.env.AZEDARACH_NO_DAEMON
	if (explicitDisable !== undefined && explicitDisable.trim().length > 0) {
		const parsed = parseBooleanLike(explicitDisable)
		if (parsed === true) {
			return {
				autoDaemonize: false,
				decision: "disabled-by-env",
			}
		}
		if (parsed === false) {
			return {
				autoDaemonize: true,
				decision: "enabled-by-env",
			}
		}
		return {
			autoDaemonize: true,
			decision: "ignored-invalid-env",
		}
	}

	return {
		autoDaemonize: true,
		decision: "enabled-by-default",
	}
}

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
