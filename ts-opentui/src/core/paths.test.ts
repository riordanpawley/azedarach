import { describe, expect, it } from "bun:test"
import { decodeBeadSessionName, getBeadSessionName, parseSessionName } from "./paths.js"

describe("paths session naming", () => {
	it("keeps already-safe bead IDs unchanged", () => {
		expect(getBeadSessionName("az-05y")).toBe("az-05y")
	})

	it("encodes dotted bead IDs into tmux-safe names", () => {
		expect(getBeadSessionName("az.foo")).toBe("az_x2e_foo")
	})

	it("round-trips canonical encoded names", () => {
		const beadId = "az.foo_bar"
		const sessionName = getBeadSessionName(beadId)
		expect(decodeBeadSessionName(sessionName)).toBe(beadId)
	})

	it("parses canonical encoded bead session names", () => {
		expect(parseSessionName("az_x2e_foo")).toEqual({
			type: "bead",
			beadId: "az.foo",
		})
	})

	it("parses AI-prefixed encoded session names", () => {
		expect(parseSessionName("claude-az_x2e_foo")).toEqual({
			type: "bead",
			beadId: "az.foo",
		})
		expect(parseSessionName("opencode-az_x2e_foo")).toEqual({
			type: "bead",
			beadId: "az.foo",
		})
	})

	it("parses legacy raw session names", () => {
		expect(parseSessionName("az.foo")).toEqual({
			type: "bead",
			beadId: "az.foo",
		})
	})

	it("parses legacy underscore-normalized session names", () => {
		expect(parseSessionName("az_foo")).toEqual({
			type: "bead",
			beadId: "az.foo",
		})
		expect(parseSessionName("claude-az_foo")).toEqual({
			type: "bead",
			beadId: "az.foo",
		})
	})

	it("rejects non-bead names", () => {
		expect(parseSessionName("notasession")).toBeUndefined()
		expect(parseSessionName("")).toBeUndefined()
	})
})
