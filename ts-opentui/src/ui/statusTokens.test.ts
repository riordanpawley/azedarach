import { describe, expect, it } from "vitest"
import {
	getGitConflictToken,
	getGitDirtyToken,
	getIssueStatusToken,
	getNetworkToken,
	getPhaseToken,
	getPrStateToken,
	getSessionStateToken,
	getWorktreeToken,
	type SymbolTier,
} from "./statusTokens.js"

const collectTokens = (tier: SymbolTier): readonly string[] => [
	getIssueStatusToken("open", tier),
	getIssueStatusToken("in_progress", tier),
	getIssueStatusToken("blocked", tier),
	getIssueStatusToken("closed", tier),
	getSessionStateToken("initializing", tier),
	getSessionStateToken("busy", tier),
	getSessionStateToken("waiting", tier),
	getSessionStateToken("done", tier),
	getSessionStateToken("error", tier),
	getSessionStateToken("paused", tier),
	getSessionStateToken("warning", tier),
	getSessionStateToken("crashed", tier),
	getPrStateToken("open", tier),
	getPrStateToken("draft", tier),
	getPrStateToken("merged", tier),
	getPrStateToken("closed", tier),
	getPrStateToken("unknown", tier),
	getWorktreeToken(),
	getPhaseToken("planning", tier),
	getPhaseToken("action", tier),
	getPhaseToken("verification", tier),
	getPhaseToken("planMode", tier),
	getGitDirtyToken(tier),
	getGitConflictToken(),
	getNetworkToken(true),
	getNetworkToken(false),
]

describe("statusTokens", () => {
	it("has no cross-domain token collisions for ascii", () => {
		const tokens = collectTokens("ascii")
		expect(new Set(tokens).size).toBe(tokens.length)
	})

	it("has no cross-domain token collisions for unicode", () => {
		const tokens = collectTokens("unicode")
		expect(new Set(tokens).size).toBe(tokens.length)
	})
})
