import type { AgentPhase, ColumnStatus, PRState, SessionState } from "./types.js"

export type SymbolTier = "ascii" | "unicode"

const parseTierOverride = (value: string | undefined): SymbolTier | undefined => {
	if (!value) return undefined
	const normalized = value.trim().toLowerCase()
	if (normalized === "ascii") return "ascii"
	if (normalized === "unicode") return "unicode"
	return undefined
}

const localeSupportsUnicode = (): boolean => {
	const locale = process.env.LC_ALL ?? process.env.LC_CTYPE ?? process.env.LANG
	if (!locale) return true
	const normalized = locale.toLowerCase()
	return normalized.includes("utf-8") || normalized.includes("utf8")
}

export const resolveSymbolTier = (): SymbolTier => {
	const override = parseTierOverride(process.env.AZEDARACH_SYMBOL_TIER)
	if (override) return override
	return localeSupportsUnicode() ? "unicode" : "ascii"
}

const ISSUE_STATUS_TOKENS: Record<SymbolTier, Record<ColumnStatus, string>> = {
	ascii: {
		open: "I:O",
		in_progress: "I:P",
		blocked: "I:B",
		closed: "I:C",
	},
	unicode: {
		open: "I:O",
		in_progress: "I:P",
		blocked: "I:B",
		closed: "I:C",
	},
}

const SESSION_STATE_TOKENS: Record<SymbolTier, Record<SessionState, string>> = {
	ascii: {
		idle: "",
		initializing: "S:I",
		busy: "S:B",
		waiting: "S:W",
		done: "S:D",
		error: "S:E",
		paused: "S:P",
		warning: "S:!",
		crashed: "S:X",
	},
	unicode: {
		idle: "",
		initializing: "S:I",
		busy: "S:B",
		waiting: "S:W",
		done: "S:D",
		error: "S:E",
		paused: "S:P",
		warning: "S:!",
		crashed: "S:X",
	},
}

const PR_STATE_TOKENS: Record<SymbolTier, Record<PRState | "unknown", string>> = {
	ascii: {
		open: "P:O",
		draft: "P:D",
		merged: "P:M",
		closed: "P:C",
		unknown: "P:?",
	},
	unicode: {
		open: "P:O",
		draft: "P:D",
		merged: "P:M",
		closed: "P:C",
		unknown: "P:?",
	},
}

const PHASE_TOKENS: Record<SymbolTier, Record<AgentPhase, string>> = {
	ascii: {
		idle: "",
		planning: "A:P",
		action: "A:A",
		verification: "A:V",
		planMode: "A:M",
	},
	unicode: {
		idle: "",
		planning: "A:P",
		action: "A:A",
		verification: "A:V",
		planMode: "A:M",
	},
}

const tokenTier = (tier?: SymbolTier): SymbolTier => tier ?? resolveSymbolTier()

export const getIssueStatusToken = (status: ColumnStatus, tier?: SymbolTier): string =>
	ISSUE_STATUS_TOKENS[tokenTier(tier)][status]

export const getSessionStateToken = (state: SessionState, tier?: SymbolTier): string =>
	SESSION_STATE_TOKENS[tokenTier(tier)][state]

export const getPrStateToken = (state: PRState | "unknown", tier?: SymbolTier): string =>
	PR_STATE_TOKENS[tokenTier(tier)][state]

export const getWorktreeToken = (): string => "W:Y"

export const getTmuxSessionToken = (): string => "T:Y"

export const getPhaseToken = (phase: AgentPhase, tier?: SymbolTier): string =>
	PHASE_TOKENS[tokenTier(tier)][phase]

export const getGitDirtyToken = (tier?: SymbolTier): string =>
	tokenTier(tier) === "unicode" ? "G:✎" : "G:D"

export const getGitBehindToken = (count: number, tier?: SymbolTier): string =>
	tokenTier(tier) === "unicode" ? `G:↓${count}` : `G:B${count}`

export const getGitConflictToken = (): string => "G:!"

export const getNetworkToken = (connected: boolean): string => (connected ? "N:UP" : "N:DN")
