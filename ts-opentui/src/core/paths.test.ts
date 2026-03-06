import { describe, expect, it } from "bun:test"
import {
	decodeIssueSessionName,
	getIssueSessionName,
	getProjectSessionPrefix,
	issueIdsEqualForLookup,
	normalizeIssueIdForLookup,
	parseIssueSessionName,
} from "./paths.js"

describe("paths session naming", () => {
	it("keeps already-safe issue IDs unchanged", () => {
		expect(getIssueSessionName("az-05y")).toBe("az-05y")
	})

	it("encodes dotted issue IDs into tmux-safe names", () => {
		expect(getIssueSessionName("az.foo")).toBe("az_x2e_foo")
	})

	it("prefixes session names with project shorthand when project path is provided", () => {
		expect(getIssueSessionName("b", "/Users/user/prog/azedarach")).toBe("az-b")
		expect(getIssueSessionName("a", "/Users/user/prog/Chefy")).toBe("ch-a")
	})

	it("round-trips canonical encoded names", () => {
		const issueId = "az.foo_bar"
		const sessionName = getIssueSessionName(issueId)
		expect(decodeIssueSessionName(sessionName)).toBe(issueId)
	})

	it("parses canonical encoded issue session names", () => {
		expect(parseIssueSessionName("az_x2e_foo")).toEqual({
			type: "issue",
			issueId: "az.foo",
		})
	})

	it("parses AI-prefixed encoded session names", () => {
		expect(parseIssueSessionName("claude-az_x2e_foo")).toEqual({
			type: "issue",
			issueId: "az.foo",
		})
		expect(parseIssueSessionName("opencode-az_x2e_foo")).toEqual({
			type: "issue",
			issueId: "az.foo",
		})
		expect(parseIssueSessionName("codex-az_x2e_foo")).toEqual({
			type: "issue",
			issueId: "az.foo",
		})
	})

	it("parses legacy raw session names", () => {
		expect(parseIssueSessionName("az.foo")).toEqual({
			type: "issue",
			issueId: "az.foo",
		})
	})

	it("parses project-prefixed names with explicit project path", () => {
		expect(parseIssueSessionName("az-b", "/Users/user/prog/azedarach")).toEqual({
			type: "issue",
			issueId: "b",
		})
		expect(parseIssueSessionName("ch-AZE-123", "/Users/user/prog/Chefy")).toEqual({
			type: "issue",
			issueId: "AZE-123",
		})
	})

	it("parses legacy underscore-normalized session names", () => {
		expect(parseIssueSessionName("az_foo")).toEqual({
			type: "issue",
			issueId: "az.foo",
		})
		expect(parseIssueSessionName("claude-az_foo")).toEqual({
			type: "issue",
			issueId: "az.foo",
		})
	})

	it("rejects non-issue names", () => {
		expect(parseIssueSessionName("not a session")).toBeUndefined()
		expect(parseIssueSessionName("!invalid")).toBeUndefined()
		expect(parseIssueSessionName("")).toBeUndefined()
	})

	it("parses plain local issue IDs", () => {
		expect(parseIssueSessionName("a")).toEqual({
			type: "issue",
			issueId: "a",
		})
		expect(parseIssueSessionName("123")).toEqual({
			type: "issue",
			issueId: "123",
		})
		expect(parseIssueSessionName("claude-a")).toEqual({
			type: "issue",
			issueId: "a",
		})
		expect(parseIssueSessionName("opencode-123")).toEqual({
			type: "issue",
			issueId: "123",
		})
		expect(parseIssueSessionName("codex-123")).toEqual({
			type: "issue",
			issueId: "123",
		})
	})

	it("parses Linear-style uppercase identifiers", () => {
		expect(parseIssueSessionName("AZE-123")).toEqual({
			type: "issue",
			issueId: "AZE-123",
		})
		expect(parseIssueSessionName("claude-AZE-123")).toEqual({
			type: "issue",
			issueId: "AZE-123",
		})
	})
})

describe("paths lookup normalization", () => {
	it("derives stable two-letter project prefixes", () => {
		expect(getProjectSessionPrefix("/Users/user/prog/azedarach")).toBe("az")
		expect(getProjectSessionPrefix("/Users/user/prog/Chefy")).toBe("ch")
	})

	it("normalizes only Linear identifiers to uppercase", () => {
		expect(normalizeIssueIdForLookup("aze-123")).toBe("AZE-123")
		expect(normalizeIssueIdForLookup("AZE-123")).toBe("AZE-123")
		expect(normalizeIssueIdForLookup("az-05y")).toBe("az-05y")
	})

	it("compares Linear identifiers case-insensitively for lookup", () => {
		expect(issueIdsEqualForLookup("AZE-123", "aze-123")).toBe(true)
		expect(issueIdsEqualForLookup("AZE-123", "AZE-124")).toBe(false)
		expect(issueIdsEqualForLookup("az-05y", "AZ-05Y")).toBe(false)
	})
})
